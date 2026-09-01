package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/prdlk/ical-cli/internal/event"
)

func newEditCommand(a *app) *cobra.Command {
	flags := &eventFlags{}
	var occurrence string

	cmd := &cobra.Command{
		Use:   "edit <uid>",
		Short: "Change an existing event (CalDAV only)",
		Long: "Change an existing event, applying only the flags given.\n\n" +
			"Edit is a read-modify-write: the event is fetched, the named fields are\n" +
			"replaced, and everything else is preserved, including attendees, alarms\n" +
			"and custom X- properties. SEQUENCE is bumped and LAST-MODIFIED is set.\n" +
			"The stored ETag is sent as If-Match, so a concurrent change is reported\n" +
			"as a conflict (exit status 3) instead of overwriting silently.\n\n" +
			"On a recurring event the master is edited by default. Pass --occurrence\n" +
			"to edit a single instance, which creates a RECURRENCE-ID override.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !touchesEvent(cmd) {
				return fmt.Errorf("nothing to change: pass at least one of " +
					"--summary, --start, --end, --duration, --location, --description, --rrule, --status, --all-day")
			}

			rt, err := a.newRuntime(cmd)
			if err != nil {
				return err
			}

			master, err := rt.Client.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			target := master
			scope := "event"

			if cmd.Flags().Changed(flagOccurrence) {
				target, err = resolveOccurrenceTarget(master, occurrence, rt)
				if err != nil {
					return err
				}
				scope = "occurrence"
			}

			if err := flags.apply(target, cmd, rt, false); err != nil {
				return err
			}
			target.Touch(rt.Now)

			// Writing the master writes the whole calendar object, which is what
			// carries any override component.
			if err := rt.Client.Put(cmd.Context(), master); err != nil {
				return err
			}

			return rt.Out.Result(
				fmt.Sprintf("updated %s %s  %s", scope, target.UID(), target.Summary()),
				editResult{
					Updated:      true,
					UID:          target.UID(),
					Summary:      target.Summary(),
					Sequence:     target.Sequence(),
					Scope:        scope,
					RecurrenceID: stampOrEmpty(target, rt),
				},
			)
		},
	}

	flags.register(cmd)
	cmd.Flags().StringVar(&occurrence, flagOccurrence, "",
		"edit only the instance starting at this time, via RECURRENCE-ID")
	return cmd
}

// resolveOccurrenceTarget locates or creates the override component for a
// single instance of a recurring series.
func resolveOccurrenceTarget(master *event.Event, occurrence string, rt *runtime) (*event.Event, error) {
	if !master.Recurring() {
		return nil, fmt.Errorf("event %s is not recurring: --occurrence only applies to a recurring event",
			master.UID())
	}

	at, err := event.ParseDate(occurrence, rt.Loc, rt.Now)
	if err != nil {
		return nil, err
	}

	instant, err := event.FindOccurrence(master, at.Time, rt.Loc)
	if err != nil {
		return nil, err
	}

	if existing := master.FindOverride(instant, rt.Loc); existing != nil {
		return existing, nil
	}
	return master.NewOverride(instant, master.Duration(), rt.Now), nil
}

func stampOrEmpty(ev *event.Event, rt *runtime) string {
	if ev.RecurrenceID.IsZero() {
		return ""
	}
	return ev.RecurrenceID.In(rt.Loc).Format("2006-01-02T15:04:05Z07:00")
}

// editResult is the JSON shape of a successful edit.
type editResult struct {
	Updated      bool   `json:"updated"`
	UID          string `json:"uid"`
	Summary      string `json:"summary"`
	Sequence     int    `json:"sequence"`
	Scope        string `json:"scope"`
	RecurrenceID string `json:"recurrence_id,omitempty"`
}
