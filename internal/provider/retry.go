package provider

import (
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// RetryTransport wraps an http.RoundTripper with retry logic for transient errors.
type RetryTransport struct {
	Base         http.RoundTripper
	MaxRetries   int
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

// NewRetryTransport creates a new RetryTransport with sensible defaults.
func NewRetryTransport(base http.RoundTripper) *RetryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &RetryTransport{
		Base:         base,
		MaxRetries:   3,
		InitialDelay: 500 * time.Millisecond,
		MaxDelay:     30 * time.Second,
	}
}

// RoundTrip executes the HTTP request, retrying on 429, 500, 502, 503, 504.
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var bodyBytes []byte
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
	}

	maxRetries := t.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	for attempt := 0; ; attempt++ {
		// Reset body for this attempt
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := t.Base.RoundTrip(req)

		// Determine if we should retry
		shouldRetry := false
		var retryDelay time.Duration

		if err != nil {
			if req.Context().Err() != nil {
				return nil, req.Context().Err()
			}
			shouldRetry = true
		} else if isRetryableStatus(resp.StatusCode) {
			shouldRetry = true
			retryDelay = parseRetryAfter(resp.Header.Get("Retry-After"))
			// Drain and close response body before retrying
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		if !shouldRetry || attempt >= maxRetries {
			return resp, err
		}

		// Calculate backoff
		if retryDelay <= 0 {
			backoff := t.InitialDelay * (1 << attempt)
			// Add 10-20% jitter
			jitter := time.Duration(float64(backoff) * (0.1 + rand.Float64()*0.1))
			retryDelay = backoff + jitter
			if t.MaxDelay > 0 && retryDelay > t.MaxDelay {
				retryDelay = t.MaxDelay
			}
		}

		select {
		case <-time.After(retryDelay):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
}

func isRetryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	default:
		return false
	}
}

func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	// Try parsing as seconds
	if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	// Try parsing as HTTP date (RFC1123)
	if t, err := http.ParseTime(val); err == nil {
		diff := time.Until(t)
		if diff > 0 {
			return diff
		}
	}
	return 0
}
