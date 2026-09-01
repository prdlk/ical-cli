package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/prdlk/ical-cli/internal/event"
)

func newAddCommand(a *app) *cobra.Command {
	flags := &eventFlags{}

	cmd := &cobra.Command{
		Use:   "add --summary SUMMARY",
		Short: "Create an event (CalDAV only)",
		Long: "Create an event in the calendar collection.\n\n" +
			"The UID is generated as <uuid>@ical-cli. With no --start the event\n" +
			"begins now; with neither --end nor --duration a timed event lasts one\n" +
			"hour and an --all-day event covers one day.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed(flagSummary) {
				return fmt.Errorf("--summary is required")
			}

			rt, err := a.newRuntime(cmd)
			if err != nil {
				return err
			}

			ev := event.New(event.NewUID(), rt.Now)
			if err := flags.apply(ev, cmd, rt, true); err != nil {
				return err
			}

			if err := rt.Client.Put(cmd.Context(), ev); err != nil {
				return err
			}

			return rt.Out.Result(
				fmt.Sprintf("created %s  %s", ev.UID(), ev.Summary()),
				addResult{Created: true, UID: ev.UID(), Path: ev.Path, Summary: ev.Summary()},
			)
		},
	}

	flags.register(cmd)
	return cmd
}

// addResult is the JSON shape of a successful add.
type addResult struct {
	Created bool   `json:"created"`
	UID     string `json:"uid"`
	Summary string `json:"summary"`
	Path    string `json:"path,omitempty"`
}
