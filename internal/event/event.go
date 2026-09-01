package event

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// ProductID identifies this tool in generated calendars.
const ProductID = "-//prdlk//ical-cli//EN"

// Event wraps a VEVENT component together with the calendar object that carries
// it. Holding the original *ical.Calendar and *ical.Component (rather than a
// flattened struct) is what lets edit preserve properties this tool does not
// model: ATTENDEE, VALARM children, custom X- properties, and VTIMEZONE.
type Event struct {
	// Cal is the owning VCALENDAR. It may hold VTIMEZONE components and
	// recurrence overrides alongside Comp.
	Cal *ical.Calendar
	// Comp is the VEVENT itself. It is a pointer into Cal.Children.
	Comp *ical.Component

	// Path is the CalDAV resource path, empty in ICS mode.
	Path string
	// ETag is the CalDAV entity tag used for If-Match on write.
	ETag string

	// Start and End are the resolved bounds of this event or occurrence.
	Start time.Time
	End   time.Time
	// AllDay reports a DATE-valued DTSTART.
	AllDay bool
	// RecurrenceID is non-zero for an expanded occurrence or an override
	// instance, identifying which instance of the series this is.
	RecurrenceID time.Time
	// Occurrence marks an instance synthesised by RRULE expansion rather than
	// one stored on the server.
	Occurrence bool
}

// New builds an Event with the minimum properties a valid VEVENT requires.
func New(uid string, now time.Time) *Event {
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, uid)
	setUTCDateTime(ev.Props, ical.PropDateTimeStamp, now)
	setUTCDateTime(ev.Props, ical.PropCreated, now)

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProductID)
	cal.Children = append(cal.Children, ev.Component)

	return &Event{Cal: cal, Comp: ev.Component}
}

// Resolve recomputes Start, End and AllDay from the component's properties.
// loc is used for floating (timezone-less) values.
func (e *Event) Resolve(loc *time.Location) error {
	if loc == nil {
		loc = time.Local
	}
	ev := &ical.Event{Component: e.Comp}

	start, err := ev.DateTimeStart(loc)
	if err != nil {
		return fmt.Errorf("resolve DTSTART for %s: %w", e.UID(), err)
	}
	end, err := ev.DateTimeEnd(loc)
	if err != nil {
		return fmt.Errorf("resolve DTEND for %s: %w", e.UID(), err)
	}

	e.Start, e.End = start, end
	e.AllDay = isDateValued(e.Comp, ical.PropDateTimeStart)

	if p := e.Comp.Props.Get(ical.PropRecurrenceID); p != nil {
		rid, err := p.DateTime(loc)
		if err != nil {
			return fmt.Errorf("resolve RECURRENCE-ID for %s: %w", e.UID(), err)
		}
		e.RecurrenceID = rid
	}
	return nil
}

// Clone deep-copies the event so an edit can be staged without mutating the
// value fetched from the server. The copy is fully independent.
func (e *Event) Clone() *Event {
	cal := &ical.Calendar{Component: cloneComponent(e.Cal.Component)}
	out := &Event{
		Cal:          cal,
		Path:         e.Path,
		ETag:         e.ETag,
		Start:        e.Start,
		End:          e.End,
		AllDay:       e.AllDay,
		RecurrenceID: e.RecurrenceID,
		Occurrence:   e.Occurrence,
	}
	// Re-point Comp at the matching child in the cloned tree.
	uid, rid := e.UID(), e.RecurrenceID
	for _, child := range cal.Children {
		if child.Name != ical.CompEvent {
			continue
		}
		cu, _ := child.Props.Text(ical.PropUID)
		if cu != uid {
			continue
		}
		if p := child.Props.Get(ical.PropRecurrenceID); p != nil {
			if got, err := p.DateTime(time.UTC); err == nil && !rid.IsZero() && !got.Equal(rid) {
				continue
			}
		}
		out.Comp = child
		break
	}
	if out.Comp == nil && len(cal.Children) > 0 {
		out.Comp = cal.Children[0]
	}
	return out
}

func cloneComponent(c *ical.Component) *ical.Component {
	if c == nil {
		return nil
	}
	out := &ical.Component{Name: c.Name, Props: make(ical.Props, len(c.Props))}
	for name, props := range c.Props {
		cp := make([]ical.Prop, len(props))
		for i, p := range props {
			cp[i] = ical.Prop{Name: p.Name, Value: p.Value, Params: make(ical.Params, len(p.Params))}
			for k, vs := range p.Params {
				cp[i].Params[k] = append([]string(nil), vs...)
			}
		}
		out.Props[name] = cp
	}
	for _, child := range c.Children {
		out.Children = append(out.Children, cloneComponent(child))
	}
	return out
}

// UID returns the event UID.
func (e *Event) UID() string {
	s, _ := e.Comp.Props.Text(ical.PropUID)
	return s
}

// Summary returns the SUMMARY text.
func (e *Event) Summary() string {
	s, _ := e.Comp.Props.Text(ical.PropSummary)
	return s
}

// Description returns the DESCRIPTION text.
func (e *Event) Description() string {
	s, _ := e.Comp.Props.Text(ical.PropDescription)
	return s
}

// Location returns the LOCATION text.
func (e *Event) Location() string {
	s, _ := e.Comp.Props.Text(ical.PropLocation)
	return s
}

// Status returns the STATUS text.
func (e *Event) Status() string {
	s, _ := e.Comp.Props.Text(ical.PropStatus)
	return s
}

// RRule returns the raw RRULE value, empty when the event is not recurring.
func (e *Event) RRule() string {
	if p := e.Comp.Props.Get(ical.PropRecurrenceRule); p != nil {
		return p.Value
	}
	return ""
}

// Recurring reports whether the event carries a recurrence rule.
func (e *Event) Recurring() bool { return e.RRule() != "" }

// Sequence returns the SEQUENCE revision counter, 0 when absent.
func (e *Event) Sequence() int {
	if p := e.Comp.Props.Get(ical.PropSequence); p != nil {
		if n, err := p.Int(); err == nil {
			return n
		}
	}
	return 0
}

// Organizer returns the ORGANIZER value.
func (e *Event) Organizer() string {
	if p := e.Comp.Props.Get(ical.PropOrganizer); p != nil {
		return p.Value
	}
	return ""
}

// Attendees returns every ATTENDEE value.
func (e *Event) Attendees() []string {
	values := e.Comp.Props.Values(ical.PropAttendee)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, p := range values {
		out = append(out, p.Value)
	}
	return out
}

// Categories returns the CATEGORIES list.
func (e *Event) Categories() []string {
	if p := e.Comp.Props.Get(ical.PropCategories); p != nil {
		if l, err := p.TextList(); err == nil {
			return l
		}
	}
	return nil
}

// LastModified returns LAST-MODIFIED, falling back to DTSTAMP.
func (e *Event) LastModified(loc *time.Location) time.Time {
	for _, name := range []string{ical.PropLastModified, ical.PropDateTimeStamp} {
		if p := e.Comp.Props.Get(name); p != nil {
			if t, err := p.DateTime(loc); err == nil && !t.IsZero() {
				return t
			}
		}
	}
	return time.Time{}
}

// Duration reports the event length.
func (e *Event) Duration() time.Duration {
	if e.End.IsZero() || e.Start.IsZero() {
		return 0
	}
	return e.End.Sub(e.Start)
}

// SetSummary replaces SUMMARY.
func (e *Event) SetSummary(s string) { e.Comp.Props.SetText(ical.PropSummary, s) }

// SetDescription replaces DESCRIPTION, deleting it when s is empty.
func (e *Event) SetDescription(s string) { setOrDelete(e.Comp.Props, ical.PropDescription, s) }

// SetLocation replaces LOCATION, deleting it when s is empty.
func (e *Event) SetLocation(s string) { setOrDelete(e.Comp.Props, ical.PropLocation, s) }

// SetStatus replaces STATUS, deleting it when s is empty.
func (e *Event) SetStatus(s string) {
	setOrDelete(e.Comp.Props, ical.PropStatus, strings.ToUpper(strings.TrimSpace(s)))
}

// SetRRule replaces RRULE. An empty rule deletes the property, turning a
// recurring event into a single one.
func (e *Event) SetRRule(rule string) error {
	rule = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(rule)), "RRULE:"))
	if rule == "" {
		e.Comp.Props.Del(ical.PropRecurrenceRule)
		return nil
	}
	if err := ValidateRRule(rule); err != nil {
		return err
	}
	p := ical.NewProp(ical.PropRecurrenceRule)
	p.Value = rule
	e.Comp.Props.Set(p)
	return nil
}

// SetStart writes DTSTART. When allDay is true a DATE value is written,
// otherwise a DATE-TIME. Any DURATION is left intact so that a duration-based
// event keeps its length.
func (e *Event) SetStart(t time.Time, allDay bool) {
	if allDay {
		e.Comp.Props.SetDate(ical.PropDateTimeStart, t)
		return
	}
	setUTCDateTime(e.Comp.Props, ical.PropDateTimeStart, t)
}

// SetEnd writes DTEND and removes DURATION, which RFC 5545 forbids alongside
// DTEND and which go-ical rejects at encode time. For an all-day event the
// caller must pass the exclusive end date.
func (e *Event) SetEnd(t time.Time, allDay bool) {
	e.Comp.Props.Del(ical.PropDuration)
	if allDay {
		e.Comp.Props.SetDate(ical.PropDateTimeEnd, t)
		return
	}
	setUTCDateTime(e.Comp.Props, ical.PropDateTimeEnd, t)
}

// Touch bumps SEQUENCE and refreshes LAST-MODIFIED and DTSTAMP, as required of
// a published event that has been revised.
func (e *Event) Touch(now time.Time) {
	seq := ical.NewProp(ical.PropSequence)
	seq.Value = strconv.Itoa(e.Sequence() + 1)
	e.Comp.Props.Set(seq)

	setUTCDateTime(e.Comp.Props, ical.PropLastModified, now)
	setUTCDateTime(e.Comp.Props, ical.PropDateTimeStamp, now)
}

// Calendar returns a VCALENDAR ready to serialize, guaranteeing the PRODID and
// VERSION properties go-ical requires at encode time.
func (e *Event) Calendar() *ical.Calendar {
	cal := e.Cal
	if cal == nil {
		cal = ical.NewCalendar()
		cal.Children = append(cal.Children, e.Comp)
		e.Cal = cal
	}
	if cal.Props.Get(ical.PropVersion) == nil {
		cal.Props.SetText(ical.PropVersion, "2.0")
	}
	if cal.Props.Get(ical.PropProductID) == nil {
		cal.Props.SetText(ical.PropProductID, ProductID)
	}
	return cal
}

// Encode serializes the owning calendar as an ICS document.
func (e *Event) Encode(w io.Writer) error {
	if err := ical.NewEncoder(w).Encode(e.Calendar()); err != nil {
		return fmt.Errorf("encode event %s: %w", e.UID(), err)
	}
	return nil
}

// setUTCDateTime writes a DATE-TIME in UTC.
//
// go-ical's SetDateTime emits TZID=<Location.String()> for any non-UTC
// location, which for time.Local yields the literal and invalid "TZID=Local".
// Normalising to UTC sidesteps that and avoids having to emit a VTIMEZONE
// component, which the encoder never generates.
func setUTCDateTime(props ical.Props, name string, t time.Time) {
	props.SetDateTime(name, t.UTC())
}

func setOrDelete(props ical.Props, name, value string) {
	if value == "" {
		props.Del(name)
		return
	}
	props.SetText(name, value)
}

func isDateValued(comp *ical.Component, name string) bool {
	p := comp.Props.Get(name)
	return p != nil && p.ValueType() == ical.ValueDate
}

// Events extracts every VEVENT from cal as an Event, resolving bounds in loc.
// Components that cannot be resolved are reported as errors rather than
// silently dropped.
func Events(cal *ical.Calendar, loc *time.Location) ([]*Event, error) {
	var out []*Event
	for _, child := range cal.Children {
		if child.Name != ical.CompEvent {
			continue
		}
		ev := &Event{Cal: cal, Comp: child}
		if err := ev.Resolve(loc); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// SplitCalendar returns one single-event calendar per UID, carrying over
// calendar-level properties and every VTIMEZONE component. Recurrence
// overrides that share a UID stay together in one calendar, which is what
// CalDAV requires of a calendar object resource.
func SplitCalendar(cal *ical.Calendar) []*ical.Calendar {
	var timezones []*ical.Component
	order := []string{}
	byUID := map[string][]*ical.Component{}

	for _, child := range cal.Children {
		switch child.Name {
		case ical.CompTimezone:
			timezones = append(timezones, child)
		case ical.CompEvent:
			uid, _ := child.Props.Text(ical.PropUID)
			if _, seen := byUID[uid]; !seen {
				order = append(order, uid)
			}
			byUID[uid] = append(byUID[uid], child)
		}
	}

	out := make([]*ical.Calendar, 0, len(order))
	for _, uid := range order {
		c := ical.NewCalendar()
		for name, props := range cal.Props {
			c.Props[name] = props
		}
		if c.Props.Get(ical.PropVersion) == nil {
			c.Props.SetText(ical.PropVersion, "2.0")
		}
		if c.Props.Get(ical.PropProductID) == nil {
			c.Props.SetText(ical.PropProductID, ProductID)
		}
		c.Children = append(c.Children, timezones...)
		c.Children = append(c.Children, byUID[uid]...)
		out = append(out, c)
	}
	return out
}
