package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/prdlk/ical-cli/internal/event"
)

// Flag names shared by add and edit.
const (
	flagSummary     = "summary"
	flagStart       = "start"
	flagEnd         = "end"
	flagDuration    = "duration"
	flagLocation    = "location"
	flagDescription = "description"
	flagRRule       = "rrule"
	flagStatus      = "status"
	flagAllDay      = "all-day"
	flagOccurrence  = "occurrence"
)

// defaultDuration is the length given to a timed event created without an end
// or duration.
const defaultDuration = time.Hour

// eventFlags holds the event-shaping flags. add applies all of them; edit
// applies only those the user actually set, which is what makes edit a
// read-modify-write that preserves everything else.
type eventFlags struct {
	summary     string
	start       string
	end         string
	duration    string
	location    string
	description string
	rrule       string
	status      string
	allDay      bool
}

// register attaches the shared flags to cmd.
func (f *eventFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.summary, flagSummary, "", "event summary")
	fl.StringVar(&f.start, flagStart, "", "start time: RFC3339, YYYY-MM-DD, or relative (today, tomorrow, +3d)")
	fl.StringVar(&f.end, flagEnd, "", "end time; for --all-day this is the last day, inclusive")
	fl.StringVar(&f.duration, flagDuration, "", "event length, e.g. 1h30m or 2d (alternative to --end)")
	fl.StringVar(&f.location, flagLocation, "", "event location")
	fl.StringVar(&f.description, flagDescription, "", "event description")
	fl.StringVar(&f.rrule, flagRRule, "", "recurrence rule, e.g. FREQ=WEEKLY;BYDAY=MO (empty string clears it)")
	fl.StringVar(&f.status, flagStatus, "", "event status: TENTATIVE, CONFIRMED or CANCELLED")
	fl.BoolVar(&f.allDay, flagAllDay, false, "treat the event as all-day, using DATE values")
}

// touchesEvent reports whether any event-shaping flag was set.
func touchesEvent(cmd *cobra.Command) bool {
	for _, name := range []string{
		flagSummary, flagStart, flagEnd, flagDuration,
		flagLocation, flagDescription, flagRRule, flagStatus, flagAllDay,
	} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// apply writes the flags onto ev. When isNew is false only flags the user set
// are applied, leaving every other property untouched.
func (f *eventFlags) apply(ev *event.Event, cmd *cobra.Command, rt *runtime, isNew bool) error {
	fl := cmd.Flags()

	if fl.Changed(flagEnd) && fl.Changed(flagDuration) {
		return fmt.Errorf("--end and --duration are mutually exclusive: pick one")
	}

	if isNew || fl.Changed(flagSummary) {
		ev.SetSummary(f.summary)
	}
	if fl.Changed(flagLocation) {
		ev.SetLocation(f.location)
	}
	if fl.Changed(flagDescription) {
		ev.SetDescription(f.description)
	}
	if fl.Changed(flagStatus) {
		ev.SetStatus(f.status)
	}
	if fl.Changed(flagRRule) {
		if err := ev.SetRRule(f.rrule); err != nil {
			return err
		}
	}
	return f.applyTiming(ev, cmd, rt, isNew)
}

// applyTiming resolves DTSTART, DTEND and the all-day value type.
//
// On edit, timing is only recomputed when a timing flag was set. Shifting the
// start alone preserves the original length, so moving an event does not
// silently change how long it lasts.
//
// Every bound is parsed before the all-day decision is made, because an
// explicit sub-day end or duration contradicts the all-day reading of a bare
// date. Deciding first would truncate DTEND to a DATE and collapse the event to
// zero length.
func (f *eventFlags) applyTiming(ev *event.Event, cmd *cobra.Command, rt *runtime, isNew bool) error {
	fl := cmd.Flags()

	timingChanged := fl.Changed(flagStart) || fl.Changed(flagEnd) ||
		fl.Changed(flagDuration) || fl.Changed(flagAllDay)
	if !isNew && !timingChanged {
		return nil
	}

	// Parse every supplied bound up front.
	var (
		startParsed event.ParsedDate
		endParsed   event.ParsedDate
		dur         time.Duration
		err         error
	)
	if fl.Changed(flagStart) {
		if startParsed, err = event.ParseDate(f.start, rt.Loc, rt.Now); err != nil {
			return err
		}
	}
	if fl.Changed(flagEnd) {
		if endParsed, err = event.ParseDate(f.end, rt.Loc, rt.Now); err != nil {
			return err
		}
	}
	if fl.Changed(flagDuration) {
		if dur, err = event.ParseDuration(f.duration); err != nil {
			return err
		}
		if dur <= 0 {
			return fmt.Errorf("--duration must be positive, got %s", f.duration)
		}
	}

	allDay := resolveAllDay(f, fl, ev, isNew, startParsed, endParsed, dur)
	if allDay && fl.Changed(flagDuration) && dur%(24*time.Hour) != 0 {
		return fmt.Errorf("--duration %s is not a whole number of days, "+
			"which an --all-day event requires", f.duration)
	}

	oldStart, oldDuration := ev.Start, ev.Duration()
	hadExplicitEnd := ev.End.After(oldStart) && !oldStart.IsZero()

	// Start.
	start := ev.Start
	switch {
	case fl.Changed(flagStart):
		start = startParsed.Time
	case isNew:
		start = rt.Now.Truncate(time.Minute)
	}
	if start.IsZero() {
		return fmt.Errorf("event has no start time: pass --start")
	}
	if allDay {
		start = event.StartOfDay(start, rt.Loc)
	}
	ev.SetStart(start, allDay)
	ev.AllDay = allDay

	// End.
	var end time.Time
	switch {
	case fl.Changed(flagEnd):
		end = endParsed.Time
		if allDay {
			// DTEND is exclusive; the user names the last day.
			end = event.StartOfDay(end, rt.Loc).AddDate(0, 0, 1)
		}
		if !end.After(start) {
			return fmt.Errorf("end %s is not after start %s",
				end.Format(time.RFC3339), start.Format(time.RFC3339))
		}

	case fl.Changed(flagDuration):
		end = start.Add(dur)

	case isNew:
		if allDay {
			end = start.AddDate(0, 0, 1)
		} else {
			end = start.Add(defaultDuration)
		}

	default:
		// Timing changed without naming an end: keep the original length so a
		// move does not resize the event. A duration-based event needs no
		// action because its end derives from DTSTART.
		if hadExplicitEnd && oldDuration > 0 {
			end = start.Add(oldDuration)
		}
	}

	if end.IsZero() {
		return nil
	}
	// An all-day event always spans at least one whole day.
	if allDay && !end.After(start) {
		end = start.AddDate(0, 0, 1)
	}
	ev.SetEnd(end, allDay)
	ev.End = end
	return nil
}

// resolveAllDay decides whether the event uses DATE values.
//
// An explicit --all-day always wins. Otherwise a new event is all-day only when
// its start is a bare date AND no supplied end or duration contradicts that:
// "--start 2026-04-10 --duration 45m" is a timed event, not a broken all-day
// one.
func resolveAllDay(f *eventFlags, fl *pflag.FlagSet, ev *event.Event, isNew bool,
	startParsed, endParsed event.ParsedDate, dur time.Duration,
) bool {
	if fl.Changed(flagAllDay) {
		return f.allDay
	}
	if !isNew {
		return ev.AllDay
	}
	if !fl.Changed(flagStart) || !startParsed.AllDay {
		return false
	}
	if fl.Changed(flagDuration) && dur%(24*time.Hour) != 0 {
		return false
	}
	if fl.Changed(flagEnd) && !endParsed.AllDay {
		return false
	}
	return true
}
