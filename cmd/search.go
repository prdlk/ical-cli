package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/prdlk/ical-cli/internal/client"
	"github.com/prdlk/ical-cli/internal/event"
)

func newSearchCommand(a *app) *cobra.Command {
	var (
		from  string
		to    string
		limit int
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Find events whose summary, description or location matches",
		Long: "Case-insensitive substring search over SUMMARY, DESCRIPTION and LOCATION.\n\n" +
			"Search covers the whole calendar unless --from or --to narrow it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := a.newRuntime(cmd)
			if err != nil {
				return err
			}

			// Search defaults to the entire calendar: restricting it to a
			// default window would silently hide older matches.
			q := client.Query{Location: rt.Loc}
			fl := cmd.Flags()
			if fl.Changed("from") {
				parsed, err := event.ParseDate(from, rt.Loc, rt.Now)
				if err != nil {
					return err
				}
				q.From = parsed.Time
			}
			if fl.Changed("to") {
				parsed, err := event.ParseDate(to, rt.Loc, rt.Now)
				if err != nil {
					return err
				}
				q.To = parsed.Time
				if parsed.AllDay {
					q.To = event.StartOfDay(parsed.Time, rt.Loc).AddDate(0, 0, 1)
				}
			}

			events, err := rt.Client.List(cmd.Context(), q)
			if err != nil {
				return err
			}

			matches := filterMatches(events, args[0])
			if limit > 0 && len(matches) > limit {
				matches = matches[:limit]
			}
			return rt.Out.Events(matches)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&from, "from", "", "only search events ending at or after this time")
	fl.StringVar(&to, "to", "", "only search events starting before this time")
	fl.IntVar(&limit, "limit", 0, "maximum number of matches to return (0 = no limit)")

	return cmd
}

// filterMatches keeps events whose summary, description or location contains
// query, compared case-insensitively.
func filterMatches(events []*event.Event, query string) []*event.Event {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return nil
	}

	out := make([]*event.Event, 0, len(events))
	for _, ev := range events {
		if matchesQuery(ev, needle) {
			out = append(out, ev)
		}
	}
	return out
}

func matchesQuery(ev *event.Event, needle string) bool {
	for _, field := range [...]string{ev.Summary(), ev.Description(), ev.Location()} {
		if field != "" && strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}
