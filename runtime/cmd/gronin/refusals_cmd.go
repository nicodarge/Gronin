package main

import (
	"time"

	"github.com/spf13/cobra"
)

// newRefusalsCommand is SC-108's surface: every trigger that did not become a run, most
// recent first, naming which mechanism refused it (FR-117).
//
// It opens the record store the way `gronin runs` does, so it works after `serve` has
// been killed — which is precisely when what it has to show is worth reading.
func newRefusalsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "refusals",
		Short: "List triggers that did not become runs, most recent first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := openConfig(cmd)
			if err != nil {
				return err
			}
			catalog, err := openCatalog(cmd)
			if err != nil {
				return err
			}
			deployment, err := openDeployment(cmd, cfg, catalog)
			if err != nil {
				return err
			}
			defer deployment.close()

			// A trigger whose process was killed while it waited left nobody to record the
			// drop. Reading is when it is recorded, which is why it shows here (FR-113).
			if _, err := deployment.reconcile(cmd.Context()); err != nil {
				return err
			}

			limit, _ := cmd.Flags().GetInt("limit")
			refusals, err := deployment.store.ListRefusals(cmd.Context(), limit)
			if err != nil {
				return err
			}
			if len(refusals) == 0 {
				cmd.Println("no refusals recorded")
				return nil
			}
			for _, one := range refusals {
				cmd.Printf("%s  %-12s %-8s %-19s %s\n",
					one.RefusedAt.UTC().Format(time.RFC3339), one.PlaybookName,
					one.TriggerKind, one.Mechanism, one.Detail)
			}
			return nil
		},
	}
	command.Flags().Int("limit", 50, "how many refusals to list")
	return command
}
