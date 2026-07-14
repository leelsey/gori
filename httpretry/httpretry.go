// Package httpretry adds bounded, backoff retries to net/http calls for the
// provider adapters. Pure standard library.
package httpretry

import (
	"context"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// Policy controls retry behaviour. A Policy with Attempts <= 1 disables retries.
type Policy struct {
	Attempts int           // total attempts including the first
	Base     time.Duration // base backoff delay
	Cap      time.Duration // maximum backoff delay per attempt
}

// Default is the standard policy used by providers unless overridden.
func Default() Policy {
	return Policy{Attempts: 3, Base: 500 * time.Millisecond, Cap: 10 * time.Second}
}

// Do issues the request built by mkReq, retrying network errors, 429 and 5xx
// with jittered backoff (honouring Retry-After). mkReq must build a fresh
// request each call; the returned response is the caller's to close.
func Do(ctx context.Context, client *http.Client, p Policy, mkReq func() (*http.Request, error)) (*http.Response, error) {
	attempts := p.Attempts
	if attempts < 1 {
		attempts = 1
	}
	var resp *http.Response
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			d := backoff(p, attempt, resp)
			if resp != nil {
				// Drain before closing so the keep-alive connection can be reused
				// for the retry instead of forcing a fresh TCP+TLS handshake.
				DrainClose(resp.Body)
				resp = nil
			}
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			}
		}
		var req *http.Request
		if req, err = mkReq(); err != nil {
			return nil, err
		}
		resp, err = client.Do(req)
		if err != nil {
			resp = nil
			continue
		}
		if !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
	}
	// Exhausted: return the last response (e.g. a persistent 429/5xx) or error.
	return resp, err
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// maxRetryAfter bounds an honoured Retry-After so a hostile or buggy server
// cannot make the client sleep absurdly long.
const maxRetryAfter = 2 * time.Minute

// drainLimit bounds how much of a discarded response body is read back before a
// retry, so connection reuse never lets a hostile server stall the client.
const drainLimit = 64 << 10 // 64 KiB

// DrainClose drains a bounded amount of rc and closes it, so the underlying
// keep-alive connection can be returned to the pool for reuse. Provider adapters
// use it on every consumed response body.
func DrainClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, drainLimit))
	rc.Close()
}

func backoff(p Policy, attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := retryAfter(resp); ra > 0 {
			if ra > maxRetryAfter {
				ra = maxRetryAfter
			}
			return ra
		}
	}
	base := p.Base
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	maxD := p.Cap
	if maxD <= 0 {
		maxD = 30 * time.Second
	}
	// Exponential, clamped to maxD. Guard the shift so a large attempt count can
	// neither overflow to a negative duration nor panic rand.Int63n.
	d := maxD
	if shift := attempt - 1; shift >= 0 && shift < 31 {
		if grown := base * time.Duration(int64(1)<<uint(shift)); grown > 0 && grown < maxD {
			d = grown
		}
	}
	if d <= 0 {
		d = base
	}
	return time.Duration(rand.Int63n(int64(d) + 1)) // full jitter in [0, d]
}

func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs >= 0 {
		if maxSecs := int64(maxRetryAfter / time.Second); secs > maxSecs {
			return maxRetryAfter // clamp before the multiply so it cannot overflow
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
