package run_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// begin takes the claim the way the guard does and begins the run under it. Every test
// below that used to reach the lock through Manager.Begin goes through the file lock
// instead, which is where the lock now lives.
func begin(
	t *testing.T, manager *run.Manager, playbookName string, kind record.TriggerKind,
) (*run.Run, error) {
	t.Helper()
	claimed, err := claim(t, manager, playbookName)
	if err != nil {
		return nil, err
	}
	started, err := manager.Begin(t.Context(), claimed, kind, "")
	if err != nil {
		_ = claimed.Claim.Release(t.Context())
		return nil, err
	}
	return started, nil
}

func claim(t *testing.T, manager *run.Manager, playbookName string) (run.Claimed, error) {
	t.Helper()
	id := nextRunID(t)
	lock := manager.FileLock()
	held, err := lock.Acquire(t.Context(), guard.AcquireRequest{
		Name:   playbookName,
		Holder: guard.Holder{Host: "host.example.com", Instance: "instance", RunID: id},
	})
	if err != nil {
		return run.Claimed{}, err
	}
	return run.Claimed{Claim: held, RunID: id, PlaybookName: playbookName, Reach: lock.Reach()}, nil
}

func nextRunID(t *testing.T) string {
	t.Helper()
	id, err := run.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newManager(t *testing.T) (*run.Manager, *record.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	work := filepath.Join(dir, "work")
	return run.NewManager(store, work, filepath.Join(dir, "locks")), store, work
}

// A playbook name is placed into a lock file path, and the contract that constrains it
// (specs/001-runtime-core/contracts/playbook.schema.json) is enforced elsewhere, at
// load. The claim asserts it again rather than trusting that a caller always went
// through the gate first.
func TestBeginRefusesAPlaybookNameUnsafeAsAFileName(t *testing.T) {
	manager, _, _ := newManager(t)

	for _, name := range []string{"../escape", "UPPER", "", "with/slash", "a."} {
		if _, err := begin(t, manager, name, record.TriggerManual); err == nil {
			t.Fatalf("playbook name %q was accepted", name)
		}
	}
}

// FR-016. The guard is what stops a schedule that fires faster than its playbook
// finishes from stacking runs, each spending money to hide that the schedule is wrong.
func TestASecondRunOfTheSamePlaybookIsRefused(t *testing.T) {
	manager, _, _ := newManager(t)
	ctx := t.Context()

	first, err := begin(t, manager, "drift-check", record.TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}

	_, err = begin(t, manager, "drift-check", record.TriggerManual)
	if !errors.Is(err, run.ErrAlreadyRunning) {
		t.Fatalf("the second run began: %v", err)
	}
	// The refusal names the run holding the playbook: from the in-process claim here, and
	// from the lock file's own contents for a second process.
	if !strings.Contains(err.Error(), first.ID) {
		t.Fatalf("the refusal does not name the run holding the playbook: %v", err)
	}

	// A different playbook is not blocked by it.
	other, err := begin(t, manager, "cert-expiry", record.TriggerManual)
	if err != nil {
		t.Fatalf("an unrelated playbook was blocked: %v", err)
	}

	// And the claim is released when the first finishes.
	if err := manager.Finish(ctx, first, record.Run{Status: record.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	third, err := begin(t, manager, "drift-check", record.TriggerManual)
	if err != nil {
		t.Fatalf("the playbook stayed claimed after its run finished: %v", err)
	}
	_ = manager.Finish(ctx, other, record.Run{Status: record.StatusSucceeded})
	_ = manager.Finish(ctx, third, record.Run{Status: record.StatusSucceeded})
}

// FR-016 across processes. Two Managers over one state directory is what two `gronin`
// processes sharing a deployment look like — a `serve` running a schedule and a `run`
// invoked by hand, each with its own in-memory claim. The in-memory map alone cannot see
// across that boundary; the file lock is what does.
func TestASecondManagerOverTheSameStateDirIsRefused(t *testing.T) {
	dir := t.TempDir()
	locksDir := filepath.Join(dir, "locks")
	ctx := t.Context()

	storeA, err := record.Open(ctx, filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	managerA := run.NewManager(storeA, filepath.Join(dir, "work"), locksDir)

	storeB, err := record.Open(ctx, filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeB.Close() })
	managerB := run.NewManager(storeB, filepath.Join(dir, "work"), locksDir)

	first, err := begin(t, managerA, "drift-check", record.TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := begin(t, managerB, "drift-check", record.TriggerManual); !errors.Is(err, run.ErrAlreadyRunning) {
		t.Fatalf("a second manager over the same state directory began the run: %v", err)
	}

	if err := managerA.Finish(ctx, first, record.Run{Status: record.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}

	second, err := begin(t, managerB, "drift-check", record.TriggerManual)
	if err != nil {
		t.Fatalf("the playbook stayed claimed after the holding manager finished: %v", err)
	}
	_ = managerB.Finish(ctx, second, record.Run{Status: record.StatusSucceeded})
}

// The guard is a claim taken before anything is created, so a race cannot leave two
// runs each believing they hold the playbook.
func TestOnlyOneOfManyRacingTriggersWins(t *testing.T) {
	manager, _, _ := newManager(t)
	ctx := t.Context()

	const racers = 24
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		won     []*run.Run
		refused int
	)
	wg.Add(racers)
	for range racers {
		go func() {
			defer wg.Done()
			started, err := begin(t, manager, "drift-check", record.TriggerSchedule)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won = append(won, started)
			case errors.Is(err, run.ErrAlreadyRunning):
				refused++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if len(won) != 1 || refused != racers-1 {
		t.Fatalf("%d runs began and %d were refused, want 1 and %d", len(won), refused, racers-1)
	}
	_ = manager.Finish(ctx, won[0], record.Run{Status: record.StatusSucceeded})
}

// FR-011, on every path out. The directory is where a run's untrusted working state
// lives; leaving one behind on the unhappy path is how a disk fills months later.
func TestTheWorkingDirectoryIsGoneWhateverTheOutcome(t *testing.T) {
	for _, status := range []record.Status{
		record.StatusSucceeded, record.StatusFailed, record.StatusTimedOut,
		record.StatusRefused, record.StatusCapped,
	} {
		t.Run(string(status), func(t *testing.T) {
			manager, store, work := newManager(t)
			ctx := t.Context()

			started, err := begin(t, manager, "drift-check", record.TriggerManual)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(started.WorkDir); err != nil {
				t.Fatalf("the working directory was not created: %v", err)
			}
			if err := os.WriteFile(filepath.Join(started.WorkDir, "facts.json"),
				[]byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := manager.Finish(ctx, started, record.Run{Status: status}); err != nil {
				t.Fatal(err)
			}

			if _, err := os.Stat(started.WorkDir); !os.IsNotExist(err) {
				t.Fatalf("the working directory survived a %s run: %v", status, err)
			}
			entries, err := os.ReadDir(work)
			if err == nil && len(entries) != 0 {
				t.Fatalf("%d directories left under the work root", len(entries))
			}

			got, err := store.GetRun(ctx, started.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != status {
				t.Fatalf("status recorded as %q, want %q", got.Status, status)
			}
			if got.EndedAt.IsZero() {
				t.Fatal("a finished run has no end time")
			}
		})
	}
}

// A failure between the claim and the record has to release the claim, or the playbook
// is wedged until the process restarts — a failure mode that outlives its cause.
func TestAFailureToBeginDoesNotWedgeThePlaybook(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	// A work root that cannot hold directories: the file is in the way.
	blocked := filepath.Join(dir, "work")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := run.NewManager(store, blocked, filepath.Join(dir, "locks"))

	if _, err := begin(t, manager, "drift-check", record.TriggerManual); err == nil {
		t.Fatal("beginning a run succeeded with no writable work root")
	}
	if manager.InFlight("drift-check") {
		t.Fatal("the playbook is still claimed by a run that never began")
	}
}

func TestRunIdentifiersSortByTimeAndDoNotCollide(t *testing.T) {
	manager, _, _ := newManager(t)
	ctx := t.Context()

	seen := map[string]bool{}
	var previous string
	for i := range 50 {
		name := "playbook-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		started, err := begin(t, manager, name, record.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if seen[started.ID] {
			t.Fatalf("identifier %q was issued twice", started.ID)
		}
		seen[started.ID] = true
		if previous != "" && started.ID[:16] < previous[:16] {
			t.Fatalf("identifiers do not sort by time: %q then %q", previous, started.ID)
		}
		previous = started.ID
		if err := manager.Finish(ctx, started, record.Run{Status: record.StatusSucceeded}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFinishRecordsWhatTheRunProduced(t *testing.T) {
	manager, store, _ := newManager(t)
	ctx := t.Context()

	started, err := begin(t, manager, "drift-check", record.TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Finish(ctx, started, record.Run{
		Status: record.StatusSucceeded, CostUSD: 1.5, Tokens: 900,
		AgentSessionID: "sess", CredentialSource: "ANTHROPIC_API_KEY",
		EndedAt: started.StartedAt.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetRun(ctx, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CostUSD != 1.5 || got.Tokens != 900 || got.CredentialSource != "ANTHROPIC_API_KEY" {
		t.Fatalf("recorded as %+v", got)
	}
	if got.TriggerKind != record.TriggerSchedule {
		t.Fatalf("trigger kind = %q", got.TriggerKind)
	}
}

// A lock that cannot be taken for a reason other than contention must not be reported as
// contention: a read-only or full filesystem answered "held by another process" would
// send an operator looking for a concurrent run that does not exist.
func TestAFailureThatIsNotContentionIsNotReportedAsAnotherRun(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// A regular file where the locks directory has to be: MkdirAll fails with ENOTDIR
	// whatever the caller's privileges, which a permissions-based fixture cannot promise.
	locks := filepath.Join(dir, "locks")
	if err := os.WriteFile(locks, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	manager := run.NewManager(store, filepath.Join(dir, "work"), locks)
	_, err = begin(t, manager, "daily-drift", record.TriggerManual)
	if err == nil {
		t.Fatal("a run began although its lock could not be taken")
	}
	if errors.Is(err, run.ErrAlreadyRunning) {
		t.Fatalf("a filesystem failure was reported as a concurrent run: %v", err)
	}
}
