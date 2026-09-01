package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/prdlk/ical-cli/internal/event"
)

const outputFixture = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//x//EN
BEGIN:VEVENT
UID:review@example.com
DTSTAMP:20260101T090000Z
DTSTART:20260305T140000Z
DTEND:20260305T150000Z
SUMMARY:Q1 review
LOCATION:Boardroom
DESCRIPTION:Line one\nLine two
STATUS:CONFIRMED
SEQUENCE:2
ORGANIZER;CN=Ada:mailto:ada@example.com
ATTENDEE;CN=Bob:mailto:bob@example.com
CATEGORIES:work,finance
X-CUSTOM-TAG:keep-me
END:VEVENT
BEGIN:VEVENT
UID:offsite@example.com
DTSTAMP:20260101T090000Z
DTSTART;VALUE=DATE:20260410
DTEND;VALUE=DATE:20260413
SUMMARY:Team offsite
LOCATION:Lisbon
END:VEVENT
END:VCALENDAR
`

func fixtureEvents(t *testing.T) []*event.Event {
	t.Helper()

	cal, err := event.DecodeCalendar(strings.NewReader(outputFixture))
	if err != nil {
		t.Fatalf("DecodeCalendar returned error: %v", err)
	}
	events, err := event.Events(cal, time.UTC)
	if err != nil {
		t.Fatalf("Events returned error: %v", err)
	}
	return events
}

func TestTableOutput(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	w := New(&out, &errOut, time.UTC, false)

	if err := w.Events(fixtureEvents(t)); err != nil {
		t.Fatalf("Events returned error: %v", err)
	}

	got := out.String()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("table has %d lines, want 3 (header plus two events)\n%s", len(lines), got)
	}

	for _, want := range []string{"UID", "START", "END", "SUMMARY", "LOCATION"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header is missing %q: %s", want, lines[0])
		}
	}

	// The short UID drops the domain part.
	if !strings.Contains(lines[1], "review") || strings.Contains(lines[1], "@example.com") {
		t.Errorf("uid column is not shortened: %s", lines[1])
	}
	if !strings.Contains(lines[1], "2026-03-05 14:00") {
		t.Errorf("start is not rendered in the display timezone: %s", lines[1])
	}
	// An all-day DTEND is exclusive, so the displayed last day is one earlier.
	if !strings.Contains(lines[2], "2026-04-10") || !strings.Contains(lines[2], "2026-04-12") {
		t.Errorf("all-day row does not show an inclusive date range: %s", lines[2])
	}
	// A multi-line description must not break alignment.
	if strings.Count(got, "\n") != 3 {
		t.Errorf("table contains embedded newlines:\n%s", got)
	}
}

func TestTableOutputEmpty(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	w := New(&out, &errOut, time.UTC, false)

	if err := w.Events(nil); err != nil {
		t.Fatalf("Events returned error: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "no events" {
		t.Errorf("empty output = %q, want %q", got, "no events")
	}
}

func TestJSONOutput(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	w := New(&out, &errOut, time.UTC, true)

	if err := w.Events(fixtureEvents(t)); err != nil {
		t.Fatalf("Events returned error: %v", err)
	}

	var decoded []EventJSON
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded %d events, want 2", len(decoded))
	}

	review := decoded[0]
	tests := []struct {
		field string
		got   any
		want  any
	}{
		{field: "uid", got: review.UID, want: "review@example.com"},
		{field: "summary", got: review.Summary, want: "Q1 review"},
		{field: "location", got: review.Location, want: "Boardroom"},
		{field: "start", got: review.Start, want: "2026-03-05T14:00:00Z"},
		{field: "end", got: review.End, want: "2026-03-05T15:00:00Z"},
		{field: "all_day", got: review.AllDay, want: false},
		{field: "duration_seconds", got: review.DurationSecs, want: int64(3600)},
		{field: "status", got: review.Status, want: "CONFIRMED"},
		{field: "sequence", got: review.Sequence, want: 2},
		{field: "organizer", got: review.Organizer, want: "mailto:ada@example.com"},
		{field: "recurring", got: review.Recurring, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %v, want %v", tc.field, tc.got, tc.want)
			}
		})
	}

	if len(review.Attendees) != 1 || review.Attendees[0] != "mailto:bob@example.com" {
		t.Errorf("attendees = %v, want [mailto:bob@example.com]", review.Attendees)
	}
	if len(review.Categories) != 2 {
		t.Errorf("categories = %v, want two entries", review.Categories)
	}
	// Unmodeled properties must remain visible to JSON consumers.
	if got := review.Extra["X-CUSTOM-TAG"]; len(got) != 1 || got[0] != "keep-me" {
		t.Errorf("extra[X-CUSTOM-TAG] = %v, want [keep-me]", got)
	}

	// An all-day event reports dates, not timestamps.
	offsite := decoded[1]
	if offsite.Start != "2026-04-10" {
		t.Errorf("all-day start = %q, want 2026-04-10", offsite.Start)
	}
	if !offsite.AllDay {
		t.Error("all_day = false, want true")
	}
}

// TestJSONFieldNamesAreStable guards the documented output contract: renaming a
// field silently breaks every script parsing it.
func TestJSONFieldNamesAreStable(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	w := New(&out, &errOut, time.UTC, true)

	if err := w.Detail(fixtureEvents(t)[0]); err != nil {
		t.Fatalf("Detail returned error: %v", err)
	}

	var generic map[string]any
	if err := json.Unmarshal(out.Bytes(), &generic); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	for _, key := range []string{
		"uid", "summary", "description", "location", "start", "end",
		"all_day", "duration_seconds", "recurring", "occurrence",
		"status", "sequence", "organizer", "attendees", "categories",
		"last_modified", "extra",
	} {
		if _, ok := generic[key]; !ok {
			t.Errorf("JSON output is missing the %q field", key)
		}
	}
}

func TestTimezoneRendering(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("timezone database unavailable: %v", err)
	}

	var out, errOut bytes.Buffer
	w := New(&out, &errOut, loc, false)

	if err := w.Events(fixtureEvents(t)[:1]); err != nil {
		t.Fatalf("Events returned error: %v", err)
	}

	// 14:00 UTC is 09:00 in New York during March.
	if !strings.Contains(out.String(), "2026-03-05 09:00") {
		t.Errorf("output is not converted to the display timezone:\n%s", out.String())
	}
}

func TestResultRendering(t *testing.T) {
	t.Parallel()

	payload := struct {
		Created bool   `json:"created"`
		UID     string `json:"uid"`
	}{Created: true, UID: "x@ical-cli"}

	t.Run("table mode prints the message", func(t *testing.T) {
		var out, errOut bytes.Buffer
		w := New(&out, &errOut, time.UTC, false)
		if err := w.Result("created x@ical-cli", payload); err != nil {
			t.Fatalf("Result returned error: %v", err)
		}
		if got := strings.TrimSpace(out.String()); got != "created x@ical-cli" {
			t.Errorf("output = %q, want the human message", got)
		}
	})

	t.Run("json mode prints the payload", func(t *testing.T) {
		var out, errOut bytes.Buffer
		w := New(&out, &errOut, time.UTC, true)
		if err := w.Result("created x@ical-cli", payload); err != nil {
			t.Fatalf("Result returned error: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
			t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
		}
		if decoded["uid"] != "x@ical-cli" || decoded["created"] != true {
			t.Errorf("decoded payload = %v", decoded)
		}
	})
}

// TestInfoGoesToStderr keeps diagnostics out of parseable stdout.
func TestInfoGoesToStderr(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	w := New(&out, &errOut, time.UTC, true)
	w.Info("wrote %d bytes", 42)

	if out.Len() != 0 {
		t.Errorf("Info wrote to stdout: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "wrote 42 bytes") {
		t.Errorf("stderr = %q, want the diagnostic", errOut.String())
	}
}

func TestOneLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "plain", input: "hello", want: "hello"},
		{name: "unix newline", input: "a\nb", want: "a b"},
		{name: "windows newline", input: "a\r\nb", want: "a b"},
		{name: "tab", input: "a\tb", want: "a b"},
		{name: "trimmed", input: "  a  ", want: "a"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := oneLine(tc.input); got != tc.want {
				t.Errorf("oneLine(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
