package guard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// InstancesDir is where, in the state directory, each process that can accept a waiting
// trigger holds its lock.
const InstancesDir = "instances"

// instancePattern is what may name a lock file. An instance is placed into a path, so the
// constraint is asserted rather than trusted.
var instancePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Instance is a process's proof that it is alive: an exclusive advisory lock on
// instances/<id>.lock, held for as long as the process lives. The kernel releases it
// however the process ends, SIGKILL included, which is what lets a waiting trigger be read
// as dropped with nothing written on the way out (FR-113).
type Instance struct {
	id       string
	stateDir string
	file     *os.File
	closed   sync.Once
}

// HoldInstance takes this process's instance lock in stateDir.
func HoldInstance(stateDir, id string) (*Instance, error) {
	if !instancePattern.MatchString(id) {
		return nil, fmt.Errorf("instance %q does not match %s", id, instancePattern)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, InstancesDir), 0o700); err != nil {
		return nil, fmt.Errorf("creating the instances directory: %w", err)
	}
	path := instancePath(stateDir, id)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // id is checked against instancePattern above
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return &Instance{id: id, stateDir: stateDir, file: file}, nil
}

// ID is the instance a waiting row names.
func (i *Instance) ID() string { return i.id }

// Close gives the lock back. The file goes first, so a reconciliation that opens it after
// this finds nothing to read as alive; one that opened it before finds it locked until the
// descriptor closes, and leaves the row for the next.
func (i *Instance) Close() error {
	var err error
	i.closed.Do(func() {
		err = os.Remove(instancePath(i.stateDir, i.id))
		_ = syscall.Flock(int(i.file.Fd()), syscall.LOCK_UN)
		err = errors.Join(err, i.file.Close())
	})
	return err
}

func instancePath(stateDir, id string) string {
	return filepath.Join(stateDir, InstancesDir, id+".lock")
}

// probed is called while alive holds a dead instance's lock; nil outside the test that acts
// at that instant.
var probed func(id string)

// alive reports whether the process that accepted a waiting trigger still holds its
// instance lock.
//
// Unlike the file lock's poll, this looks by taking the lock, and that cannot make a real
// contender fail: the only process that ever asks for an instance's lock is the one that
// takes it at start, before any row names that instance, and no identifier is used twice.
// Another reconciliation looking at the same instance at the same instant reads it as alive
// and leaves the row to this one.
//
// A lock file that is there and cannot be examined reads as alive: dropping a trigger that
// is still waiting loses it, while a row left waiting is looked at again by the next
// reconciliation.
func alive(stateDir, id string) bool {
	if !instancePattern.MatchString(id) {
		return false
	}
	file, err := os.OpenFile(instancePath(stateDir, id), os.O_RDWR, 0) //nolint:gosec // id is checked against instancePattern above
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer func() { _ = file.Close() }()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	if probed != nil {
		probed(id)
	}
	// Proven dead while the lock is held, and nothing takes an instance twice: a killed
	// process's lock file would otherwise stay for good.
	_ = os.Remove(instancePath(stateDir, id))
	return false
}

// Reconcile marks dropped every waiting trigger whose process is gone, and writes the
// refusal that says so (FR-113). It is what makes a drop readable after a kill: `serve` runs
// it at startup, `gronin refusals` before it reads, and a trigger about to wait before it
// takes the slot, which a dead process's row would otherwise hold for ever. A row a run
// already names ran, and is recorded as such rather than as refused (FR-117).
func Reconcile(ctx context.Context, stateDir string, store Store, clock Clock) (int, error) {
	if clock == nil {
		clock = SystemClock()
	}
	waiting, err := store.StillWaiting(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading the waiting triggers: %w", err)
	}
	dropped := 0
	for _, trigger := range waiting {
		if trigger.RunID != "" {
			if _, err := store.EndWait(ctx, trigger.ID, record.WaitEnd{
				Outcome: record.WaitRan, At: clock.Wall(), RunID: trigger.RunID,
			}); err != nil {
				return dropped, err
			}
			continue
		}
		if alive(stateDir, trigger.Instance) {
			continue
		}
		at := clock.Wall()
		ended, err := store.EndWait(ctx, trigger.ID, record.WaitEnd{
			Outcome: record.WaitDropped, At: at,
			Refusal: &record.Refusal{
				PlaybookName: trigger.PlaybookName, TriggerKind: trigger.TriggerKind,
				WaitingTriggerID: trigger.ID, Mechanism: record.MechanismDropped,
				Detail: fmt.Sprintf("accepted at %s; process %s ended before it ran",
					trigger.AcceptedAt.UTC().Format(TimeOfDay), trigger.Instance),
				RefusedAt: at,
			},
		})
		if err != nil {
			return dropped, err
		}
		if ended {
			dropped++
		}
	}
	return dropped, nil
}
