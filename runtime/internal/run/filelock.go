package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// FileLock is the guard's single-host Coordinator, over the advisory file lock the
// runtime core already ships (FR-109). A deployment that configures no coordination
// backend gets this, and `serve` says so at startup: two hosts running these playbooks
// would each run them.
//
// It lives in this package rather than in guard because the lock does, and because run
// already imports guard to fence before its side effects; the reverse import would be a
// cycle.
type FileLock struct {
	manager *Manager
	clock   guard.Clock
}

var _ guard.Coordinator = (*FileLock)(nil)

// FileLock returns the single-host coordinator over this manager's locks.
func (m *Manager) FileLock() *FileLock {
	return &FileLock{manager: m, clock: guard.SystemClock()}
}

// Reach implements guard.Coordinator.
func (l *FileLock) Reach() string { return guard.ReachSingleHost }

// lockHolder is what the lock file carries: read by whoever is refused, so that a
// refusal on this host names the run holding the playbook rather than only the fact that
// something does.
type lockHolder struct {
	Host     string    `json:"host"`
	Instance string    `json:"instance"`
	RunID    string    `json:"run"`
	Taken    time.Time `json:"taken"`
}

// Acquire implements guard.Coordinator. The claim, and for a scheduled trigger the
// playbook's last tick, are taken together: the tick is read and written while the lock
// is held, which is what makes the two one step here (C13).
func (l *FileLock) Acquire(ctx context.Context, req guard.AcquireRequest) (guard.Claim, error) {
	if err := req.Check(); err != nil {
		return nil, err
	}
	taken, err := l.manager.claim(req.Name)
	if err != nil {
		if errors.Is(err, ErrAlreadyRunning) {
			return nil, l.held(req.Name, err)
		}
		return nil, fmt.Errorf("%w: %w", guard.ErrUnavailable, err)
	}
	l.record(taken, req.Holder)

	if req.Trigger.Kind == guard.KindSchedule {
		if err := l.tick(ctx, req); err != nil {
			l.manager.unclaim(taken)
			return nil, err
		}
	}
	return &fileClaim{lock: l, held: taken}, nil
}

// held names the holder from the lock file when it can be read. The lock excludes;
// carrying the holder is what lets a refusal name the run that has the playbook, here
// and in the process that was refused.
func (l *FileLock) held(name string, refusal error) error {
	data, err := os.ReadFile(l.manager.lockPath(name)) //nolint:gosec // the path claim() checked
	if err != nil {
		return refusal
	}
	var holder lockHolder
	if err := json.Unmarshal(data, &holder); err != nil || holder.RunID == "" {
		return refusal
	}
	return fmt.Errorf("%w: %s", ErrAlreadyRunning, guard.Holder{
		Host: holder.Host, Instance: holder.Instance, RunID: holder.RunID,
	})
}

// record writes the holder into the lock file. A failure to write it costs a refusal its
// detail, never the claim: the lock is what excludes, not its contents. It runs once the
// lock is held, so a contender refused in between reads the previous holder — a stale name
// in a message, never a stale decision.
func (l *FileLock) record(taken *held, holder guard.Holder) {
	data, err := json.Marshal(lockHolder{
		Host: holder.Host, Instance: holder.Instance, RunID: holder.RunID, Taken: l.clock.Wall(),
	})
	if err != nil {
		return
	}
	if err := taken.lockFile.Truncate(0); err != nil {
		return
	}
	_, _ = taken.lockFile.WriteAt(data, 0)
}

// tick is FR-128 on one host: the playbook's last tick, in the record store, read and
// written while the lock is held.
func (l *FileLock) tick(ctx context.Context, req guard.AcquireRequest) error {
	last, recorded, err := l.manager.store.LastTick(ctx, req.Name)
	if err != nil {
		return fmt.Errorf("%w: %w", guard.ErrUnavailable, err)
	}
	if recorded && !req.Trigger.DueAt.After(last.DueAt) {
		return guard.TickRanAs(last.DueAt, guard.Holder{
			Host: last.Host, Instance: last.Instance, RunID: last.RunID,
		})
	}
	if err := l.manager.store.SetLastTick(ctx, req.Name, record.Tick{
		DueAt: req.Trigger.DueAt, Host: req.Holder.Host,
		Instance: req.Holder.Instance, RunID: req.Holder.RunID,
	}); err != nil {
		return fmt.Errorf("%w: %w", guard.ErrUnavailable, err)
	}
	return nil
}

// releasedPoll is how often Released looks. There is nothing to watch here: the kernel
// releases a flock without telling anyone, including when the holding process dies.
const releasedPoll = 25 * time.Millisecond

// Released implements guard.Coordinator: it returns once the lock can be taken.
func (l *FileLock) Released(ctx context.Context, name string) error {
	for {
		if l.free(name) {
			return nil
		}
		select {
		case <-time.After(releasedPoll):
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", guard.ErrUnavailable, ctx.Err())
		}
	}
}

func (l *FileLock) free(name string) bool { return !l.manager.InFlight(name) }

// fileClaim is one held file lock. Renew and Fence are no-ops: a flock cannot be lost
// while its holder lives, and the kernel releases it when the holder dies.
type fileClaim struct {
	lock     *FileLock
	held     *held
	released sync.Once
}

// Token is zero: this deployment has no fencing token, and a run records none.
func (c *fileClaim) Token() int64 { return 0 }

// Expiry is zero: the claim does not expire, it ends with the process that holds it.
func (c *fileClaim) Expiry() time.Duration { return 0 }

func (c *fileClaim) Renew(context.Context) error { return nil }

func (c *fileClaim) Fence(context.Context) error { return nil }

// Release gives back the lock, once however often it is called:
// a second release must not free a claim someone else has taken since.
func (c *fileClaim) Release(context.Context) error {
	c.released.Do(func() { c.lock.manager.unclaim(c.held) })
	return nil
}
