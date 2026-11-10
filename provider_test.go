package openstack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"

	"github.com/radome-io/fleeting-plugin-openstack/internal/cloud"
	"github.com/radome-io/fleeting-plugin-openstack/internal/cloud/fake"
)

const groupTag = "fleeting:runner"

var staticCredentials = provider.Settings{
	ConnectorConfig: provider.ConnectorConfig{Username: "runner", UseStaticCredentials: true},
}

func newBackend() *fake.Client {
	return &fake.Client{
		RegionName:     "RegionOne",
		Flavors:        map[string]string{"m1.large": "flavor-large"},
		Networks:       map[string]string{"public": "network-public"},
		SecurityGroups: map[string]string{"ssh-from-manager": "group-ssh"},
		ServerGroups:   map[string]string{"workers": "group-workers"},
		KeyPairs:       map[string]bool{"runner": true},
		Images: map[string]cloud.Image{
			"Ubuntu 24.04": {ID: "image-ubuntu", OSType: "linux", Architecture: "x86_64"},
		},
	}
}

func newGroup(backend cloud.Client, config Config) *InstanceGroup {
	return &InstanceGroup{
		Config: config,
		connect: func(context.Context, cloud.Options) (cloud.Client, error) {
			return backend, nil
		},
	}
}

func initGroup(t *testing.T, backend cloud.Client, config Config) *InstanceGroup {
	t.Helper()

	g := newGroup(backend, config)
	_, err := g.Init(t.Context(), hclog.NewNullLogger(), staticCredentials)
	require.NoError(t, err)

	return g
}

func states(t *testing.T, g *InstanceGroup) map[string]provider.State {
	t.Helper()

	result := make(map[string]provider.State)
	err := g.Update(t.Context(), func(id string, state provider.State) { result[id] = state })
	require.NoError(t, err)

	return result
}

func TestPluginConfigDecodesIntoTheGroup(t *testing.T) {
	var g InstanceGroup

	err := json.Unmarshal([]byte(`{"cloud": "mycloud", "name": "runner", "server": {"flavor": "m1.large"}}`), &g)

	require.NoError(t, err)
	assert.Equal(t, "mycloud", g.Cloud)
	assert.Equal(t, "runner", g.Name)
	assert.Equal(t, "m1.large", g.Server.Flavor)
}

func TestPluginConfigRejectsUnknownKeys(t *testing.T) {
	var g InstanceGroup

	err := json.Unmarshal([]byte(`{"cloud": "mycloud", "server": {"user_dta_file": "/etc/cloud-init.yaml"}}`), &g)

	require.ErrorContains(t, err, `unknown field "user_dta_file"`)
}

func TestInit(t *testing.T) {
	g := newGroup(newBackend(), validConfig())

	info, err := g.Init(t.Context(), hclog.NewNullLogger(), staticCredentials)

	require.NoError(t, err)
	assert.Equal(t, "openstack/RegionOne/runner", info.ID)
	assert.Equal(t, maxSize, info.MaxSize)
	assert.Empty(t, info.Capabilities)
}

func TestInitFails(t *testing.T) {
	tests := []struct {
		name     string
		change   func(*Config)
		settings provider.Settings
		err      string
	}{
		{
			name:     "invalid configuration",
			change:   func(c *Config) { c.Cloud = "" },
			settings: staticCredentials,
			err:      "plugin_config: cloud is required",
		},
		{
			name:   "no static credentials",
			change: func(*Config) {},
			err:    "connector_config: use_static_credentials must be true",
		},
		{
			name:     "unknown flavor",
			change:   func(c *Config) { c.Server.Flavor = "m1.missing" },
			settings: staticCredentials,
			err:      `server.flavor: flavor "m1.missing": not found`,
		},
		{
			name:     "unknown network",
			change:   func(c *Config) { c.Server.Networks = []string{"public", "ci"} },
			settings: staticCredentials,
			err:      `server.networks: network "ci": not found`,
		},
		{
			name:     "address network not among the networks",
			change:   func(c *Config) { c.AddressNetwork = "ci" },
			settings: staticCredentials,
			err:      `address_network "ci" must be one of server.networks`,
		},
		{
			name:     "unknown security group",
			change:   func(c *Config) { c.Server.SecurityGroups = []string{"ssh-from-anywhere"} },
			settings: staticCredentials,
			err:      `server.security_groups: security group "ssh-from-anywhere": not found`,
		},
		{
			name:     "unknown server group",
			change:   func(c *Config) { c.Server.ServerGroup = "spread" },
			settings: staticCredentials,
			err:      `server.server_group: server group "spread": not found`,
		},
		{
			name:     "unknown key pair",
			change:   func(c *Config) { c.Server.KeyName = "runer" },
			settings: staticCredentials,
			err:      `server.key_name: key pair "runer": not found`,
		},
		{
			name:     "unknown image",
			change:   func(c *Config) { c.Server.Image = "Debian 99" },
			settings: staticCredentials,
			err:      `server.image: image "Debian 99": not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			tt.change(&config)
			g := newGroup(newBackend(), config)

			_, err := g.Init(t.Context(), hclog.NewNullLogger(), tt.settings)

			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestInitFailsToConnect(t *testing.T) {
	g := &InstanceGroup{
		Config: validConfig(),
		connect: func(context.Context, cloud.Options) (cloud.Client, error) {
			return nil, errors.New(`authenticate to cloud "mycloud": 401`)
		},
	}

	_, err := g.Init(t.Context(), hclog.NewNullLogger(), staticCredentials)

	require.EqualError(t, err, `authenticate to cloud "mycloud": 401`)
}

func TestUserDataFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud-init.yaml")
	require.NoError(t, os.WriteFile(path, []byte("#cloud-config\nruncmd: [true]\n"), 0o600))

	backend := newBackend()
	config := validConfig()
	config.Server.UserDataFile = path
	g := initGroup(t, backend, config)

	_, err := g.Increase(t.Context(), 1)

	require.NoError(t, err)
	assert.Equal(t, "#cloud-config\nruncmd: [true]\n", backend.Created()[0].UserData)
}

func TestUserDataFileFails(t *testing.T) {
	tooLarge := filepath.Join(t.TempDir(), "too-large.yaml")
	require.NoError(t, os.WriteFile(tooLarge, []byte(strings.Repeat("a", 49150)), 0o600))

	tests := []struct {
		name string
		path string
		err  string
	}{
		{name: "missing", path: filepath.Join(t.TempDir(), "missing.yaml"), err: "server.user_data_file: open "},
		{name: "too large", path: tooLarge, err: "65536 bytes once base64-encoded, more than the 65535 Nova accepts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			config.Server.UserDataFile = tt.path
			g := newGroup(newBackend(), config)

			_, err := g.Init(t.Context(), hclog.NewNullLogger(), staticCredentials)

			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestUpdateReportsTheGroupOnly(t *testing.T) {
	backend := newBackend()
	backend.Add(cloud.Server{ID: "building", Status: "BUILD"}, groupTag)
	backend.Add(cloud.Server{ID: "running", Status: "ACTIVE"}, groupTag)
	backend.Add(cloud.Server{ID: "leaving", Status: "ACTIVE", TaskState: "deleting"}, groupTag)
	backend.Add(cloud.Server{ID: "other-group", Status: "ACTIVE"}, "fleeting:other")
	backend.Add(cloud.Server{ID: "untagged", Status: "ACTIVE"})
	g := initGroup(t, backend, validConfig())

	assert.Equal(t, map[string]provider.State{
		"building": provider.StateCreating,
		"running":  provider.StateRunning,
		"leaving":  provider.StateDeleting,
	}, states(t, g))
	assert.Empty(t, backend.Deleted())
}

func TestCustomTag(t *testing.T) {
	backend := newBackend()
	backend.Add(cloud.Server{ID: "tagged", Status: "ACTIVE"}, "ci")
	backend.Add(cloud.Server{ID: "default-tag", Status: "ACTIVE"}, groupTag)
	config := validConfig()
	config.Tag = "ci"
	g := initGroup(t, backend, config)

	assert.Equal(t, map[string]provider.State{"tagged": provider.StateRunning}, states(t, g))

	_, err := g.Increase(t.Context(), 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"ci"}, backend.Created()[0].Tags)
}

func TestUpdateDeletesBrokenServers(t *testing.T) {
	backend := newBackend()
	backend.Add(cloud.Server{ID: "failed", Status: "ERROR", Fault: "No valid host was found."}, groupTag)
	backend.Add(cloud.Server{ID: "stopped", Status: "SHUTOFF"}, groupTag)
	g := initGroup(t, backend, validConfig())

	want := map[string]provider.State{
		"failed":  provider.StateDeleting,
		"stopped": provider.StateDeleting,
	}
	assert.Equal(t, want, states(t, g))
	assert.ElementsMatch(t, []string{"failed", "stopped"}, backend.Deleted())

	assert.Equal(t, want, states(t, g), "Nova lists them while it deletes them")
	assert.Equal(t, 1, backend.DeleteAttempts("failed"), "a server being deleted is not deleted again")
}

func TestUpdateDeletesServersStuckBuilding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := newBackend()
		backend.Add(cloud.Server{ID: "stuck", Status: "BUILD", Created: time.Now()}, groupTag)
		time.Sleep(15 * time.Minute)
		backend.Add(cloud.Server{ID: "building", Status: "BUILD", Created: time.Now()}, groupTag)
		time.Sleep(time.Minute)
		g := initGroup(t, backend, validConfig())

		assert.Equal(t, map[string]provider.State{
			"stuck":    provider.StateDeleting,
			"building": provider.StateCreating,
		}, states(t, g))
		assert.Equal(t, []string{"stuck"}, backend.Deleted())
	})
}

func TestUpdateHonorsTheBuildTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := newBackend()
		backend.Add(cloud.Server{ID: "slow", Status: "BUILD", Created: time.Now()}, groupTag)
		time.Sleep(16 * time.Minute)
		config := validConfig()
		config.BuildTimeout = "30m"
		g := initGroup(t, backend, config)

		assert.Equal(t, map[string]provider.State{"slow": provider.StateCreating}, states(t, g))
	})
}

func TestUpdateRetriesFailedDeletionsOnceAMinute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := newBackend()
		backend.Add(cloud.Server{ID: "failed", Status: "ERROR"}, groupTag)
		backend.Locked = map[string]bool{"failed": true}
		g := initGroup(t, backend, validConfig())

		for range 3 {
			assert.Equal(t, map[string]provider.State{"failed": provider.StateDeleting}, states(t, g))
		}
		assert.Equal(t, 1, backend.DeleteAttempts("failed"))

		time.Sleep(time.Minute)
		states(t, g)
		assert.Equal(t, 2, backend.DeleteAttempts("failed"))
	})
}

func TestUpdateFailsWhenListingFails(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	backend.ListErr = errors.New("compute API unavailable")

	err := g.Update(t.Context(), func(string, provider.State) {})

	require.EqualError(t, err, "compute API unavailable")
}

func TestIncrease(t *testing.T) {
	backend := newBackend()
	config := validConfig()
	config.Server.SecurityGroups = []string{"ssh-from-manager"}
	config.Server.KeyName = "runner"
	config.Server.UserData = "#cloud-config\n"
	config.Server.ConfigDrive = true
	config.Server.Metadata = map[string]string{"purpose": "ci"}
	config.Server.ServerGroup = "workers"
	g := initGroup(t, backend, config)

	succeeded, err := g.Increase(t.Context(), 2)

	require.NoError(t, err)
	assert.Equal(t, 2, succeeded)

	created := backend.Created()
	require.Len(t, created, 2)
	assert.NotEqual(t, created[0].Name, created[1].Name)

	for _, req := range created {
		assert.Regexp(t, `^runner-[a-z2-7]{8}$`, req.Name)
		assert.Equal(t, cloud.CreateRequest{
			Name:             req.Name,
			FlavorID:         "flavor-large",
			ImageID:          "image-ubuntu",
			NetworkIDs:       []string{"network-public"},
			SecurityGroupIDs: []string{"group-ssh"},
			KeyName:          "runner",
			UserData:         "#cloud-config\n",
			ConfigDrive:      true,
			Metadata:         map[string]string{"purpose": "ci"},
			Tags:             []string{groupTag},
			ServerGroupID:    "group-workers",
		}, req)
	}

	assert.Len(t, states(t, g), 2)
}

func TestIncreaseTagsServers(t *testing.T) {
	backend := newBackend()
	config := validConfig()
	config.Server.Tags = []string{"team:ci", "env:prod"}
	g := initGroup(t, backend, config)

	_, err := g.Increase(t.Context(), 1)

	require.NoError(t, err)
	assert.Equal(t, []string{groupTag, "team:ci", "env:prod"}, backend.Created()[0].Tags)
}

func TestIncreaseUsesTheCurrentImage(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	backend.Images["Ubuntu 24.04"] = cloud.Image{ID: "image-ubuntu-rebuilt"}

	_, err := g.Increase(t.Context(), 1)

	require.NoError(t, err)
	assert.Equal(t, "image-ubuntu-rebuilt", backend.Created()[0].ImageID)
}

func TestIncreaseBacksOffWhileServersFail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs strings.Builder
		backend := newBackend()
		g := newGroup(backend, validConfig())
		_, err := g.Init(t.Context(), hclog.New(&hclog.LoggerOptions{Output: &logs}), staticCredentials)
		require.NoError(t, err)

		_, err = g.Increase(t.Context(), 1)
		require.NoError(t, err)
		require.NoError(t, backend.Set(cloud.Server{ID: "server-1", Status: "ERROR", Fault: "No valid host was found."}))
		states(t, g)

		for range 3 {
			succeeded, err := g.Increase(t.Context(), 1)
			require.NoError(t, err, "a pause is not an error")
			assert.Zero(t, succeeded)
		}
		assert.Len(t, backend.Created(), 1, "no server is requested while paused")
		assert.Equal(t, 1, strings.Count(logs.String(), "pausing scale-ups"), "a pause is logged once")
		assert.Contains(t, logs.String(), `reason="ERROR: No valid host was found."`)

		time.Sleep(30 * time.Second)
		succeeded, err := g.Increase(t.Context(), 3)
		require.NoError(t, err)
		assert.Equal(t, 1, succeeded, "after the pause, one server is tried")

		succeeded, err = g.Increase(t.Context(), 3)
		require.NoError(t, err)
		assert.Zero(t, succeeded, "nothing more until the tried server comes up")

		require.NoError(t, backend.Set(cloud.Server{ID: "server-2", Status: "ACTIVE"}))
		states(t, g)
		backend.Add(cloud.Server{ID: "server-9", Status: "ERROR"}, groupTag)
		states(t, g)

		succeeded, err = g.Increase(t.Context(), 3)
		require.NoError(t, err)
		assert.Equal(t, 3, succeeded, "the tried server came up; an older server's failure does not count")
		assert.Len(t, backend.Created(), 5)
	})
}

func TestIncreaseCountsServersFailingTogetherOnce(t *testing.T) {
	var logs strings.Builder
	backend := newBackend()
	g := newGroup(backend, validConfig())
	_, err := g.Init(t.Context(), hclog.New(&hclog.LoggerOptions{Output: &logs}), staticCredentials)
	require.NoError(t, err)

	_, err = g.Increase(t.Context(), 6)
	require.NoError(t, err)
	require.Len(t, states(t, g), 6)

	for i := 1; i <= 6; i++ {
		require.NoError(t, backend.Set(cloud.Server{ID: fmt.Sprintf("server-%d", i), Status: "ERROR"}))
	}
	states(t, g)

	assert.Equal(t, 1, strings.Count(logs.String(), "pausing scale-ups"))
	assert.Contains(t, logs.String(), "failures_in_a_row=1 pause=30s")
}

func TestIncreaseBacksOffAfterABuildTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := newBackend()
		g := initGroup(t, backend, validConfig())

		_, err := g.Increase(t.Context(), 1)
		require.NoError(t, err)

		time.Sleep(16 * time.Minute)

		assert.Equal(t, map[string]provider.State{"server-1": provider.StateDeleting}, states(t, g))
		succeeded, err := g.Increase(t.Context(), 1)
		require.NoError(t, err)
		assert.Zero(t, succeeded, "a server stuck building pauses scale-ups")
	})
}

func TestIncreaseIgnoresServersDeletedWhileBuilding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := newBackend()
		g := initGroup(t, backend, validConfig())

		_, err := g.Increase(t.Context(), 2)
		require.NoError(t, err)
		require.Len(t, states(t, g), 2)

		// Nova never finishes deleting server-1: Reap is not called.
		_, err = g.Decrease(t.Context(), []string{"server-1"})
		require.NoError(t, err)
		require.NoError(t, backend.Set(cloud.Server{ID: "server-2", Status: "ERROR"}))
		states(t, g)

		time.Sleep(30 * time.Second)
		succeeded, err := g.Increase(t.Context(), 1)

		require.NoError(t, err)
		assert.Equal(t, 1, succeeded, "a server deleted while building is not waited for")
	})
}

func TestIncreaseFails(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	backend.CreateErr = errors.New("flavor too small for the image")

	succeeded, err := g.Increase(t.Context(), 3)

	assert.Zero(t, succeeded)
	require.EqualError(t, err, "flavor too small for the image")
	assert.Equal(t, 1, backend.CreateAttempts(), "the first failure stops the scale-up")

	succeeded, err = g.Increase(t.Context(), 3)

	assert.Zero(t, succeeded)
	require.NoError(t, err, "a scale-up that created nothing pauses the next ones")
	assert.Equal(t, 1, backend.CreateAttempts())
}

func TestIncreaseCanceledStartsNoPause(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	backend.CreateErr = context.Canceled

	_, err := g.Increase(ctx, 1)
	require.ErrorIs(t, err, context.Canceled)

	backend.CreateErr = nil
	succeeded, err := g.Increase(t.Context(), 1)
	require.NoError(t, err)
	assert.Equal(t, 1, succeeded, "a scale-up canceled at shutdown is not a failure of the cloud")
}

func TestIncreaseReportsAPartialScaleUp(t *testing.T) {
	backend := newBackend()
	backend.Quota = 1
	g := initGroup(t, backend, validConfig())

	succeeded, err := g.Increase(t.Context(), 3)

	require.NoError(t, err, "an error would make fleeting forget the server created")
	assert.Equal(t, 1, succeeded)
	assert.Len(t, backend.Created(), 1)
	assert.Equal(t, 2, backend.CreateAttempts())
}

func TestIncreaseFailsWithoutTheImage(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	delete(backend.Images, "Ubuntu 24.04")

	succeeded, err := g.Increase(t.Context(), 1)

	assert.Zero(t, succeeded)
	require.ErrorIs(t, err, cloud.ErrNotFound)
	assert.Empty(t, backend.Created())
}

func TestDecrease(t *testing.T) {
	backend := newBackend()
	backend.Add(cloud.Server{ID: "a", Status: "ACTIVE"}, groupTag)
	g := initGroup(t, backend, validConfig())

	succeeded, err := g.Decrease(t.Context(), []string{"a", "already-gone"})

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "already-gone"}, succeeded)
	assert.Equal(t, map[string]provider.State{"a": provider.StateDeleting}, states(t, g))
}

func TestDecreaseReportsAPartialDeletion(t *testing.T) {
	backend := newBackend()
	for _, id := range []string{"a", "b", "c"} {
		backend.Add(cloud.Server{ID: id, Status: "ACTIVE"}, groupTag)
	}
	backend.Locked = map[string]bool{"b": true}
	g := initGroup(t, backend, validConfig())

	succeeded, err := g.Decrease(t.Context(), []string{"a", "b", "c"})

	require.NoError(t, err, "an error would make fleeting forget the deletion requested")
	assert.Equal(t, []string{"a"}, succeeded)
	assert.Zero(t, backend.DeleteAttempts("c"), "the first failure stops the deletions")
}

func TestDecreaseFails(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	backend.DeleteErr = errors.New("compute API unavailable")

	succeeded, err := g.Decrease(t.Context(), []string{"a", "b"})

	assert.Empty(t, succeeded)
	require.EqualError(t, err, "compute API unavailable")
	assert.Zero(t, backend.DeleteAttempts("b"))
}

func TestConnectInfo(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	backend.Add(cloud.Server{
		ID:        "a",
		Status:    "ACTIVE",
		ImageID:   "image-ubuntu",
		Addresses: []cloud.Address{{Network: "public", IP: "203.0.113.10", Version: 4}},
	}, groupTag)

	info, err := g.ConnectInfo(t.Context(), "a")

	require.NoError(t, err)
	assert.Equal(t, provider.ConnectInfo{
		ConnectorConfig: provider.ConnectorConfig{
			OS:                   "linux",
			Arch:                 "amd64",
			Protocol:             provider.ProtocolSSH,
			Username:             "runner",
			UseStaticCredentials: true,
		},
		ID:           "a",
		InternalAddr: "203.0.113.10",
		ExternalAddr: "203.0.113.10",
	}, info)
}

func TestConnectInfoOnTheAddressNetwork(t *testing.T) {
	for _, ref := range []string{"ci", "network-ci"} {
		t.Run(ref, func(t *testing.T) {
			backend := newBackend()
			backend.Networks["ci"] = "network-ci"
			config := validConfig()
			config.Server.Networks = []string{"public", "ci"}
			config.AddressNetwork = ref
			g := initGroup(t, backend, config)
			backend.Add(cloud.Server{
				ID:      "a",
				Status:  "ACTIVE",
				ImageID: "image-ubuntu",
				Addresses: []cloud.Address{
					{Network: "ci", IP: "10.0.0.5", Version: 4},
					{Network: "public", IP: "203.0.113.10", Version: 4},
				},
			}, groupTag)

			info, err := g.ConnectInfo(t.Context(), "a")

			require.NoError(t, err)
			assert.Equal(t, "10.0.0.5", info.InternalAddr)
		})
	}
}

func TestConnectInfoPlatform(t *testing.T) {
	tests := []struct {
		name     string
		imageID  string
		images   map[string]cloud.Image
		os       string
		arch     string
		protocol provider.Protocol
	}{
		{
			name:     "from the image properties",
			imageID:  "image-arm",
			images:   map[string]cloud.Image{"Debian": {ID: "image-arm", OSType: "linux", Architecture: "aarch64"}},
			os:       "linux",
			arch:     "arm64",
			protocol: provider.ProtocolSSH,
		},
		{
			name:     "windows image",
			imageID:  "image-windows",
			images:   map[string]cloud.Image{"Windows": {ID: "image-windows", OSType: "windows", Architecture: "x86_64"}},
			os:       "windows",
			arch:     "amd64",
			protocol: provider.ProtocolWinRM,
		},
		{
			name:     "image without properties",
			imageID:  "image-bare",
			images:   map[string]cloud.Image{"Bare": {ID: "image-bare"}},
			os:       "linux",
			arch:     "amd64",
			protocol: provider.ProtocolSSH,
		},
		{
			name:     "deleted image",
			imageID:  "image-deleted",
			os:       "linux",
			arch:     "amd64",
			protocol: provider.ProtocolSSH,
		},
		{
			name:     "volume-backed server",
			os:       "linux",
			arch:     "amd64",
			protocol: provider.ProtocolSSH,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newBackend()
			g := initGroup(t, backend, validConfig())
			backend.Images = tt.images
			backend.Add(cloud.Server{
				ID:        "a",
				Status:    "ACTIVE",
				ImageID:   tt.imageID,
				Addresses: []cloud.Address{{Network: "public", IP: "203.0.113.10", Version: 4}},
			}, groupTag)

			info, err := g.ConnectInfo(t.Context(), "a")

			require.NoError(t, err)
			assert.Equal(t, tt.os, info.OS)
			assert.Equal(t, tt.arch, info.Arch)
			assert.Equal(t, tt.protocol, info.Protocol)
		})
	}
}

func TestGoArch(t *testing.T) {
	tests := map[string]string{
		"":        "amd64",
		"x86_64":  "amd64",
		"aarch64": "arm64",
		"i686":    "386",
		"armv7l":  "arm",
		"ppc64le": "ppc64le",
	}

	for architecture, arch := range tests {
		assert.Equal(t, arch, goArch(architecture), architecture)
	}
}

func TestConnectInfoKeepsTheConnectorSettings(t *testing.T) {
	backend := newBackend()
	g := newGroup(backend, validConfig())

	settings := staticCredentials
	settings.OS = "windows"
	settings.Arch = "arm64"
	settings.Protocol = provider.ProtocolSSH
	_, err := g.Init(t.Context(), hclog.NewNullLogger(), settings)
	require.NoError(t, err)

	backend.Add(cloud.Server{
		ID:        "a",
		Status:    "ACTIVE",
		ImageID:   "image-ubuntu",
		Addresses: []cloud.Address{{Network: "public", IP: "203.0.113.10", Version: 4}},
	}, groupTag)

	info, err := g.ConnectInfo(t.Context(), "a")

	require.NoError(t, err)
	assert.Equal(t, "windows", info.OS)
	assert.Equal(t, "arm64", info.Arch, "the image says x86_64")
	assert.Equal(t, provider.ProtocolSSH, info.Protocol, "windows alone would mean winrm")
}

func TestConnectInfoFailsWhenTheImageCannotBeRead(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	backend.Add(cloud.Server{
		ID:        "a",
		Status:    "ACTIVE",
		ImageID:   "image-unknown",
		Addresses: []cloud.Address{{Network: "public", IP: "203.0.113.10", Version: 4}},
	}, groupTag)
	backend.GetImageErr = errors.New("image API unavailable")

	_, err := g.ConnectInfo(t.Context(), "a")

	require.EqualError(t, err, "read the platform of image image-unknown: image API unavailable")
}

func TestConnectInfoFails(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	backend.Add(cloud.Server{ID: "building", Status: "BUILD"}, groupTag)
	backend.Add(cloud.Server{ID: "leaving", Status: "ACTIVE", TaskState: "deleting"}, groupTag)

	_, err := g.ConnectInfo(t.Context(), "building")
	require.EqualError(t, err, "server building is not running: BUILD")

	_, err = g.ConnectInfo(t.Context(), "leaving")
	require.EqualError(t, err, "server leaving is not running: being deleted")

	_, err = g.ConnectInfo(t.Context(), "gone")
	require.ErrorIs(t, err, cloud.ErrNotFound)
}

func TestHeartbeat(t *testing.T) {
	backend := newBackend()
	g := initGroup(t, backend, validConfig())
	backend.Add(cloud.Server{ID: "running", Status: "ACTIVE"}, groupTag)
	backend.Add(cloud.Server{ID: "rebooting", Status: "REBOOT"}, groupTag)
	backend.Add(cloud.Server{ID: "failed", Status: "ERROR"}, groupTag)

	require.NoError(t, g.Heartbeat(t.Context(), "running"))
	require.NoError(t, g.Heartbeat(t.Context(), "rebooting"), "a reboot ends by itself")
	require.ErrorIs(t, g.Heartbeat(t.Context(), "failed"), provider.ErrInstanceUnhealthy)
	require.ErrorIs(t, g.Heartbeat(t.Context(), "gone"), provider.ErrInstanceUnhealthy)
}

func TestHeartbeatIgnoresAPIErrors(t *testing.T) {
	g := initGroup(t, newBackend(), validConfig())
	g.client = unreachable{g.client}

	require.NoError(t, g.Heartbeat(t.Context(), "running"))
}

func TestSuspendAndResumeAreNotSupported(t *testing.T) {
	g := initGroup(t, newBackend(), validConfig())

	_, err := g.Suspend(t.Context(), []string{"a"})
	require.ErrorIs(t, err, provider.ErrSuspendResumeNotSupported)

	_, err = g.Resume(t.Context(), []string{"a"})
	require.ErrorIs(t, err, provider.ErrSuspendResumeNotSupported)
}

// unreachable fails server lookups.
type unreachable struct {
	cloud.Client
}

func (unreachable) GetServer(context.Context, string) (cloud.Server, error) {
	return cloud.Server{}, errors.New("compute API unavailable")
}
