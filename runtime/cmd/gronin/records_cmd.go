package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/stage/retrieve"
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
			catalog, err := openCatalog(cmd)
			if err != nil {
				return err
			}
			deployment, err := openDeployment(cmd, cfg, catalog)
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
	command := &cobra.Command{
		Use:   "show <run>",
		Short: "Print one run's record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			ctx := cmd.Context()
			one, err := deployment.store.GetRun(ctx, args[0])
			if err != nil {
				return err
			}
			if name, _ := cmd.Flags().GetString("retrieval"); name != "" {
				return writeRetrieval(cmd, deployment.store, one.ID, name)
			}

			cmd.Printf("run       %s\n", one.ID)
			cmd.Printf("playbook  %s\n", one.PlaybookName)
			cmd.Printf("status    %s\n", one.Status)
			cmd.Printf("trigger   %s\n", one.TriggerKind)
			if one.WaitingTriggerID != "" {
				// A run that started well after it was asked for reads as a late one unless it
				// says it waited (FR-125).
				cmd.Printf("waited    %s\n", waitedFor(one))
			}
			if one.ClaimReach != "" {
				// Which guarantee this run ran under. A single-host run says so, rather
				// than leaving a reader of the record to assume the stronger one.
				cmd.Printf("guarantee %s\n", one.ClaimReach)
			}
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

			retrievals, err := deployment.store.Retrievals(ctx, one.ID)
			if err != nil {
				return err
			}
			for _, retrieval := range retrievals {
				printRetrieval(cmd, retrieval)
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
	command.Flags().String("retrieval", "",
		"write the results file the named retrieval handed the agent, byte for byte, and nothing else")
	return command
}

// printRetrieval is one retrieval as contracts/cli.md shows it: what was searched, from
// which generation, and where each result came from (FR-225).
func printRetrieval(cmd *cobra.Command, retrieval record.Retrieval) {
	searched := ""
	if retrieval.Generation != "" {
		short := retrieval.Generation
		if len(short) > 12 {
			short = short[:12]
		}
		searched = fmt.Sprintf(" generation %s built %s",
			short, retrieval.GenerationBuiltAt.UTC().Format(time.RFC3339))
	}
	outcome := string(retrieval.Outcome)
	switch retrieval.Outcome {
	case record.RetrievalFound:
		outcome = fmt.Sprintf("found %d", len(retrieval.Items))
	case record.RetrievalRefused:
		outcome = "refused — " + retrieval.Error
	}
	cmd.Printf("retrieved %s from %s (%s)%s: %s\n",
		retrieval.AsName, retrieval.Collection, retrieval.Mode, searched, outcome)
	if retrieval.Query != "" {
		cmd.Printf("  query     %s\n", retrieve.Printable(strings.Join(strings.Fields(retrieval.Query), " ")))
	}
	var cut []string
	if retrieval.QueryTruncated {
		cut = append(cut, "the query")
	}
	if retrieval.CountTruncated {
		cut = append(cut, "the result count")
	}
	if retrieval.BytesTruncated {
		cut = append(cut, "the bytes")
	}
	if len(cut) > 0 {
		cmd.Printf("  cut       %s\n", strings.Join(cut, ", "))
	}
	for _, item := range retrieval.Items {
		cmd.Printf("  result    %d  %.4f  %s#%d\n", item.Rank, item.Score, item.Source, item.Ordinal)
	}
}

// writeRetrieval writes the results file a retrieval handed the agent, from the record
// and nothing else: the index has moved on since, and the file is what the run saw.
func writeRetrieval(cmd *cobra.Command, store *record.Store, runID, name string) error {
	retrievals, err := store.Retrievals(cmd.Context(), runID)
	if err != nil {
		return err
	}
	for _, retrieval := range retrievals {
		if retrieval.AsName != name {
			continue
		}
		if retrieval.ResultsRef == "" {
			return fmt.Errorf("run %s's retrieval %s was refused and handed the agent nothing: %s",
				runID, name, retrieval.Error)
		}
		data, err := store.Blobs().Get(retrieval.ResultsRef)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(data)
		return err
	}
	return fmt.Errorf("run %s has no retrieval named %q", runID, name)
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
		catalog, err := openCatalog(cmd)
		if err != nil {
			return err
		}
		deployment, err := openDeployment(cmd, cfg, catalog)
		if err != nil {
			return err
		}
		defer deployment.close()

		parent, err := deployment.store.GetRun(cmd.Context(), args[0])
		if err != nil {
			return err
		}

		declared, err := openCollections(cmd)
		if err != nil {
			return err
		}
		loaded, err := loadPlaybooks(cmd, cfg, catalog, declared)
		if err != nil {
			return errSilent{err}
		}
		deployment.retrieving(declared)
		book, found := loaded.Find(parent.PlaybookName)
		if !found {
			return fmt.Errorf("run %s ran the playbook %q, which is not loaded now",
				parent.ID, parent.PlaybookName)
		}

		finished, err := do(deployment, cmd, parent.ID, &bookRef{book: book})
		var refused *guard.Refused
		if errors.As(err, &refused) {
			cmd.Printf("%s was not run: %s\n  %s\n", book.Name, refused.Mechanism, refused.Detail)
			return errSilent{err}
		}
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

// waitedFor is how long a run waited for the one it collided with, as the waiting process
// measured it.
func waitedFor(one record.Run) string {
	return guard.HumanDuration(time.Duration(one.WaitedMS) * time.Millisecond)
}

func cost(one record.Run) string {
	if one.CostUSD == 0 {
		return "$0"
	}
	return fmt.Sprintf("$%.4f", one.CostUSD)
}
