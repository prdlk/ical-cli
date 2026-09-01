package event

import (
	"testing"
	"time"
)

// reference is the fixed "now" every relative case is resolved against.
var reference = time.Date(2026, 3, 5, 14, 30, 0, 0, time.UTC)

func TestParseDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantTime   time.Time
		wantAllDay bool
		wantErr    bool
	}{
		{
			name:       "rfc3339 with zone",
			input:      "2026-03-05T14:00:00Z",
			wantTime:   time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC),
			wantAllDay: false,
		},
		{
			name:       "rfc3339 with offset",
			input:      "2026-03-05T14:00:00+02:00",
			wantTime:   time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC),
			wantAllDay: false,
		},
		{
			name:       "date only is all-day",
			input:      "2026-03-05",
			wantTime:   time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "local date-time without zone",
			input:      "2026-03-05T14:00",
			wantTime:   time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC),
			wantAllDay: false,
		},
		{
			name:       "space separated date-time",
			input:      "2026-03-05 14:00:00",
			wantTime:   time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC),
			wantAllDay: false,
		},
		{
			name:       "ical basic utc",
			input:      "20260305T140000Z",
			wantTime:   time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC),
			wantAllDay: false,
		},
		{
			name:       "ical basic date",
			input:      "20260305",
			wantTime:   time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "now keyword",
			input:      "now",
			wantTime:   reference,
			wantAllDay: false,
		},
		{
			name:       "today keyword",
			input:      "today",
			wantTime:   time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "tomorrow keyword",
			input:      "tomorrow",
			wantTime:   time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "yesterday keyword",
			input:      "yesterday",
			wantTime:   time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "case insensitive keyword",
			input:      "Tomorrow",
			wantTime:   time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "relative days forward",
			input:      "+3d",
			wantTime:   time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "relative days without sign",
			input:      "3d",
			wantTime:   time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "relative days backward",
			input:      "-2d",
			wantTime:   time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "relative weeks",
			input:      "+1w",
			wantTime:   time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "relative months",
			input:      "+2mo",
			wantTime:   time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "relative years",
			input:      "+1y",
			wantTime:   time.Date(2027, 3, 5, 0, 0, 0, 0, time.UTC),
			wantAllDay: true,
		},
		{
			name:       "relative hours keeps time of day",
			input:      "+6h",
			wantTime:   reference.Add(6 * time.Hour),
			wantAllDay: false,
		},
		{name: "empty input", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "unparseable", input: "not-a-date", wantErr: true},
		{name: "bad month", input: "2026-13-01", wantErr: true},
		{name: "bad relative unit", input: "+3q", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseDate(tc.input, time.UTC, reference)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseDate(%q) = %v, want error", tc.input, got.Time)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDate(%q) returned unexpected error: %v", tc.input, err)
			}
			if !got.Time.Equal(tc.wantTime) {
				t.Errorf("ParseDate(%q) time = %s, want %s",
					tc.input, got.Time.Format(time.RFC3339), tc.wantTime.Format(time.RFC3339))
			}
			if got.AllDay != tc.wantAllDay {
				t.Errorf("ParseDate(%q) allDay = %v, want %v", tc.input, got.AllDay, tc.wantAllDay)
			}
		})
	}
}

// TestParseDateHonoursLocation checks that a bare date resolves to midnight in
// the display timezone, not UTC. Getting this wrong shifts every all-day event.
func TestParseDateHonoursLocation(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("timezone database unavailable: %v", err)
	}

	got, err := ParseDate("2026-03-05", loc, reference)
	if err != nil {
		t.Fatalf("ParseDate returned error: %v", err)
	}

	want := time.Date(2026, 3, 5, 0, 0, 0, 0, loc)
	if !got.Time.Equal(want) {
		t.Errorf("ParseDate = %s, want %s", got.Time.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if !got.AllDay {
		t.Error("ParseDate allDay = false, want true for a bare date")
	}
}

func TestParseDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "hours and minutes", input: "1h30m", want: 90 * time.Minute},
		{name: "minutes", input: "45m", want: 45 * time.Minute},
		{name: "seconds", input: "30s", want: 30 * time.Second},
		{name: "bare days", input: "2d", want: 48 * time.Hour},
		{name: "single day", input: "1d", want: 24 * time.Hour},
		{name: "compound", input: "1h30m15s", want: time.Hour + 30*time.Minute + 15*time.Second},
		{name: "empty", input: "", wantErr: true},
		{name: "garbage", input: "soon", wantErr: true},
		{name: "bare number", input: "90", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseDuration(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseDuration(%q) = %s, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDuration(%q) returned unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseDuration(%q) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

func TestLoadLocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty means local", input: "", want: time.Local.String()},
		{name: "local keyword", input: "local", want: time.Local.String()},
		{name: "local keyword mixed case", input: "Local", want: time.Local.String()},
		{name: "utc", input: "utc", want: "UTC"},
		{name: "utc uppercase", input: "UTC", want: "UTC"},
		{name: "iana zone", input: "Europe/Berlin", want: "Europe/Berlin"},
		{name: "unknown zone", input: "Mars/Olympus", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := LoadLocation(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LoadLocation(%q) = %v, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadLocation(%q) returned unexpected error: %v", tc.input, err)
			}
			if got.String() != tc.want {
				t.Errorf("LoadLocation(%q) = %q, want %q", tc.input, got.String(), tc.want)
			}
		})
	}
}

func TestStartOfDay(t *testing.T) {
	t.Parallel()

	got := StartOfDay(time.Date(2026, 3, 5, 23, 59, 59, 999, time.UTC), time.UTC)
	want := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("StartOfDay = %s, want %s", got.Format(time.RFC3339Nano), want.Format(time.RFC3339))
	}
}
