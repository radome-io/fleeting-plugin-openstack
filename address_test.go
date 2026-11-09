package openstack

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/radome-io/fleeting-plugin-openstack/internal/cloud"
)

func TestAddresses(t *testing.T) {
	public := []cloud.Address{
		{Network: "public", IP: "203.0.113.10", Version: 4},
		{Network: "public", IP: "2001:db8::10", Version: 6},
	}
	private := []cloud.Address{
		{Network: "ci", IP: "10.0.0.5", Version: 4},
		{Network: "ci", IP: "198.51.100.7", Version: 4, Floating: true},
	}

	tests := []struct {
		name      string
		addresses []cloud.Address
		network   string
		version   int
		internal  string
		external  string
		err       string
	}{
		{
			name:      "public network, IPv4",
			addresses: public,
			version:   4,
			internal:  "203.0.113.10",
			external:  "203.0.113.10",
		},
		{
			name:      "public network, IPv6",
			addresses: public,
			version:   6,
			internal:  "2001:db8::10",
			external:  "2001:db8::10",
		},
		{
			name:      "floating IP is the external address",
			addresses: private,
			version:   4,
			internal:  "10.0.0.5",
			external:  "198.51.100.7",
		},
		{
			name:      "several networks need a choice",
			addresses: append(append([]cloud.Address{}, public...), private...),
			version:   4,
			err:       "server s1 has IPv4 addresses on networks public, ci: set address_network",
		},
		{
			name:      "the chosen network wins",
			addresses: append(append([]cloud.Address{}, public...), private...),
			network:   "ci",
			version:   4,
			internal:  "10.0.0.5",
			external:  "198.51.100.7",
		},
		{
			name:      "no address on the chosen network",
			addresses: public,
			network:   "ci",
			version:   4,
			err:       "server s1 has no fixed IPv4 address on network ci",
		},
		{
			name:      "no address of the version",
			addresses: private,
			version:   6,
			err:       "server s1 has no fixed IPv6 address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			internal, external, err := addresses(cloud.Server{ID: "s1", Addresses: tt.addresses}, tt.network, tt.version)

			if tt.err != "" {
				require.EqualError(t, err, tt.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.internal, internal)
			assert.Equal(t, tt.external, external)
		})
	}
}
