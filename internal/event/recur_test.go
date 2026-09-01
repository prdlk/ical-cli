package event

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// seriesFixture builds a calendar with one recurring master plus the extra
// property lines supplied by the caller.
func seriesFixture(t *testing.T, extra ...string) []*Event {
	t.Helper()

	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//test//x//EN\n")
	b.WriteString("BEGIN:VEVENT\nUID:series@example.com\nDTSTAMP:20260101T090000Z\n")
	b.WriteString("DTSTART:20260302T090000Z\nDTEND:20260302T091500Z\nSUMMARY:Standup\n")
	for _, line := range extra {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("END:VEVENT\nEND:VCALENDAR\n")

	cal, err := DecodeCalendar(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("DecodeCalendar returned error: %v", err)
	}
	events, err := Events(cal, time.UTC)
	if err != nil {
		t.Fatalf("Events returned error: %v", err)
	}
	return events
}

func TestExpandRecurrence(t *testing.T) {
	t.Parallel()

	march := func(day, hour, minute int) time.Time {
		return time.Date(2026, 3, day, hour, minute, 0, 0, time.UTC)
	}

	tests := []struct {
		name       string
		extra      []string
		from, to   time.Time
		wantStarts []time.Time
	}{
		{
			name:  "weekly by day within window",
			extra: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=4"},
			from:  march(1, 0, 0),
			to:    march(31, 0, 0),
			wantStarts: []time.Time{
				march(2, 9, 0), march(9, 9, 0), march(16, 9, 0), march(23, 9, 0),
			},
		},
		{
			name:  "exdate removes one instance",
			extra: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=4", "EXDATE:20260316T090000Z"},
			from:  march(1, 0, 0),
			to:    march(31, 0, 0),
			wantStarts: []time.Time{
				march(2, 9, 0), march(9, 9, 0), march(23, 9, 0),
			},
		},
		{
			name:  "two exdates",
			extra: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=4", "EXDATE:20260309T090000Z", "EXDATE:20260316T090000Z"},
			from:  march(1, 0, 0),
			to:    march(31, 0, 0),
			wantStarts: []time.Time{
				march(2, 9, 0), march(23, 9, 0),
			},
		},
		{
			name:  "window clips the series",
			extra: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=4"},
			from:  march(9, 0, 0),
			to:    march(17, 0, 0),
			wantStarts: []time.Time{
				march(9, 9, 0), march(16, 9, 0),
			},
		},
		{
			name:  "daily count",
			extra: []string{"RRULE:FREQ=DAILY;COUNT=3"},
			from:  march(1, 0, 0),
			to:    march(31, 0, 0),
			wantStarts: []time.Time{
				march(2, 9, 0), march(3, 9, 0), march(4, 9, 0),
			},
		},
		{
			name:  "until bounds the series",
			extra: []string{"RRULE:FREQ=DAILY;UNTIL=20260304T235959Z"},
			from:  march(1, 0, 0),
			to:    march(31, 0, 0),
			wantStarts: []time.Time{
				march(2, 9, 0), march(3, 9, 0), march(4, 9, 0),
			},
		},
		{
			name:  "rdate adds an instance",
			extra: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=2", "RDATE:20260305T090000Z"},
			from:  march(1, 0, 0),
			to:    march(31, 0, 0),
			wantStarts: []time.Time{
				march(2, 9, 0), march(5, 9, 0), march(9, 9, 0),
			},
		},
		{
			name:       "non-recurring event outside the window is dropped",
			extra:      nil,
			from:       march(10, 0, 0),
			to:         march(20, 0, 0),
			wantStarts: nil,
		},
		{
			name:       "non-recurring event inside the window is kept",
			extra:      nil,
			from:       march(1, 0, 0),
			to:         march(5, 0, 0),
			wantStarts: []time.Time{march(2, 9, 0)},
		},
		{
			name:       "window before the series yields nothing",
			extra:      []string{"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=4"},
			from:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			to:         time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
			wantStarts: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			events := seriesFixture(t, tc.extra...)
			got, err := Expand(events, tc.from, tc.to, time.UTC)
			if err != nil {
				t.Fatalf("Expand returned error: %v", err)
			}

			if len(got) != len(tc.wantStarts) {
				t.Fatalf("Expand returned %d occurrences, want %d\n%s",
					len(got), len(tc.wantStarts), formatStarts(got))
			}
			for i, want := range tc.wantStarts {
				if !got[i].Start.Equal(want) {
					t.Errorf("occurrence %d start = %s, want %s",
						i, got[i].Start.Format(time.RFC3339), want.Format(time.RFC3339))
				}
			}
		})
	}
}

func formatStarts(events []*Event) string {
	var b strings.Builder
	for _, ev := range events {
		fmt.Fprintf(&b, "  %s %s\n", ev.Start.Format(time.RFC3339), ev.Summary())
	}
	return b.String()
}

// TestExpandPreservesOccurrenceDuration checks each generated instance keeps
// the master's length.
func TestExpandPreservesOccurrenceDuration(t *testing.T) {
	t.Parallel()

	events := seriesFixture(t, "RRULE:FREQ=DAILY;COUNT=3")
	got, err := Expand(events,
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		time.UTC)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}

	for _, ev := range got {
		if got := ev.Duration(); got != 15*time.Minute {
			t.Errorf("occurrence at %s has duration %s, want 15m", ev.Start, got)
		}
		if !ev.Occurrence {
			t.Errorf("occurrence at %s is not flagged as expanded", ev.Start)
		}
		if ev.RecurrenceID.IsZero() {
			t.Errorf("occurrence at %s has no recurrence id", ev.Start)
		}
	}
}

// TestExpandUnboundedRuleIsBounded is the guard against rrule-go's ~292-year
// default horizon: an endless rule must still terminate at the window edge.
func TestExpandUnboundedRuleIsBounded(t *testing.T) {
	t.Parallel()

	events := seriesFixture(t, "RRULE:FREQ=DAILY")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)

	done := make(chan []*Event, 1)
	go func() {
		got, err := Expand(events, from, to, time.UTC)
		if err != nil {
			t.Errorf("Expand returned error: %v", err)
		}
		done <- got
	}()

	select {
	case got := <-done:
		// March 2 through 10 inclusive.
		if len(got) != 9 {
			t.Errorf("Expand returned %d occurrences, want 9\n%s", len(got), formatStarts(got))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Expand did not terminate on an unbounded rule within 10s")
	}
}

// TestExpandOverrideReplacesInstance checks a RECURRENCE-ID override wins over
// the generated occurrence, and that a CANCELLED override removes it.
func TestExpandOverrideReplacesInstance(t *testing.T) {
	t.Parallel()

	build := func(overrideProps string) []*Event {
		t.Helper()
		ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//x//EN
BEGIN:VEVENT
UID:series@example.com
DTSTAMP:20260101T090000Z
DTSTART:20260302T090000Z
DTEND:20260302T091500Z
SUMMARY:Standup
RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:series@example.com
RECURRENCE-ID:20260309T090000Z
DTSTAMP:20260101T090000Z
DTSTART:20260309T100000Z
DTEND:20260309T103000Z
SUMMARY:Standup (moved)
` + overrideProps + `END:VEVENT
END:VCALENDAR
`
		cal, err := DecodeCalendar(strings.NewReader(ics))
		if err != nil {
			t.Fatalf("DecodeCalendar returned error: %v", err)
		}
		events, err := Events(cal, time.UTC)
		if err != nil {
			t.Fatalf("Events returned error: %v", err)
		}
		return events
	}

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	t.Run("override replaces the generated instance", func(t *testing.T) {
		got, err := Expand(build(""), from, to, time.UTC)
		if err != nil {
			t.Fatalf("Expand returned error: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("Expand returned %d occurrences, want 3\n%s", len(got), formatStarts(got))
		}
		moved := got[1]
		if want := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC); !moved.Start.Equal(want) {
			t.Errorf("overridden instance start = %s, want %s", moved.Start, want)
		}
		if moved.Summary() != "Standup (moved)" {
			t.Errorf("overridden instance summary = %q, want %q", moved.Summary(), "Standup (moved)")
		}
		if moved.Occurrence {
			t.Error("overridden instance is flagged as generated; it is a stored override")
		}
	})

	t.Run("cancelled override removes the instance", func(t *testing.T) {
		got, err := Expand(build("STATUS:CANCELLED\n"), from, to, time.UTC)
		if err != nil {
			t.Fatalf("Expand returned error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("Expand returned %d occurrences, want 2\n%s", len(got), formatStarts(got))
		}
		for _, ev := range got {
			if ev.Start.Day() == 9 {
				t.Error("cancelled occurrence on March 9 was not removed")
			}
		}
	})
}

func TestFindOccurrence(t *testing.T) {
	t.Parallel()

	events := seriesFixture(t, "RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=4")
	master := events[0]

	tests := []struct {
		name    string
		at      time.Time
		want    time.Time
		wantErr bool
	}{
		{
			name: "exact instant",
			at:   time.Date(2026, 3, 9, 9, 0, 0, 0, time.UTC),
			want: time.Date(2026, 3, 9, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "first instance",
			at:   time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
			want: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		},
		{
			name:    "instant that is not an occurrence",
			at:      time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC),
			wantErr: true,
		},
		{
			name:    "after the series ends",
			at:      time.Date(2027, 3, 9, 9, 0, 0, 0, time.UTC),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := FindOccurrence(master, tc.at, time.UTC)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("FindOccurrence(%s) = %s, want error", tc.at, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindOccurrence(%s) returned unexpected error: %v", tc.at, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("FindOccurrence(%s) = %s, want %s", tc.at, got, tc.want)
			}
		})
	}
}

// TestAddExceptionDateRemovesOccurrence verifies the delete --occurrence path:
// adding an EXDATE must drop exactly that instance on the next expansion.
func TestAddExceptionDateRemovesOccurrence(t *testing.T) {
	t.Parallel()

	events := seriesFixture(t, "RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=4")
	master := events[0]

	target := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	master.AddExceptionDate(target)

	got, err := Expand([]*Event{master},
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		time.UTC)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("Expand returned %d occurrences, want 3\n%s", len(got), formatStarts(got))
	}
	for _, ev := range got {
		if ev.Start.Equal(target) {
			t.Errorf("excluded occurrence %s is still present", target)
		}
	}

	// The exclusion must survive a serialize/parse round trip.
	encoded, err := EncodeCalendar(master.Calendar())
	if err != nil {
		t.Fatalf("EncodeCalendar returned error: %v", err)
	}
	if !strings.Contains(string(encoded), "EXDATE:20260316T090000Z") {
		t.Errorf("encoded master is missing the EXDATE\n%s", encoded)
	}
}

// TestNewOverrideInheritsProperties checks edit --occurrence keeps the master's
// unmodeled properties while dropping the recurrence rule.
func TestNewOverrideInheritsProperties(t *testing.T) {
	t.Parallel()

	events := seriesFixture(t,
		"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=4",
		"X-CUSTOM-TAG:keep-me",
		"ATTENDEE;CN=Bob:mailto:bob@example.com",
	)
	master := events[0]

	rid := time.Date(2026, 3, 9, 9, 0, 0, 0, time.UTC)
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	override := master.NewOverride(rid, master.Duration(), now)

	if override.RRule() != "" {
		t.Errorf("override carries RRULE %q; an override describes one instance", override.RRule())
	}
	if !override.RecurrenceID.Equal(rid) {
		t.Errorf("override recurrence id = %s, want %s", override.RecurrenceID, rid)
	}
	if got := override.Duration(); got != 15*time.Minute {
		t.Errorf("override duration = %s, want 15m", got)
	}

	encoded, err := EncodeCalendar(master.Calendar())
	if err != nil {
		t.Fatalf("EncodeCalendar returned error: %v", err)
	}
	out := string(encoded)
	for _, want := range []string{
		"RECURRENCE-ID:20260309T090000Z",
		"X-CUSTOM-TAG:keep-me",
		"mailto:bob@example.com",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("encoded object is missing %q\n%s", want, out)
		}
	}

	// FindOverride must locate what NewOverride attached.
	if found := master.FindOverride(rid, time.UTC); found == nil {
		t.Error("FindOverride did not locate the override just created")
	}
}
