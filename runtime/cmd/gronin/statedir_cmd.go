package main

import "github.com/spf13/cobra"

// newStateDirCommand prints where this invocation would write. It exists because the
// answer comes from a flag, an environment variable and two fallbacks, and an operator
// debugging a deployment should be able to ask rather than reconstruct.
func newStateDirCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "state-dir",
		Short: "Print the directory this deployment writes to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Println(stateDirOf(cmd))
			return nil
		},
	}
}
