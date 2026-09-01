package cmd

import (
	"github.com/spf13/cobra"
)

func newGetCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get <uid>",
		Short: "Show one event in full",
		Long: "Show every property of a single event.\n\n" +
			"The UID may be abbreviated to any unambiguous prefix. An ambiguous\n" +
			"prefix lists the matching UIDs and exits with status 1; an unknown one\n" +
			"exits with status 2.",
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
			return rt.Out.Detail(ev)
		},
	}
}
