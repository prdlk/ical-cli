// Package cmd implements the ical-cli command tree.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/prdlk/ical-cli/internal/client"
	"github.com/prdlk/ical-cli/internal/event"
	"github.com/prdlk/ical-cli/internal/output"
)

// Exit codes are part of the tool's contract with scripts.
const (
	ExitOK       = 0
	ExitError    = 1
	ExitNotFound = 2
	ExitConflict = 3
)

// Configuration keys, shared by flags, environment variables and the config
// file. The environment form is the key uppercased with the ICAL_CLI_ prefix,
// e.g. ICAL_CLI_URL.
const (
	keyURL    = "url"
	keyUser   = "user"
	keyPass   = "pass"
	keyCalDAV = "caldav"
	keyJSON   = "json"
	keyTZ     = "tz"
)

// envPrefix namespaces environment variables.
const envPrefix = "ICAL_CLI"

// app owns the state one command tree needs.
//
// The viper instance and the --config value live here rather than in package
// variables so that building a command tree mutates nothing global. Sharing
// them would make NewRootCommand non-reentrant, which the race detector
// catches as soon as two callers build a tree concurrently.
type app struct {
	v          *viper.Viper
	configFile string
}

// runtime carries the per-invocation dependencies commands need.
type runtime struct {
	Client client.CalendarClient
	Out    *output.Writer
	Loc    *time.Location
	Now    time.Time
}

// NewRootCommand builds the command tree.
func NewRootCommand() *cobra.Command {
	a := &app{v: viper.New()}

	root := &cobra.Command{
		Use:   "ical-cli",
		Short: "Read, write and edit calendar events at a remote iCalendar URL",
		Long: strings.TrimSpace(`
ical-cli reads, writes and edits calendar events at a remote calendar URL.

A plain .ics URL is a single read-only document as far as HTTP is concerned, so
list, get, search and export work against one but add, edit, delete and import
require a CalDAV collection. Pass --caldav to force CalDAV, or let the tool
auto-detect it with OPTIONS and PROPFIND.

Configuration is read from flags, then ICAL_CLI_* environment variables, then
~/.config/ical-cli/config.yaml.`),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.bindConfig(cmd)
		},
	}

	flags := root.PersistentFlags()
	flags.StringVar(&a.configFile, "config", "", "config file (default ~/.config/ical-cli/config.yaml)")
	flags.String(keyURL, "", "calendar URL (env ICAL_CLI_URL)")
	flags.String(keyUser, "", "basic auth username (env ICAL_CLI_USER)")
	flags.String(keyPass, "", "basic auth password (env ICAL_CLI_PASS)")
	flags.Bool(keyCalDAV, false, "force CalDAV mode instead of auto-detecting")
	flags.Bool(keyJSON, false, "emit machine-readable JSON instead of a table")
	flags.String(keyTZ, "", "display timezone (default: local)")

	root.AddCommand(
		newListCommand(a),
		newGetCommand(a),
		newAddCommand(a),
		newEditCommand(a),
		newDeleteCommand(a),
		newSearchCommand(a),
		newExportCommand(a),
		newImportCommand(a),
	)
	return root
}

// bindConfig wires flags, environment and the config file into viper. Binding
// happens in PersistentPreRunE rather than at construction so that the running
// command's own flag set is registered first.
func (a *app) bindConfig(cmd *cobra.Command) error {
	a.v.SetEnvPrefix(envPrefix)
	a.v.AutomaticEnv()
	a.v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	for _, key := range []string{keyURL, keyUser, keyPass, keyCalDAV, keyJSON, keyTZ} {
		flag := cmd.Flags().Lookup(key)
		if flag == nil {
			flag = cmd.Root().PersistentFlags().Lookup(key)
		}
		if flag != nil {
			if err := a.v.BindPFlag(key, flag); err != nil {
				return fmt.Errorf("bind flag %s: %w", key, err)
			}
		}
	}

	if a.configFile != "" {
		a.v.SetConfigFile(a.configFile)
	} else {
		dir, err := configDir()
		if err != nil {
			return err
		}
		a.v.SetConfigName("config")
		a.v.SetConfigType("yaml")
		a.v.AddConfigPath(dir)
	}

	if err := a.v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		// An explicitly requested config file that is missing is an error; the
		// default location being absent is not.
		if errors.As(err, &notFound) || (a.configFile == "" && os.IsNotExist(err)) {
			return nil
		}
		if a.configFile != "" {
			return fmt.Errorf("read config file %s: %w", a.configFile, err)
		}
		return fmt.Errorf("read config: %w", err)
	}
	return nil
}

// configDir returns the directory holding config.yaml.
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("locate config directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ical-cli"), nil
}

// newRuntime resolves configuration and connects to the calendar.
func (a *app) newRuntime(cmd *cobra.Command) (*runtime, error) {
	loc, err := event.LoadLocation(a.v.GetString(keyTZ))
	if err != nil {
		return nil, err
	}

	out := output.New(cmd.OutOrStdout(), cmd.ErrOrStderr(), loc, a.v.GetBool(keyJSON))

	cl, err := client.New(cmd.Context(), client.Config{
		URL:         a.v.GetString(keyURL),
		User:        a.v.GetString(keyUser),
		Pass:        a.v.GetString(keyPass),
		ForceCalDAV: a.v.GetBool(keyCalDAV),
		Timeout:     client.DefaultTimeout,
		Location:    loc,
	})
	if err != nil {
		return nil, err
	}

	return &runtime{Client: cl, Out: out, Loc: loc, Now: time.Now().In(loc)}, nil
}

// Execute runs the command tree and returns the process exit code.
func Execute() int {
	root := NewRootCommand()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)

		// Surface the candidate UIDs so an ambiguous prefix is actionable.
		var ambiguous *event.AmbiguousUIDError
		if errors.As(err, &ambiguous) {
			for _, uid := range ambiguous.Matches {
				fmt.Fprintln(os.Stderr, "  ", uid)
			}
		}
		return exitCode(err)
	}
	return ExitOK
}

// exitCode maps an error onto the documented exit codes.
func exitCode(err error) int {
	switch {
	case errors.Is(err, client.ErrConflict):
		return ExitConflict
	case errors.Is(err, event.ErrNotFound):
		return ExitNotFound
	default:
		return ExitError
	}
}
