package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/config"
	"github.com/gophercloud/gophercloud/v2/openstack/config/clouds"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/utils"
)

// microversion 2.52 is the first that tags a server at creation.
const microversion = "2.52"

// requestTimeout keeps a hung request from blocking the runner's scaling loop.
const requestTimeout = time.Minute

// Options locate the cloud.
type Options struct {
	Cloud        string // clouds.yaml entry
	CloudsConfig string // clouds.yaml path; standard locations when empty
}

type openStack struct {
	region  string
	nova    *gophercloud.ServiceClient
	glance  *gophercloud.ServiceClient
	neutron *gophercloud.ServiceClient
}

// New authenticates to the cloud.
func New(ctx context.Context, opts Options) (Client, error) {
	parseOptions := []clouds.ParseOption{clouds.WithCloudName(opts.Cloud)}
	if opts.CloudsConfig != "" {
		parseOptions = append(parseOptions, clouds.WithLocations(opts.CloudsConfig))
	}

	auth, endpoint, tlsConfig, err := clouds.Parse(parseOptions...)
	if err != nil {
		return nil, fmt.Errorf("read cloud %q: %w", opts.Cloud, err)
	}

	// The plugin outlives its token.
	auth.AllowReauth = true

	provider, err := config.NewProviderClient(ctx, auth,
		config.WithHTTPClient(http.Client{Timeout: requestTimeout}),
		config.WithTLSConfig(tlsConfig))
	if err != nil {
		return nil, fmt.Errorf("authenticate to cloud %q: %w", opts.Cloud, err)
	}
	provider.RetryFunc = retrier{delay: firstRetryDelay}.retry

	nova, err := openstack.NewComputeV2(provider, endpoint)
	if err != nil {
		return nil, fmt.Errorf("find the compute endpoint: %w", err)
	}

	versioned, err := utils.RequireMicroversion(ctx, *nova, microversion)
	if err != nil {
		return nil, fmt.Errorf("negotiate the compute API version: %w", err)
	}

	glance, err := openstack.NewImageV2(provider, endpoint)
	if err != nil {
		return nil, fmt.Errorf("find the image endpoint: %w", err)
	}

	neutron, err := openstack.NewNetworkV2(provider, endpoint)
	if err != nil {
		return nil, fmt.Errorf("find the network endpoint: %w", err)
	}

	return &openStack{
		region:  endpoint.Region,
		nova:    &versioned,
		glance:  glance,
		neutron: neutron,
	}, nil
}

func (o *openStack) Region() string {
	return o.region
}

func (o *openStack) ListServers(ctx context.Context, tag string) ([]Server, error) {
	pages, err := servers.List(o.nova, servers.ListOpts{Tags: tag}).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list servers tagged %q: %w", tag, err)
	}

	var found []server
	if err := servers.ExtractServersInto(pages, &found); err != nil {
		return nil, fmt.Errorf("read servers tagged %q: %w", tag, err)
	}

	result := make([]Server, len(found))
	for i, s := range found {
		result[i] = s.toServer()
	}

	return result, nil
}

func (o *openStack) GetServer(ctx context.Context, id string) (Server, error) {
	var s server

	err := servers.Get(ctx, o.nova, id).ExtractInto(&s)
	if gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		return Server{}, fmt.Errorf("server %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Server{}, fmt.Errorf("get server %s: %w", id, err)
	}

	return s.toServer(), nil
}

func (o *openStack) CreateServer(ctx context.Context, req CreateRequest) (string, error) {
	attached := make([]servers.Network, len(req.NetworkIDs))
	for i, id := range req.NetworkIDs {
		attached[i] = servers.Network{UUID: id}
	}

	// gophercloud only encodes user data that is not valid base64 already.
	var userData []byte
	if req.UserData != "" {
		userData = []byte(base64.StdEncoding.EncodeToString([]byte(req.UserData)))
	}

	var opts servers.CreateOptsBuilder = servers.CreateOpts{
		Name:             req.Name,
		FlavorRef:        req.FlavorID,
		ImageRef:         req.ImageID,
		Networks:         attached,
		SecurityGroups:   req.SecurityGroupIDs,
		UserData:         userData,
		Metadata:         req.Metadata,
		Tags:             req.Tags,
		AvailabilityZone: req.AvailabilityZone,
	}
	if req.KeyName != "" {
		opts = keypairs.CreateOptsExt{CreateOptsBuilder: opts, KeyName: req.KeyName}
	}

	created, err := servers.Create(ctx, o.nova, opts, nil).Extract()
	if err != nil {
		return "", fmt.Errorf("create server %s: %w", req.Name, err)
	}

	return created.ID, nil
}

func (o *openStack) DeleteServer(ctx context.Context, id string) error {
	err := servers.Delete(ctx, o.nova, id).ExtractErr()
	if err != nil && !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		return fmt.Errorf("delete server %s: %w", id, err)
	}

	return nil
}

func (o *openStack) FindFlavor(ctx context.Context, ref string) (string, error) {
	pages, err := flavors.ListDetail(o.nova, nil).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("list flavors: %w", err)
	}

	all, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return "", fmt.Errorf("read flavors: %w", err)
	}

	return pick("flavor", ref, all, func(f flavors.Flavor) (string, string) { return f.ID, f.Name })
}

func (o *openStack) FindNetwork(ctx context.Context, ref string) (string, error) {
	pages, err := networks.List(o.neutron, nil).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("list networks: %w", err)
	}

	all, err := networks.ExtractNetworks(pages)
	if err != nil {
		return "", fmt.Errorf("read networks: %w", err)
	}

	return pick("network", ref, all, func(n networks.Network) (string, string) { return n.ID, n.Name })
}

func (o *openStack) FindSecurityGroup(ctx context.Context, ref string) (string, error) {
	pages, err := groups.List(o.neutron, groups.ListOpts{}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("list security groups: %w", err)
	}

	all, err := groups.ExtractGroups(pages)
	if err != nil {
		return "", fmt.Errorf("read security groups: %w", err)
	}

	return pick("security group", ref, all, func(g groups.SecGroup) (string, string) { return g.ID, g.Name })
}

func (o *openStack) CheckKeyPair(ctx context.Context, name string) error {
	_, err := keypairs.Get(ctx, o.nova, name, nil).Extract()
	if gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		return fmt.Errorf("key pair %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("get key pair %q: %w", name, err)
	}

	return nil
}

func (o *openStack) FindImage(ctx context.Context, ref string) (Image, error) {
	opts := images.ListOpts{
		Name:   ref,
		Status: images.ImageStatusActive,
		Sort:   "created_at:desc",
	}

	pages, err := images.List(o.glance, opts).AllPages(ctx)
	if err != nil {
		return Image{}, fmt.Errorf("list images named %q: %w", ref, err)
	}

	named, err := images.ExtractImages(pages)
	if err != nil {
		return Image{}, fmt.Errorf("read images named %q: %w", ref, err)
	}

	if len(named) > 0 {
		return toImage(named[0]), nil
	}

	return o.GetImage(ctx, ref)
}

func (o *openStack) GetImage(ctx context.Context, id string) (Image, error) {
	image, err := images.Get(ctx, o.glance, id).Extract()
	if gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		return Image{}, fmt.Errorf("image %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Image{}, fmt.Errorf("get image %q: %w", id, err)
	}

	return toImage(*image), nil
}

// pick returns the ID of the item with ID ref, or else of the one named ref.
func pick[T any](kind, ref string, items []T, key func(T) (id, name string)) (string, error) {
	var named []string

	for _, item := range items {
		id, name := key(item)
		if id == ref {
			return id, nil
		}
		if name == ref {
			named = append(named, id)
		}
	}

	switch len(named) {
	case 0:
		return "", fmt.Errorf("%s %q: %w", kind, ref, ErrNotFound)
	case 1:
		return named[0], nil
	default:
		return "", fmt.Errorf("%s %q: %d share that name, use an ID", kind, ref, len(named))
	}
}

func toImage(image images.Image) Image {
	return Image{
		ID:           image.ID,
		OSType:       stringProperty(image.Properties, "os_type"),
		Architecture: stringProperty(image.Properties, "architecture"),
	}
}

func stringProperty(properties map[string]any, key string) string {
	value, _ := properties[key].(string)

	return value
}

// server is a Nova server as JSON.
type server struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	TaskState string    `json:"OS-EXT-STS:task_state"`
	Created   time.Time `json:"created"`
	Image     imageRef  `json:"image"`
	Fault     struct {
		Message string `json:"message"`
	} `json:"fault"`
	Addresses map[string][]struct {
		Addr    string `json:"addr"`
		Version int    `json:"version"`
		Type    string `json:"OS-EXT-IPS:type"`
	} `json:"addresses"`
}

func (s server) toServer() Server {
	result := Server{
		ID:        s.ID,
		Name:      s.Name,
		Status:    s.Status,
		TaskState: s.TaskState,
		Created:   s.Created,
		ImageID:   s.Image.ID,
		Fault:     s.Fault.Message,
	}

	for _, network := range slices.Sorted(maps.Keys(s.Addresses)) {
		for _, a := range s.Addresses[network] {
			result.Addresses = append(result.Addresses, Address{
				Network:  network,
				IP:       a.Addr,
				Version:  a.Version,
				Floating: a.Type == "floating",
			})
		}
	}

	return result
}

// imageRef is Nova's image field: an object, or "" for a volume-backed server.
type imageRef struct {
	ID string
}

func (r *imageRef) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '{' {
		return nil
	}

	var image struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &image); err != nil {
		return fmt.Errorf("read the server image: %w", err)
	}

	r.ID = image.ID

	return nil
}
