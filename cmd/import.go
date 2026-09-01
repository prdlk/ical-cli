package cmd

import (
	"fmt"
	"os"

	"github.com/emersion/go-ical"
	"github.com/spf13/cobra"

	"github.com/prdlk/ical-cli/internal/client"
	"github.com/prdlk/ical-cli/internal/event"
)

func newImportCommand(a *app) *cobra.Command {
	var (
		replace bool
		dryRun  bool
	)

	cmd := &cobra.Command{
		Use:   "import <file.ics>",
		Short: "Push events from a local ICS file (CalDAV only)",
		Long: "Read an iCalendar file and create its events in the collection.\n\n" +
			"Each UID becomes one calendar object, carrying its recurrence overrides\n" +
			"and the VTIMEZONE components it needs. An event whose UID already exists\n" +
			"is skipped unless --replace is given.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := a.newRuntime(cmd)
			if err != nil {
				return err
			}

			if rt.Client.Mode() != client.ModeCalDAV {
				return fmt.Errorf("%w: import creates events and requires a CalDAV "+
					"collection; a plain .ics URL is a single read-only document",
					client.ErrReadOnly)
			}

			file, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open %s: %w", args[0], err)
			}
			defer file.Close()

			cals, err := event.DecodeCalendars(file)
			if err != nil {
				return fmt.Errorf("parse %s: %w", args[0], err)
			}

			objects := event.SplitCalendar(event.MergeCalendars(cals))
			if len(objects) == 0 {
				return fmt.Errorf("no events found in %s", args[0])
			}

			existing, err := existingByUID(cmd, rt)
			if err != nil {
				return err
			}

			result := importResult{}
			for _, obj := range objects {
				evs, err := event.Events(obj, rt.Loc)
				if err != nil {
					return err
				}
				master := pickMaster(evs)
				if master == nil {
					result.Skipped++
					continue
				}

				ensureRequiredProps(master, rt)

				uid := master.UID()
				prior, exists := existing[uid]

				switch {
				case exists && !replace:
					result.Skipped++
					result.SkippedUIDs = append(result.SkippedUIDs, uid)
					continue
				case exists:
					// Reuse the stored path and ETag so the replacement is a
					// conditional overwrite rather than a blind create.
					master.Path = prior.Path
					master.ETag = prior.ETag
				}

				if dryRun {
					if exists {
						result.Replaced++
					} else {
						result.Created++
					}
					continue
				}

				if err := rt.Client.Put(cmd.Context(), master); err != nil {
					return fmt.Errorf("import event %s: %w", uid, err)
				}
				if exists {
					result.Replaced++
				} else {
					result.Created++
				}
				result.CreatedUIDs = append(result.CreatedUIDs, uid)
			}

			verb := "imported"
			if dryRun {
				verb = "would import"
			}
			return rt.Out.Result(
				fmt.Sprintf("%s: %d created, %d replaced, %d skipped",
					verb, result.Created, result.Replaced, result.Skipped),
				result,
			)
		},
	}

	cmd.Flags().BoolVar(&replace, "replace", false, "overwrite events whose UID already exists")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change without writing")
	return cmd
}

// existingByUID indexes the collection so import can tell a create from a
// replace without a round trip per event.
func existingByUID(cmd *cobra.Command, rt *runtime) (map[string]*event.Event, error) {
	events, err := rt.Client.List(cmd.Context(), client.Query{Location: rt.Loc})
	if err != nil {
		return nil, err
	}
	index := make(map[string]*event.Event, len(events))
	for _, ev := range events {
		if ev.RecurrenceID.IsZero() {
			index[ev.UID()] = ev
		}
	}
	return index, nil
}

// pickMaster returns the series master, or the sole event when the object holds
// only an override.
func pickMaster(events []*event.Event) *event.Event {
	for _, ev := range events {
		if ev.RecurrenceID.IsZero() {
			return ev
		}
	}
	if len(events) > 0 {
		return events[0]
	}
	return nil
}

// ensureRequiredProps supplies the UID and DTSTAMP that go-ical requires at
// encode time, which hand-written or exported files sometimes omit.
func ensureRequiredProps(ev *event.Event, rt *runtime) {
	if ev.Comp.Props.Get(ical.PropUID) == nil {
		ev.Comp.Props.SetText(ical.PropUID, event.NewUID())
	}
	if ev.Comp.Props.Get(ical.PropDateTimeStamp) == nil {
		ev.Comp.Props.SetDateTime(ical.PropDateTimeStamp, rt.Now.UTC())
	}
}

// importResult is the JSON shape of an import run.
type importResult struct {
	Created     int      `json:"created"`
	Replaced    int      `json:"replaced"`
	Skipped     int      `json:"skipped"`
	CreatedUIDs []string `json:"created_uids,omitempty"`
	SkippedUIDs []string `json:"skipped_uids,omitempty"`
}
