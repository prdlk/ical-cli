package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/prdlk/ical-cli/internal/event"
)

// maxObjectBytes bounds a single calendar object response.
const maxObjectBytes = 8 << 20

// caldavClient provides full read/write access to a CalDAV collection.
//
// Reads use go-webdav's REPORT helper. Writes and single-object reads use raw
// HTTP requests instead, for two reasons:
//
//   - caldav.Client.PutCalendarObject does not send If-Match or If-None-Match
//     (upstream carries a TODO to that effect), so conditional writes are
//     impossible through it and lost updates would go undetected.
//   - go-webdav wraps every non-2xx response in internal.HTTPError, an
//     unexported type in an internal package. Callers cannot recover the status
//     code from it, and this tool must tell 412 Precondition Failed apart from
//     404 Not Found to report conflicts accurately.
//
// Raw requests reuse the same retrying transport, so timeout, retry and
// authentication behaviour is identical on both paths.
type caldavClient struct {
	cfg  Config
	http *retryClient
	dav  *caldav.Client

	// base is the endpoint URL, used to resolve collection-relative hrefs.
	base *url.URL
	// collection is the escaped path of the calendar collection.
	collection string
}

func newCalDAVClient(ctx context.Context, cfg Config, hc *retryClient) (*caldavClient, error) {
	base, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse calendar url %q: %w", cfg.URL, err)
	}
	if base.Path == "" {
		base.Path = "/"
	}

	dav, err := caldav.NewClient(hc, cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("create caldav client for %s: %w", cfg.URL, err)
	}

	c := &caldavClient{cfg: cfg, http: hc, dav: dav, base: base}

	collection, err := c.resolveCollection(ctx)
	if err != nil {
		return nil, err
	}
	c.collection = collection
	return c, nil
}

// Mode reports CalDAV mode.
func (c *caldavClient) Mode() Mode { return ModeCalDAV }

// Collection returns the resolved calendar collection path.
func (c *caldavClient) Collection() string { return c.collection }

// resolveCollection finds the calendar collection to operate on.
//
// The supplied URL usually already is the collection, which a single PROPFIND
// confirms. Otherwise RFC 6764 discovery runs: current-user-principal ->
// calendar-home-set -> the first calendar that accepts VEVENT.
func (c *caldavClient) resolveCollection(ctx context.Context) (string, error) {
	if probePropFind(ctx, c.http, c.base.String()) {
		return c.base.EscapedPath(), nil
	}

	principal, err := c.dav.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return "", fmt.Errorf("discover caldav principal at %s: %w "+
			"(if this URL is a plain .ics feed, drop --caldav; writes require a CalDAV server)",
			c.cfg.URL, err)
	}

	homeSet, err := c.dav.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return "", fmt.Errorf("discover calendar home set for principal %s: %w", principal, err)
	}

	calendars, err := c.dav.FindCalendars(ctx, homeSet)
	if err != nil {
		return "", fmt.Errorf("list calendars in %s: %w", homeSet, err)
	}
	if len(calendars) == 0 {
		return "", fmt.Errorf("no calendars found in home set %s", homeSet)
	}

	for i := range calendars {
		if supportsEvents(&calendars[i]) {
			return calendars[i].Path, nil
		}
	}
	return calendars[0].Path, nil
}

func supportsEvents(cal *caldav.Calendar) bool {
	if len(cal.SupportedComponentSet) == 0 {
		return true // unspecified means no restriction
	}
	for _, comp := range cal.SupportedComponentSet {
		if strings.EqualFold(comp, ical.CompEvent) {
			return true
		}
	}
	return false
}

// List returns events in the collection matching q.
func (c *caldavClient) List(ctx context.Context, q Query) ([]*event.Event, error) {
	loc := q.Location
	if loc == nil {
		loc = c.cfg.Location
	}

	objects, err := c.query(ctx, q.From, q.To, "")
	if err != nil {
		return nil, err
	}

	events, err := c.eventsFromObjects(objects, loc)
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

func (c *caldavClient) eventsFromObjects(objects []caldav.CalendarObject, loc *time.Location) ([]*event.Event, error) {
	events := make([]*event.Event, 0, len(objects))
	for _, obj := range objects {
		if obj.Data == nil {
			continue
		}
		evs, err := event.Events(obj.Data, loc)
		if err != nil {
			return nil, err
		}
		for _, ev := range evs {
			ev.Path = obj.Path
			ev.ETag = obj.ETag
			events = append(events, ev)
		}
	}
	return events, nil
}

// query issues a calendar-query REPORT for VEVENTs. Zero bounds leave the time
// range open. A non-empty uid adds a UID property filter, making an exact
// lookup a single round trip.
func (c *caldavClient) query(ctx context.Context, from, to time.Time, uid string) ([]caldav.CalendarObject, error) {
	comp := caldav.CompFilter{Name: ical.CompEvent}
	if !from.IsZero() {
		comp.Start = from.UTC()
	}
	if !to.IsZero() {
		comp.End = to.UTC()
	}
	if uid != "" {
		comp.Props = []caldav.PropFilter{{
			Name:      ical.PropUID,
			TextMatch: &caldav.TextMatch{Text: uid},
		}}
	}

	// AllProps/AllComps requests the complete calendar object. Full fidelity is
	// required: edit is a read-modify-write that must preserve attendees,
	// alarms and custom X- properties it does not model.
	q := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     ical.CompCalendar,
			AllProps: true,
			AllComps: true,
		},
		CompFilter: caldav.CompFilter{
			Name:  ical.CompCalendar,
			Comps: []caldav.CompFilter{comp},
		},
	}

	objects, err := c.dav.QueryCalendar(ctx, c.collection, q)
	if err != nil {
		if statusFromError(err) == http.StatusNotFound {
			return nil, fmt.Errorf("%w: calendar collection %s", ErrNotFound, c.collection)
		}
		return nil, fmt.Errorf("query calendar %s: %w", c.collection, err)
	}
	return objects, nil
}

// Get resolves a UID or unambiguous UID prefix to the stored event, then
// re-reads the object so the returned ETag and data come from a single
// authoritative GET.
func (c *caldavClient) Get(ctx context.Context, uid string) (*event.Event, error) {
	loc := c.cfg.Location

	// Fast path: exact UID match server-side.
	objects, err := c.query(ctx, time.Time{}, time.Time{}, uid)
	if err != nil {
		return nil, err
	}
	events, err := c.eventsFromObjects(objects, loc)
	if err != nil {
		return nil, err
	}

	var target *event.Event
	for _, ev := range events {
		if ev.UID() == uid && ev.RecurrenceID.IsZero() {
			target = ev
			break
		}
	}

	// Slow path: prefix matching needs the full UID set.
	if target == nil {
		all, err := c.List(ctx, Query{Location: loc})
		if err != nil {
			return nil, err
		}
		masters := make([]*event.Event, 0, len(all))
		for _, ev := range all {
			if ev.RecurrenceID.IsZero() {
				masters = append(masters, ev)
			}
		}
		target, err = selectByUID(masters, uid)
		if err != nil {
			return nil, err
		}
	}

	// Re-read for an authoritative ETag and complete data.
	if target.Path != "" {
		fresh, err := c.getObject(ctx, target.Path, target.UID(), loc)
		if err != nil {
			return nil, err
		}
		return fresh, nil
	}
	return target, nil
}

// getObject performs a raw GET so ETag parsing stays under this package's
// control. go-webdav's populateCalendarObject runs strconv.Unquote on the ETag
// header, which turns a weak validator such as W/"abc" into an error rather
// than a usable value.
func (c *caldavClient) getObject(ctx context.Context, objPath, uid string, loc *time.Location) (*event.Event, error) {
	u, err := c.resolve(objPath)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build GET for %s: %w", objPath, err)
	}
	req.Header.Set("Accept", ical.MIMEType)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get event %s: %w", objPath, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: event at %s", ErrNotFound, objPath)
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("get event %s: unexpected status %s", objPath, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxObjectBytes))
	if err != nil {
		return nil, fmt.Errorf("read event %s: %w", objPath, err)
	}

	cal, err := event.DecodeCalendar(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse event %s: %w", objPath, err)
	}

	events, err := event.Events(cal, loc)
	if err != nil {
		return nil, err
	}

	etag := resp.Header.Get("ETag")
	for _, ev := range events {
		if ev.RecurrenceID.IsZero() && (uid == "" || ev.UID() == uid) {
			ev.Path = objPath
			ev.ETag = etag
			return ev, nil
		}
	}
	if len(events) > 0 {
		events[0].Path = objPath
		events[0].ETag = etag
		return events[0], nil
	}
	return nil, fmt.Errorf("%w: no VEVENT in object %s", ErrNotFound, objPath)
}

// Put creates or replaces an event.
//
// A non-empty ETag becomes If-Match, so a server-side change since the read
// surfaces as ErrConflict rather than silently overwriting. A new event
// (no path) uses If-None-Match: * so an accidental UID collision cannot clobber
// an existing object.
func (c *caldavClient) Put(ctx context.Context, ev *event.Event) error {
	objPath := ev.Path
	create := objPath == ""
	if create {
		objPath = c.objectPath(ev.UID())
	}

	body, err := event.EncodeCalendar(ev.Calendar())
	if err != nil {
		return err
	}

	u, err := c.resolve(objPath)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build PUT for %s: %w", objPath, err)
	}
	req.Header.Set("Content-Type", ical.MIMEType)
	// Some servers, notably Radicale, reject a chunked calendar PUT.
	req.ContentLength = int64(len(body))

	switch {
	case ev.ETag != "":
		req.Header.Set("If-Match", formatConditional(ev.ETag))
	case create:
		req.Header.Set("If-None-Match", "*")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("put event %s: %w", ev.UID(), err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode == http.StatusPreconditionFailed:
		return fmt.Errorf("%w: event %s changed on the server since it was read; "+
			"re-run the command to pick up the current version", ErrConflict, ev.UID())
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: cannot write to %s", ErrNotFound, objPath)
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("put event %s: unexpected status %s", ev.UID(), resp.Status)
	}

	ev.Path = objPath
	if etag := resp.Header.Get("ETag"); etag != "" {
		ev.ETag = etag
	}
	return nil
}

// Delete removes an event, sending If-Match when an ETag is known.
func (c *caldavClient) Delete(ctx context.Context, ev *event.Event) error {
	objPath := ev.Path
	if objPath == "" {
		objPath = c.objectPath(ev.UID())
	}

	u, err := c.resolve(objPath)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), http.NoBody)
	if err != nil {
		return fmt.Errorf("build DELETE for %s: %w", objPath, err)
	}
	if ev.ETag != "" {
		req.Header.Set("If-Match", formatConditional(ev.ETag))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete event %s: %w", ev.UID(), err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode == http.StatusPreconditionFailed:
		return fmt.Errorf("%w: event %s changed on the server since it was read; "+
			"re-run the command to pick up the current version", ErrConflict, ev.UID())
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: event %s at %s", ErrNotFound, ev.UID(), objPath)
	case resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNoContent:
		return fmt.Errorf("delete event %s: unexpected status %s", ev.UID(), resp.Status)
	}
	return nil
}

// Raw returns every event in the collection as one merged ICS document.
func (c *caldavClient) Raw(ctx context.Context) ([]byte, error) {
	objects, err := c.query(ctx, time.Time{}, time.Time{}, "")
	if err != nil {
		return nil, err
	}

	cals := make([]*ical.Calendar, 0, len(objects))
	for _, obj := range objects {
		if obj.Data != nil {
			cals = append(cals, obj.Data)
		}
	}
	if len(cals) == 0 {
		// An empty collection still serializes to a valid, empty document.
		return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:" + event.ProductID + "\r\nEND:VCALENDAR\r\n"), nil
	}
	return event.EncodeCalendar(event.MergeCalendars(cals))
}

// objectPath derives a collection-relative resource path for a new event.
func (c *caldavClient) objectPath(uid string) string {
	name := uid
	if !strings.HasSuffix(strings.ToLower(name), ".ics") {
		name += ".ics"
	}
	ref := &url.URL{Path: path.Join(unescapePath(c.collection), name)}
	return ref.EscapedPath()
}

// resolve turns a server href or path into an absolute URL.
//
// Hrefs arrive URL-escaped. Parsing them as a reference and resolving against
// the base preserves RawPath, so String() re-encodes exactly once; building a
// URL by assigning to Path would double-escape any percent sequence.
func (c *caldavClient) resolve(href string) (*url.URL, error) {
	ref, err := url.Parse(href)
	if err != nil {
		return nil, fmt.Errorf("parse resource path %q: %w", href, err)
	}
	return c.base.ResolveReference(ref), nil
}

func unescapePath(p string) string {
	if decoded, err := url.PathUnescape(p); err == nil {
		return decoded
	}
	return p
}

// formatConditional renders an ETag for an If-Match header.
//
// ETags reach this client from two sources: raw response headers, which keep
// their quotes and any weak W/ prefix, and go-webdav's REPORT decoding, which
// strips the quotes. Quoting only when quotes are absent normalises both
// without corrupting a weak validator.
func formatConditional(etag string) string {
	e := strings.TrimSpace(etag)
	if e == "" {
		return e
	}
	if strings.HasPrefix(e, "W/") || strings.HasPrefix(e, `"`) {
		return e
	}
	return `"` + e + `"`
}
