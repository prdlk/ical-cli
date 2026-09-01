// Package client provides read and write access to a remote calendar behind a
// single interface, with a read-only implementation for plain .ics URLs and a
// full read/write implementation for CalDAV collections.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/prdlk/ical-cli/internal/event"
)

// Mode identifies which protocol a client speaks.
type Mode string

const (
	// ModeICS is a plain iCalendar document fetched over HTTP. Read-only.
	ModeICS Mode = "ics"
	// ModeCalDAV is a CalDAV collection. Read/write.
	ModeCalDAV Mode = "caldav"
)

// Sentinel errors. Commands map these onto process exit codes.
var (
	// ErrReadOnly reports a write attempted against a plain ICS URL.
	ErrReadOnly = errors.New("calendar is read-only")
	// ErrConflict reports a failed If-Match precondition: the stored event
	// changed after it was read.
	ErrConflict = errors.New("conflict")
	// ErrNotFound reports a missing event or resource.
	ErrNotFound = event.ErrNotFound
)

// readOnlyError explains which command failed and what would be required.
func readOnlyError(op string) error {
	return fmt.Errorf("%w: cannot %s over a plain .ics URL, which HTTP exposes as a "+
		"single read-only document; %s requires a CalDAV collection (pass --caldav "+
		"with a CalDAV URL, or use a URL this tool can auto-detect as CalDAV)",
		ErrReadOnly, op, op)
}

// Query selects which events List returns.
type Query struct {
	// From and To bound the query window. A zero value is unbounded.
	From time.Time
	To   time.Time
	// Limit caps the number of returned events; 0 means unlimited.
	Limit int
	// Expand requests RRULE expansion into individual occurrences within the
	// window. When false, recurring series are returned as their masters.
	Expand bool
	// Location resolves floating timestamps and all-day boundaries.
	Location *time.Location
}

// CalendarClient is the single abstraction commands depend on. icsClient
// implements the reads and rejects the writes; caldavClient implements all of
// it.
type CalendarClient interface {
	// List returns the events matching q, ordered by start time.
	List(ctx context.Context, q Query) ([]*event.Event, error)
	// Get resolves a UID or unambiguous UID prefix to a stored event.
	Get(ctx context.Context, uid string) (*event.Event, error)
	// Put creates or replaces an event. A non-empty Event.ETag is sent as
	// If-Match so a concurrent modification surfaces as ErrConflict.
	Put(ctx context.Context, ev *event.Event) error
	// Delete removes an event, honouring Event.ETag as If-Match.
	Delete(ctx context.Context, ev *event.Event) error
	// Raw returns the calendar as a single ICS document.
	Raw(ctx context.Context) ([]byte, error)
	// Mode reports the protocol in use.
	Mode() Mode
}

// Config describes how to reach a calendar.
type Config struct {
	// URL is the calendar address. webcal:// is normalised to https://.
	URL string
	// User and Pass supply HTTP basic authentication.
	User string
	Pass string
	// ForceCalDAV skips auto-detection and requires CalDAV.
	ForceCalDAV bool
	// Timeout bounds each HTTP exchange. Zero uses DefaultTimeout.
	Timeout time.Duration
	// Location resolves floating timestamps.
	Location *time.Location
}

// New builds a client for cfg, auto-detecting the protocol unless
// cfg.ForceCalDAV is set.
//
// Detection probes the URL with OPTIONS and PROPFIND looking for the
// calendar-access capability or a CalDAV resourcetype. A URL that does not
// advertise CalDAV falls back to read-only ICS mode.
func New(ctx context.Context, cfg Config) (CalendarClient, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("no calendar URL: pass --url, set ICAL_CLI_URL, or add `url:` to the config file")
	}

	endpoint, err := NormalizeURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	cfg.URL = endpoint
	if cfg.Location == nil {
		cfg.Location = time.Local
	}

	http := newRetryClient(cfg.User, cfg.Pass, cfg.Timeout)

	if cfg.ForceCalDAV {
		return newCalDAVClient(ctx, cfg, http)
	}

	caldavOK, err := detectCalDAV(ctx, http, endpoint)
	if err != nil {
		return nil, err
	}
	if caldavOK {
		return newCalDAVClient(ctx, cfg, http)
	}
	return newICSClient(cfg, http), nil
}

// NormalizeURL validates the calendar URL and rewrites the webcal scheme,
// which is a display-only alias for HTTP(S) that net/http cannot dial.
func NormalizeURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse calendar url %q: %w", raw, err)
	}

	switch strings.ToLower(u.Scheme) {
	case "webcal", "webcals":
		u.Scheme = "https"
	case "http", "https":
	case "":
		return "", fmt.Errorf("calendar url %q has no scheme: expected http, https, or webcal", raw)
	default:
		return "", fmt.Errorf("unsupported calendar url scheme %q: expected http, https, or webcal", u.Scheme)
	}

	if u.Host == "" {
		return "", fmt.Errorf("calendar url %q has no host", raw)
	}
	return u.String(), nil
}

// looksLikeICS reports whether a URL path names a static iCalendar document.
func looksLikeICS(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".ics")
}

// applyLimit truncates events to n, keeping the earliest.
func applyLimit(events []*event.Event, n int) []*event.Event {
	if n > 0 && len(events) > n {
		return events[:n]
	}
	return events
}
