package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/prdlk/ical-cli/internal/event"
)

// maxICSBytes bounds how much of a remote document is read, guarding against a
// hostile or misconfigured endpoint streaming indefinitely.
const maxICSBytes = 64 << 20 // 64 MiB

// icsClient reads a single iCalendar document over HTTP. A plain .ics URL has
// no write semantics: HTTP exposes it as one opaque document with no per-event
// addressing, so every write method returns ErrReadOnly.
type icsClient struct {
	cfg  Config
	http *retryClient

	// once guards a single fetch per process; list, get and search all read the
	// same document.
	once sync.Once
	raw  []byte
	cal  *ical.Calendar
	err  error
}

func newICSClient(cfg Config, hc *retryClient) *icsClient {
	return &icsClient{cfg: cfg, http: hc}
}

// Mode reports ICS mode.
func (c *icsClient) Mode() Mode { return ModeICS }

// fetch retrieves and parses the document once.
func (c *icsClient) fetch(ctx context.Context) ([]byte, *ical.Calendar, error) {
	c.once.Do(func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.URL, http.NoBody)
		if err != nil {
			c.err = fmt.Errorf("build request for %s: %w", c.cfg.URL, err)
			return
		}
		req.Header.Set("Accept", "text/calendar, text/plain, */*")

		resp, err := c.http.Do(req)
		if err != nil {
			c.err = fmt.Errorf("fetch calendar %s: %w", c.cfg.URL, err)
			return
		}
		defer resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusNotFound:
			c.err = fmt.Errorf("%w: calendar %s returned 404", ErrNotFound, c.cfg.URL)
			return
		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			c.err = fmt.Errorf("fetch calendar %s: %s (supply --user/--pass or check credentials)",
				c.cfg.URL, resp.Status)
			return
		case resp.StatusCode/100 != 2:
			c.err = fmt.Errorf("fetch calendar %s: unexpected status %s", c.cfg.URL, resp.Status)
			return
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, maxICSBytes))
		if err != nil {
			c.err = fmt.Errorf("read calendar %s: %w", c.cfg.URL, err)
			return
		}

		cals, err := event.DecodeCalendars(bytes.NewReader(body))
		if err != nil {
			c.err = fmt.Errorf("parse calendar %s: %w", c.cfg.URL, err)
			return
		}

		c.raw = body
		c.cal = event.MergeCalendars(cals)
	})
	return c.raw, c.cal, c.err
}

// List returns the events in the document matching q.
func (c *icsClient) List(ctx context.Context, q Query) ([]*event.Event, error) {
	_, cal, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}

	loc := q.Location
	if loc == nil {
		loc = c.cfg.Location
	}

	events, err := event.Events(cal, loc)
	if err != nil {
		return nil, err
	}

	if q.Expand {
		events, err = event.Expand(events, q.From, q.To, loc)
		if err != nil {
			return nil, err
		}
	} else {
		events = filterWindow(events, q.From, q.To)
	}
	return applyLimit(events, q.Limit), nil
}

// Get resolves a UID or unambiguous prefix within the document.
func (c *icsClient) Get(ctx context.Context, uid string) (*event.Event, error) {
	events, err := c.List(ctx, Query{Location: c.cfg.Location})
	if err != nil {
		return nil, err
	}
	return selectByUID(events, uid)
}

// Put always fails: an ICS URL is read-only.
func (c *icsClient) Put(context.Context, *event.Event) error {
	return readOnlyError("add or edit events")
}

// Delete always fails: an ICS URL is read-only.
func (c *icsClient) Delete(context.Context, *event.Event) error {
	return readOnlyError("delete events")
}

// Raw returns the document exactly as served.
func (c *icsClient) Raw(ctx context.Context) ([]byte, error) {
	raw, _, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// filterWindow keeps events overlapping [from, to), leaving recurring masters
// in place without expansion.
func filterWindow(events []*event.Event, from, to time.Time) []*event.Event {
	if from.IsZero() && to.IsZero() {
		return events
	}
	out := make([]*event.Event, 0, len(events))
	for _, ev := range events {
		// A recurring master may have instances inside the window even when its
		// own DTSTART lies before it, so recurring events are never dropped on
		// the strength of the master's bounds alone.
		if ev.Recurring() {
			out = append(out, ev)
			continue
		}
		if eventOverlaps(ev, from, to) {
			out = append(out, ev)
		}
	}
	return out
}

func eventOverlaps(ev *event.Event, from, to time.Time) bool {
	end := ev.End
	if end.IsZero() || !end.After(ev.Start) {
		end = ev.Start.Add(time.Nanosecond)
	}
	if !from.IsZero() && !end.After(from) {
		return false
	}
	if !to.IsZero() && !ev.Start.Before(to) {
		return false
	}
	return true
}

// selectByUID resolves a UID query against a set of events.
func selectByUID(events []*event.Event, uid string) (*event.Event, error) {
	uids := make([]string, 0, len(events))
	for _, ev := range events {
		uids = append(uids, ev.UID())
	}

	match, err := event.MatchUID(uids, uid)
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		if ev.UID() == match {
			return ev, nil
		}
	}
	return nil, fmt.Errorf("%w: uid %q", ErrNotFound, uid)
}
