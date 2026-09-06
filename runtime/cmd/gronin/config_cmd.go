package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// newConfigCommand is FR-040, and it is part of the contract rather than a convenience.
//
// A playbook holds `${config.ops_webhook}`; the value lives on the deployment. That
// separation is the whole of what makes a playbook committable and shareable, so the
// command that maintains it is as much a part of the product as the runtime.
func newConfigCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Set and inspect the values playbooks interpolate against",
	}

	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a deployment configuration value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := openConfig(cmd)
			if err != nil {
				return err
			}
			deployment, err := openDeployment(cmd, cfg)
			if err != nil {
				return err
			}
			defer deployment.close()

			secret, err := cmd.Flags().GetBool("secret")
			if err != nil {
				return err
			}
			if err := deployment.config.Set(args[0], config.Value{Value: args[1], Secret: secret}); err != nil {
				return err
			}
			// The value is not echoed. It may be the secret that was just set, and this
			// command is run in a shell whose history keeps what it is told.
			cmd.Printf("set %s\n", args[0])
			return nil
		},
	}
	// Marked rather than guessed: a heuristic reading "token" and "webhook" as secret and
	// "endpoint" as not is wrong on the first deployment that disagrees, and wrong
	// silently.
	set.Flags().Bool("secret", false, "treat the value as a secret: redact it everywhere")

	list := &cobra.Command{
		Use:   "list",
		Short: "List configuration keys, secrets redacted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := openConfig(cmd)
			if err != nil {
				return err
			}
			deployment, err := openDeployment(cmd, cfg)
			if err != nil {
				return err
			}
			defer deployment.close()

			keys := deployment.config.Keys()
			if len(keys) == 0 {
				cmd.Printf("no configuration values in %s\n", deployment.stateDir)
				return nil
			}
			for _, key := range keys {
				value, _ := deployment.config.Get(key)
				shown := value.Value
				if value.Secret {
					shown = record.Placeholder
				}
				cmd.Println(fmt.Sprintf("%-24s %s", key, shown))
			}
			return nil
		},
	}

	command.AddCommand(set, list)
	return command
}
