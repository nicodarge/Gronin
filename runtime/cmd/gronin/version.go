package main

import (
	"runtime/debug"

	"github.com/spf13/cobra"
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
