package client

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prdlk/ical-cli/internal/event"
)

const reviewICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//x//EN
BEGIN:VEVENT
UID:review@example.com
DTSTAMP:20260101T090000Z
DTSTART:20260305T140000Z
DTEND:20260305T150000Z
SUMMARY:Q1 review
LOCATION:Boardroom
ATTENDEE;CN=Bob:mailto:bob@example.com
X-CUSTOM-TAG:keep-me
END:VEVENT
END:VCALENDAR
`

const standupICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//x//EN
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

// object is one stored calendar object resource.
type object struct {
	data string
	etag string
}

// fakeCalDAV is a minimal CalDAV server: enough of PROPFIND, REPORT, GET, PUT
// and DELETE to exercise the client, including conditional request handling.
type fakeCalDAV struct {
	mu      sync.Mutex
	objects map[string]object

	// requests records method and conditional headers for assertions.
	requests []recordedRequest

	// advertiseDav controls whether OPTIONS reports calendar-access, which is
	// what auto-detection looks for first.
	advertiseDav bool
	// propfindIsCalendar controls whether PROPFIND reports a calendar
	// resourcetype.
	propfindIsCalendar bool
	// supportDiscovery enables the RFC 6764 chain: current-user-principal ->
	// calendar-home-set -> calendar collections.
	supportDiscovery bool
}

type recordedRequest struct {
	Method      string
	Path        string
	IfMatch     string
	IfNoneMatch string
	Body        string
}

func newFakeCalDAV() *fakeCalDAV {
	return &fakeCalDAV{
		objects: map[string]object{
			"/cal/review.ics":  {data: reviewICS, etag: `"etag-review-1"`},
			"/cal/standup.ics": {data: standupICS, etag: `"etag-standup-1"`},
		},
		advertiseDav:       true,
		propfindIsCalendar: true,
	}
}

func (f *fakeCalDAV) record(r *http.Request, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, recordedRequest{
		Method:      r.Method,
		Path:        r.URL.Path,
		IfMatch:     r.Header.Get("If-Match"),
		IfNoneMatch: r.Header.Get("If-None-Match"),
		Body:        body,
	})
}

// lastOf returns the most recent recorded request with the given method.
func (f *fakeCalDAV) lastOf(method string) (recordedRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if f.requests[i].Method == method {
			return f.requests[i], true
		}
	}
	return recordedRequest{}, false
}

func (f *fakeCalDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.record(r, string(body))

	switch r.Method {
	case http.MethodOptions:
		if f.advertiseDav {
			w.Header().Set("Dav", "1, 2, 3, calendar-access, addressbook")
		} else {
			w.Header().Set("Dav", "1, 2")
		}
		w.WriteHeader(http.StatusOK)

	case "PROPFIND":
		f.servePropFind(w, r, string(body))

	case "REPORT":
		f.serveReport(w)

	case http.MethodGet:
		f.serveGet(w, r)

	case http.MethodPut:
		f.servePut(w, r, body)

	case http.MethodDelete:
		f.serveDelete(w, r)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeCalDAV) servePropFind(w http.ResponseWriter, r *http.Request, body string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")

	switch {
	case strings.Contains(body, "current-user-principal"):
		if !f.supportDiscovery {
			f.writeMultiStatus(w, r.URL.Path, "", `<current-user-principal/>`)
			return
		}
		f.writeMultiStatus(w, r.URL.Path,
			`<current-user-principal><href>/principals/user/</href></current-user-principal>`,
			"")

	case strings.Contains(body, "calendar-home-set"):
		if !f.supportDiscovery {
			f.writeMultiStatus(w, r.URL.Path, "", `<C:calendar-home-set/>`)
			return
		}
		f.writeMultiStatus(w, r.URL.Path,
			`<C:calendar-home-set><href>/cal-home/</href></C:calendar-home-set>`,
			"")

	case strings.Contains(body, "supported-calendar-component-set"):
		// FindCalendars: Depth 1 listing of the calendar home set.
		f.writeMultiStatus(w, "/cal/",
			`<resourcetype><collection/><C:calendar/></resourcetype>`+
				`<displayname>Work</displayname>`+
				`<C:supported-calendar-component-set><C:comp name="VEVENT"/></C:supported-calendar-component-set>`,
			"")

	default:
		resourceType := "<collection/>"
		if f.propfindIsCalendar {
			resourceType = "<collection/><C:calendar/>"
		}
		f.writeMultiStatus(w, r.URL.Path,
			"<resourcetype>"+resourceType+"</resourcetype>", "")
	}
}

// writeMultiStatus emits a 207 carrying one response. foundProps are reported
// with 200; missingProps with 404, which go-webdav tolerates for optional
// properties.
func (f *fakeCalDAV) writeMultiStatus(w http.ResponseWriter, href, foundProps, missingProps string) {
	var propstats strings.Builder
	if foundProps != "" {
		fmt.Fprintf(&propstats,
			"<propstat><prop>%s</prop><status>HTTP/1.1 200 OK</status></propstat>", foundProps)
	}
	if missingProps != "" {
		fmt.Fprintf(&propstats,
			"<propstat><prop>%s</prop><status>HTTP/1.1 404 Not Found</status></propstat>", missingProps)
	}
	w.WriteHeader(http.StatusMultiStatus)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<multistatus xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <response><href>%s</href>%s</response>
</multistatus>`, href, propstats.String())
}

func (f *fakeCalDAV) serveReport(w http.ResponseWriter) {
	f.mu.Lock()
	paths := make([]string, 0, len(f.objects))
	for p := range f.objects {
		paths = append(paths, p)
	}
	snapshot := make(map[string]object, len(f.objects))
	for k, v := range f.objects {
		snapshot[k] = v
	}
	f.mu.Unlock()

	// Deterministic ordering keeps assertions stable.
	sortStrings(paths)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<multistatus xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + "\n")
	for _, p := range paths {
		obj := snapshot[p]
		var escaped strings.Builder
		_ = xml.EscapeText(&escaped, []byte(obj.data))
		fmt.Fprintf(&b, `  <response>
    <href>%s</href>
    <propstat>
      <prop>
        <getetag>%s</getetag>
        <C:calendar-data>%s</C:calendar-data>
      </prop>
      <status>HTTP/1.1 200 OK</status>
    </propstat>
  </response>
`, p, obj.etag, escaped.String())
	}
	b.WriteString(`</multistatus>`)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, b.String())
}

func (f *fakeCalDAV) serveGet(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	obj, ok := f.objects[r.URL.Path]
	f.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/calendar")
	w.Header().Set("ETag", obj.etag)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, obj.data)
}

func (f *fakeCalDAV) servePut(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	obj, exists := f.objects[r.URL.Path]

	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
		if !exists || ifMatch != obj.etag {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
	}
	if r.Header.Get("If-None-Match") == "*" && exists {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}

	newETag := `"etag-` + fmt.Sprint(len(f.requests)) + `"`
	f.objects[r.URL.Path] = object{data: string(body), etag: newETag}

	w.Header().Set("ETag", newETag)
	if exists {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeCalDAV) serveDelete(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	obj, exists := f.objects[r.URL.Path]
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && ifMatch != obj.etag {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	delete(f.objects, r.URL.Path)
	w.WriteHeader(http.StatusNoContent)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// newTestCalDAV starts the fake server and returns a connected client.
func newTestCalDAV(t *testing.T, force bool) (*fakeCalDAV, CalendarClient) {
	t.Helper()

	fake := newFakeCalDAV()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	cl, err := New(context.Background(), Config{
		URL:         srv.URL + "/cal/",
		User:        "user",
		Pass:        "secret",
		ForceCalDAV: force,
		Location:    time.UTC,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if got := cl.Mode(); got != ModeCalDAV {
		t.Fatalf("Mode = %q, want %q", got, ModeCalDAV)
	}
	return fake, cl
}

func TestCalDAVAutoDetect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		advertiseDav       bool
		propfindIsCalendar bool
		urlPath            string
		wantMode           Mode
	}{
		{
			name:               "options advertises calendar-access",
			advertiseDav:       true,
			propfindIsCalendar: true,
			urlPath:            "/cal/",
			wantMode:           ModeCalDAV,
		},
		{
			name:               "propfind reports a calendar resourcetype",
			advertiseDav:       false,
			propfindIsCalendar: true,
			urlPath:            "/cal/",
			wantMode:           ModeCalDAV,
		},
		{
			name:               "neither probe matches",
			advertiseDav:       false,
			propfindIsCalendar: false,
			urlPath:            "/cal/",
			wantMode:           ModeICS,
		},
		{
			// A .ics path is taken as a static document without probing, which
			// is the read-only shape the tool documents.
			name:               "ics suffix skips probing",
			advertiseDav:       true,
			propfindIsCalendar: true,
			urlPath:            "/cal.ics",
			wantMode:           ModeICS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeCalDAV()
			fake.advertiseDav = tc.advertiseDav
			fake.propfindIsCalendar = tc.propfindIsCalendar
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			cl, err := New(context.Background(), Config{
				URL:      srv.URL + tc.urlPath,
				Location: time.UTC,
			})
			if err != nil {
				t.Fatalf("New returned error: %v", err)
			}
			if got := cl.Mode(); got != tc.wantMode {
				t.Errorf("Mode = %q, want %q", got, tc.wantMode)
			}
		})
	}
}

func TestCalDAVList(t *testing.T) {
	t.Parallel()

	_, cl := newTestCalDAV(t, true)

	events, err := cl.List(context.Background(), Query{
		From:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		To:       time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		Expand:   true,
		Location: time.UTC,
	})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	// Three standup occurrences plus the review.
	if len(events) != 4 {
		t.Fatalf("List returned %d events, want 4", len(events))
	}

	// Path and ETag must be carried through so a later write can be conditional.
	for _, ev := range events {
		if ev.Path == "" {
			t.Errorf("event %s has no resource path", ev.UID())
		}
		if ev.ETag == "" {
			t.Errorf("event %s has no etag", ev.UID())
		}
	}
}

func TestCalDAVGet(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			_, cl := newTestCalDAV(t, true)

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
				t.Errorf("summary = %q, want %q", ev.Summary(), tc.wantSummary)
			}
			// The ETag must come from the authoritative GET so a following
			// write can use If-Match.
			if ev.ETag == "" {
				t.Error("Get returned an event with no etag")
			}
			if ev.Path == "" {
				t.Error("Get returned an event with no resource path")
			}
		})
	}
}

// TestCalDAVPutSendsIfMatch is the lost-update guard: an existing event must be
// written conditionally on the ETag that was read.
func TestCalDAVPutSendsIfMatch(t *testing.T) {
	t.Parallel()

	fake, cl := newTestCalDAV(t, true)

	ev, err := cl.Get(context.Background(), "review@example.com")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	original := ev.ETag

	ev.SetSummary("Q1 review (updated)")
	ev.Touch(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))

	if err := cl.Put(context.Background(), ev); err != nil {
		t.Fatalf("Put returned error: %v", err)
	}

	put, ok := fake.lastOf(http.MethodPut)
	if !ok {
		t.Fatal("server received no PUT")
	}
	if put.IfMatch != original {
		t.Errorf("If-Match = %q, want %q", put.IfMatch, original)
	}
	if put.Path != "/cal/review.ics" {
		t.Errorf("PUT path = %q, want /cal/review.ics", put.Path)
	}

	// The written body must keep the properties the tool does not model.
	for _, want := range []string{
		"SUMMARY:Q1 review (updated)",
		"X-CUSTOM-TAG:keep-me",
		"mailto:bob@example.com",
		"SEQUENCE:1",
	} {
		if !strings.Contains(put.Body, want) {
			t.Errorf("PUT body is missing %q\n%s", want, put.Body)
		}
	}

	// The refreshed ETag must be adopted for any subsequent write.
	if ev.ETag == original {
		t.Error("event etag was not refreshed from the PUT response")
	}
}

// TestCalDAVPutConflict checks a stale ETag surfaces as ErrConflict, which the
// CLI maps to exit status 3.
func TestCalDAVPutConflict(t *testing.T) {
	t.Parallel()

	_, cl := newTestCalDAV(t, true)

	ev, err := cl.Get(context.Background(), "review@example.com")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	// Simulate another client having written since the read.
	ev.ETag = `"etag-stale"`
	ev.SetSummary("racing write")

	err = cl.Put(context.Background(), ev)
	if err == nil {
		t.Fatal("Put returned nil error, want a conflict")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("error = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "changed on the server") {
		t.Errorf("conflict message is not actionable: %v", err)
	}
}

// TestCalDAVPutNewUsesIfNoneMatch checks a create cannot clobber an existing
// object through a UID collision.
func TestCalDAVPutNewUsesIfNoneMatch(t *testing.T) {
	t.Parallel()

	fake, cl := newTestCalDAV(t, true)

	ev := event.New("brand-new@ical-cli", time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	ev.SetSummary("New event")
	ev.SetStart(time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC), false)
	ev.SetEnd(time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), false)

	if err := cl.Put(context.Background(), ev); err != nil {
		t.Fatalf("Put returned error: %v", err)
	}

	put, ok := fake.lastOf(http.MethodPut)
	if !ok {
		t.Fatal("server received no PUT")
	}
	if put.IfNoneMatch != "*" {
		t.Errorf("If-None-Match = %q, want *", put.IfNoneMatch)
	}
	if put.IfMatch != "" {
		t.Errorf("If-Match = %q, want empty on create", put.IfMatch)
	}
	if want := "/cal/brand-new@ical-cli.ics"; put.Path != want {
		t.Errorf("PUT path = %q, want %q", put.Path, want)
	}
	if ev.Path == "" {
		t.Error("Put did not record the resource path on the event")
	}
}

func TestCalDAVDelete(t *testing.T) {
	t.Parallel()

	fake, cl := newTestCalDAV(t, true)

	ev, err := cl.Get(context.Background(), "review@example.com")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	etag := ev.ETag

	if err := cl.Delete(context.Background(), ev); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	del, ok := fake.lastOf(http.MethodDelete)
	if !ok {
		t.Fatal("server received no DELETE")
	}
	if del.IfMatch != etag {
		t.Errorf("If-Match = %q, want %q", del.IfMatch, etag)
	}

	fake.mu.Lock()
	_, still := fake.objects["/cal/review.ics"]
	fake.mu.Unlock()
	if still {
		t.Error("object survived the DELETE")
	}
}

func TestCalDAVDeleteConflict(t *testing.T) {
	t.Parallel()

	_, cl := newTestCalDAV(t, true)

	ev, err := cl.Get(context.Background(), "review@example.com")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	ev.ETag = `"etag-stale"`

	err = cl.Delete(context.Background(), ev)
	if err == nil {
		t.Fatal("Delete returned nil error, want a conflict")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("error = %v, want ErrConflict", err)
	}
}

func TestCalDAVDeleteMissing(t *testing.T) {
	t.Parallel()

	_, cl := newTestCalDAV(t, true)

	ev := event.New("ghost@ical-cli", time.Now())
	err := cl.Delete(context.Background(), ev)
	if err == nil {
		t.Fatal("Delete returned nil error, want failure")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestCalDAVRawMergesObjects(t *testing.T) {
	t.Parallel()

	_, cl := newTestCalDAV(t, true)

	raw, err := cl.Raw(context.Background())
	if err != nil {
		t.Fatalf("Raw returned error: %v", err)
	}

	out := string(raw)
	for _, want := range []string{
		"BEGIN:VCALENDAR",
		"UID:review@example.com",
		"UID:standup@example.com",
		"END:VCALENDAR",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("merged export is missing %q\n%s", want, out)
		}
	}
	// Exactly one calendar wrapper.
	if got := strings.Count(out, "BEGIN:VCALENDAR"); got != 1 {
		t.Errorf("merged export has %d VCALENDAR wrappers, want 1", got)
	}
	// The merged document must still parse.
	if _, err := event.DecodeCalendar(strings.NewReader(out)); err != nil {
		t.Errorf("merged export does not parse: %v", err)
	}
}

// TestCalDAVSendsBasicAuth checks credentials reach both the library's requests
// and this package's raw ones, since they share one transport.
func TestCalDAVSendsBasicAuth(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	seen := map[string]bool{}

	fake := newFakeCalDAV()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		mu.Lock()
		seen[r.Method] = ok && user == "user" && pass == "secret"
		mu.Unlock()
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cl, err := New(context.Background(), Config{
		URL: srv.URL + "/cal/", User: "user", Pass: "secret",
		ForceCalDAV: true, Location: time.UTC,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ev, err := cl.Get(context.Background(), "review@example.com")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	ev.SetSummary("authed")
	if err := cl.Put(context.Background(), ev); err != nil {
		t.Fatalf("Put returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// REPORT comes from go-webdav; GET and PUT are raw requests.
	for _, method := range []string{"REPORT", http.MethodGet, http.MethodPut} {
		if !seen[method] {
			t.Errorf("%s request did not carry basic auth", method)
		}
	}
}

// TestCalDAVDiscoversCollection exercises the RFC 6764 fallback: when the
// supplied URL is not itself a calendar collection, the client walks
// current-user-principal -> calendar-home-set -> calendars.
func TestCalDAVDiscoversCollection(t *testing.T) {
	t.Parallel()

	fake := newFakeCalDAV()
	fake.advertiseDav = true
	fake.propfindIsCalendar = false // the root is not a calendar
	fake.supportDiscovery = true

	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	cl, err := New(context.Background(), Config{
		URL:         srv.URL + "/",
		ForceCalDAV: true,
		Location:    time.UTC,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	dav, ok := cl.(*caldavClient)
	if !ok {
		t.Fatalf("client type = %T, want *caldavClient", cl)
	}
	if got := dav.Collection(); got != "/cal/" {
		t.Errorf("discovered collection = %q, want /cal/", got)
	}

	// The discovered collection must be usable.
	events, err := cl.List(context.Background(), Query{Location: time.UTC})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("List returned %d events, want 2", len(events))
	}
}

// TestCalDAVDiscoveryFailureIsExplained checks a forced-CalDAV URL that is
// neither a collection nor discoverable produces guidance rather than a bare
// protocol error.
func TestCalDAVDiscoveryFailureIsExplained(t *testing.T) {
	t.Parallel()

	fake := newFakeCalDAV()
	fake.propfindIsCalendar = false
	fake.supportDiscovery = false

	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	_, err := New(context.Background(), Config{
		URL:         srv.URL + "/",
		ForceCalDAV: true,
		Location:    time.UTC,
	})
	if err == nil {
		t.Fatal("New returned nil error, want failure")
	}
	if !strings.Contains(err.Error(), "CalDAV") {
		t.Errorf("error does not mention CalDAV: %v", err)
	}
}
