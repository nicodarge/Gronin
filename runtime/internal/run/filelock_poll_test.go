package run_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// FR-101 on one host, from the other side: a trigger waiting for a playbook must not be
// what refuses another. The poll hook runs at the instant the waiter looks at the lock, and
// there a scheduled tick from another process on the same state directory asks for it.
// Nothing is running, so the tick has to get it; a poll that looks by taking the lock
// refuses it as held, naming a run that already ended, and a tick does not wait.
func TestTheWaitersPollCannotRefuseATick(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	work, locks := filepath.Join(dir, "work"), filepath.Join(dir, "locks")
	waiting, serving := run.NewManager(store, work, locks), run.NewManager(store, work, locks)

	ended, err := waiting.FileLock().Acquire(t.Context(), guard.AcquireRequest{
		Name: "drift-check", Holder: guard.Holder{RunID: "run-ended"},
		Trigger: guard.TriggerRef{Kind: guard.KindManual},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ended.Release(t.Context()); err != nil {
		t.Fatal(err)
	}

	var (
		polled  bool
		tickErr error
	)
	waiter := waiting.FileLock()
	waiter.OnPoll(func(name string) {
		if polled {
			return
		}
		polled = true
		tick, err := serving.FileLock().Acquire(t.Context(), guard.AcquireRequest{
			Name: name, Holder: guard.Holder{RunID: "run-tick"},
			Trigger: guard.TriggerRef{
				Kind: guard.KindSchedule, DueAt: time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC),
			},
		})
		tickErr = err
		if err == nil {
			_ = tick.Release(t.Context())
		}
	})

	watch, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_ = waiter.Released(watch, "drift-check")

	if !polled {
		t.Fatal("the waiter never polled the lock")
	}
	if errors.Is(tickErr, guard.ErrHeld) {
		t.Fatalf("a tick was refused as held while nothing ran: %v", tickErr)
	}
	if tickErr != nil {
		t.Fatalf("the tick could not take a free lock: %v", tickErr)
	}
}
