package openstack

import (
	"fmt"
	"slices"
	"strings"

	"github.com/radome-io/fleeting-plugin-openstack/internal/cloud"
)

// addresses returns the first fixed address of the IP version as the internal
// one, and the first floating one, or else the fixed one, as the external one.
// Without a network, the server must have such addresses on one network only.
func addresses(s cloud.Server, network string, version int) (internal, external string, err error) {
	var candidates []cloud.Address
	var networks []string

	for _, a := range s.Addresses {
		if a.Version != version || (network != "" && a.Network != network) {
			continue
		}
		candidates = append(candidates, a)
		if !slices.Contains(networks, a.Network) {
			networks = append(networks, a.Network)
		}
	}

	if len(networks) > 1 {
		return "", "", fmt.Errorf("server %s has IPv%d addresses on networks %s: set address_network",
			s.ID, version, strings.Join(networks, ", "))
	}

	for _, a := range candidates {
		if !a.Floating && internal == "" {
			internal = a.IP
		}
		if a.Floating && external == "" {
			external = a.IP
		}
	}

	if internal == "" {
		where := ""
		if network != "" {
			where = " on network " + network
		}

		return "", "", fmt.Errorf("server %s has no fixed IPv%d address%s", s.ID, version, where)
	}

	if external == "" {
		external = internal
	}

	return internal, external, nil
}
