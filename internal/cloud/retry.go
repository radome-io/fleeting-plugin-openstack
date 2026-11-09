package cloud

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gophercloud/gophercloud/v2"
)

const maxRetries = 3

const maxRetryAfter = 30 * time.Second

// firstRetryDelay doubles with each retry.
const firstRetryDelay = time.Second

// retrier retries failed requests. Tests shorten its delay.
type retrier struct {
	delay time.Duration
}

// retry is gophercloud's RetryFunc: nil retries, an error gives up. A 429 is
// retried for any request, since the cloud did not carry it out. A 502, 503,
// 504 or network error is retried only for idempotent requests: a creation
// may have happened.
func (r retrier) retry(ctx context.Context, method, _ string, _ *gophercloud.RequestOpts, err error, failCount uint) error {
	if failCount > maxRetries {
		return err
	}

	wait := r.delay << (failCount - 1)

	var response gophercloud.ErrUnexpectedResponseCode
	var network net.Error

	switch {
	case errors.As(err, &response) && response.Actual == http.StatusTooManyRequests:
		if after := retryAfter(response.ResponseHeader); after > 0 {
			wait = after
		}
	case errors.As(err, &response) && unavailable(response.Actual) && idempotent(method):
	case !errors.As(err, &response) && errors.As(err, &network) && idempotent(method):
	default:
		return err
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	case <-timer.C:
		return nil
	}
}

func unavailable(status int) bool {
	return status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

func idempotent(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodDelete
}

// retryAfter reads a Retry-After header in seconds, zero when absent.
func retryAfter(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(header.Get("Retry-After"))
	if err != nil || seconds <= 0 {
		return 0
	}

	return min(time.Duration(seconds)*time.Second, maxRetryAfter)
}
