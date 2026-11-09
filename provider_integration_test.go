package openstack

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gitlab.com/gitlab-org/fleeting/fleeting/integration"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"

	"github.com/radome-io/fleeting-plugin-openstack/internal/cloud"
)

// TestProvisioning runs fleeting's provisioning test against a real cloud. It
// creates billed servers: it is skipped without FLEETING_OPENSTACK_CLOUD.
func TestProvisioning(t *testing.T) {
	cloudName := os.Getenv("FLEETING_OPENSTACK_CLOUD")
	if cloudName == "" {
		t.Skip("FLEETING_OPENSTACK_CLOUD is not set")
	}

	key, err := os.ReadFile(requireEnv(t, "FLEETING_OPENSTACK_SSH_KEY_FILE"))
	require.NoError(t, err)

	config := Config{
		Cloud:        cloudName,
		CloudsConfig: os.Getenv("FLEETING_OPENSTACK_CLOUDS_CONFIG"),
		Name:         "fleeting-test-" + strings.ToLower(rand.Text()[:8]),
		Server: ServerConfig{
			Flavor:   requireEnv(t, "FLEETING_OPENSTACK_FLAVOR"),
			Image:    requireEnv(t, "FLEETING_OPENSTACK_IMAGE"),
			Networks: []string{requireEnv(t, "FLEETING_OPENSTACK_NETWORK")},
			KeyName:  requireEnv(t, "FLEETING_OPENSTACK_KEY_NAME"),
		},
	}
	t.Cleanup(func() { deleteGroup(t, config) })

	plugin := integration.BuildPluginBinary(t, "cmd/fleeting-plugin-openstack", "fleeting-plugin-openstack")

	integration.TestProvisioning(t, plugin, integration.Config{
		PluginConfig: config,
		ConnectorConfig: provider.ConnectorConfig{
			Username:             requireEnv(t, "FLEETING_OPENSTACK_SSH_USERNAME"),
			Key:                  key,
			UseStaticCredentials: true,
			Timeout:              10 * time.Minute,
		},
		MaxInstances:    3,
		UseExternalAddr: true,
	})
}

// deleteGroup deletes the servers a failed test left behind.
func deleteGroup(t *testing.T, config Config) {
	t.Helper()

	if err := deleteTagged(context.Background(), config); err != nil {
		t.Errorf("cleaning up: delete the servers tagged %s by hand: %v", config.tag(), err)
	}
}

func deleteTagged(ctx context.Context, config Config) error {
	client, err := cloud.New(ctx, cloud.Options{Cloud: config.Cloud, CloudsConfig: config.CloudsConfig})
	if err != nil {
		return err
	}

	servers, err := client.ListServers(ctx, config.tag())
	if err != nil {
		return err
	}

	for _, s := range servers {
		if err := client.DeleteServer(ctx, s.ID); err != nil {
			return err
		}
	}

	return nil
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()

	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is not set", name)
	}

	return value
}
