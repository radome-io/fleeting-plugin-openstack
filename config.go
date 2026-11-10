package openstack

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultBuildTimeout = 15 * time.Minute
	defaultTagPrefix    = "fleeting:"
)

// Nova's limits, in characters except for user data.
const (
	maxTagLength      = 60
	maxTags           = 50
	maxMetadataLength = 255
	maxUserDataSize   = 65535 // bytes, once base64-encoded
)

// namePattern keeps server names (name, dash, 8 characters) valid hostnames.
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,52}[a-z0-9])?$`)

// Config is the plugin_config of the runner's autoscaler.
type Config struct {
	Cloud        string `json:"cloud"`         // clouds.yaml entry
	CloudsConfig string `json:"clouds_config"` // clouds.yaml path; standard locations when empty
	Name         string `json:"name"`          // prefixes server names
	// Tag marks the group's servers; the plugin may delete any server carrying
	// it. Defaults to fleeting:<name>.
	Tag            string       `json:"tag"`
	Server         ServerConfig `json:"server"`
	AddressNetwork string       `json:"address_network"` // name or ID, one of server.networks; required with several networks
	AddressFamily  string       `json:"address_family"`  // ipv4 (default) or ipv6
	BuildTimeout   string       `json:"build_timeout"`   // Go duration, default 15m
}

// ServerConfig is the template of the servers.
type ServerConfig struct {
	Flavor string `json:"flavor"` // name or ID
	// Image is a name or ID. A name resolves to the newest active image at
	// each scale-up.
	Image            string            `json:"image"`
	Networks         []string          `json:"networks"`        // names or IDs
	SecurityGroups   []string          `json:"security_groups"` // names or IDs
	KeyName          string            `json:"key_name"`
	UserData         string            `json:"user_data"`
	UserDataFile     string            `json:"user_data_file"` // read at startup, instead of user_data
	ConfigDrive      bool              `json:"config_drive"`
	Tags             []string          `json:"tags"` // added to the group tag
	Metadata         map[string]string `json:"metadata"`
	AvailabilityZone string            `json:"availability_zone"`
	ServerGroup      string            `json:"server_group"` // name or ID
}

// UnmarshalJSON decodes the plugin_config, rejecting unknown keys.
func (g *InstanceGroup) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	return decoder.Decode(&g.Config)
}

type parsed struct {
	addressVersion int
	buildTimeout   time.Duration
}

// validate reports every configuration problem at once.
func (c *Config) validate() (parsed, error) {
	var p parsed
	var errs []error

	if c.Cloud == "" {
		errs = append(errs, errors.New("cloud is required"))
	}
	if !namePattern.MatchString(c.Name) {
		errs = append(errs, fmt.Errorf("name %q must be 1 to 54 lowercase letters, digits or dashes, "+
			"and start and end with a letter or digit", c.Name))
	}
	if err := checkTag(c.tag()); err != nil {
		if c.Tag == "" {
			err = fmt.Errorf("%w: set tag, or shorten name", err)
		}
		errs = append(errs, err)
	}
	if c.Server.Flavor == "" {
		errs = append(errs, errors.New("server.flavor is required"))
	}
	if c.Server.Image == "" {
		errs = append(errs, errors.New("server.image is required"))
	}
	if len(c.Server.Networks) == 0 {
		errs = append(errs, errors.New("server.networks needs at least one network"))
	}
	if c.Server.UserData != "" && c.Server.UserDataFile != "" {
		errs = append(errs, errors.New("server.user_data and server.user_data_file exclude each other"))
	}
	if err := checkUserData(c.Server.UserData); err != nil {
		errs = append(errs, fmt.Errorf("server.user_data: %w", err))
	}
	errs = append(errs, c.checkServerTags()...)
	errs = append(errs, c.checkMetadata()...)
	timeout, err := buildTimeout(c.BuildTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	version, err := ipVersion(c.AddressFamily)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return p, errors.Join(errs...)
	}

	p.buildTimeout = timeout
	p.addressVersion = version

	return p, nil
}

func (c *Config) tag() string {
	return cmp.Or(c.Tag, defaultTagPrefix+c.Name)
}

// checkTag fails for a tag Nova would refuse.
func checkTag(tag string) error {
	if tag == "" || utf8.RuneCountInString(tag) > maxTagLength || strings.ContainsAny(tag, "/,") {
		return fmt.Errorf("tag %q must be 1 to %d characters, without / or ,", tag, maxTagLength)
	}

	return nil
}

func (c *Config) checkServerTags() []error {
	var errs []error

	if count := len(c.Server.Tags) + 1; count > maxTags {
		errs = append(errs, fmt.Errorf("server.tags: servers would carry %d tags with the group tag, "+
			"more than the %d Nova accepts", count, maxTags))
	}

	seen := map[string]bool{c.tag(): true}
	for _, tag := range c.Server.Tags {
		if err := checkTag(tag); err != nil {
			errs = append(errs, fmt.Errorf("server.tags: %w", err))
		}
		if seen[tag] {
			errs = append(errs, fmt.Errorf("server.tags: %q is listed twice, or is the group tag", tag))
		}
		seen[tag] = true
	}

	return errs
}

func (c *Config) checkMetadata() []error {
	var errs []error

	for _, key := range slices.Sorted(maps.Keys(c.Server.Metadata)) {
		keyLength := utf8.RuneCountInString(key)
		if keyLength == 0 || keyLength > maxMetadataLength ||
			utf8.RuneCountInString(c.Server.Metadata[key]) > maxMetadataLength {
			errs = append(errs, fmt.Errorf("server.metadata %q: keys must be 1 to %d characters, values at most %d",
				key, maxMetadataLength, maxMetadataLength))
		}
	}

	return errs
}

// checkUserData fails for user data larger than Nova accepts.
func checkUserData(userData string) error {
	if size := base64.StdEncoding.EncodedLen(len(userData)); size > maxUserDataSize {
		return fmt.Errorf("%d bytes once base64-encoded, more than the %d Nova accepts", size, maxUserDataSize)
	}

	return nil
}

// readUserData returns the user data, from user_data_file when set.
func (s *ServerConfig) readUserData() (string, error) {
	if s.UserDataFile == "" {
		return s.UserData, nil
	}

	content, err := os.ReadFile(s.UserDataFile)
	if err != nil {
		return "", fmt.Errorf("server.user_data_file: %w", err)
	}
	if err := checkUserData(string(content)); err != nil {
		return "", fmt.Errorf("server.user_data_file: %w", err)
	}

	return string(content), nil
}

// buildTimeout parses build_timeout.
func buildTimeout(setting string) (time.Duration, error) {
	if setting == "" {
		return defaultBuildTimeout, nil
	}

	timeout, err := time.ParseDuration(setting)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("build_timeout %q must be a positive duration, such as 15m", setting)
	}

	return timeout, nil
}

// ipVersion parses address_family.
func ipVersion(family string) (int, error) {
	switch family {
	case "", "ipv4":
		return 4, nil
	case "ipv6":
		return 6, nil
	default:
		return 0, fmt.Errorf("address_family %q must be ipv4 or ipv6", family)
	}
}
