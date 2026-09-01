// Package output renders events as an aligned table or as machine-readable
// JSON with stable field names.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/emersion/go-ical"
	"github.com/prdlk/ical-cli/internal/event"
)

// Layouts used by the table renderer and by JSON date fields.
const (
	dateTimeLayout = "2006-01-02 15:04"
	dateLayout     = "2006-01-02"
	// uidWidth keeps the UID column narrow enough for an 80-column terminal
	// while staying long enough to stay unambiguous in practice.
	uidWidth = 12
)

// Writer renders command results in the selected format.
type Writer struct {
	out  io.Writer
	err  io.Writer
	loc  *time.Location
	json bool
}

// New builds a Writer. When asJSON is set every result is emitted as JSON on
// out; otherwise results are rendered as aligned text.
func New(out, errOut io.Writer, loc *time.Location, asJSON bool) *Writer {
	if loc == nil {
		loc = time.Local
	}
	return &Writer{out: out, err: errOut, loc: loc, json: asJSON}
}

// JSON reports whether machine-readable output is selected.
func (w *Writer) JSON() bool { return w.json }

// EventJSON is the stable JSON shape of an event. Field names are part of this
// tool's contract: they are safe to parse from scripts.
type EventJSON struct {
	UID          string   `json:"uid"`
	Summary      string   `json:"summary"`
	Description  string   `json:"description,omitempty"`
	Location     string   `json:"location,omitempty"`
	Start        string   `json:"start"`
	End          string   `json:"end,omitempty"`
	AllDay       bool     `json:"all_day"`
	DurationSecs int64    `json:"duration_seconds"`
	Duration     string   `json:"duration,omitempty"`
	RRule        string   `json:"rrule,omitempty"`
	Recurring    bool     `json:"recurring"`
	RecurrenceID string   `json:"recurrence_id,omitempty"`
	Occurrence   bool     `json:"occurrence"`
	Status       string   `json:"status,omitempty"`
	Sequence     int      `json:"sequence"`
	Organizer    string   `json:"organizer,omitempty"`
	Attendees    []string `json:"attendees,omitempty"`
	Categories   []string `json:"categories,omitempty"`
	LastModified string   `json:"last_modified,omitempty"`
	Path         string   `json:"path,omitempty"`
	ETag         string   `json:"etag,omitempty"`
	// Extra carries properties this tool does not model, so JSON consumers see
	// the whole event. Values are the raw iCalendar property values.
	Extra map[string][]string `json:"extra,omitempty"`
}

// modeledProps are rendered as dedicated JSON fields and so are excluded from
// Extra.
var modeledProps = map[string]struct{}{
	ical.PropUID: {}, ical.PropSummary: {}, ical.PropDescription: {},
	ical.PropLocation: {}, ical.PropDateTimeStart: {}, ical.PropDateTimeEnd: {},
	ical.PropDuration: {}, ical.PropRecurrenceRule: {}, ical.PropRecurrenceID: {},
	ical.PropStatus: {}, ical.PropSequence: {}, ical.PropOrganizer: {},
	ical.PropAttendee: {}, ical.PropCategories: {}, ical.PropLastModified: {},
}

// ToJSON projects an event into its stable JSON representation.
func ToJSON(ev *event.Event, loc *time.Location) EventJSON {
	if loc == nil {
		loc = time.Local
	}

	out := EventJSON{
		UID:          ev.UID(),
		Summary:      ev.Summary(),
		Description:  ev.Description(),
		Location:     ev.Location(),
		Start:        formatStamp(ev.Start, ev.AllDay, loc),
		End:          formatStamp(ev.End, ev.AllDay, loc),
		AllDay:       ev.AllDay,
		DurationSecs: int64(ev.Duration() / time.Second),
		RRule:        ev.RRule(),
		Recurring:    ev.Recurring(),
		Occurrence:   ev.Occurrence,
		Status:       ev.Status(),
		Sequence:     ev.Sequence(),
		Organizer:    ev.Organizer(),
		Attendees:    ev.Attendees(),
		Categories:   ev.Categories(),
		Path:         ev.Path,
		ETag:         strings.Trim(ev.ETag, `"`),
	}
	if d := ev.Duration(); d > 0 {
		out.Duration = d.String()
	}
	if !ev.RecurrenceID.IsZero() {
		out.RecurrenceID = formatStamp(ev.RecurrenceID, ev.AllDay, loc)
	}
	if lm := ev.LastModified(loc); !lm.IsZero() {
		out.LastModified = lm.In(loc).Format(time.RFC3339)
	}

	extra := map[string][]string{}
	for name, props := range ev.Comp.Props {
		if _, skip := modeledProps[name]; skip {
			continue
		}
		values := make([]string, 0, len(props))
		for _, p := range props {
			values = append(values, p.Value)
		}
		extra[name] = values
	}
	if len(extra) > 0 {
		out.Extra = extra
	}
	return out
}

// formatStamp renders an instant as a date for all-day events and as RFC3339
// otherwise. A zero time renders empty.
func formatStamp(t time.Time, allDay bool, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	if allDay {
		return t.In(loc).Format(dateLayout)
	}
	return t.In(loc).Format(time.RFC3339)
}

// Events renders a list of events.
func (w *Writer) Events(events []*event.Event) error {
	if w.json {
		payload := make([]EventJSON, 0, len(events))
		for _, ev := range events {
			payload = append(payload, ToJSON(ev, w.loc))
		}
		return w.writeJSON(payload)
	}

	if len(events) == 0 {
		_, err := fmt.Fprintln(w.out, "no events")
		return err
	}

	tw := tabwriter.NewWriter(w.out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "UID\tSTART\tEND\tSUMMARY\tLOCATION"); err != nil {
		return err
	}
	for _, ev := range events {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			event.ShortUID(ev.UID(), uidWidth),
			w.stamp(ev.Start, ev.AllDay),
			w.endStamp(ev),
			oneLine(ev.Summary()),
			oneLine(ev.Location()),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// Detail renders one event in full.
func (w *Writer) Detail(ev *event.Event) error {
	if w.json {
		return w.writeJSON(ToJSON(ev, w.loc))
	}

	tw := tabwriter.NewWriter(w.out, 0, 4, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s:\t%s\n", k, v)
		}
	}

	row("UID", ev.UID())
	row("Summary", ev.Summary())
	row("Start", w.stamp(ev.Start, ev.AllDay))
	row("End", w.endStamp(ev))
	if ev.AllDay {
		row("All day", "yes")
	}
	if d := ev.Duration(); d > 0 {
		row("Duration", d.String())
	}
	row("Location", ev.Location())
	row("Description", oneLine(ev.Description()))
	row("Status", ev.Status())
	row("RRULE", ev.RRule())
	if !ev.RecurrenceID.IsZero() {
		row("Recurrence ID", w.stamp(ev.RecurrenceID, ev.AllDay))
	}
	if ev.Occurrence {
		row("Occurrence", "expanded from RRULE")
	}
	row("Sequence", fmt.Sprintf("%d", ev.Sequence()))
	row("Organizer", ev.Organizer())
	if a := ev.Attendees(); len(a) > 0 {
		row("Attendees", strings.Join(a, ", "))
	}
	if c := ev.Categories(); len(c) > 0 {
		row("Categories", strings.Join(c, ", "))
	}
	if lm := ev.LastModified(w.loc); !lm.IsZero() {
		row("Last modified", lm.In(w.loc).Format(dateTimeLayout))
	}
	row("Path", ev.Path)

	// Show unmodeled properties so nothing is invisible to the user.
	extra := make([]string, 0, len(ev.Comp.Props))
	for name := range ev.Comp.Props {
		if _, skip := modeledProps[name]; skip {
			continue
		}
		extra = append(extra, name)
	}
	sort.Strings(extra)
	for _, name := range extra {
		for _, p := range ev.Comp.Props.Values(name) {
			row(name, oneLine(p.Value))
		}
	}
	if len(ev.Comp.Children) > 0 {
		kinds := make([]string, 0, len(ev.Comp.Children))
		for _, child := range ev.Comp.Children {
			kinds = append(kinds, child.Name)
		}
		row("Components", strings.Join(kinds, ", "))
	}

	return tw.Flush()
}

// Result reports the outcome of a mutating command. In JSON mode payload is
// serialized; otherwise the human-readable message is printed.
func (w *Writer) Result(message string, payload any) error {
	if w.json {
		return w.writeJSON(payload)
	}
	_, err := fmt.Fprintln(w.out, message)
	return err
}

// Info writes a diagnostic that is never part of machine-readable output.
func (w *Writer) Info(format string, args ...any) {
	fmt.Fprintf(w.err, format+"\n", args...)
}

// Raw writes bytes verbatim, used by export.
func (w *Writer) Raw(b []byte) error {
	_, err := w.out.Write(b)
	return err
}

func (w *Writer) writeJSON(v any) error {
	enc := json.NewEncoder(w.out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json output: %w", err)
	}
	return nil
}

func (w *Writer) stamp(t time.Time, allDay bool) string {
	if t.IsZero() {
		return ""
	}
	if allDay {
		return t.In(w.loc).Format(dateLayout)
	}
	return t.In(w.loc).Format(dateTimeLayout)
}

// endStamp renders the inclusive end a user expects. An all-day event stores an
// exclusive DTEND, so the displayed date is stepped back one day; a single
// all-day event then shows the same date at both ends.
func (w *Writer) endStamp(ev *event.Event) string {
	if ev.End.IsZero() {
		return ""
	}
	if ev.AllDay {
		return ev.End.In(w.loc).AddDate(0, 0, -1).Format(dateLayout)
	}
	return ev.End.In(w.loc).Format(dateTimeLayout)
}

// oneLine collapses embedded newlines and tabs so a value cannot break table
// alignment.
func oneLine(s string) string {
	if s == "" {
		return ""
	}
	r := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")
	return strings.TrimSpace(r.Replace(s))
}
