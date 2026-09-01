package event

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

const fixtureICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//fixture//EN
BEGIN:VEVENT
UID:timed@example.com
DTSTAMP:20260101T090000Z
DTSTART:20260305T140000Z
DTEND:20260305T150000Z
SUMMARY:Q1 review
LOCATION:Boardroom
DESCRIPTION:Quarterly numbers
SEQUENCE:2
ATTENDEE;CN=Bob;PARTSTAT=ACCEPTED:mailto:bob@example.com
ATTENDEE;CN=Cara:mailto:cara@example.com
X-CUSTOM-TAG:keep-me
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT10M
END:VALARM
END:VEVENT
BEGIN:VEVENT
UID:allday@example.com
DTSTAMP:20260101T090000Z
DTSTART;VALUE=DATE:20260410
DTEND;VALUE=DATE:20260413
SUMMARY:Team offsite
CATEGORIES:travel,team
END:VEVENT
END:VCALENDAR
`

// loadFixture parses fixtureICS and indexes the events by UID.
func loadFixture(t *testing.T) map[string]*Event {
	t.Helper()

	cal, err := DecodeCalendar(strings.NewReader(fixtureICS))
	if err != nil {
		t.Fatalf("DecodeCalendar returned error: %v", err)
	}
	events, err := Events(cal, time.UTC)
	if err != nil {
		t.Fatalf("Events returned error: %v", err)
	}

	index := make(map[string]*Event, len(events))
	for _, ev := range events {
		index[ev.UID()] = ev
	}
	return index
}

func TestEventAccessors(t *testing.T) {
	t.Parallel()

	events := loadFixture(t)

	timed, ok := events["timed@example.com"]
	if !ok {
		t.Fatal("fixture is missing the timed event")
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{name: "summary", got: timed.Summary(), want: "Q1 review"},
		{name: "location", got: timed.Location(), want: "Boardroom"},
		{name: "description", got: timed.Description(), want: "Quarterly numbers"},
		{name: "sequence", got: timed.Sequence(), want: 2},
		{name: "all day", got: timed.AllDay, want: false},
		{name: "recurring", got: timed.Recurring(), want: false},
		{name: "duration", got: timed.Duration(), want: time.Hour},
		{name: "attendee count", got: len(timed.Attendees()), want: 2},
		{name: "start", got: timed.Start.Equal(time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC)), want: true},
		{name: "end", got: timed.End.Equal(time.Date(2026, 3, 5, 15, 0, 0, 0, time.UTC)), want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestAllDayEventBounds pins the DATE-valued all-day representation: DTEND is
// exclusive, so a three-day offsite spans 72 hours.
func TestAllDayEventBounds(t *testing.T) {
	t.Parallel()

	events := loadFixture(t)
	allDay, ok := events["allday@example.com"]
	if !ok {
		t.Fatal("fixture is missing the all-day event")
	}

	if !allDay.AllDay {
		t.Error("AllDay = false, want true for a VALUE=DATE DTSTART")
	}
	wantStart := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	if !allDay.Start.Equal(wantStart) {
		t.Errorf("Start = %s, want %s", allDay.Start, wantStart)
	}
	wantEnd := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	if !allDay.End.Equal(wantEnd) {
		t.Errorf("End = %s, want %s", allDay.End, wantEnd)
	}
	if got := allDay.Duration(); got != 72*time.Hour {
		t.Errorf("Duration = %s, want 72h", got)
	}
	if got := allDay.Categories(); len(got) != 2 || got[0] != "travel" || got[1] != "team" {
		t.Errorf("Categories = %v, want [travel team]", got)
	}
}

// TestEditPreservesUnmodeledProperties is the core guarantee of edit: changing
// one field must not drop attendees, alarms or custom X- properties.
func TestEditPreservesUnmodeledProperties(t *testing.T) {
	t.Parallel()

	events := loadFixture(t)
	original := events["timed@example.com"]

	edited := original.Clone()
	edited.SetSummary("Q1 review (rescheduled)")
	edited.Touch(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))

	encoded, err := EncodeCalendar(edited.Calendar())
	if err != nil {
		t.Fatalf("EncodeCalendar returned error: %v", err)
	}
	out := string(encoded)

	for _, want := range []string{
		"SUMMARY:Q1 review (rescheduled)",
		"X-CUSTOM-TAG:keep-me",
		"mailto:bob@example.com",
		"mailto:cara@example.com",
		"BEGIN:VALARM",
		"TRIGGER:-PT10M",
		"LOCATION:Boardroom",
		"SEQUENCE:3",
		"LAST-MODIFIED:20260201T120000Z",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("encoded event is missing %q\n%s", want, out)
		}
	}

	// Cloning must not disturb the original.
	if got := original.Summary(); got != "Q1 review" {
		t.Errorf("original summary changed to %q; Clone must be independent", got)
	}
	if got := original.Sequence(); got != 2 {
		t.Errorf("original sequence changed to %d; Clone must be independent", got)
	}
}

func TestTouchBumpsSequence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	ev := New("test@ical-cli", now)

	if got := ev.Sequence(); got != 0 {
		t.Fatalf("new event sequence = %d, want 0", got)
	}
	ev.Touch(now)
	if got := ev.Sequence(); got != 1 {
		t.Errorf("sequence after one Touch = %d, want 1", got)
	}
	ev.Touch(now)
	if got := ev.Sequence(); got != 2 {
		t.Errorf("sequence after two Touches = %d, want 2", got)
	}
	if lm := ev.LastModified(time.UTC); !lm.Equal(now) {
		t.Errorf("LAST-MODIFIED = %s, want %s", lm, now)
	}
}

// TestSetStartAllDayUsesDateValue guards the DATE vs DATE-TIME distinction,
// and that a timed value is normalised to UTC rather than emitting the invalid
// "TZID=Local" that go-ical produces for time.Local.
func TestSetStartAllDayUsesDateValue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		allDay     bool
		start      time.Time
		wantSubstr string
		notSubstr  string
	}{
		{
			name:       "all-day writes a DATE value",
			allDay:     true,
			start:      time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
			wantSubstr: "DTSTART;VALUE=DATE:20260410",
		},
		{
			name:       "timed writes a UTC DATE-TIME",
			allDay:     false,
			start:      time.Date(2026, 4, 10, 9, 30, 0, 0, time.UTC),
			wantSubstr: "DTSTART:20260410T093000Z",
			notSubstr:  "TZID=Local",
		},
		{
			name:       "local time is normalised to UTC",
			allDay:     false,
			start:      time.Date(2026, 4, 10, 9, 30, 0, 0, time.Local),
			wantSubstr: "DTSTART:",
			notSubstr:  "TZID=Local",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := New("test@ical-cli", now)
			ev.SetSummary("x")
			ev.SetStart(tc.start, tc.allDay)
			ev.SetEnd(tc.start.Add(time.Hour), tc.allDay)

			encoded, err := EncodeCalendar(ev.Calendar())
			if err != nil {
				t.Fatalf("EncodeCalendar returned error: %v", err)
			}
			out := string(encoded)

			if !strings.Contains(out, tc.wantSubstr) {
				t.Errorf("encoded event is missing %q\n%s", tc.wantSubstr, out)
			}
			if tc.notSubstr != "" && strings.Contains(out, tc.notSubstr) {
				t.Errorf("encoded event must not contain %q\n%s", tc.notSubstr, out)
			}
		})
	}
}

// TestSetEndRemovesDuration guards against emitting both DTEND and DURATION,
// which RFC 5545 forbids and go-ical rejects at encode time.
func TestSetEndRemovesDuration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	ev := New("test@ical-cli", now)
	ev.SetSummary("x")
	ev.SetStart(now, false)

	dur := ical.NewProp(ical.PropDuration)
	dur.SetDuration(90 * time.Minute)
	ev.Comp.Props.Set(dur)

	ev.SetEnd(now.Add(time.Hour), false)

	if ev.Comp.Props.Get(ical.PropDuration) != nil {
		t.Error("DURATION survived SetEnd; encoding would fail")
	}
	if _, err := EncodeCalendar(ev.Calendar()); err != nil {
		t.Errorf("EncodeCalendar returned error: %v", err)
	}
}

func TestSetRRuleValidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rule    string
		wantErr bool
		want    string
	}{
		{name: "weekly by day", rule: "FREQ=WEEKLY;BYDAY=MO", want: "FREQ=WEEKLY;BYDAY=MO"},
		{name: "strips the RRULE prefix", rule: "RRULE:FREQ=DAILY", want: "FREQ=DAILY"},
		{name: "lowercase is upcased", rule: "freq=daily", want: "FREQ=DAILY"},
		{name: "empty clears the rule", rule: "", want: ""},
		{name: "missing FREQ", rule: "COUNT=5", wantErr: true},
		{name: "unknown property", rule: "FREQ=DAILY;NOPE=1", wantErr: true},
		{name: "garbage", rule: "every monday", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := New("test@ical-cli", time.Now())
			err := ev.SetRRule(tc.rule)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SetRRule(%q) = nil, want error", tc.rule)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetRRule(%q) returned unexpected error: %v", tc.rule, err)
			}
			if got := ev.RRule(); got != tc.want {
				t.Errorf("RRule() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecodeCalendarRejectsGarbage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty input", input: ""},
		{name: "not a calendar", input: "hello world\n"},
		{name: "wrong toplevel component", input: "BEGIN:VEVENT\nUID:x\nEND:VEVENT\n"},
		{name: "truncated component", input: "BEGIN:VCALENDAR\nVERSION:2.0\n"},
		{name: "truncated param", input: "BEGIN:VCALENDAR\nDTSTART;\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The contract is an error, never a panic: remote calendar data is
			// untrusted and go-ical's decoder panics on some malformed input.
			if _, err := DecodeCalendar(strings.NewReader(tc.input)); err == nil {
				t.Errorf("DecodeCalendar(%q) = nil error, want error", tc.input)
			}
		})
	}
}

func TestSplitCalendar(t *testing.T) {
	t.Parallel()

	cal, err := DecodeCalendar(strings.NewReader(fixtureICS))
	if err != nil {
		t.Fatalf("DecodeCalendar returned error: %v", err)
	}

	objects := SplitCalendar(cal)
	if len(objects) != 2 {
		t.Fatalf("SplitCalendar returned %d objects, want 2", len(objects))
	}

	for _, obj := range objects {
		events := obj.Events()
		if len(events) != 1 {
			t.Errorf("split object holds %d events, want 1", len(events))
		}
		// Each split object must independently encode, which requires PRODID
		// and VERSION.
		if _, err := EncodeCalendar(obj); err != nil {
			t.Errorf("EncodeCalendar returned error for a split object: %v", err)
		}
	}
}

// TestSplitCalendarKeepsSeriesTogether checks that a master and its
// RECURRENCE-ID override stay in one object, which is what CalDAV requires of
// a calendar object resource.
func TestSplitCalendarKeepsSeriesTogether(t *testing.T) {
	t.Parallel()

	const seriesICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//x//EN
BEGIN:VTIMEZONE
TZID:Europe/Berlin
BEGIN:STANDARD
DTSTART:19701025T030000
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:series@example.com
DTSTAMP:20260101T090000Z
DTSTART:20260302T090000Z
DTEND:20260302T091500Z
SUMMARY:Standup
RRULE:FREQ=WEEKLY;COUNT=4
END:VEVENT
BEGIN:VEVENT
UID:series@example.com
RECURRENCE-ID:20260309T090000Z
DTSTAMP:20260101T090000Z
DTSTART:20260309T100000Z
DTEND:20260309T103000Z
SUMMARY:Standup (moved)
END:VEVENT
END:VCALENDAR
`

	cal, err := DecodeCalendar(strings.NewReader(seriesICS))
	if err != nil {
		t.Fatalf("DecodeCalendar returned error: %v", err)
	}

	objects := SplitCalendar(cal)
	if len(objects) != 1 {
		t.Fatalf("SplitCalendar returned %d objects, want 1 for a single UID", len(objects))
	}
	if got := len(objects[0].Events()); got != 2 {
		t.Errorf("object holds %d events, want 2 (master plus override)", got)
	}

	// The VTIMEZONE the events reference must travel with them.
	var timezones int
	for _, child := range objects[0].Children {
		if child.Name == ical.CompTimezone {
			timezones++
		}
	}
	if timezones != 1 {
		t.Errorf("object holds %d VTIMEZONE components, want 1", timezones)
	}
}
