// Command fleeting-plugin-openstack is the OpenStack fleeting plugin.
package main

import (
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"

	openstack "github.com/radome-io/fleeting-plugin-openstack"
)

func main() {
	plugin.Main(&openstack.InstanceGroup{}, openstack.Version)
}
