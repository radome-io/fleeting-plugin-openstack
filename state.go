package openstack

import (
	"cmp"
	"time"

	"gitlab.com/gitlab-org/fleeting/fleeting/provider"

	"github.com/radome-io/fleeting-plugin-openstack/internal/cloud"
)

// classify returns the fleeting state of a server, and why it is broken if it
// is. A broken server is reported as deleting, never deleted: fleeting would
// forget it without deleting it.
func classify(s cloud.Server, now time.Time, buildTimeout time.Duration) (state provider.State, broken string) {
	if s.TaskState == "deleting" {
		return provider.StateDeleting, ""
	}

	switch {
	case s.Status == "BUILD":
		// Neither Nova, by default, nor fleeting times out a build.
		if !s.Created.IsZero() && now.Sub(s.Created) > buildTimeout {
			return provider.StateDeleting, "still building after " + buildTimeout.String()
		}

		return provider.StateCreating, ""
	case running(s.Status):
		return provider.StateRunning, ""
	case s.Status == "DELETED" || s.Status == "SOFT_DELETED":
		return provider.StateDeleted, ""
	case s.Status == "ERROR":
		return provider.StateDeleting, "ERROR: " + cmp.Or(s.Fault, "no fault reported")
	default:
		// Out of service (SHUTOFF, SHELVED, RESIZE...): workers are disposable.
		return provider.StateDeleting, s.Status
	}
}

// describe says why a server is not running.
func describe(s cloud.Server) string {
	if s.TaskState == "deleting" {
		return "being deleted"
	}

	return s.Status
}

// healthy reports whether a server is running and not being deleted.
func healthy(s cloud.Server) bool {
	return running(s.Status) && s.TaskState != "deleting"
}

// running reports whether a status is running. Migrations and reboots end by
// themselves.
func running(status string) bool {
	switch status {
	case "ACTIVE", "MIGRATING", "REBOOT", "HARD_REBOOT":
		return true
	default:
		return false
	}
}
