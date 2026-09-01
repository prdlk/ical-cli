package cmd

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/prdlk/ical-cli/internal/client"
	"github.com/prdlk/ical-cli/internal/event"
)

// defaultExpandWindow bounds RRULE expansion when the user gives no --to.
//
// A rule without COUNT or UNTIL recurs indefinitely, so an unbounded window
// would expand until the per-series occurrence budget ran out. One year of
// upcoming occurrences is the useful answer.
const defaultExpandWindow = 365 * 24 * time.Hour

func newListCommand(a *app) *cobra.Command {
	var (
		from  string
		to    string
		limit int
		all   bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List events, expanding recurring series within the window",
		Long: "List events ordered by start time. Recurring events are expanded into\n" +
			"individual occurrences inside the query window.\n\n" +
			"With no --from the window starts today; with no --to it covers one year.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := a.newRuntime(cmd)
			if err != nil {
				return err
			}

			window, err := resolveWindow(cmd, rt, from, to, all)
			if err != nil {
				return err
			}
			window.Limit = limit
			// Expansion needs a bounded window: a rule with neither COUNT nor
			// UNTIL recurs forever, so --all reports the stored series masters
			// rather than an arbitrary truncation of an endless series.
			window.Expand = !all

			events, err := rt.Client.List(cmd.Context(), window)
			if err != nil {
				return err
			}
			return rt.Out.Events(events)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&from, "from", "", "window start: RFC3339, YYYY-MM-DD, or relative (default today)")
	fl.StringVar(&to, "to", "", "window end (default one year after the start)")
	fl.IntVar(&limit, "limit", 0, "maximum number of events to return (0 = no limit)")
	fl.BoolVar(&all, "all", false,
		"list the whole calendar without a window; recurring series are shown as masters, not expanded")

	return cmd
}

// resolveWindow turns --from/--to into a client query window.
func resolveWindow(cmd *cobra.Command, rt *runtime, from, to string, all bool) (client.Query, error) {
	q := client.Query{Location: rt.Loc}

	fl := cmd.Flags()
	if all && (fl.Changed("from") || fl.Changed("to")) {
		return q, errFlagConflict("--all", "--from/--to")
	}
	if all {
		return q, nil
	}

	if fl.Changed("from") {
		parsed, err := event.ParseDate(from, rt.Loc, rt.Now)
		if err != nil {
			return q, err
		}
		q.From = parsed.Time
	} else {
		q.From = event.StartOfDay(rt.Now, rt.Loc)
	}

	if fl.Changed("to") {
		parsed, err := event.ParseDate(to, rt.Loc, rt.Now)
		if err != nil {
			return q, err
		}
		// A bare date names an inclusive final day, so the exclusive window end
		// is the following midnight.
		if parsed.AllDay {
			q.To = event.StartOfDay(parsed.Time, rt.Loc).AddDate(0, 0, 1)
		} else {
			q.To = parsed.Time
		}
	} else {
		q.To = q.From.Add(defaultExpandWindow)
	}

	if !q.To.After(q.From) {
		return q, errWindow(q.From, q.To)
	}
	return q, nil
}
