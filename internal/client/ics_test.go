package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prdlk/ical-cli/internal/event"
)

const testICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//sample//EN
BEGIN:VEVENT
UID:review@example.com
DTSTAMP:20260101T090000Z
DTSTART:20260305T140000Z
DTEND:20260305T150000Z
SUMMARY:Q1 review
LOCATION:Boardroom
END:VEVENT
BEGIN:VEVENT
UID:standup@example.com
DTSTAMP:20260101T090000Z
DTSTART:20260302T090000Z
DTEND:20260302T091500Z
SUMMARY:Daily standup
RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3
END:VEVENT
END:VCALENDAR
`

// newICSServer serves body at /cal.ics with the supplied status.
func newICSServer(t *testing.T, status int, body string) (*httptest.Server, *int64) {
	t.Helper()

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/calendar")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// newTestICSClient builds a client pointed at url without probing, since a
// .ics path is recognised as read-only without a round trip.
func newTestICSClient(t *testing.T, url string) CalendarClient {
	t.Helper()

	cl, err := New(context.Background(), Config{URL: url, Location: time.UTC})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if got := cl.Mode(); got != ModeICS {
		t.Fatalf("Mode = %q, want %q", got, ModeICS)
	}
	return cl
}

func TestICSClientList(t *testing.T) {
	t.Parallel()

	srv, _ := newICSServer(t, http.StatusOK, testICS)
	cl := newTestICSClient(t, srv.URL+"/cal.ics")

	tests := []struct {
		name      string
		query     Query
		wantCount int
		wantFirst string
	}{
		{
			name:      "whole calendar without expansion",
			query:     Query{Location: time.UTC},
			wantCount: 2,
		},
		{
			name: "expanded window covers every occurrence",
			query: Query{
				From:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
				To:       time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
				Expand:   true,
				Location: time.UTC,
			},
			// Three standup occurrences plus the review.
			wantCount: 4,
			wantFirst: "Daily standup",
		},
		{
			name: "limit truncates",
			query: Query{
				From:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
				To:       time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
				Expand:   true,
				Limit:    2,
				Location: time.UTC,
			},
			wantCount: 2,
			wantFirst: "Daily standup",
		},
		{
			name: "window before the calendar is empty",
			query: Query{
				From:     time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				To:       time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC),
				Expand:   true,
				Location: time.UTC,
			},
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events, err := cl.List(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("List returned error: %v", err)
			}
			if len(events) != tc.wantCount {
				t.Fatalf("List returned %d events, want %d", len(events), tc.wantCount)
			}
			if tc.wantFirst != "" && events[0].Summary() != tc.wantFirst {
				t.Errorf("first event summary = %q, want %q", events[0].Summary(), tc.wantFirst)
			}
		})
	}
}

// TestICSClientFetchesOnce checks the document is retrieved a single time per
// process, however many reads a command performs.
func TestICSClientFetchesOnce(t *testing.T) {
	t.Parallel()

	srv, hits := newICSServer(t, http.StatusOK, testICS)
	cl := newTestICSClient(t, srv.URL+"/cal.ics")

	for range 3 {
		if _, err := cl.List(context.Background(), Query{Location: time.UTC}); err != nil {
			t.Fatalf("List returned error: %v", err)
		}
	}
	if _, err := cl.Raw(context.Background()); err != nil {
		t.Fatalf("Raw returned error: %v", err)
	}

	if got := atomic.LoadInt64(hits); got != 1 {
		t.Errorf("server received %d requests, want 1", got)
	}
}

func TestICSClientGet(t *testing.T) {
	t.Parallel()

	srv, _ := newICSServer(t, http.StatusOK, testICS)
	cl := newTestICSClient(t, srv.URL+"/cal.ics")

	tests := []struct {
		name         string
		uid          string
		wantSummary  string
		wantErr      bool
		wantNotFound bool
	}{
		{name: "exact uid", uid: "review@example.com", wantSummary: "Q1 review"},
		{name: "unique prefix", uid: "stand", wantSummary: "Daily standup"},
		{name: "unknown uid", uid: "nope", wantErr: true, wantNotFound: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := cl.Get(context.Background(), tc.uid)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Get(%q) = %v, want error", tc.uid, ev)
				}
				if tc.wantNotFound && !errors.Is(err, ErrNotFound) {
					t.Errorf("Get(%q) error = %v, want ErrNotFound", tc.uid, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get(%q) returned error: %v", tc.uid, err)
			}
			if ev.Summary() != tc.wantSummary {
				t.Errorf("Get(%q) summary = %q, want %q", tc.uid, ev.Summary(), tc.wantSummary)
			}
		})
	}
}

// TestICSClientRejectsWrites is the read-only contract: a plain .ics URL has no
// per-event addressing, so writes must fail with an error naming CalDAV.
func TestICSClientRejectsWrites(t *testing.T) {
	t.Parallel()

	srv, _ := newICSServer(t, http.StatusOK, testICS)
	cl := newTestICSClient(t, srv.URL+"/cal.ics")

	ev := event.New("x@ical-cli", time.Now())

	tests := []struct {
		name string
		op   func() error
	}{
		{name: "put", op: func() error { return cl.Put(context.Background(), ev) }},
		{name: "delete", op: func() error { return cl.Delete(context.Background(), ev) }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.op()
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !errors.Is(err, ErrReadOnly) {
				t.Errorf("error = %v, want ErrReadOnly", err)
			}
			// The message must tell the user what is required.
			if !strings.Contains(err.Error(), "CalDAV") {
				t.Errorf("error message does not mention CalDAV: %v", err)
			}
		})
	}
}

func TestICSClientRawIsVerbatim(t *testing.T) {
	t.Parallel()

	srv, _ := newICSServer(t, http.StatusOK, testICS)
	cl := newTestICSClient(t, srv.URL+"/cal.ics")

	raw, err := cl.Raw(context.Background())
	if err != nil {
		t.Fatalf("Raw returned error: %v", err)
	}
	if string(raw) != testICS {
		t.Errorf("Raw returned modified bytes:\ngot:\n%s\nwant:\n%s", raw, testICS)
	}
}

func TestICSClientErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       int
		body         string
		wantNotFound bool
	}{
		{name: "not found", status: http.StatusNotFound, body: "missing", wantNotFound: true},
		{name: "unauthorized", status: http.StatusUnauthorized, body: "denied"},
		{name: "forbidden", status: http.StatusForbidden, body: "denied"},
		{name: "malformed body", status: http.StatusOK, body: "this is not a calendar"},
		{name: "empty body", status: http.StatusOK, body: ""},
		{name: "truncated calendar", status: http.StatusOK, body: "BEGIN:VCALENDAR\nVERSION:2.0\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, _ := newICSServer(t, tc.status, tc.body)
			cl := newTestICSClient(t, srv.URL+"/cal.ics")

			_, err := cl.List(context.Background(), Query{Location: time.UTC})
			if err == nil {
				t.Fatal("List returned nil error, want failure")
			}
			if tc.wantNotFound && !errors.Is(err, ErrNotFound) {
				t.Errorf("error = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestRetryOnServerError checks the transport retries 5xx with backoff and
// succeeds once the server recovers.
func TestRetryOnServerError(t *testing.T) {
	t.Parallel()

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt64(&hits, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = w.Write([]byte(testICS))
	}))
	t.Cleanup(srv.Close)

	cl := newTestICSClient(t, srv.URL+"/cal.ics")

	events, err := cl.List(context.Background(), Query{Location: time.UTC})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("List returned %d events, want 2", len(events))
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Errorf("server received %d requests, want 3 (two failures then success)", got)
	}
}

// TestRetryGivesUp checks the transport stops after the configured attempts and
// reports the final status rather than looping.
func TestRetryGivesUp(t *testing.T) {
	t.Parallel()

	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	cl := newTestICSClient(t, srv.URL+"/cal.ics")

	if _, err := cl.List(context.Background(), Query{Location: time.UTC}); err == nil {
		t.Fatal("List returned nil error, want failure")
	}
	if got := atomic.LoadInt64(&hits); got != defaultAttempts {
		t.Errorf("server received %d requests, want %d", got, defaultAttempts)
	}
}

func TestNormalizeURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "https passes through", input: "https://host/cal.ics", want: "https://host/cal.ics"},
		{name: "http passes through", input: "http://host/cal.ics", want: "http://host/cal.ics"},
		{name: "webcal becomes https", input: "webcal://host/cal.ics", want: "https://host/cal.ics"},
		{name: "webcals becomes https", input: "webcals://host/cal.ics", want: "https://host/cal.ics"},
		{name: "surrounding space is trimmed", input: "  https://host/cal.ics  ", want: "https://host/cal.ics"},
		{name: "credentials survive", input: "https://u:p@host/cal.ics", want: "https://u:p@host/cal.ics"},
		{name: "no scheme", input: "host/cal.ics", wantErr: true},
		{name: "unsupported scheme", input: "ftp://host/cal.ics", wantErr: true},
		{name: "no host", input: "https:///cal.ics", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeURL(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeURL(%q) = %q, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeURL(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNewRequiresURL(t *testing.T) {
	t.Parallel()

	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("New with no URL returned nil error, want failure")
	}
}
