package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/prdlk/ical-cli/internal/event"
)

// testNow is the fixed clock every relative form resolves against.
var testNow = time.Date(2026, 3, 5, 14, 30, 0, 0, time.UTC)

// applyFlags parses args into a fresh eventFlags and applies them to ev.
func applyFlags(t *testing.T, ev *event.Event, isNew bool, args ...string) error {
	t.Helper()

	flags := &eventFlags{}
	cmd := &cobra.Command{Use: "test"}
	flags.register(cmd)
	cmd.Flags().String(flagOccurrence, "", "")

	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parsing %v failed: %v", args, err)
	}

	rt := &runtime{Loc: time.UTC, Now: testNow}
	return flags.apply(ev, cmd, rt, isNew)
}

// existingEvent builds a one-hour timed event to edit.
func existingEvent(t *testing.T) *event.Event {
	t.Helper()

	const ics = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//x//EN
BEGIN:VEVENT
UID:existing@example.com
DTSTAMP:20260101T090000Z
DTSTART:20260305T140000Z
DTEND:20260305T150000Z
SUMMARY:Original
LOCATION:Room A
DESCRIPTION:Original description
X-CUSTOM:keep
END:VEVENT
END:VCALENDAR
`
	cal, err := event.DecodeCalendar(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("DecodeCalendar returned error: %v", err)
	}
	events, err := event.Events(cal, time.UTC)
	if err != nil {
		t.Fatalf("Events returned error: %v", err)
	}
	return events[0]
}

func TestAddTiming(t *testing.T) {
	t.Parallel()

	utc := func(y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, time.UTC)
	}

	tests := []struct {
		name       string
		args       []string
		wantStart  time.Time
		wantEnd    time.Time
		wantAllDay bool
		wantErr    string
	}{
		{
			name:      "summary only defaults to one hour from now",
			args:      []string{"--summary", "Chat"},
			wantStart: testNow.Truncate(time.Minute),
			wantEnd:   testNow.Truncate(time.Minute).Add(time.Hour),
		},
		{
			name:      "explicit start and end",
			args:      []string{"--summary", "x", "--start", "2026-03-10T09:00:00Z", "--end", "2026-03-10T10:30:00Z"},
			wantStart: utc(2026, 3, 10, 9, 0),
			wantEnd:   utc(2026, 3, 10, 10, 30),
		},
		{
			name:      "duration instead of end",
			args:      []string{"--summary", "x", "--start", "2026-03-10T09:00:00Z", "--duration", "1h30m"},
			wantStart: utc(2026, 3, 10, 9, 0),
			wantEnd:   utc(2026, 3, 10, 10, 30),
		},
		{
			name:      "day duration",
			args:      []string{"--summary", "x", "--start", "2026-03-10T09:00:00Z", "--duration", "2d"},
			wantStart: utc(2026, 3, 10, 9, 0),
			wantEnd:   utc(2026, 3, 12, 9, 0),
		},
		{
			// A bare date carries no time of day, so it implies an all-day
			// event without the user having to say so.
			name:       "bare date start implies all-day",
			args:       []string{"--summary", "x", "--start", "2026-04-10"},
			wantStart:  utc(2026, 4, 10, 0, 0),
			wantEnd:    utc(2026, 4, 11, 0, 0),
			wantAllDay: true,
		},
		{
			// DTEND is exclusive, so a three-day span ends on the fourth day.
			name:       "all-day range uses an exclusive end",
			args:       []string{"--summary", "x", "--all-day", "--start", "2026-04-10", "--end", "2026-04-12"},
			wantStart:  utc(2026, 4, 10, 0, 0),
			wantEnd:    utc(2026, 4, 13, 0, 0),
			wantAllDay: true,
		},
		{
			name:       "all-day with no end covers one day",
			args:       []string{"--summary", "x", "--all-day", "--start", "2026-04-10"},
			wantStart:  utc(2026, 4, 10, 0, 0),
			wantEnd:    utc(2026, 4, 11, 0, 0),
			wantAllDay: true,
		},
		{
			name:       "all-day truncates a supplied time of day",
			args:       []string{"--summary", "x", "--all-day", "--start", "2026-04-10T13:45:00Z"},
			wantStart:  utc(2026, 4, 10, 0, 0),
			wantEnd:    utc(2026, 4, 11, 0, 0),
			wantAllDay: true,
		},
		{
			// "tomorrow" is a bare date, but a 45-minute duration contradicts
			// an all-day reading, so the event stays timed.
			name:       "relative start with a sub-day duration stays timed",
			args:       []string{"--summary", "x", "--start", "tomorrow", "--duration", "45m"},
			wantStart:  utc(2026, 3, 6, 0, 0),
			wantEnd:    utc(2026, 3, 6, 0, 45),
			wantAllDay: false,
		},
		{
			name:       "relative start with a day duration is all-day",
			args:       []string{"--summary", "x", "--start", "tomorrow", "--duration", "2d"},
			wantStart:  utc(2026, 3, 6, 0, 0),
			wantEnd:    utc(2026, 3, 8, 0, 0),
			wantAllDay: true,
		},
		{
			// A timed end contradicts the all-day reading of a bare start date.
			name:       "bare date start with a timed end stays timed",
			args:       []string{"--summary", "x", "--start", "2026-04-10", "--end", "2026-04-10T09:30:00Z"},
			wantStart:  utc(2026, 4, 10, 0, 0),
			wantEnd:    utc(2026, 4, 10, 9, 30),
			wantAllDay: false,
		},
		{
			name:    "all-day rejects a sub-day duration",
			args:    []string{"--summary", "x", "--all-day", "--start", "2026-04-10", "--duration", "45m"},
			wantErr: "whole number of days",
		},
		{
			name:    "end and duration together",
			args:    []string{"--summary", "x", "--end", "2026-03-10T10:00:00Z", "--duration", "1h"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "end before start",
			args:    []string{"--summary", "x", "--start", "2026-03-10T10:00:00Z", "--end", "2026-03-10T09:00:00Z"},
			wantErr: "not after",
		},
		{
			name:    "end equal to start",
			args:    []string{"--summary", "x", "--start", "2026-03-10T10:00:00Z", "--end", "2026-03-10T10:00:00Z"},
			wantErr: "not after",
		},
		{
			name:    "zero duration",
			args:    []string{"--summary", "x", "--start", "2026-03-10T10:00:00Z", "--duration", "0s"},
			wantErr: "must be positive",
		},
		{
			name:    "unparseable start",
			args:    []string{"--summary", "x", "--start", "whenever"},
			wantErr: "parse date",
		},
		{
			name:    "unparseable duration",
			args:    []string{"--summary", "x", "--duration", "soon"},
			wantErr: "parse duration",
		},
		{
			name:    "invalid rrule",
			args:    []string{"--summary", "x", "--rrule", "every tuesday"},
			wantErr: "invalid rrule",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := event.New("new@ical-cli", testNow)
			err := applyFlags(t, ev, true, tc.args...)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("apply(%v) = nil error, want %q", tc.args, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("apply(%v) error = %q, want it to contain %q", tc.args, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("apply(%v) returned error: %v", tc.args, err)
			}

			if err := ev.Resolve(time.UTC); err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if !ev.Start.Equal(tc.wantStart) {
				t.Errorf("start = %s, want %s",
					ev.Start.Format(time.RFC3339), tc.wantStart.Format(time.RFC3339))
			}
			if !ev.End.Equal(tc.wantEnd) {
				t.Errorf("end = %s, want %s",
					ev.End.Format(time.RFC3339), tc.wantEnd.Format(time.RFC3339))
			}
			if ev.AllDay != tc.wantAllDay {
				t.Errorf("allDay = %v, want %v", ev.AllDay, tc.wantAllDay)
			}
		})
	}
}

// TestEditAppliesOnlyGivenFlags is the read-modify-write contract: an unset
// flag must leave its property exactly as it was.
func TestEditAppliesOnlyGivenFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		args            []string
		wantSummary     string
		wantLocation    string
		wantDescription string
		wantStart       time.Time
		wantEnd         time.Time
	}{
		{
			name:            "summary only leaves timing and other fields alone",
			args:            []string{"--summary", "Renamed"},
			wantSummary:     "Renamed",
			wantLocation:    "Room A",
			wantDescription: "Original description",
			wantStart:       time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC),
			wantEnd:         time.Date(2026, 3, 5, 15, 0, 0, 0, time.UTC),
		},
		{
			// Moving an event must not resize it.
			name:            "moving the start preserves the length",
			args:            []string{"--start", "2026-03-06T09:00:00Z"},
			wantSummary:     "Original",
			wantLocation:    "Room A",
			wantDescription: "Original description",
			wantStart:       time.Date(2026, 3, 6, 9, 0, 0, 0, time.UTC),
			wantEnd:         time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC),
		},
		{
			name:            "duration resizes without moving",
			args:            []string{"--duration", "30m"},
			wantSummary:     "Original",
			wantLocation:    "Room A",
			wantDescription: "Original description",
			wantStart:       time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC),
			wantEnd:         time.Date(2026, 3, 5, 14, 30, 0, 0, time.UTC),
		},
		{
			name:            "location only",
			args:            []string{"--location", "Room B"},
			wantSummary:     "Original",
			wantLocation:    "Room B",
			wantDescription: "Original description",
			wantStart:       time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC),
			wantEnd:         time.Date(2026, 3, 5, 15, 0, 0, 0, time.UTC),
		},
		{
			// An explicitly empty value clears the property.
			name:            "empty description clears it",
			args:            []string{"--description", ""},
			wantSummary:     "Original",
			wantLocation:    "Room A",
			wantDescription: "",
			wantStart:       time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC),
			wantEnd:         time.Date(2026, 3, 5, 15, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := existingEvent(t)
			if err := applyFlags(t, ev, false, tc.args...); err != nil {
				t.Fatalf("apply(%v) returned error: %v", tc.args, err)
			}
			if err := ev.Resolve(time.UTC); err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}

			if got := ev.Summary(); got != tc.wantSummary {
				t.Errorf("summary = %q, want %q", got, tc.wantSummary)
			}
			if got := ev.Location(); got != tc.wantLocation {
				t.Errorf("location = %q, want %q", got, tc.wantLocation)
			}
			if got := ev.Description(); got != tc.wantDescription {
				t.Errorf("description = %q, want %q", got, tc.wantDescription)
			}
			if !ev.Start.Equal(tc.wantStart) {
				t.Errorf("start = %s, want %s", ev.Start, tc.wantStart)
			}
			if !ev.End.Equal(tc.wantEnd) {
				t.Errorf("end = %s, want %s", ev.End, tc.wantEnd)
			}

			// The unmodeled property must survive every edit.
			if ev.Comp.Props.Get("X-CUSTOM") == nil {
				t.Error("X-CUSTOM property was dropped")
			}
		})
	}
}

// TestEditClearingRRule checks an empty --rrule turns a series into a single
// event.
func TestEditClearingRRule(t *testing.T) {
	t.Parallel()

	ev := existingEvent(t)
	if err := applyFlags(t, ev, false, "--rrule", "FREQ=WEEKLY;COUNT=3"); err != nil {
		t.Fatalf("apply returned error: %v", err)
	}
	if !ev.Recurring() {
		t.Fatal("event is not recurring after setting an rrule")
	}

	if err := applyFlags(t, ev, false, "--rrule", ""); err != nil {
		t.Fatalf("apply returned error: %v", err)
	}
	if ev.Recurring() {
		t.Errorf("event still recurring after clearing the rrule: %q", ev.RRule())
	}
}

func TestTouchesEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "no flags", args: nil, want: false},
		{name: "summary", args: []string{"--summary", "x"}, want: true},
		{name: "start", args: []string{"--start", "today"}, want: true},
		{name: "all-day", args: []string{"--all-day"}, want: true},
		{name: "empty description still counts", args: []string{"--description", ""}, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			flags := &eventFlags{}
			cmd := &cobra.Command{Use: "test"}
			flags.register(cmd)
			if err := cmd.Flags().Parse(tc.args); err != nil {
				t.Fatalf("parse returned error: %v", err)
			}
			if got := touchesEvent(cmd); got != tc.want {
				t.Errorf("touchesEvent(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
