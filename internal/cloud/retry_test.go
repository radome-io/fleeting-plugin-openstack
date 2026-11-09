package cloud

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fast = retrier{delay: time.Millisecond}

func status(code int) error {
	return gophercloud.ErrUnexpectedResponseCode{Actual: code}
}

func TestRetry(t *testing.T) {
	tests := []struct {
		name   string
		method string
		err    error
		retry  bool
	}{
		{name: "rate limited creation", method: http.MethodPost, err: status(http.StatusTooManyRequests), retry: true},
		{name: "unavailable listing", method: http.MethodGet, err: status(http.StatusServiceUnavailable), retry: true},
		{name: "gateway timeout on deletion", method: http.MethodDelete, err: status(http.StatusGatewayTimeout), retry: true},
		{name: "unavailable creation", method: http.MethodPost, err: status(http.StatusServiceUnavailable)},
		{name: "not found", method: http.MethodGet, err: status(http.StatusNotFound)},
		{name: "conflict", method: http.MethodDelete, err: status(http.StatusConflict)},
		{name: "network error on listing", method: http.MethodGet, err: &net.OpError{Op: "dial", Err: errors.New("refused")}, retry: true},
		{name: "network error on creation", method: http.MethodPost, err: &net.OpError{Op: "read", Err: errors.New("reset")}},
		{name: "other error", method: http.MethodGet, err: errors.New("invalid JSON")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fast.retry(t.Context(), tt.method, "", nil, tt.err, 1)

			if tt.retry {
				require.NoError(t, err)
			} else {
				require.Equal(t, tt.err, err, "the error is returned as is")
			}
		})
	}
}

func TestRetryGivesUp(t *testing.T) {
	err := status(http.StatusServiceUnavailable)

	require.NoError(t, fast.retry(t.Context(), http.MethodGet, "", nil, err, maxRetries))
	require.Equal(t, err, fast.retry(t.Context(), http.MethodGet, "", nil, err, maxRetries+1))
}

func TestRetryStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := fast.retry(ctx, http.MethodGet, "", nil, status(http.StatusServiceUnavailable), 1)

	require.ErrorIs(t, err, context.Canceled)
}

func TestRetryWaits(t *testing.T) {
	rateLimited := gophercloud.ErrUnexpectedResponseCode{
		Actual:         http.StatusTooManyRequests,
		ResponseHeader: http.Header{"Retry-After": {"5"}},
	}

	tests := []struct {
		name      string
		err       error
		failCount uint
		wait      time.Duration
	}{
		{name: "first retry", err: status(http.StatusServiceUnavailable), failCount: 1, wait: time.Second},
		{name: "third retry", err: status(http.StatusServiceUnavailable), failCount: 3, wait: 4 * time.Second},
		{name: "as Retry-After says", err: rateLimited, failCount: 1, wait: 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()

				require.NoError(t, retrier{delay: firstRetryDelay}.retry(t.Context(), http.MethodGet, "", nil, tt.err, tt.failCount))

				assert.Equal(t, tt.wait, time.Since(start))
			})
		})
	}
}

func TestRetryAfter(t *testing.T) {
	assert.Equal(t, 5*time.Second, retryAfter(http.Header{"Retry-After": {"5"}}))
	assert.Equal(t, maxRetryAfter, retryAfter(http.Header{"Retry-After": {"3600"}}))
	assert.Zero(t, retryAfter(http.Header{"Retry-After": {"Wed, 21 Oct 2026 07:28:00 GMT"}}))
	assert.Zero(t, retryAfter(http.Header{}))
}

func TestRequestsAreRetried(t *testing.T) {
	var listings, creations atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("GET /compute/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		if listings.Add(1) < 3 {
			respond(t, w, http.StatusServiceUnavailable, `{}`)

			return
		}
		respond(t, w, http.StatusOK, `{"servers": []}`)
	})
	mux.HandleFunc("POST /compute/servers", func(w http.ResponseWriter, _ *http.Request) {
		creations.Add(1)
		respond(t, w, http.StatusServiceUnavailable, `{}`)
	})

	client := newTestClient(t, mux)
	client.nova.RetryFunc = fast.retry

	_, err := client.ListServers(t.Context(), "fleeting:runner")
	require.NoError(t, err)
	assert.Equal(t, int32(3), listings.Load())

	_, err = client.CreateServer(t.Context(), CreateRequest{Name: "a"})
	require.Error(t, err)
	assert.Equal(t, int32(1), creations.Load(), "a creation is never repeated after a 503")
}
