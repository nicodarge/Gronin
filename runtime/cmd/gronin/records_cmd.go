package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// newRunsCommand lists runs, most recent first (FR-021).
func newRunsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "runs",
		Short: "List runs, most recent first",
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

			limit, _ := cmd.Flags().GetInt("limit")
			runs, err := deployment.store.ListRuns(cmd.Context(), limit)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				cmd.Println("no runs recorded")
				return nil
			}
			for _, one := range runs {
				cmd.Printf("%-30s %-12s %-9s %-8s %8s  %s\n",
					one.ID, one.PlaybookName, one.Status, one.TriggerKind,
					duration(one), cost(one))
			}
			return nil
		},
	}
	command.Flags().Int("limit", 50, "how many runs to list")
	return command
}

// newShowCommand prints one run's record (FR-021). It is the answer to "why did it do
// that", which is the question the record exists for.
func newShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <run>",
		Short: "Print one run's record",
		Args:  cobra.ExactArgs(1),
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

			ctx := cmd.Context()
			one, err := deployment.store.GetRun(ctx, args[0])
			if err != nil {
				return err
			}

			cmd.Printf("run       %s\n", one.ID)
			cmd.Printf("playbook  %s\n", one.PlaybookName)
			cmd.Printf("status    %s\n", one.Status)
			cmd.Printf("trigger   %s\n", one.TriggerKind)
			if one.ParentRunID != "" {
				cmd.Printf("derives   %s\n", one.ParentRunID)
			}
			cmd.Printf("started   %s\n", one.StartedAt.Format(time.RFC3339))
			if !one.EndedAt.IsZero() {
				cmd.Printf("took      %s\n", duration(one))
			}
			cmd.Printf("cost      %s over %d tokens\n", cost(one), one.Tokens)
			if one.CredentialSource != "" {
				cmd.Printf("credential %s\n", one.CredentialSource)
			}
			if one.Error != "" {
				cmd.Printf("error     %s\n", one.Error)
			}

			inputs, err := deployment.store.GatheredInputs(ctx, one.ID)
			if err != nil {
				return err
			}
			for _, input := range inputs {
				truncated := ""
				if input.Truncated {
					truncated = " (truncated)"
				}
				cmd.Printf("gathered  %s %d bytes exit %d%s\n",
					input.Name, input.Bytes, input.ExitCode, truncated)
			}

			calls, err := deployment.store.ToolCalls(ctx, one.ID)
			if err != nil {
				return err
			}
			for _, call := range calls {
				cmd.Printf("tool      %d %s %s\n", call.Sequence, call.Name, call.Outcome)
			}

			// The small table with the high value: a run that succeeds while repeatedly
			// reaching for something it cannot have is telling you its tool set is wrong.
			refused, err := deployment.store.RefusedActions(ctx, one.ID)
			if err != nil {
				return err
			}
			for _, action := range refused {
				cmd.Printf("refused   %s: %s\n", action.Tool, action.Asked)
			}

			outcomes, err := deployment.store.SinkOutcomes(ctx, one.ID)
			if err != nil {
				return err
			}
			for _, outcome := range outcomes {
				detail := ""
				if outcome.Detail != "" {
					detail = " — " + outcome.Detail
				}
				cmd.Printf("sink      %s %s%s\n", outcome.Sink, outcome.Status, detail)
			}

			for label, ref := range map[string]string{
				"prompt": one.PromptRef, "report": one.ReportRef, "playbook as run": one.ResolvedPlaybookRef,
			} {
				if ref != "" {
					cmd.Printf("%-9s %s\n", strings.ReplaceAll(label, " ", "-"), ref)
				}
			}
			return nil
		},
	}
}

// newReplayCommand re-runs the agent stage against the recorded inputs (FR-027).
func newReplayCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "replay <run>",
		Short: "Re-run the agent stage against a recorded run's inputs",
		Args:  cobra.ExactArgs(1),
		RunE: fromRecord(func(e *deployment, cmd *cobra.Command, parentID string, book *bookRef) (record.Run, error) {
			return e.executor.Replay(cmd.Context(), parentID, book.book)
		}),
	}
}

// newResumeCommand re-runs only the sinks (FR-028).
func newResumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <run>",
		Short: "Re-run only the sinks against a recorded run's report",
		Args:  cobra.ExactArgs(1),
		RunE: fromRecord(func(e *deployment, cmd *cobra.Command, parentID string, book *bookRef) (record.Run, error) {
			return e.executor.Resume(cmd.Context(), parentID, book.book)
		}),
	}
}

// bookRef carries the loaded playbook without this file importing the playbook package
// under a name that would shadow the local variable it is usually held in.
type bookRef struct{ book *playbook.Playbook }

// fromRecord is the shape replay and resume share: find the run, find the playbook it
// named, do the thing, report the outcome and the exit code.
func fromRecord(
	do func(*deployment, *cobra.Command, string, *bookRef) (record.Run, error),
) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := openConfig(cmd)
		if err != nil {
			return err
		}
		deployment, err := openDeployment(cmd, cfg)
		if err != nil {
			return err
		}
		defer deployment.close()

		parent, err := deployment.store.GetRun(cmd.Context(), args[0])
		if err != nil {
			return err
		}

		loaded, err := loadPlaybooks(cmd, cfg)
		if err != nil {
			return errSilent{err}
		}
		book, found := loaded.Find(parent.PlaybookName)
		if !found {
			return fmt.Errorf("run %s ran the playbook %q, which is not loaded now",
				parent.ID, parent.PlaybookName)
		}

		finished, err := do(deployment, cmd, parent.ID, &bookRef{book: book})
		if err != nil {
			return err
		}
		cmd.Printf("%s %s\n", finished.ID, finished.Status)
		if finished.Error != "" {
			cmd.Printf("  %s\n", finished.Error)
		}
		if finished.Status != record.StatusSucceeded {
			return errNotSucceeded{status: string(finished.Status)}
		}
		return nil
	}
}

func duration(one record.Run) string {
	if one.EndedAt.IsZero() {
		return "-"
	}
	return one.EndedAt.Sub(one.StartedAt).Round(time.Millisecond).String()
}

func cost(one record.Run) string {
	if one.CostUSD == 0 {
		return "$0"
	}
	return fmt.Sprintf("$%.4f", one.CostUSD)
}
