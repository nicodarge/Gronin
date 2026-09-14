package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/api"
	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
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
			// FR-305, ahead of the ingress this deployment does not yet have: a loaded
			// webhook playbook refuses startup before anything is armed. T054 narrows
			// this once --ingress-address exists, so it refuses only when that flag is
			// absent rather than unconditionally.
			if name, found := firstWebhookPlaybook(loaded); found {
				err := fmt.Errorf(
					"%s: its trigger is webhook, and this deployment has no ingress for it yet", name)
				cmd.PrintErrln(err)
				cmd.PrintErrln("Nothing was armed.")
				return errSilent{err}
			}

			deployment, err := openDeployment(cmd, cfg, catalog)
			if err != nil {
				// A coordination configuration whose durations cannot hold is refused
				// here, before a single schedule is armed (FR-123).
				cmd.PrintErrln(err)
				cmd.PrintErrln("Nothing was armed.")
				return errSilent{err}
			}
			defer deployment.close()
			deployment.retrieving(declared)

			// FR-109: the reach of the guarantee is the first thing said about the guard,
			// so an operator never has to infer which one this deployment has.
			cmd.Println(deployment.coordination.describe())
			if err := deployment.coordination.reachable(cmd.Context()); err != nil {
				// Not a reason to stop: the backend may be back before the first trigger,
				// and every trigger until then is refused naming it (FR-107).
				cmd.Println(deployment.coordination.unreachable(err))
			}

			// FR-019 then FR-033, before anything is armed. A bounding flag an older
			// executable does not recognise is ignored rather than refused, and a
			// deployment that cannot authenticate arms schedules that will fail one by
			// one, each after a working directory and a record row.
			version, err := agent.CheckVersion(cmd.Context(), deployment.agentExecutable)
			if err != nil {
				return err
			}
			source, err := agent.VerifyCredential(cmd.Context(), deployment.agentExecutable,
				deployment.executor.AgentEnv, deployment.stateDir,
				agent.CredentialProbeTimeout)
			if err != nil {
				return err
			}
			// Phrased around the source rather than "credential from <source>", because
			// the ordinary answer for an OAuth session is that the executable names
			// none, and "credential from not named by the agent" reads as a fault.
			if source == agent.SourceNotNamed {
				cmd.Printf("agent %s, authenticated; the agent named no credential source\n", version)
			} else {
				cmd.Printf("agent %s, credential from %s\n", version, source)
			}

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
			// FR-113: nothing runs from a trigger whose process ended while it waited, and
			// the drop is recorded rather than forgotten.
			dropped, err := deployment.reconcile(cmd.Context())
			if err != nil {
				return err
			}
			if dropped > 0 {
				cmd.Printf("marked %d waiting trigger(s) dropped: their process ended before they ran\n", dropped)
			}

			scheduler := schedule.New(
				func(ctx context.Context, name string, dueAt time.Time) error {
					return fire(ctx, deployment, loaded, name, dueAt)
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

			// FR-036, checked before anything listens: a deployment must not be one
			// restart away from an unauthenticated listener that can invoke playbooks.
			address, _ := cmd.Flags().GetString("api-address")
			token := ""
			if value, configured := deployment.config.Get("api_token"); configured {
				token = value.Value
			}
			if err := api.CheckAddress(address, token); err != nil {
				return err
			}
			// Through a ListenConfig so the bind itself is bounded by the command's
			// context: a serve that is cancelled while binding should stop, not hang.
			var listenConfig net.ListenConfig
			listener, err := listenConfig.Listen(cmd.Context(), "tcp", address)
			if err != nil {
				return fmt.Errorf("binding the API: %w", err)
			}
			server := &http.Server{
				Handler:           api.New(deployment.store, token).Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			go func() {
				if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					deployment.log.Error("the API stopped", "err", err)
				}
			}()
			defer func() { _ = server.Close() }()
			cmd.Printf("API on %s\n", listener.Addr())

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

// firstWebhookPlaybook names the first loaded playbook, in load order, whose trigger is
// webhook — enough to refuse startup by (FR-305); which one is first is not load-bearing,
// only that refusing before anything is armed names at least one.
func firstWebhookPlaybook(loaded playbook.Loaded) (string, bool) {
	for _, book := range loaded.Playbooks {
		if book.Trigger.Type == "webhook" {
			return book.Name, true
		}
	}
	return "", false
}

// fire is what the scheduler calls for one occurrence. It is a function of its own
// because what it hands the guard is a requirement in its own right (FR-129) and a
// closure inside serve's body is not reachable by a test.
func fire(
	ctx context.Context, deployment *deployment, loaded playbook.Loaded,
	name string, dueAt time.Time,
) error {
	book, found := loaded.Find(name)
	if !found {
		return fmt.Errorf("no playbook named %q", name)
	}
	// The instant handed to the guard is the one the schedule computed, never this
	// host's clock: two hosts firing one tick compute the same instant whatever their
	// clocks read (FR-129).
	finished, err := deployment.executor.Execute(ctx, book, run.Trigger{
		Kind: record.TriggerSchedule, DueAt: dueAt,
	})
	if outcome := scheduledOutcome(finished, err); outcome != nil {
		return outcome
	}
	if finished.Status != record.StatusSucceeded {
		deployment.log.Warn("a scheduled run did not succeed",
			"playbook", name, "run", finished.ID,
			"status", string(finished.Status), "err", finished.Error)
	}
	return nil
}

// scheduledOutcome decides what the scheduler is told about an occurrence.
//
// It is a function of its own because the distinction it makes is the one this branch
// got wrong: the scheduler records a fire's error as a MISSED occurrence, and a run that
// happened and failed is not one. Telling an operator that something did not happen when
// it did is worse than saying nothing, and it costs them the run record they would
// otherwise go looking for.
//
// A trigger the guard refused is not a missed occurrence either. It is recorded as a
// refusal, which says which mechanism refused it; reporting it as missed as well would
// tell an operator that one event happened twice.
//
// An error otherwise means no run exists: the playbook was already in flight, or its
// working directory could not be made. Any status at all means one does.
func scheduledOutcome(finished record.Run, err error) error {
	var refused *guard.Refused
	if errors.As(err, &refused) {
		return nil
	}
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
