package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/collections"
	"github.com/nicodarge/Gronin/runtime/internal/index"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/stage/retrieve"
)

// newCollectionsCommand is the operator's view of what a playbook may retrieve from
// (FR-215, FR-220). None of it reads a run's record, so it works with no run history, and
// all of it works while `serve` runs: an index is a database two processes can open.
func newCollectionsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "collections",
		Short: "List, inspect and rebuild the collections a playbook may retrieve from",
	}

	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "One line per collection: its mode, source, generation and whether its sources changed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			declared, err := openCollections(cmd)
			if err != nil {
				return err
			}
			names := declared.Names()
			if len(names) == 0 {
				cmd.Printf("no collections declared in %s\n", stateDirOf(cmd))
				return nil
			}
			store, err := openRecordStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()
			for _, name := range names {
				collection, _ := declared.Get(name)
				cmd.Println(listLine(cmd.Context(), stateDirOf(cmd), store, collection))
			}
			return nil
		},
	})

	command.AddCommand(&cobra.Command{
		Use:   "show <collection>",
		Short: "Every document a collection holds and every file it skipped, with the reason",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			collection, err := declaredCollection(cmd, args[0])
			if err != nil {
				return err
			}
			store, err := openRecordStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()
			found, err := inspect(cmd.Context(), stateDirOf(cmd), store, collection)
			if err != nil {
				return err
			}
			printInspection(cmd, found)
			return nil
		},
	})

	command.AddCommand(&cobra.Command{
		Use:   "rebuild <collection>",
		Short: "Rebuild a collection's index in full, outside any run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			collection, err := declaredCollection(cmd, args[0])
			if err != nil {
				return err
			}
			store, err := openRecordStore(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()
			generation, documents, err := rebuild(cmd.Context(), stateDirOf(cmd), store, collection)
			if err != nil {
				// The previous generation is still in place: a rebuild is one transaction.
				return fmt.Errorf("rebuilding %s: %w", collection.Name, err)
			}
			cmd.Printf("rebuilt %s: generation %s built %s from %d document(s)\n",
				collection.Name, generation.Short(), generation.BuiltAt.Format(time.RFC3339), documents)
			return nil
		},
	})
	return command
}

func declaredCollection(cmd *cobra.Command, name string) (collections.Collection, error) {
	declared, err := openCollections(cmd)
	if err != nil {
		return collections.Collection{}, err
	}
	collection, found := declared.Get(name)
	if !found {
		return collections.Collection{}, fmt.Errorf("no collection named %q; declared: %s",
			name, strings.Join(declared.Names(), ", "))
	}
	return collection, nil
}

// inspection is a collection's sources set against the generation its index holds.
type inspection struct {
	collection collections.Collection
	walk       index.Walk
	stored     index.Stored
	comparison index.Comparison
}

// listingWait is how long `list` and `show` wait behind an update holding the index before
// saying so. A rebuild holds it for as long as it takes, and a process stopped mid-update
// holds it until it is killed.
const listingWait = 3 * time.Second

// inspect walks and compares, and writes nothing: listing a collection is not a request to
// index it (FR-221).
func inspect(
	ctx context.Context, stateDir string, store *record.Store, collection collections.Collection,
) (inspection, error) {
	walk, cfg, err := retrieve.SourceWalk(ctx, store, collection)
	if err != nil {
		return inspection{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, listingWait)
	defer cancel()
	stored, _, err := index.ReadStored(bounded, indexDir(stateDir), collection.Name)
	if errors.Is(err, index.ErrHeld) {
		return inspection{}, fmt.Errorf("%w for more than %s", index.ErrHeld, listingWait)
	}
	if err != nil {
		return inspection{}, err
	}
	return inspection{
		collection: collection, walk: walk, stored: stored,
		comparison: index.Compare(cfg, stored, walk),
	}, nil
}

func listLine(ctx context.Context, stateDir string, store *record.Store, collection collections.Collection) string {
	line := fmt.Sprintf("%-15s %-9s %-25s ", collection.Name, collection.Mode(), collection.Source())
	found, err := inspect(ctx, stateDir, store, collection)
	switch {
	case err != nil:
		return line + "cannot be listed: " + err.Error()
	case found.stored.Generation.ID == "":
		return line + "not indexed yet"
	}
	status := "unchanged"
	if found.comparison.Changed {
		status = "changed since"
	}
	generation := found.stored.Generation
	return line + fmt.Sprintf("generation %s built %s  %s",
		generation.Short(), generation.BuiltAt.Format(time.RFC3339), status)
}

func printInspection(cmd *cobra.Command, found inspection) {
	collection, generation, comparison := found.collection, found.stored.Generation, found.comparison
	cmd.Printf("collection  %s\n", collection.Name)
	cmd.Printf("mode        %s\n", collection.Mode())
	cmd.Printf("source      %s\n", collection.Source())
	if generation.ID == "" {
		cmd.Println("generation  not indexed yet")
	} else {
		identity := generation.Identity
		if len(identity) > 4 {
			identity = identity[:4]
		}
		cmd.Printf("generation  %s built %s under identity %s…\n",
			generation.Short(), generation.BuiltAt.Format(time.RFC3339), identity)
	}
	switch {
	case generation.ID == "":
		cmd.Println("changed     not indexed yet")
	case comparison.Changed:
		cmd.Printf("changed     yes — %d added, %d changed, %d removed\n",
			comparison.Added, comparison.Modified, len(comparison.Removed))
	default:
		cmd.Println("changed     no")
	}
	cmd.Println()
	for _, document := range found.walk.Documents {
		cmd.Printf("document    %-21s %4d bytes  %s\n",
			document.Source, document.Bytes, comparison.Mark(document.Source))
	}
	for _, source := range comparison.Removed {
		cmd.Printf("removed     %s\n", source)
	}
	for _, skipped := range found.walk.Skipped {
		cmd.Printf("skipped     %-21s %s\n", skipped.Source, skipped.Reason)
	}
}

// rebuild replaces a collection's index with a walk of its sources, in one transaction.
func rebuild(
	ctx context.Context, stateDir string, store *record.Store, collection collections.Collection,
) (index.Generation, int, error) {
	walk, cfg, err := retrieve.SourceWalk(ctx, store, collection)
	if err != nil {
		return index.Generation{}, 0, err
	}
	ix, err := index.Open(ctx, indexDir(stateDir), collection.Name, cfg)
	if err != nil {
		return index.Generation{}, 0, err
	}
	defer func() { _ = ix.Close() }()
	generation, err := ix.Rebuild(ctx, walk)
	return generation, len(walk.Documents), err
}
