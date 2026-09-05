package main

import (
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/stage/agent"
)

// version is set at link time for a release build. An ordinary `go build` leaves it
// empty and the build info below answers instead, so the command works either way.
var version string

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of this executable",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Println(resolveVersion())

			// And the agent it found, because the runtime's own version says nothing
			// about whether the bounding flags will be applied.
			executable, _ := cmd.Flags().GetString("agent")
			if executable == "" {
				executable = "claude"
			}
			found, err := agent.CheckVersion(cmd.Context(), executable)
			switch {
			case found == "":
				cmd.Printf("agent %s: not found\n", executable)
				return errSilent{err}
			case err != nil:
				cmd.Printf("agent %s: %s\n", executable, found)
				return errSilent{err}
			default:
				cmd.Printf("agent %s: %s (floor %s)\n", executable, found, agent.VersionFloor)
			}
			return nil
		},
	}
}

func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}
