package guard

import (
	"context"
	"errors"
	"fmt"
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
	// Held is what holds the claim it waits for.
	Held string
	UpTo time.Duration
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
		w.req.OnWait(Waiting{TriggerID: w.row.ID, Held: held.Error(), UpTo: w.expiry})
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

// try is one round of the wait: until the claim frees or the wait expires, then the file
// read again and the claim asked for again.
func (g *Guard) try(ctx context.Context, w *waitingTrigger) (*Admitted, error) {
	left := g.remaining(w)
	if left <= 0 {
		return nil, &waitEnded{record.MechanismWaitExpired,
			fmt.Sprintf("waited the %s %s allows, and its claim was still held", w.expiry, w.book.Name)}
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

	edited, err := g.Slot.Reload(w.book.Path)
	if err != nil {
		return nil, &waitEnded{record.MechanismPlaybookChanged,
			fmt.Sprintf("%s cannot be loaded now: %v", w.book.Path, err)}
	}
	// The name is the claim's identity. A trigger that followed a rename would run under a
	// claim and a window it never collided with (research.md §4).
	if edited.Name != w.book.Name {
		return nil, &waitEnded{record.MechanismPlaybookChanged,
			fmt.Sprintf("%s now declares %q, not %q", w.book.Path, edited.Name, w.book.Name)}
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
	claim, err := g.Coordinator.Acquire(decide, g.ask(edited.Name, req))
	if errors.Is(err, ErrHeld) {
		return nil, errStillHeld
	}
	if err != nil {
		return nil, err
	}
	return &Admitted{
		Claim: claim, Reach: g.Coordinator.Reach(), RunID: req.RunID, Book: edited,
		WaitingTriggerID: w.row.ID, Waited: g.clock().Monotonic().Sub(w.accepted), sent: sent,
	}, nil
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

// Started ends the wait of a trigger that became the run runID. It writes no refusal: the
// trigger was deferred, not refused (FR-117). A run that did not wait has nothing to end.
func (g *Guard) Started(ctx context.Context, admitted *Admitted, runID string) error {
	if admitted == nil || admitted.WaitingTriggerID == "" {
		return nil
	}
	_, err := g.Store.EndWait(ctx, admitted.WaitingTriggerID, record.WaitEnd{
		Outcome: record.WaitRan, At: g.clock().Wall(), RunID: runID,
	})
	return err
}
