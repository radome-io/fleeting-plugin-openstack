package openstack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var start = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestBackoffDelay(t *testing.T) {
	assert.Equal(t, 30*time.Second, backoffDelay(1))
	assert.Equal(t, time.Minute, backoffDelay(2))
	assert.Equal(t, 8*time.Minute, backoffDelay(5))
	assert.Equal(t, 10*time.Minute, backoffDelay(6))
	assert.Equal(t, 10*time.Minute, backoffDelay(100))
}

func TestBackoffCountsServersFailingTogetherOnce(t *testing.T) {
	var b backoff
	for _, id := range []string{"a", "b", "c"} {
		b.created(id)
	}

	failures, pause, _ := b.failed(map[string]string{"a": "ERROR", "b": "ERROR", "c": "ERROR"}, start)

	assert.Equal(t, 1, failures)
	assert.Equal(t, 30*time.Second, pause)
}

func TestBackoffGivesTheReasonOfACountedServer(t *testing.T) {
	var b backoff
	b.created("new")

	_, _, reason := b.failed(map[string]string{"new": "ERROR: No valid host was found.", "older": "SHUTOFF"}, start)

	assert.Equal(t, "ERROR: No valid host was found.", reason)
}

func TestBackoffPausesThenTriesOneServer(t *testing.T) {
	var b backoff
	assert.Equal(t, 5, first(b.allowed(5, start)), "no failure, no limit")

	b.created("a")
	b.failed(map[string]string{"a": "ERROR"}, start)

	allowed, remaining := b.allowed(5, start.Add(10*time.Second))
	assert.Zero(t, allowed)
	assert.Equal(t, 20*time.Second, remaining)

	after := start.Add(30 * time.Second)
	assert.Equal(t, 1, first(b.allowed(5, after)), "one server is tried after the pause")

	b.created("probe")
	assert.Zero(t, first(b.allowed(5, after)), "nothing more until the tried server comes up")

	b.running("probe")
	assert.Equal(t, 5, first(b.allowed(5, after)), "a server that came up ends the backoff")
}

func TestBackoffEscalates(t *testing.T) {
	var b backoff

	b.created("a")
	b.failed(map[string]string{"a": "ERROR"}, start)
	b.created("b")
	failures, pause, _ := b.failed(map[string]string{"b": "ERROR"}, start)

	assert.Equal(t, 2, failures)
	assert.Equal(t, time.Minute, pause)

	failures, pause = b.creationFailed(start)
	assert.Equal(t, 3, failures)
	assert.Equal(t, 2*time.Minute, pause)
}

func TestBackoffIgnoresOtherServers(t *testing.T) {
	var b backoff

	_, pause, _ := b.failed(map[string]string{"older": "ERROR"}, start)
	assert.Zero(t, pause, "a server this process did not create starts no backoff")

	b.created("a")
	b.failed(map[string]string{"a": "ERROR"}, start)
	b.running("older")
	assert.Zero(t, first(b.allowed(1, start)), "a server this process did not create ends no backoff")
}

func TestBackoffForgetsServersDeletedBeforeComingUp(t *testing.T) {
	var b backoff
	b.created("listed")
	b.created("not-listed-yet")

	b.listed(map[string]bool{"listed": true})
	b.listed(map[string]bool{})

	_, pause, _ := b.failed(map[string]string{"listed": "ERROR"}, start)
	assert.Zero(t, pause, "a server listed then gone was deleted")

	_, pause, _ = b.failed(map[string]string{"not-listed-yet": "ERROR"}, start)
	assert.Positive(t, pause, "a server never listed yet still counts")
}

func first(allowed int, _ time.Duration) int {
	return allowed
}
