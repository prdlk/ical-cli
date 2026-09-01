package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/cobra"

	"github.com/prdlk/ical-cli/internal/client"
	"github.com/prdlk/ical-cli/internal/event"
)

// TestExitCode pins the documented exit-code contract: 0 ok, 1 error,
// 2 not found, 3 conflict. Scripts depend on these.
func TestExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "generic error", err: errors.New("boom"), want: ExitError},
		{name: "not found", err: event.ErrNotFound, want: ExitNotFound},
		{
			name: "wrapped not found",
			err:  fmt.Errorf("get event: %w", event.ErrNotFound),
			want: ExitNotFound,
		},
		{name: "conflict", err: client.ErrConflict, want: ExitConflict},
		{
			name: "wrapped conflict",
			err:  fmt.Errorf("put event: %w", client.ErrConflict),
			want: ExitConflict,
		},
		{
			// Read-only is a plain failure, not a conflict or a lookup miss.
			name: "read only",
			err:  client.ErrReadOnly,
			want: ExitError,
		},
		{
			name: "ambiguous uid is a plain error",
			err:  &event.AmbiguousUIDError{Query: "a", Matches: []string{"a1", "a2"}},
			want: ExitError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := exitCode(tc.err); got != tc.want {
				t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestCommandTree checks every documented command is wired up with the global
// flags available.
func TestCommandTree(t *testing.T) {
	t.Parallel()

	root := NewRootCommand()

	wantCommands := []string{
		"list", "get", "add", "edit", "delete", "search", "export", "import",
	}
	for _, name := range wantCommands {
		t.Run(name, func(t *testing.T) {
			for _, c := range root.Commands() {
				if c.Name() == name {
					return
				}
			}
			t.Errorf("command %q is not registered", name)
		})
	}

	for _, flag := range []string{keyURL, keyUser, keyPass, keyCalDAV, keyJSON, keyTZ} {
		if root.PersistentFlags().Lookup(flag) == nil {
			t.Errorf("global flag --%s is not registered", flag)
		}
	}
}

// TestCommandArgValidation checks the argument arity each command advertises.
func TestCommandArgValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		args    []string
		wantErr bool
	}{
		{command: "get", args: nil, wantErr: true},
		{command: "get", args: []string{"uid"}, wantErr: false},
		{command: "get", args: []string{"a", "b"}, wantErr: true},
		{command: "search", args: nil, wantErr: true},
		{command: "search", args: []string{"query"}, wantErr: false},
		{command: "import", args: nil, wantErr: true},
		{command: "import", args: []string{"file.ics"}, wantErr: false},
		{command: "delete", args: nil, wantErr: true},
		{command: "delete", args: []string{"uid"}, wantErr: false},
		{command: "list", args: []string{"unexpected"}, wantErr: true},
		{command: "export", args: []string{"unexpected"}, wantErr: true},
		{command: "add", args: []string{"unexpected"}, wantErr: true},
	}

	root := NewRootCommand()

	for _, tc := range tests {
		name := tc.command + " " + fmt.Sprint(tc.args)
		t.Run(name, func(t *testing.T) {
			var target = findCommand(root, tc.command)
			if target == nil {
				t.Fatalf("command %q not found", tc.command)
			}
			err := target.Args(target, tc.args)
			if tc.wantErr && err == nil {
				t.Errorf("Args(%v) = nil, want an error", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Args(%v) returned error: %v", tc.args, err)
			}
		})
	}
}

func findCommand(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
