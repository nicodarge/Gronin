package run_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// T088. The single-host rate window is the record store's own count of runs of the
// playbook started within rate.per, on the wall reading of an injected clock — never freed
// by a release, only by the window moving. A request declaring no limit, as guard.go
// builds for a replay or a resume, takes no slot and is never refused by it.
func TestFileLockRateWindowIsTheRecordStoresCountOfRuns(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := run.NewManager(store, filepath.Join(dir, "work"), filepath.Join(dir, "locks"))
	lock := manager.FileLock()
	clock := guardtest.NewClock(time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC))
	lock.SetClock(clock)

	limit := &guard.RateLimit{Runs: 2, Per: time.Minute}
	rated := func(id string) guard.AcquireRequest {
		return guard.AcquireRequest{
			Name: "drift-check", Holder: guard.Holder{RunID: id},
			Trigger: guard.TriggerRef{Kind: guard.KindManual}, Rate: limit,
		}
	}
	take := func(t *testing.T, req guard.AcquireRequest) {
		t.Helper()
		claim, err := lock.Acquire(t.Context(), req)
		if err != nil {
			t.Fatalf("acquiring %s: %v", req.Holder.RunID, err)
		}
		if err := claim.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	refusedForRate := func(t *testing.T, req guard.AcquireRequest) {
		t.Helper()
		if _, err := lock.Acquire(t.Context(), req); !errors.Is(err, guard.ErrRateLimited) {
			t.Fatalf("acquiring %s was not refused for rate: %v", req.Holder.RunID, err)
		}
	}

	take(t, rated("run-1"))
	take(t, rated("run-2"))
	// The window is full though every claim above was released (C9): a third is refused.
	refusedForRate(t, rated("run-3"))

	// The window moves past both runs.
	clock.Advance(time.Minute + time.Second)
	take(t, rated("run-4"))
	take(t, rated("run-5"))
	refusedForRate(t, rated("run-6"))

	// A request with no rate limit — the shape guard.go builds for a replay or a resume —
	// is never refused for rate, and leaves the window it did not consult unchanged.
	unrated := guard.AcquireRequest{
		Name: "drift-check", Holder: guard.Holder{RunID: "run-replay"},
		Trigger: guard.TriggerRef{Kind: guard.KindManual},
	}
	take(t, unrated)
	refusedForRate(t, rated("run-7"))
}
