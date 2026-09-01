package event

import (
	"time"

	"github.com/emersion/go-ical"
)

// AddExceptionDate appends an EXDATE to a recurring master, which is how RFC
// 5545 removes a single occurrence from a series without touching the rule.
// The value type follows the event's DTSTART so the exclusion matches an
// expanded instant.
func (e *Event) AddExceptionDate(t time.Time) {
	p := ical.NewProp(ical.PropExceptionDates)
	if e.AllDay {
		p.SetDate(t)
	} else {
		p.SetDateTime(t.UTC())
	}
	e.Comp.Props.Add(p)
}

// ExceptionDates returns every excluded instant.
func (e *Event) ExceptionDates(loc *time.Location) []time.Time {
	var out []time.Time
	for _, p := range e.Comp.Props.Values(ical.PropExceptionDates) {
		prop := p
		if t, err := prop.DateTime(loc); err == nil && !t.IsZero() {
			out = append(out, t)
		}
	}
	return out
}

// FindOverride returns the override component in the owning calendar whose
// RECURRENCE-ID matches rid, or nil when the instance has no override yet.
func (e *Event) FindOverride(rid time.Time, loc *time.Location) *Event {
	uid := e.UID()
	for _, child := range e.Cal.Children {
		if child.Name != ical.CompEvent {
			continue
		}
		if cu, _ := child.Props.Text(ical.PropUID); cu != uid {
			continue
		}
		p := child.Props.Get(ical.PropRecurrenceID)
		if p == nil {
			continue
		}
		got, err := p.DateTime(loc)
		if err != nil || !got.Equal(rid) {
			continue
		}
		ov := &Event{Cal: e.Cal, Comp: child, Path: e.Path, ETag: e.ETag}
		if err := ov.Resolve(loc); err != nil {
			return nil
		}
		return ov
	}
	return nil
}

// NewOverride creates a RECURRENCE-ID override for the instance starting at
// rid and attaches it to the owning calendar object.
//
// The override inherits the master's properties, so attendees, alarms and
// custom X- properties survive, but drops the recurrence rule and its
// exceptions: an override describes exactly one instance.
func (e *Event) NewOverride(rid time.Time, dur time.Duration, now time.Time) *Event {
	comp := cloneComponent(e.Comp)
	comp.Props.Del(ical.PropRecurrenceRule)
	comp.Props.Del(ical.PropExceptionDates)
	comp.Props.Del(ical.PropRecurrenceDates)
	comp.Props.Del(ical.PropSequence)

	rp := ical.NewProp(ical.PropRecurrenceID)
	if e.AllDay {
		rp.SetDate(rid)
	} else {
		rp.SetDateTime(rid.UTC())
	}
	comp.Props.Set(rp)

	ov := &Event{Cal: e.Cal, Comp: comp, Path: e.Path, ETag: e.ETag, AllDay: e.AllDay}
	ov.SetStart(rid, e.AllDay)
	if dur > 0 {
		ov.SetEnd(rid.Add(dur), e.AllDay)
	}
	setUTCDateTime(comp.Props, ical.PropDateTimeStamp, now)
	setUTCDateTime(comp.Props, ical.PropCreated, now)

	e.Cal.Children = append(e.Cal.Children, comp)
	ov.RecurrenceID = rid
	ov.Start = rid
	if dur > 0 {
		ov.End = rid.Add(dur)
	}
	return ov
}
