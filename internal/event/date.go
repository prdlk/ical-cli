package event

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DateLayout is the layout used for DATE-only (all-day) values.
const DateLayout = "2006-01-02"

// dateTimeLayouts are tried in order for absolute timestamp forms.
var dateTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
}

// relativeOffset matches signed relative durations such as "+3d", "-2w", "1mo".
var relativeOffset = regexp.MustCompile(`^([+-]?)(\d+)(mo|[dwmyh])$`)

// bareDays matches a day-suffixed duration such as "2d", which
// time.ParseDuration rejects.
var bareDays = regexp.MustCompile(`^(\d+)d$`)

// ParsedDate is the result of parsing a user-supplied date expression. AllDay
// reports whether the input carried no time-of-day component, which lets
// callers choose between DATE and DATE-TIME iCalendar value types.
type ParsedDate struct {
	Time   time.Time
	AllDay bool
}

// ParseDate resolves a user-supplied date expression in loc. It accepts
// RFC3339, "YYYY-MM-DD", a handful of date-time layouts, the keywords
// "now", "today", "tomorrow", "yesterday", and relative offsets such as
// "+3d", "-1w", "2mo", "+6h".
//
// Relative and keyword forms are resolved against now so that callers can
// supply a deterministic clock in tests.
func ParseDate(s string, loc *time.Location, now time.Time) (ParsedDate, error) {
	if loc == nil {
		loc = time.Local
	}
	raw := strings.TrimSpace(s)
	if raw == "" {
		return ParsedDate{}, fmt.Errorf("parse date: empty value")
	}
	now = now.In(loc)
	lower := strings.ToLower(raw)

	switch lower {
	case "now":
		return ParsedDate{Time: now, AllDay: false}, nil
	case "today":
		return ParsedDate{Time: startOfDay(now, loc), AllDay: true}, nil
	case "tomorrow":
		return ParsedDate{Time: startOfDay(now.AddDate(0, 0, 1), loc), AllDay: true}, nil
	case "yesterday":
		return ParsedDate{Time: startOfDay(now.AddDate(0, 0, -1), loc), AllDay: true}, nil
	}

	if m := relativeOffset.FindStringSubmatch(lower); m != nil {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return ParsedDate{}, fmt.Errorf("parse date %q: bad quantity: %w", s, err)
		}
		if m[1] == "-" {
			n = -n
		}
		switch m[3] {
		case "h":
			return ParsedDate{Time: now.Add(time.Duration(n) * time.Hour)}, nil
		case "d":
			return ParsedDate{Time: startOfDay(now.AddDate(0, 0, n), loc), AllDay: true}, nil
		case "w":
			return ParsedDate{Time: startOfDay(now.AddDate(0, 0, 7*n), loc), AllDay: true}, nil
		case "mo", "m":
			return ParsedDate{Time: startOfDay(now.AddDate(0, n, 0), loc), AllDay: true}, nil
		case "y":
			return ParsedDate{Time: startOfDay(now.AddDate(n, 0, 0), loc), AllDay: true}, nil
		}
	}

	// DATE-only form: no time component, so treat as all-day.
	if t, err := time.ParseInLocation(DateLayout, raw, loc); err == nil {
		return ParsedDate{Time: t, AllDay: true}, nil
	}

	// iCalendar basic formats, e.g. 20240115T093000Z / 20240115.
	if t, ok := parseICalBasic(raw, loc); ok {
		return t, nil
	}

	for _, layout := range dateTimeLayouts {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return ParsedDate{Time: t, AllDay: false}, nil
		}
	}

	return ParsedDate{}, fmt.Errorf("parse date %q: want RFC3339, YYYY-MM-DD, or a relative form like today/tomorrow/+3d", s)
}

// parseICalBasic handles the compact iCalendar forms 20060102T150405Z,
// 20060102T150405 and 20060102.
func parseICalBasic(raw string, loc *time.Location) (ParsedDate, bool) {
	switch {
	case len(raw) == 16 && strings.HasSuffix(raw, "Z"):
		if t, err := time.ParseInLocation("20060102T150405Z", raw, time.UTC); err == nil {
			return ParsedDate{Time: t.In(loc)}, true
		}
	case len(raw) == 15:
		if t, err := time.ParseInLocation("20060102T150405", raw, loc); err == nil {
			return ParsedDate{Time: t}, true
		}
	case len(raw) == 8:
		if t, err := time.ParseInLocation("20060102", raw, loc); err == nil {
			return ParsedDate{Time: t, AllDay: true}, true
		}
	}
	return ParsedDate{}, false
}

// ParseDateTime resolves s and returns only the instant, discarding the
// all-day hint. It is a convenience for flags that always carry a timestamp.
func ParseDateTime(s string, loc *time.Location, now time.Time) (time.Time, error) {
	p, err := ParseDate(s, loc, now)
	if err != nil {
		return time.Time{}, err
	}
	return p.Time, nil
}

func startOfDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// ParseDuration wraps time.ParseDuration with a wrapped error, and additionally
// accepts a bare day suffix such as "2d" that the stdlib rejects.
func ParseDuration(s string) (time.Duration, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, fmt.Errorf("parse duration: empty value")
	}
	if m := bareDays.FindStringSubmatch(raw); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("parse duration %q: %w", s, err)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", s, err)
	}
	return d, nil
}

// StartOfDay returns midnight local to loc on the day containing t.
func StartOfDay(t time.Time, loc *time.Location) time.Time {
	return startOfDay(t, loc)
}

// LoadLocation resolves a timezone name, treating an empty name and "local" as
// the system zone.
func LoadLocation(name string) (*time.Location, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "local":
		return time.Local, nil
	case "utc":
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", name, err)
	}
	return loc, nil
}
