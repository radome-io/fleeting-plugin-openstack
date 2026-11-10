// Package openstack is a fleeting plugin for OpenStack.
package openstack

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"

	"github.com/radome-io/fleeting-plugin-openstack/internal/cloud"
)

var _ provider.InstanceGroup = (*InstanceGroup)(nil)

// deletionRetryInterval spaces out retries of a failed deletion.
const deletionRetryInterval = time.Minute

// maxSize is reported to the runner, which refuses a max_instances above it.
const maxSize = 1000

// InstanceGroup is a group of OpenStack servers, configured by its Config.
type InstanceGroup struct {
	Config

	// Tests replace connect.
	connect func(context.Context, cloud.Options) (cloud.Client, error)

	log              hclog.Logger
	client           cloud.Client
	settings         provider.Settings
	flavorID         string
	networkIDs       []string
	securityGroupIDs []string
	addressVersion   int
	buildTimeout     time.Duration
	userData         string
	backoff          backoff

	// Fleeting may overlap Update with its shutdown.
	updateMu  sync.Mutex
	deletions map[string]time.Time // last deletion request, by broken server

	imagesMu sync.Mutex
	images   map[string]cloud.Image
}

// Init validates the configuration and checks that the flavor, networks,
// security groups, key pair and image exist.
func (g *InstanceGroup) Init(ctx context.Context, log hclog.Logger, settings provider.Settings) (provider.ProviderInfo, error) {
	if g.connect == nil {
		g.connect = cloud.New
	}

	parsed, err := g.validate()
	if err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("plugin_config: %w", err)
	}
	if !settings.UseStaticCredentials {
		return provider.ProviderInfo{}, errors.New("connector_config: use_static_credentials must be true, " +
			"the plugin does not provision credentials")
	}

	userData, err := g.Server.readUserData()
	if err != nil {
		return provider.ProviderInfo{}, err
	}

	client, err := g.connect(ctx, cloud.Options{Cloud: g.Cloud, CloudsConfig: g.CloudsConfig})
	if err != nil {
		return provider.ProviderInfo{}, err
	}

	flavorID, err := client.FindFlavor(ctx, g.Server.Flavor)
	if err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("server.flavor: %w", err)
	}

	networkIDs, err := findAll(ctx, g.Server.Networks, client.FindNetwork)
	if err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("server.networks: %w", err)
	}

	securityGroupIDs, err := findAll(ctx, g.Server.SecurityGroups, client.FindSecurityGroup)
	if err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("server.security_groups: %w", err)
	}

	if g.Server.KeyName != "" {
		if err := client.CheckKeyPair(ctx, g.Server.KeyName); err != nil {
			return provider.ProviderInfo{}, fmt.Errorf("server.key_name: %w", err)
		}
	}

	if _, err := client.FindImage(ctx, g.Server.Image); err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("server.image: %w", err)
	}

	g.client = client
	g.settings = settings
	g.userData = userData
	g.flavorID = flavorID
	g.networkIDs = networkIDs
	g.securityGroupIDs = securityGroupIDs
	g.addressVersion = parsed.addressVersion
	g.buildTimeout = parsed.buildTimeout
	g.deletions = make(map[string]time.Time)
	g.images = make(map[string]cloud.Image)
	g.log = log.With("cloud", g.Cloud, "region", client.Region(), "group", g.Name)

	return provider.ProviderInfo{
		ID:      path.Join("openstack", client.Region(), g.Name),
		MaxSize: maxSize,
	}, nil
}

// Update reports the state of each server, and deletes broken ones.
func (g *InstanceGroup) Update(ctx context.Context, update func(id string, state provider.State)) error {
	g.updateMu.Lock()
	defer g.updateMu.Unlock()

	servers, err := g.client.ListServers(ctx, g.tag())
	if err != nil {
		return err
	}

	now := time.Now()
	listed := make(map[string]provider.State, len(servers))
	broken := make(map[string]string)

	for _, s := range servers {
		state, reason := classify(s, now, g.buildTimeout)
		listed[s.ID] = state

		switch {
		case reason != "":
			broken[s.ID] = reason
			g.deleteBroken(ctx, s, reason, now)
		case state == provider.StateRunning:
			g.backoff.running(s.ID)
		}

		update(s.ID, state)
	}

	if failures, pause, reason := g.backoff.failed(broken, now); pause > 0 {
		g.logPause(failures, pause, reason)
	}
	g.backoff.listed(listed)

	for id := range g.deletions {
		if _, ok := listed[id]; !ok {
			delete(g.deletions, id)
		}
	}

	return nil
}

// Increase creates up to n servers, fewer during a backoff, and returns how
// many it created. It returns an error only when it created none: fleeting
// drops the result of a failed gRPC call.
func (g *InstanceGroup) Increase(ctx context.Context, n int) (int, error) {
	allowed, pause := g.backoff.allowed(n, time.Now())
	switch {
	case pause > 0:
		g.log.Debug("scale-up paused", "remaining", pause)

		return 0, nil
	case allowed == 0:
		g.log.Debug("scale-up waits for the last server created to come up")

		return 0, nil
	}

	created, err := g.create(ctx, allowed)
	switch {
	case err == nil:
		return created, nil
	case created > 0:
		g.log.Error("scale-up created fewer servers than requested", "requested", allowed, "created", created, "err", err)

		return created, nil
	case ctx.Err() != nil:
		// Shutting down: not a failure of the cloud.
		return 0, err
	default:
		// Start the pause after the retries.
		failures, pause := g.backoff.creationFailed(time.Now())
		g.logPause(failures, pause, err.Error())

		return 0, err
	}
}

// create creates n servers, stopping at the first failure.
func (g *InstanceGroup) create(ctx context.Context, n int) (int, error) {
	image, err := g.client.FindImage(ctx, g.Server.Image)
	if err != nil {
		return 0, fmt.Errorf("server.image: %w", err)
	}
	g.rememberImage(image)

	for created := range n {
		req := cloud.CreateRequest{
			Name:             g.serverName(),
			FlavorID:         g.flavorID,
			ImageID:          image.ID,
			NetworkIDs:       g.networkIDs,
			SecurityGroupIDs: g.securityGroupIDs,
			KeyName:          g.Server.KeyName,
			UserData:         g.userData,
			Metadata:         g.Server.Metadata,
			Tags:             append([]string{g.tag()}, g.Server.Tags...),
			AvailabilityZone: g.Server.AvailabilityZone,
		}

		id, err := g.client.CreateServer(ctx, req)
		if err != nil {
			return created, err
		}

		g.log.Info("server requested", "id", id, "name", req.Name, "image", image.ID)
		g.backoff.created(id)
	}

	return n, nil
}

// Decrease deletes servers, stopping at the first failure, and returns those
// it deleted. Like Increase, it returns an error only when it deleted none.
func (g *InstanceGroup) Decrease(ctx context.Context, ids []string) ([]string, error) {
	var succeeded []string

	for _, id := range ids {
		if err := g.client.DeleteServer(ctx, id); err != nil {
			if len(succeeded) == 0 {
				return nil, err
			}

			g.log.Error("scale-down deleted fewer servers than requested",
				"requested", len(ids), "deleted", len(succeeded), "err", err)

			return succeeded, nil
		}

		g.log.Info("server deletion requested", "id", id)
		succeeded = append(succeeded, id)
	}

	return succeeded, nil
}

// ConnectInfo returns how to reach a running server.
func (g *InstanceGroup) ConnectInfo(ctx context.Context, id string) (provider.ConnectInfo, error) {
	s, err := g.client.GetServer(ctx, id)
	if err != nil {
		return provider.ConnectInfo{}, err
	}
	if !healthy(s) {
		return provider.ConnectInfo{}, fmt.Errorf("server %s is not running: %s", id, describe(s))
	}

	internal, external, err := addresses(s, g.AddressNetwork, g.addressVersion)
	if err != nil {
		return provider.ConnectInfo{}, err
	}

	info := provider.ConnectInfo{
		ConnectorConfig: g.settings.ConnectorConfig,
		ID:              id,
		InternalAddr:    internal,
		ExternalAddr:    external,
	}
	if info.OS == "" || info.Arch == "" {
		osName, arch, err := g.platform(ctx, s.ImageID)
		if err != nil {
			return provider.ConnectInfo{}, err
		}
		info.OS = cmp.Or(info.OS, osName)
		info.Arch = cmp.Or(info.Arch, arch)
	}

	if info.Protocol == "" {
		info.Protocol = provider.ProtocolSSH
		if info.OS == "windows" {
			info.Protocol = provider.ProtocolWinRM
		}
	}

	return info, nil
}

// Heartbeat fails for a server that is gone or not running. API errors are
// only logged.
func (g *InstanceGroup) Heartbeat(ctx context.Context, id string) error {
	s, err := g.client.GetServer(ctx, id)
	if errors.Is(err, cloud.ErrNotFound) {
		return fmt.Errorf("%w: server %s no longer exists", provider.ErrInstanceUnhealthy, id)
	}
	if err != nil {
		g.log.Error("heartbeat could not read the server", "id", id, "err", err)

		return nil
	}

	if !healthy(s) {
		return fmt.Errorf("%w: server %s is not running: %s", provider.ErrInstanceUnhealthy, id, describe(s))
	}

	return nil
}

// Suspend is not supported.
func (*InstanceGroup) Suspend(context.Context, []string) ([]string, error) {
	return nil, provider.ErrSuspendResumeNotSupported
}

// Resume is not supported.
func (*InstanceGroup) Resume(context.Context, []string) ([]string, error) {
	return nil, provider.ErrSuspendResumeNotSupported
}

// Shutdown does nothing.
func (*InstanceGroup) Shutdown(context.Context) error {
	return nil
}

// deleteBroken deletes a broken server, at most once per
// deletionRetryInterval.
func (g *InstanceGroup) deleteBroken(ctx context.Context, s cloud.Server, reason string, now time.Time) {
	if last, ok := g.deletions[s.ID]; ok && now.Sub(last) < deletionRetryInterval {
		return
	}
	g.deletions[s.ID] = now

	g.log.Warn("deleting a broken server", "id", s.ID, "name", s.Name, "reason", reason)
	if err := g.client.DeleteServer(ctx, s.ID); err != nil {
		g.log.Error("deleting a broken server failed", "id", s.ID, "name", s.Name, "err", err)
	}
}

func (g *InstanceGroup) logPause(failures int, pause time.Duration, reason string) {
	g.log.Warn("pausing scale-ups: new servers fail to come up",
		"failures_in_a_row", failures, "pause", pause, "reason", reason)
}

func (g *InstanceGroup) serverName() string {
	return g.Name + "-" + strings.ToLower(rand.Text()[:8])
}

func (g *InstanceGroup) rememberImage(image cloud.Image) {
	g.imagesMu.Lock()
	defer g.imagesMu.Unlock()

	g.images[image.ID] = image
}

// platform returns the OS and architecture of an image, or linux on amd64
// when there is no image. Other errors are returned: the runner caches the
// result.
func (g *InstanceGroup) platform(ctx context.Context, imageID string) (osName, arch string, err error) {
	g.imagesMu.Lock()
	image, known := g.images[imageID]
	g.imagesMu.Unlock()

	if !known && imageID != "" {
		found, err := g.client.GetImage(ctx, imageID)
		switch {
		case err == nil:
			image = found
			g.rememberImage(found)
		case !errors.Is(err, cloud.ErrNotFound):
			return "", "", fmt.Errorf("read the platform of image %s: %w", imageID, err)
		}
	}

	return cmp.Or(image.OSType, "linux"), goArch(image.Architecture), nil
}

// goArch maps a Glance architecture to a Go one.
func goArch(architecture string) string {
	switch architecture {
	case "", "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "i686", "i386":
		return "386"
	case "armv7l", "armv6l":
		return "arm"
	default:
		return architecture
	}
}

func findAll(ctx context.Context, refs []string, find func(context.Context, string) (string, error)) ([]string, error) {
	ids := make([]string, len(refs))
	for i, ref := range refs {
		id, err := find(ctx, ref)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}

	return ids, nil
}
