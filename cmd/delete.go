package cmd

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/prdlk/ical-cli/internal/event"
)

func newDeleteCommand(a *app) *cobra.Command {
	var (
		yes        bool
		occurrence string
	)

	cmd := &cobra.Command{
		Use:     "delete <uid>",
		Aliases: []string{"rm"},
		Short:   "Delete an event (CalDAV only)",
		Long: "Delete an event after confirming.\n\n" +
			"Deleting a recurring event removes the whole series. Pass --occurrence\n" +
			"to drop a single instance instead, which adds an EXDATE to the series\n" +
			"rather than deleting the stored object.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := a.newRuntime(cmd)
			if err != nil {
				return err
			}

			ev, err := rt.Client.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			single := cmd.Flags().Changed(flagOccurrence)

			var instant = ev.Start
			if single {
				if !ev.Recurring() {
					return fmt.Errorf("event %s is not recurring: --occurrence only applies to a recurring event",
						ev.UID())
				}
				at, err := event.ParseDate(occurrence, rt.Loc, rt.Now)
				if err != nil {
					return err
				}
				if instant, err = event.FindOccurrence(ev, at.Time, rt.Loc); err != nil {
					return err
				}
			}

			prompt := fmt.Sprintf("delete event %s (%s)?", ev.UID(), ev.Summary())
			if single {
				prompt = fmt.Sprintf("delete the %s occurrence of %s (%s)?",
					instant.In(rt.Loc).Format("2006-01-02 15:04"), ev.UID(), ev.Summary())
			}

			if !yes {
				confirmed, err := confirm(cmd, prompt)
				if err != nil {
					return err
				}
				if !confirmed {
					return rt.Out.Result("aborted", deleteResult{Deleted: false, UID: ev.UID(), Aborted: true})
				}
			}

			if single {
				// Excluding the instant keeps the series and its overrides intact.
				ev.AddExceptionDate(instant)
				ev.Touch(rt.Now)
				if err := rt.Client.Put(cmd.Context(), ev); err != nil {
					return err
				}
				return rt.Out.Result(
					fmt.Sprintf("deleted occurrence %s of %s",
						instant.In(rt.Loc).Format("2006-01-02 15:04"), ev.UID()),
					deleteResult{Deleted: true, UID: ev.UID(), Scope: "occurrence",
						RecurrenceID: instant.In(rt.Loc).Format("2006-01-02T15:04:05Z07:00")},
				)
			}

			if err := rt.Client.Delete(cmd.Context(), ev); err != nil {
				return err
			}
			return rt.Out.Result(
				fmt.Sprintf("deleted %s", ev.UID()),
				deleteResult{Deleted: true, UID: ev.UID(), Scope: "event"},
			)
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.Flags().StringVar(&occurrence, flagOccurrence, "",
		"delete only the instance starting at this time, via EXDATE")
	return cmd
}

// confirm asks the user to approve a destructive action. A non-interactive
// stdin is treated as a refusal so that a piped invocation never deletes
// silently; --yes is the documented way to proceed unattended.
func confirm(cmd *cobra.Command, prompt string) (bool, error) {
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", prompt)

	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(cmd.ErrOrStderr())
		return false, nil
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// deleteResult is the JSON shape of a delete.
type deleteResult struct {
	Deleted      bool   `json:"deleted"`
	Aborted      bool   `json:"aborted,omitempty"`
	UID          string `json:"uid"`
	Scope        string `json:"scope,omitempty"`
	RecurrenceID string `json:"recurrence_id,omitempty"`
}
