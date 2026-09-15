// Package run owns the run lifecycle: identifier, working directory, status
// transitions, the single-flight guard, replay and resume.
package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/idgen"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// ErrAlreadyRunning is what a trigger gets when the same playbook is already in flight.
// It is not a failure of the run that is executing, and the caller records the
// occurrence rather than queueing behind it: a schedule that fires while the previous
// occurrence is still going is telling you the schedule is too tight, and stacking runs
// spends money to hide that.
//
// It is the single-host spelling of the coordination contract's ErrHeld, and wraps it, so
// that a caller holding a Coordinator does not have to know which one it has.
var ErrAlreadyRunning = fmt.Errorf("a run of this playbook is already in flight (%w)", guard.ErrHeld)

// Run is one execution in progress.
type Run struct {
	ID           string
	PlaybookName string
	WorkDir      string
	TriggerKind  record.TriggerKind
	StartedAt    time.Time

	claim guard.Claim
}

// Claimed is a claim taken for a run that has not begun. The run's identifier is minted
// before the claim so that the claim can name it, which is what a refusal elsewhere in
// the deployment shows an operator.
type Claimed struct {
	Claim        guard.Claim
	RunID        string
	PlaybookName string
	Reach        string
	// WaitingTriggerID and WaitedMS say the run started from a trigger that waited, and
	// for how long (FR-125).
	WaitingTriggerID string
	WaitedMS         int64
}

// Manager creates runs, keeps one playbook from running twice at once, and makes sure a
// working directory does not outlive the run that owns it.
type Manager struct {
	store    *record.Store
	workRoot string
	locksDir string
}

// NewManager returns a manager writing working directories under workRoot and holding
// its cross-process locks under locksDir.
func NewManager(store *record.Store, workRoot, locksDir string) *Manager {
	return &Manager{store: store, workRoot: workRoot, locksDir: locksDir}
}

// playbookNamePattern mirrors the contract's constraint on a playbook name
// (specs/001-runtime-core/contracts/playbook.schema.json). A name is placed into a lock
// file path below, so the constraint is asserted here rather than trusted silently.
var playbookNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// NewRunID mints a run identifier. It is minted before the guard decides, so that the
// claim it takes names the run that will hold it.
func NewRunID() (string, error) { return idgen.New(time.Now().UTC()) }

// Begin creates the working directory and records the run as running, under a claim
// already taken for it. Nothing is created before the claim, so two triggers racing
// cannot both get as far as a directory.
//
// The claim is not released here on failure: it was taken by the caller, which releases
// it — the run's own claim is released by Finish.
func (m *Manager) Begin(
	ctx context.Context, claimed Claimed, kind record.TriggerKind, parentRunID string,
) (*Run, error) {
	run := &Run{
		ID: claimed.RunID, PlaybookName: claimed.PlaybookName, TriggerKind: kind,
		StartedAt: time.Now().UTC(), WorkDir: filepath.Join(m.workRoot, claimed.RunID),
		claim: claimed.Claim,
	}

	if err := os.MkdirAll(run.WorkDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the working directory: %w", err)
	}
	recorded := record.Run{
		ID: run.ID, PlaybookName: run.PlaybookName, TriggerKind: kind, ParentRunID: parentRunID,
		Status: record.StatusRunning, StartedAt: run.StartedAt,
		ClaimReach:       record.Reach(claimed.Reach),
		WaitingTriggerID: claimed.WaitingTriggerID, WaitedMS: claimed.WaitedMS,
	}
	if claimed.Claim != nil {
		recorded.ClaimToken = claimed.Claim.Token()
	}
	if err := m.store.CreateRun(ctx, recorded); err != nil {
		_ = os.RemoveAll(run.WorkDir)
		return nil, err
	}
	return run, nil
}

// claim takes the advisory file lock on a playbook. It is the single-host Coordinator's
// acquisition (filelock.go), kept here because the lock is.
//
// Two runs in one process are excluded by it as surely as two processes are: the lock is
// held by an open file description, and each acquisition opens its own.
func (m *Manager) claim(playbookName string) (*held, error) {
	if !playbookNamePattern.MatchString(playbookName) {
		return nil, fmt.Errorf("playbook name %q does not match %s", playbookName, playbookNamePattern)
	}

	lockFile, err := m.acquireLock(playbookName)
	if err != nil {
		return nil, err
	}
	return &held{playbookName: playbookName, lockFile: lockFile}, nil
}

// held is what claim took.
type held struct {
	playbookName string
	lockFile     *os.File
}

// unclaim gives it back.
func (m *Manager) unclaim(h *held) {
	m.releaseLock(h.lockFile)
}

// acquireLock takes the advisory file lock that makes the claim visible across
// processes. It is held for the life of the run and released when the claim is — the
// kernel drops it on its own if the holding process dies, including on SIGKILL, so there
// is nothing to clean up by hand and no lease to expire.
func (m *Manager) acquireLock(playbookName string) (*os.File, error) {
	if err := os.MkdirAll(m.locksDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the locks directory: %w", err)
	}
	path := m.lockPath(playbookName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // playbookName is checked against playbookNamePattern above
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		// Only contention is another process holding it. A read-only or full filesystem
		// reaches here too, and reporting that as a concurrent run sends an operator
		// looking for one that does not exist.
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: held by another process", ErrAlreadyRunning)
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return file, nil
}

func (m *Manager) lockPath(playbookName string) string {
	return filepath.Join(m.locksDir, playbookName+".lock")
}

// releaseLock unlocks and closes the file. Closing matters as much as unlocking: an
// unclosed descriptor keeps the lock held by this process even after Flock(LOCK_UN).
func (m *Manager) releaseLock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

// Finish records the run's terminal state, removes its working directory and releases
// the playbook.
//
// The directory goes on every path — success, failure, timeout, refusal — which is why
// this takes the outcome rather than being called only when things went well. Whatever
// the record needs from that directory has to have been copied into it already; FR-011
// removes the directory and FR-026 requires the inputs to survive, so the ordering is
// the whole of it.
func (m *Manager) Finish(ctx context.Context, run *Run, outcome record.Run) error {
	outcome.ID = run.ID
	if outcome.EndedAt.IsZero() {
		outcome.EndedAt = time.Now().UTC()
	}

	recordErr := m.store.FinishRun(ctx, outcome)
	removeErr := os.RemoveAll(run.WorkDir)
	releaseErr := run.claim.Release(ctx)

	return errors.Join(recordErr, removeErr, releaseErr)
}

// InFlight reports whether a playbook is currently running, by taking its lock and
// giving it back — the only way to ask. A lock that cannot be taken for another reason
// reads as running, which is the safe direction to be wrong in.
func (m *Manager) InFlight(playbookName string) bool {
	file, err := m.acquireLock(playbookName)
	if err != nil {
		return true
	}
	m.releaseLock(file)
	return false
}
