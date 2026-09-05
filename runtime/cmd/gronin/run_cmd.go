package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// newRunCommand is FR-022: a playbook invoked by hand, recorded as manually invoked, and
// subject to the same validation and bounds as a scheduled one. The same code path runs
// it, which is what makes that sentence true rather than aspirational.
func newRunCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "run <playbook>",
		Short: "Invoke a playbook immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, err := loadPlaybooks(cmd)
			if err != nil {
				return err
			}
			book, found := loaded.Find(args[0])
			if !found {
				return fmt.Errorf("no playbook named %q; loaded: %v", args[0], loaded.Names())
			}

			deployment, err := openDeployment(cmd)
			if err != nil {
				return err
			}
			defer deployment.close()

			finished, err := deployment.executor.Execute(
				cmd.Context(), book, record.TriggerManual, triggerValues(cmd))
			if err != nil {
				return err
			}

			cmd.Printf("%s %s\n", finished.ID, finished.Status)
			if finished.Error != "" {
				cmd.Printf("  %s\n", finished.Error)
			}
			if finished.Status != record.StatusSucceeded {
				// The exit code is the contract: a run that did not succeed exits
				// non-zero, so a caller in a shell script does not have to parse this.
				return errNotSucceeded{status: string(finished.Status)}
			}
			return nil
		},
	}
	command.Flags().StringToString("trigger", nil,
		"values the playbook may interpolate as ${trigger.x}")
	return command
}

type errNotSucceeded struct{ status string }

func (e errNotSucceeded) Error() string { return "the run " + e.status }

func triggerValues(cmd *cobra.Command) map[string]string {
	values, err := cmd.Flags().GetStringToString("trigger")
	if err != nil {
		return nil
	}
	return values
}
