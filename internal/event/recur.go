package event

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// maxOccurrences bounds a single series expansion.
//
// rrule-go treats a rule without COUNT or UNTIL as running for roughly 292
// years, so All() on "FREQ=SECONDLY" would try to materialise ~9e9 instants.
// Between() terminates at the window end but still iterates every occurrence
// from DTSTART forward, so a hard budget is the only safe bound.
const maxOccurrences = 100000

// ValidateRRule reports whether rule parses as an RFC 5545 recurrence rule.
func ValidateRRule(rule string) error {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return nil
	}
	if _, err := rrule.StrToROption(rule); err != nil {
		return fmt.Errorf("invalid rrule %q: %w", rule, err)
	}
	return nil
}

// Expand turns stored events into the occurrences that fall within
// [from, to). Non-recurring events pass through when they overlap the window.
// Recurring masters are expanded, with any RECURRENCE-ID override replacing the
// generated instance it targets and CANCELLED overrides removing it.
//
// A zero from or to leaves that side of the window unbounded, except that an
// unbounded window still refuses to expand a rule without COUNT or UNTIL
// beyond maxOccurrences.
func Expand(events []*Event, from, to time.Time, loc *time.Location) ([]*Event, error) {
	if loc == nil {
		loc = time.Local
	}

	masters, overrides := groupSeries(events)

	out := make([]*Event, 0, len(events))
	for _, master := range masters {
		if !master.Recurring() {
			if overlaps(master.Start, master.End, from, to) {
				out = append(out, master)
			}
			continue
		}
		instances, err := expandSeries(master, overrides[master.UID()], from, to, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, instances...)
	}

	// Overrides whose master is absent from the result set (common when a
	// server returns only the objects touching the window) still belong in the
	// output if they overlap it.
	for uid, list := range overrides {
		if _, ok := masterByUID(masters, uid); ok {
			continue
		}
		for _, ov := range list {
			if overlaps(ov.Start, ov.End, from, to) {
				out = append(out, ov)
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start.Equal(out[j].Start) {
			return out[i].Summary() < out[j].Summary()
		}
		return out[i].Start.Before(out[j].Start)
	})
	return out, nil
}

func masterByUID(masters []*Event, uid string) (*Event, bool) {
	for _, m := range masters {
		if m.UID() == uid {
			return m, true
		}
	}
	return nil, false
}

// groupSeries separates series masters from RECURRENCE-ID overrides.
func groupSeries(events []*Event) (masters []*Event, overrides map[string][]*Event) {
	masters = make([]*Event, 0, len(events))
	overrides = map[string][]*Event{}
	for _, ev := range events {
		if ev.RecurrenceID.IsZero() {
			masters = append(masters, ev)
			continue
		}
		overrides[ev.UID()] = append(overrides[ev.UID()], ev)
	}
	return masters, overrides
}

// expandSeries materialises the occurrences of one recurring master.
func expandSeries(master *Event, overrides []*Event, from, to time.Time, loc *time.Location) ([]*Event, error) {
	set, err := master.Comp.RecurrenceSet(loc)
	if err != nil {
		return nil, fmt.Errorf("expand rrule for %s: %w", master.UID(), err)
	}
	if set == nil {
		// No RRULE after all; treat as a single event.
		if overlaps(master.Start, master.End, from, to) {
			return []*Event{master}, nil
		}
		return nil, nil
	}

	byRecurrenceID := make(map[int64]*Event, len(overrides))
	for _, ov := range overrides {
		byRecurrenceID[ov.RecurrenceID.Unix()] = ov
	}

	dur := master.Duration()
	next := set.Iterator()
	out := make([]*Event, 0, 16)

	for range maxOccurrences {
		start, ok := next()
		if !ok {
			break
		}
		if !to.IsZero() && !start.Before(to) {
			break
		}

		end := start.Add(dur)
		if !overlaps(start, end, from, to) {
			continue
		}

		if ov, found := byRecurrenceID[start.Unix()]; found {
			if strings.EqualFold(ov.Status(), string(ical.EventCancelled)) {
				continue
			}
			out = append(out, ov)
			continue
		}

		out = append(out, master.instance(start, end))
	}

	return out, nil
}

// instance builds a lightweight occurrence that shares the master's component.
// Sharing is safe because expansion output is read-only: edit and delete
// re-fetch the stored object before writing.
func (e *Event) instance(start, end time.Time) *Event {
	return &Event{
		Cal:          e.Cal,
		Comp:         e.Comp,
		Path:         e.Path,
		ETag:         e.ETag,
		Start:        start,
		End:          end,
		AllDay:       e.AllDay,
		RecurrenceID: start,
		Occurrence:   true,
	}
}

// overlaps reports whether [start, end) intersects the half-open window
// [from, to). A zero bound is unbounded. A zero-length event is treated as
// touching its start instant.
func overlaps(start, end, from, to time.Time) bool {
	if end.IsZero() || !end.After(start) {
		end = start.Add(time.Nanosecond)
	}
	if !from.IsZero() && !end.After(from) {
		return false
	}
	if !to.IsZero() && !start.Before(to) {
		return false
	}
	return true
}

// FindOccurrence locates the stored event and the occurrence instant addressed
// by an --occurrence flag, so edit and delete can target a single instance.
func FindOccurrence(master *Event, at time.Time, loc *time.Location) (time.Time, error) {
	set, err := master.Comp.RecurrenceSet(loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("expand rrule for %s: %w", master.UID(), err)
	}
	if set == nil {
		return time.Time{}, fmt.Errorf("event %s is not recurring", master.UID())
	}

	next := set.Iterator()
	for range maxOccurrences {
		start, ok := next()
		if !ok {
			break
		}
		if start.Equal(at) {
			return start, nil
		}
		// Same wall-clock day match for all-day series addressed by date.
		if master.AllDay && sameDay(start, at) {
			return start, nil
		}
		if start.After(at) && !sameDay(start, at) {
			break
		}
	}
	return time.Time{}, fmt.Errorf("%w: no occurrence of %s at %s",
		ErrNotFound, master.UID(), at.Format(time.RFC3339))
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
