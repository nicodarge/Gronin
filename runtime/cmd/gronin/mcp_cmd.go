package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newMCPCommand is read-only: an operator maintains the catalogue by editing the file
// beside config.json, the way agent.mcp names a server rather than configuring one. This
// is only where a deployment says what it already holds.
func newMCPCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Inspect the MCP servers this deployment provides",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the MCP servers a playbook's agent.mcp may name",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := openConfig(cmd)
			if err != nil {
				return err
			}
			catalog, err := openCatalog(cmd, cfg)
			if err != nil {
				return err
			}
			deployment, err := openDeployment(cmd, cfg, catalog)
			if err != nil {
				return err
			}
			defer deployment.close()

			names := deployment.catalog.Names()
			if len(names) == 0 {
				cmd.Printf("no MCP servers configured in %s\n", deployment.stateDir)
				return nil
			}
			for _, name := range names {
				cmd.Println(fmt.Sprintf("%-24s %s", name, deployment.catalog.Summary(name)))
			}
			return nil
		},
	}

	command.AddCommand(list)
	return command
}
