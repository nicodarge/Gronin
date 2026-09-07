// Package run owns the run lifecycle: identifier, working directory, status
// transitions, the single-flight guard, replay and resume.
package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// ErrAlreadyRunning is what a trigger gets when the same playbook is already in flight.
// It is not a failure of the run that is executing, and the caller records the
// occurrence rather than queueing behind it: a schedule that fires while the previous
// occurrence is still going is telling you the schedule is too tight, and stacking runs
// spends money to hide that.
var ErrAlreadyRunning = errors.New("a run of this playbook is already in flight")

// Run is one execution in progress.
type Run struct {
	ID           string
	PlaybookName string
	WorkDir      string
	TriggerKind  record.TriggerKind
	StartedAt    time.Time

	lockFile *os.File
}

// Manager creates runs, keeps one playbook from running twice at once, and makes sure a
// working directory does not outlive the run that owns it.
type Manager struct {
	store    *record.Store
	workRoot string
	locksDir string

	mu       sync.Mutex
	inFlight map[string]string // playbook name -> the run holding it
}

// NewManager returns a manager writing working directories under workRoot and holding
// its cross-process locks under locksDir.
func NewManager(store *record.Store, workRoot, locksDir string) *Manager {
	return &Manager{store: store, workRoot: workRoot, locksDir: locksDir, inFlight: map[string]string{}}
}

// playbookNamePattern mirrors the contract's constraint on a playbook name
// (specs/001-runtime-core/contracts/playbook.schema.json). A name is placed into a lock
// file path below, so the constraint is asserted here rather than trusted silently.
var playbookNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Begin claims the playbook, creates the working directory and records the run as
// running. The claim is taken before anything is created, so two triggers racing cannot
// both get as far as a directory.
func (m *Manager) Begin(
	ctx context.Context, playbookName string, kind record.TriggerKind, parentRunID string,
) (*Run, error) {
	if !playbookNamePattern.MatchString(playbookName) {
		return nil, fmt.Errorf("playbook name %q does not match %s", playbookName, playbookNamePattern)
	}

	id, err := newID(time.Now().UTC())
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if holder, running := m.inFlight[playbookName]; running {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, holder)
	}
	m.inFlight[playbookName] = id
	m.mu.Unlock()

	// From here every failure has to release the claim, or the playbook is wedged until
	// the process restarts.
	lockFile, err := m.acquireLock(playbookName)
	if err != nil {
		m.release(playbookName)
		return nil, fmt.Errorf("%w: held by another process", ErrAlreadyRunning)
	}

	run := &Run{
		ID: id, PlaybookName: playbookName, TriggerKind: kind,
		StartedAt: time.Now().UTC(), WorkDir: filepath.Join(m.workRoot, id),
		lockFile: lockFile,
	}

	if err := os.MkdirAll(run.WorkDir, 0o700); err != nil {
		m.releaseLock(lockFile)
		m.release(playbookName)
		return nil, fmt.Errorf("creating the working directory: %w", err)
	}
	if err := m.store.CreateRun(ctx, record.Run{
		ID: run.ID, PlaybookName: playbookName, TriggerKind: kind, ParentRunID: parentRunID,
		Status: record.StatusRunning, StartedAt: run.StartedAt,
	}); err != nil {
		_ = os.RemoveAll(run.WorkDir)
		m.releaseLock(lockFile)
		m.release(playbookName)
		return nil, err
	}
	return run, nil
}

// acquireLock takes the advisory file lock that makes the claim visible across
// processes. It is held for the life of the run and released in Finish or on any
// failure path in Begin — the kernel drops it on its own if the holding process dies,
// including on SIGKILL, so there is nothing to clean up by hand and no lease to expire.
func (m *Manager) acquireLock(playbookName string) (*os.File, error) {
	if err := os.MkdirAll(m.locksDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the locks directory: %w", err)
	}
	path := filepath.Join(m.locksDir, playbookName+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // playbookName is checked against playbookNamePattern above
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
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
	m.releaseLock(run.lockFile)
	m.release(run.PlaybookName)

	return errors.Join(recordErr, removeErr)
}

// InFlight reports whether a playbook is currently running.
func (m *Manager) InFlight(playbookName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, running := m.inFlight[playbookName]
	return running
}

func (m *Manager) release(playbookName string) {
	m.mu.Lock()
	delete(m.inFlight, playbookName)
	m.mu.Unlock()
}

// newID returns a run identifier that sorts by time and cannot collide.
//
// The timestamp is the readable half: an operator reading `gronin runs` should be able
// to tell which run is which without a lookup. The random half is what makes it an
// identifier — two runs starting in the same nanosecond is not a scenario worth a
// coordination protocol, but it is one worth six bytes.
func newID(at time.Time) (string, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generating a run identifier: %w", err)
	}
	return at.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix), nil
}
