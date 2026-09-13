package main

import (
	"github.com/spf13/cobra"
)

// newSourcesCommand is read-only, beside newMCPCommand and for the same reason: an
// operator maintains the catalogue by editing sources.json, and this is only where a
// deployment says what it already configures (FR-311).
func newSourcesCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "sources",
		Short: "Inspect the webhook sources this deployment configures",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the sources a webhook trigger may bind to, with their secrets redacted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := openConfig(cmd)
			if err != nil {
				return err
			}
			srcs, err := openSources(cmd, cfg)
			if err != nil {
				return err
			}

			names := srcs.Names()
			if len(names) == 0 {
				cmd.Printf("no sources configured in %s\n", stateDirOf(cmd))
				return nil
			}
			for _, name := range names {
				cmd.Printf("%-8s %s\n", name, srcs.Summary(name))
			}
			return nil
		},
	}

	command.AddCommand(list)
	return command
}
