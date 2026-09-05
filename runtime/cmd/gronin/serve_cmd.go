package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/schedule"
	"github.com/nicodarge/Gronin/runtime/internal/stage/agent"
)

// newServeCommand is FR-020's daemon half: load, refuse, arm, run.
//
// The order is the design. Everything is loaded and validated before a single trigger is
// armed, and a refusal anywhere means nothing is armed at all — refusing two playbooks
// out of six and starting anyway is what this exists to prevent.
func newServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Load and validate every playbook, arm the schedules, and run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loaded, err := loadPlaybooks(cmd)
			if err != nil {
				return err
			}

			deployment, err := openDeployment(cmd)
			if err != nil {
				return err
			}
			defer deployment.close()

			// FR-019 then FR-033, before anything is armed. A bounding flag an older
			// executable does not recognise is ignored rather than refused, and a
			// deployment that cannot authenticate arms schedules that will fail one by
			// one, each after a working directory and a record row.
			version, err := agent.CheckVersion(cmd.Context(), deployment.agentExecutable)
			if err != nil {
				return err
			}
			source, err := agent.VerifyCredential(cmd.Context(), deployment.agentExecutable,
				deployment.executor.AgentEnv, deployment.stateDir)
			if err != nil {
				return err
			}
			cmd.Printf("agent %s, credential from %s\n", version, source)

			// FR-031: a run the record still calls running cannot be, because this
			// process has just started. It is marked interrupted and left alone —
			// restarting it would spend money on a decision nobody made.
			interrupted, err := deployment.store.MarkRunningAsInterrupted(cmd.Context())
			if err != nil {
				return err
			}
			if interrupted > 0 {
				cmd.Printf("marked %d interrupted run(s) from a previous process\n", interrupted)
			}

			scheduler := schedule.New(
				func(ctx context.Context, name string, _ time.Time) error {
					book, found := loaded.Find(name)
					if !found {
						return fmt.Errorf("no playbook named %q", name)
					}
					finished, err := deployment.executor.Execute(
						ctx, book, record.TriggerSchedule, nil)
					if outcome := scheduledOutcome(finished, err); outcome != nil {
						return outcome
					}
					if finished.Status != record.StatusSucceeded {
						deployment.log.Warn("a scheduled run did not succeed",
							"playbook", name, "run", finished.ID,
							"status", string(finished.Status), "err", finished.Error)
					}
					return nil
				},
				func(ctx context.Context, name string, at time.Time, reason string) error {
					return deployment.store.RecordMissedOccurrence(ctx, name, at, reason)
				},
			)

			armed := 0
			for _, book := range loaded.Playbooks {
				if book.Trigger.Type != "cron" {
					continue
				}
				parsed, err := schedule.Parse(book.Trigger.Schedule)
				if err != nil {
					return fmt.Errorf("%s: %w", book.Name, err)
				}
				scheduler.Add(book.Name, parsed, time.Now().UTC())
				armed++
			}

			cmd.Printf("armed %d schedule(s) of %d playbook(s) from %s\n",
				armed, len(loaded.Playbooks), playbooksDir(cmd))

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			err = scheduler.Run(ctx, func() time.Time { return time.Now().UTC() }, deployment.log)
			if errors.Is(err, context.Canceled) {
				cmd.Println("stopping; runs in flight are marked interrupted on the next start")
				return nil
			}
			return err
		},
	}
}

// scheduledOutcome decides what the scheduler is told about an occurrence.
//
// It is a function of its own because the distinction it makes is the one this branch
// got wrong: the scheduler records a fire's error as a MISSED occurrence, and a run that
// happened and failed is not one. Telling an operator that something did not happen when
// it did is worse than saying nothing, and it costs them the run record they would
// otherwise go looking for.
//
// An error means no run exists: the playbook was already in flight, or its working
// directory could not be made. Any status at all means one does.
func scheduledOutcome(finished record.Run, err error) error {
	if err != nil {
		return err
	}
	if finished.ID == "" {
		// No error and no run either. Nothing else should produce this, and reporting it
		// as missed is the truthful reading rather than silence.
		return errors.New("the occurrence produced no run and no error")
	}
	return nil
}
