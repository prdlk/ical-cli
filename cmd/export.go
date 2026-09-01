package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newExportCommand(a *app) *cobra.Command {
	var outputPath string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Dump the calendar as raw ICS",
		Long: "Write the calendar as a raw iCalendar document.\n\n" +
			"In ICS mode the upstream document is emitted byte for byte. In CalDAV\n" +
			"mode every calendar object is merged into one document, de-duplicating\n" +
			"VTIMEZONE components.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := a.newRuntime(cmd)
			if err != nil {
				return err
			}

			data, err := rt.Client.Raw(cmd.Context())
			if err != nil {
				return err
			}

			if outputPath == "" || outputPath == "-" {
				return rt.Out.Raw(data)
			}

			// 0o600: calendars routinely carry private information.
			if err := os.WriteFile(outputPath, data, 0o600); err != nil {
				return fmt.Errorf("write %s: %w", outputPath, err)
			}
			rt.Out.Info("wrote %d bytes to %s", len(data), outputPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "",
		"write to this file instead of stdout")
	return cmd
}
