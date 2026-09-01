package client

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds a single HTTP exchange, including body transfer.
const DefaultTimeout = 30 * time.Second

// Retry policy: three attempts total, doubling the delay each time.
const (
	defaultAttempts = 3
	defaultBackoff  = 500 * time.Millisecond
)

// UserAgent identifies this client to servers.
const UserAgent = "ical-cli/1.0"

// retryClient is an http.Client wrapper that retries idempotent-by-contract
// calendar requests on transport failures and 5xx/429 responses with
// exponential backoff. It satisfies webdav.HTTPClient, so both the CalDAV
// library and this package's raw conditional requests share one policy.
type retryClient struct {
	inner    *http.Client
	attempts int
	backoff  time.Duration
	user     string
	pass     string
}

// newRetryClient builds the shared transport. Basic auth is applied here rather
// than via webdav.HTTPClientWithBasicAuth so that raw requests issued by the
// CalDAV client get the same credentials.
func newRetryClient(user, pass string, timeout time.Duration) *retryClient {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &retryClient{
		inner:    &http.Client{Timeout: timeout},
		attempts: defaultAttempts,
		backoff:  defaultBackoff,
		user:     user,
		pass:     pass,
	}
}

// Do executes req, retrying on retryable failures.
func (c *retryClient) Do(req *http.Request) (*http.Response, error) {
	if err := makeReplayable(req); err != nil {
		return nil, err
	}

	var lastErr error
	delay := c.backoff

	for attempt := range c.attempts {
		if attempt > 0 {
			// Rewind the body before replaying.
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("rewind request body: %w", err)
				}
				req.Body = body
			}
			select {
			case <-req.Context().Done():
				return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL, req.Context().Err())
			case <-time.After(delay):
			}
			delay *= 2
		}

		attemptReq := req.Clone(req.Context())
		attemptReq.Body = req.Body
		c.decorate(attemptReq)

		resp, err := c.inner.Do(attemptReq)
		if err != nil {
			lastErr = fmt.Errorf("%s %s: %w", req.Method, req.URL, err)
			continue
		}
		if !retryableStatus(resp.StatusCode) || attempt == c.attempts-1 {
			return resp, nil
		}
		// Drain and close so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		lastErr = fmt.Errorf("%s %s: server returned %s", req.Method, req.URL, resp.Status)
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", c.attempts, lastErr)
}

func (c *retryClient) decorate(req *http.Request) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent)
	}
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
}

// makeReplayable guarantees GetBody is populated so a retry can resend the
// body. http.NewRequest sets GetBody for in-memory body types, but a caller
// passing an arbitrary io.Reader does not get one.
func makeReplayable(req *http.Request) error {
	// http.NoBody is non-nil but carries nothing, so buffering it would only
	// waste a read and an allocation on every bodyless request.
	if req.Body == nil || req.Body == http.NoBody || req.GetBody != nil {
		return nil
	}
	buf, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return fmt.Errorf("buffer request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(buf))
	req.ContentLength = int64(len(buf))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}
	return nil
}

func retryableStatus(code int) bool {
	return code >= 500 || code == http.StatusTooManyRequests
}

// statusFromError recovers an HTTP status code from an error returned by
// go-webdav.
//
// go-webdav wraps every non-2xx response in *internal.HTTPError. That type
// lives under internal/, so it cannot be named or matched with errors.As from
// outside the module. Its Error() output is documented to begin with
// "<code> <StatusText>", which makes the prefix the only available signal.
// Operations whose status actually drives control flow (conditional PUT,
// DELETE, GET) bypass the library entirely and read resp.StatusCode directly;
// this helper only classifies errors from the discovery and REPORT helpers.
func statusFromError(err error) int {
	if err == nil {
		return 0
	}
	fields := strings.Fields(err.Error())
	if len(fields) == 0 {
		return 0
	}
	code, convErr := strconv.Atoi(fields[0])
	if convErr != nil || code < 100 || code > 599 {
		return 0
	}
	return code
}
