package openstack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"

	"github.com/radome-io/fleeting-plugin-openstack/internal/cloud"
)

func TestClassify(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		server cloud.Server
		state  provider.State
		broken string
	}{
		{
			name:   "building",
			server: cloud.Server{Status: "BUILD", Created: now.Add(-time.Minute)},
			state:  provider.StateCreating,
		},
		{
			name:   "building for too long",
			server: cloud.Server{Status: "BUILD", Created: now.Add(-16 * time.Minute)},
			state:  provider.StateDeleting,
			broken: "still building after 15m0s",
		},
		{
			name:   "building, creation time unknown",
			server: cloud.Server{Status: "BUILD"},
			state:  provider.StateCreating,
		},
		{name: "active", server: cloud.Server{Status: "ACTIVE"}, state: provider.StateRunning},
		{name: "migrating", server: cloud.Server{Status: "MIGRATING"}, state: provider.StateRunning},
		{name: "rebooting", server: cloud.Server{Status: "REBOOT"}, state: provider.StateRunning},
		{name: "hard rebooting", server: cloud.Server{Status: "HARD_REBOOT"}, state: provider.StateRunning},
		{name: "deleted", server: cloud.Server{Status: "DELETED"}, state: provider.StateDeleted},
		{name: "soft deleted", server: cloud.Server{Status: "SOFT_DELETED"}, state: provider.StateDeleted},
		{
			name:   "error",
			server: cloud.Server{Status: "ERROR", Fault: "No valid host was found."},
			state:  provider.StateDeleting,
			broken: "ERROR: No valid host was found.",
		},
		{
			name:   "error without a fault",
			server: cloud.Server{Status: "ERROR"},
			state:  provider.StateDeleting,
			broken: "ERROR: no fault reported",
		},
		{name: "stopped", server: cloud.Server{Status: "SHUTOFF"}, state: provider.StateDeleting, broken: "SHUTOFF"},
		{
			name:   "shelved",
			server: cloud.Server{Status: "SHELVED_OFFLOADED"},
			state:  provider.StateDeleting,
			broken: "SHELVED_OFFLOADED",
		},
		{
			name:   "active, being deleted",
			server: cloud.Server{Status: "ACTIVE", TaskState: "deleting"},
			state:  provider.StateDeleting,
		},
		{
			name:   "error, being deleted",
			server: cloud.Server{Status: "ERROR", TaskState: "deleting"},
			state:  provider.StateDeleting,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, broken := classify(tt.server, now, defaultBuildTimeout)

			assert.Equal(t, tt.state, state)
			assert.Equal(t, tt.broken, broken)
		})
	}
}

func TestHealthy(t *testing.T) {
	tests := []struct {
		status    string
		taskState string
		healthy   bool
	}{
		{status: "ACTIVE", healthy: true},
		{status: "MIGRATING", healthy: true},
		{status: "ACTIVE", taskState: "deleting", healthy: false},
		{status: "BUILD", healthy: false},
		{status: "REBOOT", healthy: true},
		{status: "HARD_REBOOT", healthy: true},
		{status: "ERROR", healthy: false},
	}

	for _, tt := range tests {
		t.Run(tt.status+"/"+tt.taskState, func(t *testing.T) {
			assert.Equal(t, tt.healthy, healthy(cloud.Server{Status: tt.status, TaskState: tt.taskState}))
		})
	}
}
