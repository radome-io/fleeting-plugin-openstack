package openstack

import "gitlab.com/gitlab-org/fleeting/fleeting/plugin"

const name = "fleeting-plugin-openstack"

// Set at link time by mise.toml's build task. Left empty, plugin.Main reads
// them from the Go build information.
var (
	version   string
	revision  string
	reference string
	builtAt   string
)

// Version describes this build.
var Version = plugin.VersionInfo{
	Name:      name,
	Version:   version,
	Revision:  revision,
	Reference: reference,
	BuiltAt:   builtAt,
}
