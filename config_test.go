package openstack

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validConfig() Config {
	return Config{
		Cloud: "mycloud",
		Name:  "runner",
		Server: ServerConfig{
			Flavor:   "m1.large",
			Image:    "Ubuntu 24.04",
			Networks: []string{"public"},
		},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
		err    string
	}{
		{name: "valid", change: func(*Config) {}},
		{name: "ipv6", change: func(c *Config) { c.AddressFamily = "ipv6" }},
		{name: "build timeout", change: func(c *Config) { c.BuildTimeout = "30m" }},
		{name: "server tags", change: func(c *Config) { c.Server.Tags = []string{"team:ci", "env:prod"} }},
		{name: "tag of 60 characters, not bytes", change: func(c *Config) { c.Tag = strings.Repeat("é", 60) }},
		{
			name: "longest metadata",
			change: func(c *Config) {
				c.Server.Metadata = map[string]string{strings.Repeat("k", 255): strings.Repeat("v", 255)}
			},
		},
		{name: "largest user data", change: func(c *Config) { c.Server.UserData = strings.Repeat("a", 49149) }},
		{
			name: "boot from a volume",
			change: func(c *Config) {
				c.Server.VolumeSize = 20
				c.Server.VolumeType = "ssd"
			},
		},
		{name: "longest name with the default tag", change: func(c *Config) { c.Name = strings.Repeat("a", 51) }},
		{
			name: "longest name with a tag",
			change: func(c *Config) {
				c.Name = strings.Repeat("a", 54)
				c.Tag = "ci"
			},
		},
		{
			name:   "no cloud",
			change: func(c *Config) { c.Cloud = "" },
			err:    "cloud is required",
		},
		{
			name:   "name too long",
			change: func(c *Config) { c.Name = strings.Repeat("a", 55) },
			err:    "must be 1 to 54 lowercase letters",
		},
		{
			name:   "name too long for the default tag",
			change: func(c *Config) { c.Name = strings.Repeat("a", 52) },
			err:    "must be 1 to 60 characters, without / or ,: set tag, or shorten name",
		},
		{
			name:   "tag too long",
			change: func(c *Config) { c.Tag = strings.Repeat("a", 61) },
			err:    "must be 1 to 60 characters, without / or ,",
		},
		{
			name:   "tag with a slash",
			change: func(c *Config) { c.Tag = "ci/worker" },
			err:    `tag "ci/worker" must be 1 to 60 characters, without / or ,`,
		},
		{
			name:   "name with a trailing dash",
			change: func(c *Config) { c.Name = "ci-" },
			err:    "must be 1 to 54 lowercase letters",
		},
		{
			name:   "name in capitals",
			change: func(c *Config) { c.Name = "CI" },
			err:    "must be 1 to 54 lowercase letters",
		},
		{
			name:   "no flavor",
			change: func(c *Config) { c.Server.Flavor = "" },
			err:    "server.flavor is required",
		},
		{
			name:   "no image",
			change: func(c *Config) { c.Server.Image = "" },
			err:    "server.image is required",
		},
		{
			name:   "negative volume size",
			change: func(c *Config) { c.Server.VolumeSize = -1 },
			err:    "server.volume_size -1 must be a positive number of GiB",
		},
		{
			name:   "volume type without a volume size",
			change: func(c *Config) { c.Server.VolumeType = "ssd" },
			err:    "server.volume_type needs server.volume_size",
		},
		{
			name:   "no network",
			change: func(c *Config) { c.Server.Networks = nil },
			err:    "server.networks needs at least one network",
		},
		{
			name:   "user data too large",
			change: func(c *Config) { c.Server.UserData = strings.Repeat("a", 49150) },
			err:    "server.user_data: 65536 bytes once base64-encoded, more than the 65535 Nova accepts",
		},
		{
			name: "user data and user data file",
			change: func(c *Config) {
				c.Server.UserData = "#cloud-config\n"
				c.Server.UserDataFile = "/etc/gitlab-runner/cloud-init.yaml"
			},
			err: "server.user_data and server.user_data_file exclude each other",
		},
		{
			name:   "server tag with a comma",
			change: func(c *Config) { c.Server.Tags = []string{"team,ci"} },
			err:    `server.tags: tag "team,ci" must be 1 to 60 characters, without / or ,`,
		},
		{
			name:   "empty server tag",
			change: func(c *Config) { c.Server.Tags = []string{""} },
			err:    `server.tags: tag "" must be 1 to 60 characters`,
		},
		{
			name:   "server tag listed twice",
			change: func(c *Config) { c.Server.Tags = []string{"ci", "ci"} },
			err:    `server.tags: "ci" is listed twice, or is the group tag`,
		},
		{
			name:   "server tag that is the group tag",
			change: func(c *Config) { c.Server.Tags = []string{"fleeting:runner"} },
			err:    `server.tags: "fleeting:runner" is listed twice, or is the group tag`,
		},
		{
			name: "too many server tags",
			change: func(c *Config) {
				for i := range 50 {
					c.Server.Tags = append(c.Server.Tags, fmt.Sprintf("tag-%d", i))
				}
			},
			err: "server.tags: servers would carry 51 tags with the group tag, more than the 50 Nova accepts",
		},
		{
			name:   "empty metadata key",
			change: func(c *Config) { c.Server.Metadata = map[string]string{"": "ci"} },
			err:    `server.metadata "": keys must be 1 to 255 characters, values at most 255`,
		},
		{
			name:   "metadata key too long",
			change: func(c *Config) { c.Server.Metadata = map[string]string{strings.Repeat("k", 256): "ci"} },
			err:    "keys must be 1 to 255 characters",
		},
		{
			name:   "metadata value too long",
			change: func(c *Config) { c.Server.Metadata = map[string]string{"purpose": strings.Repeat("v", 256)} },
			err:    `server.metadata "purpose": keys must be 1 to 255 characters, values at most 255`,
		},
		{
			name:   "build timeout that is not a duration",
			change: func(c *Config) { c.BuildTimeout = "15" },
			err:    `build_timeout "15" must be a positive duration, such as 15m`,
		},
		{
			name:   "negative build timeout",
			change: func(c *Config) { c.BuildTimeout = "-1m" },
			err:    `build_timeout "-1m" must be a positive duration`,
		},
		{
			name:   "unknown address family",
			change: func(c *Config) { c.AddressFamily = "ip4" },
			err:    `address_family "ip4" must be ipv4 or ipv6`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.change(&c)

			_, err := c.validate()

			if tt.err == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	_, err := (&Config{}).validate()

	require.Error(t, err)
	assert.Equal(t, []string{
		"cloud is required",
		`name "" must be 1 to 54 lowercase letters, digits or dashes, and start and end with a letter or digit`,
		"server.flavor is required",
		"server.image is required",
		"server.networks needs at least one network",
	}, strings.Split(err.Error(), "\n"))
}

func TestValidateReportsEveryServerTagProblem(t *testing.T) {
	c := validConfig()
	c.Server.Tags = []string{"team,ci", "ci", "ci"}

	_, err := c.validate()

	require.Error(t, err)
	assert.Equal(t, []string{
		`server.tags: tag "team,ci" must be 1 to 60 characters, without / or ,`,
		`server.tags: "ci" is listed twice, or is the group tag`,
	}, strings.Split(err.Error(), "\n"))
}

func TestValidateParsesSettings(t *testing.T) {
	c := validConfig()
	c.AddressFamily = "ipv6"
	c.BuildTimeout = "30m"

	parsed, err := c.validate()

	require.NoError(t, err)
	assert.Equal(t, 6, parsed.addressVersion)
	assert.Equal(t, 30*time.Minute, parsed.buildTimeout)
}
