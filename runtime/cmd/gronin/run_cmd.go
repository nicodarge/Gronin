package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
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
			cfg, err := openConfig(cmd)
			if err != nil {
				return err
			}
			catalog, err := openResolvableCatalog(cmd, cfg)
			if err != nil {
				return err
			}
			declared, err := openCollections(cmd)
			if err != nil {
				return err
			}
			srcs, err := openSources(cmd, cfg)
			if err != nil {
				return err
			}
			loaded, err := loadPlaybooks(cmd, cfg, catalog, declared, srcs)
			if err != nil {
				return err
			}
			book, found := loaded.Find(args[0])
			if !found {
				return fmt.Errorf("no playbook named %q; loaded: %v", args[0], loaded.Names())
			}

			// FR-327: a webhook playbook invoked by hand is held to the same declared
			// shapes as a delivery's, so the manual path is not a way around them.
			// Checked before a deployment is even opened — the same "refused before
			// anything runs" the ingress itself gives a delivery.
			values := triggerValues(cmd)
			if book.Trigger.Type == "webhook" {
				checked, err := checkedManualValues(book, values)
				if err != nil {
					cmd.PrintErrln(err)
					return errSilent{err}
				}
				values = checked
			}

			deployment, err := openDeployment(cmd, cfg, catalog)
			if err != nil {
				return err
			}
			defer deployment.close()
			if err := deployment.acceptWaiting(cfg, catalog, declared, srcs); err != nil {
				return err
			}
			deployment.retrieving(declared)

			finished, err := deployment.executor.Execute(cmd.Context(), book, run.Trigger{
				Kind: record.TriggerManual, Values: values,
				// Said before the wait begins, so that a caller does not read it as a hang.
				OnWait: func(waiting guard.Waiting) { cmd.Println(waitingLine(book.Name, waiting)) },
			})
			var refused *guard.Refused
			if errors.As(err, &refused) {
				// The mechanism is what an operator acts on, and it is in the record as
				// well: `gronin refusals` is where the whole list lives (FR-117).
				cmd.Printf("%s was not run: %s\n  %s\n", book.Name, refused.Mechanism, refused.Detail)
				return errSilent{err}
			}
			if err != nil {
				return err
			}

			waited := ""
			recorded, err := deployment.store.GetRun(cmd.Context(), finished.ID)
			switch {
			case err != nil:
				deployment.log.Warn("the run could not be read back to say whether it waited",
					"run", finished.ID, "err", err)
			case recorded.WaitingTriggerID != "":
				waited = " (waited " + waitedFor(recorded) + ")"
			}
			cmd.Printf("%s %s%s\n", finished.ID, finished.Status, waited)
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

// unnamedHolder is what the waiting line says when the coordinator could not name who holds
// the claim: the file lock between a lock being taken and its holder being written into it.
const unnamedHolder = "held by another process"

// waitingLine is what `gronin run` says as a trigger begins to wait (contracts/cli.md).
func waitingLine(playbookName string, waiting guard.Waiting) string {
	holder := unnamedHolder
	if waiting.Holder != nil {
		holder = waiting.Holder.String()
	}
	return fmt.Sprintf("%s is running (%s); waiting up to %s",
		playbookName, holder, guard.HumanDuration(waiting.UpTo))
}

type errNotSucceeded struct{ status string }

func (e errNotSucceeded) Error() string { return "the run " + e.status }

// checkedManualValues applies FR-327 to a webhook playbook invoked by hand: a name
// --trigger did not declare is refused naming it, and every declared one is held to
// playbook.Check exactly as a delivery's would be.
func checkedManualValues(book *playbook.Playbook, values map[string]string) (map[string]string, error) {
	names := make([]string, 0, len(book.Trigger.Values))
	for name := range book.Trigger.Values {
		names = append(names, name)
	}
	sort.Strings(names)
	accepted := strings.Join(names, ", ")
	if accepted == "" {
		accepted = "nothing; this trigger declares no values"
	}

	given := make([]string, 0, len(values))
	for name := range values {
		given = append(given, name)
	}
	sort.Strings(given)
	for _, name := range given {
		if _, declared := book.Trigger.Values[name]; !declared {
			return nil, fmt.Errorf("--trigger %s: not a declared value; accepted: %s", name, accepted)
		}
	}

	asAny := make(map[string]any, len(values))
	for name, value := range values {
		asAny[name] = value
	}
	checked, refusals := playbook.Check(book, asAny)
	if len(refusals) > 0 {
		return nil, refusals[0].Problem
	}
	return checked, nil
}

func triggerValues(cmd *cobra.Command) map[string]string {
	values, err := cmd.Flags().GetStringToString("trigger")
	if err != nil {
		return nil
	}
	return values
}
