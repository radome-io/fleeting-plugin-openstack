package openstack

import (
	"maps"
	"slices"
	"sync"
	"time"

	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

// The pause doubles with each failure in a row, within these bounds.
const (
	minBackoff = 30 * time.Second
	maxBackoff = 10 * time.Minute
)

// backoff pauses scale-ups while new servers fail to come up, as when the cloud
// lacks capacity. After a pause, one server is tried before scaling up freely
// again. Only servers this process created count.
type backoff struct {
	mu       sync.Mutex
	pending  map[string]bool // servers not up yet: whether a listing showed them building
	failures int
	until    time.Time
}

// created records a new server.
func (b *backoff) created(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.pending == nil {
		b.pending = make(map[string]bool)
	}
	b.pending[id] = false
}

// running records that a server came up, ending the backoff.
func (b *backoff) running(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.pending[id]; !ok {
		return
	}

	delete(b.pending, id)
	b.failures = 0
	b.until = time.Time{}
}

// failed records servers that failed together, as one failure, given why each
// failed. It returns the failures in a row, the new pause, zero when none was
// pending, and why one of the pending servers failed.
func (b *backoff) failed(reasons map[string]string, now time.Time) (failures int, pause time.Duration, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	counted := false
	for _, id := range slices.Sorted(maps.Keys(reasons)) {
		if _, ok := b.pending[id]; ok {
			delete(b.pending, id)
			if !counted {
				reason = reasons[id]
				counted = true
			}
		}
	}
	if !counted {
		return 0, 0, ""
	}

	failures, pause = b.fail(now)

	return failures, pause, reason
}

// creationFailed records a scale-up that created nothing.
func (b *backoff) creationFailed(now time.Time) (failures int, pause time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.fail(now)
}

// fail counts a failure. The caller holds b.mu.
func (b *backoff) fail(now time.Time) (failures int, pause time.Duration) {
	b.failures++
	pause = backoffDelay(b.failures)
	b.until = now.Add(pause)

	return b.failures, pause
}

// listed records the states of the listed servers. A pending server listed in
// another state than creating, or gone after a listing showed it, was deleted
// before it came up: not a failure. One that came up or broke left pending
// earlier in Update, in running or failed.
func (b *backoff) listed(states map[string]provider.State) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for id, seen := range b.pending {
		state, ok := states[id]
		switch {
		case state == provider.StateCreating:
			b.pending[id] = true
		case ok || seen:
			delete(b.pending, id)
		}
	}
}

// allowed returns how many of n servers may be created now, and the pause
// left.
func (b *backoff) allowed(n int, now time.Time) (int, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch {
	case now.Before(b.until):
		return 0, b.until.Sub(now)
	case b.failures == 0:
		return n, 0
	case len(b.pending) > 0:
		return 0, 0
	default:
		return min(n, 1), 0
	}
}

// backoffDelay returns the pause after failures in a row.
func backoffDelay(failures int) time.Duration {
	delay := minBackoff
	for range failures - 1 {
		delay *= 2
		if delay >= maxBackoff {
			return maxBackoff
		}
	}

	return delay
}
