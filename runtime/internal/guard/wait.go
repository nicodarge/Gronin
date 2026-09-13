package guard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// WaitSlot is what a process needs to let a trigger wait for the run it collided with.
type WaitSlot struct {
	// Instance is this process's liveness lock. The waiting row names it, and a row whose
	// instance nobody holds is read as dropped (FR-113).
	Instance *Instance
	// Reload reads a playbook again from the file a trigger was accepted from, through the
	// load gate (FR-121).
	Reload func(path string) (*playbook.Playbook, error)
	// RunID mints the identifier of the run a waiting trigger becomes, once it becomes one.
	// Nil keeps the one minted on arrival.
	RunID func() (string, error)
}

// Waiting is what a trigger is told as it begins to wait (contracts/cli.md).
type Waiting struct {
	TriggerID string
	// Holder is who holds the claim it waits for, when the coordinator named one; Held is
	// the refusal it met, for when it did not.
	Holder *Holder
	Held   string
	UpTo   time.Duration
}

// waits reports whether a refused trigger waits instead: one that will not come again,
// refused because its playbook is running (FR-110). A scheduled tick's next occurrence
// comes anyway, and on two hosts a tick waiting behind the same tick's run elsewhere would
// run it twice.
func waits(kind record.TriggerKind, err error) bool {
	return kind == record.TriggerManual && errors.Is(err, ErrHeld)
}

// waitingTrigger is one wait in progress, in the memory of the process that accepted it.
type waitingTrigger struct {
	book     *playbook.Playbook
	req      Request
	expiry   time.Duration
	accepted Instant
	row      record.WaitingTrigger
	// edited is the playbook as it was last read, and stamp its directory as it stood then.
	edited *playbook.Playbook
	stamp  directoryStamp
}

// errStillHeld is a wait that has not ended: the claim is still held, or was taken first by
// another trigger when it freed.
var errStillHeld = errors.New("the claim is still held")

// waitEnded is a wait ended by something other than the backend: its expiry, or its file.
type waitEnded struct {
	mechanism record.Mechanism
	detail    string
}

func (e *waitEnded) Error() string { return string(e.mechanism) + ": " + e.detail }

// wait accepts a trigger into the slot, durably, then waits for the claim it was refused.
func (g *Guard) wait(
	ctx context.Context, book *playbook.Playbook, req Request, held error,
) (*Admitted, error) {
	expiry, err := book.WaitFor()
	if err != nil {
		return nil, err
	}
	w := &waitingTrigger{book: book, req: req, expiry: expiry}
	if err := g.accept(ctx, w); err != nil {
		return nil, err
	}
	return g.await(ctx, w, held)
}

// accept records the trigger as waiting before the wait begins (FR-127): the wait itself
// dies with this process, and a record written only on the way out is written by a process
// that may not get the chance. A process whose row still holds the slot after it died is
// reconciled first, or a kill would keep the slot full for good.
func (g *Guard) accept(ctx context.Context, w *waitingTrigger) error {
	if _, err := Reconcile(ctx, g.Slot.Instance.stateDir, g.Store, g.clock()); err != nil {
		g.log().Warn("waiting triggers whose process is gone could not be marked dropped", "err", err)
	}
	w.accepted = g.clock().Monotonic()
	acceptedAt := g.clock().Wall()
	row, err := g.Store.AcceptWaiting(ctx, record.WaitingTrigger{
		PlaybookName: w.book.Name, PlaybookPath: w.book.Path, TriggerKind: w.req.Kind,
		AcceptedAt: acceptedAt, ExpiresAt: acceptedAt.Add(w.expiry), Instance: g.Slot.Instance.ID(),
	}, w.req.Values)
	if errors.Is(err, record.ErrWaitingSlotFull) {
		detail := "a trigger is already waiting"
		if !row.AcceptedAt.IsZero() {
			detail = fmt.Sprintf("a trigger accepted at %s is already waiting",
				row.AcceptedAt.UTC().Format(time.RFC3339))
		}
		return g.recordRefusal(ctx, record.Refusal{
			PlaybookName: w.book.Name, TriggerKind: w.req.Kind,
			Mechanism: record.MechanismWaitingSlotFull, Detail: detail, RefusedAt: g.clock().Wall(),
		})
	}
	if err != nil {
		return fmt.Errorf("accepting a trigger of %s to wait: %w", w.book.Name, err)
	}
	w.row = row
	return nil
}

// await waits until the trigger runs or its wait ends, and records how it ended.
func (g *Guard) await(ctx context.Context, w *waitingTrigger, held error) (*Admitted, error) {
	if w.req.OnWait != nil {
		waiting := Waiting{TriggerID: w.row.ID, Held: held.Error(), UpTo: w.expiry}
		var named *HeldError
		if errors.As(held, &named) {
			waiting.Holder = &named.Holder
		}
		w.req.OnWait(waiting)
	}
	for {
		admitted, err := g.try(ctx, w)
		switch {
		case err == nil:
			return admitted, nil
		case errors.Is(err, errStillHeld):
			continue
		case ctx.Err() != nil:
			// This process is stopping. The row stays waiting, and is read as dropped once
			// the instance lock is gone (FR-113).
			return nil, ctx.Err()
		default:
			return nil, g.endWait(ctx, w, err)
		}
	}
}

// try is one round of the wait: until the claim may have freed or the wait expires, then
// the file read again if it changed, and only then the claim asked for.
func (g *Guard) try(ctx context.Context, w *waitingTrigger) (*Admitted, error) {
	left := g.remaining(w)
	if left <= 0 {
		return nil, &waitEnded{record.MechanismWaitExpired,
			fmt.Sprintf("waited the %s %s allows, and its claim was still held",
				HumanDuration(w.expiry), w.book.Name)}
	}
	watch, cancel := bound(ctx, g.clock(), left)
	err := g.Coordinator.Released(watch, w.book.Name)
	watched := watch.Err() != nil
	cancel()
	switch {
	case g.remaining(w) <= 0, err != nil && watched && ctx.Err() == nil:
		// The expiry ended the watch, or passed as it returned. No run starts after it has
		// (FR-112), and the next round says so.
		return nil, errStillHeld
	case err != nil:
		return nil, err
	}

	req := w.req
	if g.Slot.RunID != nil {
		if req.RunID, err = g.Slot.RunID(); err != nil {
			return nil, err
		}
	}
	decide, cancelDecide := bound(ctx, g.clock(), g.Config.DecisionBound)
	defer cancelDecide()
	sent := g.clock().Monotonic()
	claim, edited, err := g.readThenClaim(decide, w, req)
	if errors.Is(err, ErrHeld) {
		return nil, errStillHeld
	}
	if err != nil {
		return nil, err
	}
	waited := g.clock().Monotonic().Sub(w.accepted)
	// The one window left between the read and the claim is the Acquire call itself. A file
	// that changed inside it gives the claim back, and the next round reads it.
	if w.changed() {
		g.giveBack(ctx, claim)
		return nil, errStillHeld
	}
	return &Admitted{
		Claim: claim, Reach: g.Coordinator.Reach(), RunID: req.RunID, Book: edited,
		WaitingTriggerID: w.row.ID, Waited: waited, sent: sent,
	}, nil
}

// readThenClaim reads the trigger's playbook again and only then asks for the claim: a read
// that fails ends the wait having taken nothing, so no other trigger — a scheduled tick,
// which does not wait — is refused for a run that never starts.
func (g *Guard) readThenClaim(
	decide context.Context, w *waitingTrigger, req Request,
) (Claim, *playbook.Playbook, error) {
	edited, err := g.reread(w)
	if err != nil {
		return nil, nil, err
	}
	claim, err := g.Coordinator.Acquire(decide, g.ask(w.book.Name, req))
	if err != nil {
		return nil, nil, err
	}
	return claim, edited, nil
}

// reread returns the playbook as its file declares it now, through the load gate (FR-121).
// It reads only when the file or a sibling has changed since the last read: a file lock's
// waiter comes round every poll, and the gate parses every playbook in the directory.
func (g *Guard) reread(w *waitingTrigger) (*playbook.Playbook, error) {
	stamp, err := stampOf(w.book.Path)
	if err != nil {
		return nil, &waitEnded{record.MechanismPlaybookChanged,
			fmt.Sprintf("%s cannot be loaded now: %s", w.book.Path, oneLine(err))}
	}
	if w.edited != nil && stamp.equal(w.stamp) {
		return w.edited, nil
	}
	edited, err := g.Slot.Reload(w.book.Path)
	if err != nil {
		return nil, &waitEnded{record.MechanismPlaybookChanged,
			fmt.Sprintf("%s cannot be loaded now: %s", w.book.Path, oneLine(err))}
	}
	// The name is the claim's identity. A trigger that followed a rename would run under a
	// claim and a window it never collided with (research.md §4).
	if edited.Name != w.book.Name {
		return nil, &waitEnded{record.MechanismPlaybookChanged,
			fmt.Sprintf("%s now declares %q, not %q", w.book.Path, edited.Name, w.book.Name)}
	}
	w.edited, w.stamp = edited, stamp
	return edited, nil
}

// changed reports whether the playbook's file or a sibling has changed since the last read.
// A directory that cannot be read counts as changed.
func (w *waitingTrigger) changed() bool {
	stamp, err := stampOf(w.book.Path)
	return err != nil || !stamp.equal(w.stamp)
}

// giveBack releases a claim taken for a trigger that will not run on it, so that the
// playbook is not blocked until the claim expires.
func (g *Guard) giveBack(ctx context.Context, claim Claim) {
	release, cancel := bound(ctx, g.clock(), g.Config.DecisionBound)
	defer cancel()
	if err := claim.Release(release); err != nil {
		g.log().Warn("a claim taken for a trigger that did not run on it could not be released",
			"err", err)
	}
}

// oneLine is a refusal written on one line of `gronin refusals`: the gate reports one
// problem per line, indented under the file.
func oneLine(err error) string {
	var lines []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "; ")
}

// remaining is how much longer the trigger may wait, on the monotonic reading: a wall
// reading stepped backwards would lengthen the wait by exactly the step (FR-112, FR-118).
func (g *Guard) remaining(w *waitingTrigger) time.Duration {
	return w.expiry - g.clock().Monotonic().Sub(w.accepted)
}

// endWait records how a wait ended, in the one step that ends it, and returns the refusal.
func (g *Guard) endWait(ctx context.Context, w *waitingTrigger, err error) error {
	refusal := record.Refusal{
		PlaybookName: w.book.Name, TriggerKind: w.req.Kind, WaitingTriggerID: w.row.ID,
		Mechanism: mechanismOf(err), Detail: g.detail(err), RefusedAt: g.clock().Wall(),
	}
	var ended *waitEnded
	if errors.As(err, &ended) {
		refusal.Mechanism, refusal.Detail = ended.mechanism, ended.detail
	}
	if _, writeErr := g.Store.EndWait(ctx, w.row.ID, record.WaitEnd{
		Outcome: record.WaitOutcome(refusal.Mechanism), At: refusal.RefusedAt, Refusal: &refusal,
	}); writeErr != nil {
		g.log().Error("how a wait ended could not be recorded",
			"playbook", w.book.Name, "waiting_trigger", w.row.ID, "err", writeErr)
	}
	return &Refused{Mechanism: refusal.Mechanism, Detail: refusal.Detail}
}
