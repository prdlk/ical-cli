package event

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/emersion/go-ical"
)

// maxIterations caps how many components a single ICS document may contribute,
// bounding work on hostile input.
const maxCalendars = 10000

// DecodeCalendar parses exactly one VCALENDAR from r.
//
// go-ical's decoder panics on some malformed input (an unexpected character
// after a param value, a missing END property, and an out-of-range index when
// peeking at a truncated param line). Remote calendar data is untrusted, so the
// panic is converted into an error here.
func DecodeCalendar(r io.Reader) (cal *ical.Calendar, err error) {
	defer func() {
		if v := recover(); v != nil {
			cal, err = nil, fmt.Errorf("decode calendar: malformed ics: %v", v)
		}
	}()
	cal, err = ical.NewDecoder(r).Decode()
	if err != nil {
		return nil, fmt.Errorf("decode calendar: %w", err)
	}
	return cal, nil
}

// DecodeCalendars parses every VCALENDAR in the stream. Real-world .ics feeds
// occasionally concatenate documents, and go-ical's decoder supports that by
// returning one calendar per Decode call until io.EOF.
func DecodeCalendars(r io.Reader) ([]*ical.Calendar, error) {
	dec := ical.NewDecoder(r)
	var out []*ical.Calendar

	for len(out) < maxCalendars {
		cal, err := decodeNext(dec)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A trailing partial document after at least one good calendar is
			// tolerated; anything else is a hard failure.
			if len(out) > 0 {
				break
			}
			return nil, err
		}
		out = append(out, cal)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("decode calendars: no VCALENDAR found")
	}
	return out, nil
}

// decodeNext isolates the panic-to-error conversion for a single Decode call so
// that recover does not swallow the io.EOF sentinel.
func decodeNext(dec *ical.Decoder) (cal *ical.Calendar, err error) {
	defer func() {
		if v := recover(); v != nil {
			cal, err = nil, fmt.Errorf("decode calendar: malformed ics: %v", v)
		}
	}()
	cal, err = dec.Decode()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("decode calendar: %w", err)
	}
	return cal, nil
}

// MergeCalendars flattens several calendars into one, preserving VTIMEZONE
// components and de-duplicating them by TZID.
func MergeCalendars(cals []*ical.Calendar) *ical.Calendar {
	out := ical.NewCalendar()
	out.Props.SetText(ical.PropVersion, "2.0")
	out.Props.SetText(ical.PropProductID, ProductID)

	seenTZ := map[string]struct{}{}
	for _, cal := range cals {
		for _, child := range cal.Children {
			if child.Name == ical.CompTimezone {
				tzid, _ := child.Props.Text(ical.PropTimezoneID)
				if _, dup := seenTZ[tzid]; dup {
					continue
				}
				seenTZ[tzid] = struct{}{}
			}
			out.Children = append(out.Children, child)
		}
	}
	return out
}

// EncodeCalendar serializes cal to ICS bytes.
func EncodeCalendar(cal *ical.Calendar) ([]byte, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, fmt.Errorf("encode calendar: %w", err)
	}
	return buf.Bytes(), nil
}
