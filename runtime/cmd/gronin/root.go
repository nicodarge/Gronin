package main

import (
	"io"

	"github.com/spf13/cobra"
)

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "gronin",
		Short:         "Run bounded agents from declarative playbooks",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)

	// One directory holds everything this deployment persists: the record, the blobs,
	// the configuration values playbooks interpolate against, and the working
	// directories runs are given. Persistent, because every command that talks to a
	// deployment has to name the same one.
	root.PersistentFlags().String("state-dir", defaultStateDir(),
		"directory holding the record, the configuration and run working directories")

	root.PersistentFlags().String("playbooks", "",
		"directory holding the playbooks (default: <state-dir>/playbooks)")
	root.PersistentFlags().String("agent", "claude",
		"the agent executable this deployment drives")

	root.AddCommand(newVersionCommand())
	root.AddCommand(newStateDirCommand())
	root.AddCommand(newRunCommand())
	root.AddCommand(newServeCommand())
	return root
}

// stateDirOf reads the resolved state directory for a command.
func stateDirOf(cmd *cobra.Command) string {
	dir, err := cmd.Flags().GetString("state-dir")
	if err != nil || dir == "" {
		return defaultStateDir()
	}
	return dir
}
