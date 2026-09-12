package guard

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// Store is the record a refusal is written to. An interface rather than the store
// itself, so that what the guard needs of the record is one line long and visible.
type Store interface {
	RecordRefusal(ctx context.Context, refusal record.Refusal) error
}

// Guard decides whether a trigger becomes a run, and holds the claim while it does.
type Guard struct {
	// Coordinator holds the claims. Never nil: a deployment with no backend gets the
	// single-host file lock (FR-109).
	Coordinator Coordinator
	Store       Store
	Config      Config
	Clock       Clock
	// Host and Instance are who a claim names as its holder, beside the run.
	Host, Instance string
	// Exit ends this process when a run outlives the stop bound (R4). Nil is os.Exit;
	// only the test that exercises the enforcement replaces it.
	Exit func(code int)
	Log  *slog.Logger
}

// Request is one trigger asking to become a run.
type Request struct {
	RunID string
	Kind  record.TriggerKind
	// DueAt is the instant the scheduler fired for, and is set for a scheduled trigger
	// alone: the runtime computes it from the expression and never reads its own clock
	// for it (FR-129, R6).
	DueAt time.Time
}

// Admitted is a claim taken for a run that has not begun.
type Admitted struct {
	Claim Claim
	Reach string
	// sent is when the grant was sent, which is where the stop deadline is anchored (R1).
	sent Instant
}

// Refused is a trigger that did not become a run, terminally (FR-117). It carries the
// mechanism so that `gronin run` can name it without parsing a message.
type Refused struct {
	Mechanism record.Mechanism
	Detail    string
}

func (r *Refused) Error() string { return string(r.Mechanism) + ": " + r.Detail }

func (g *Guard) clock() Clock {
	if g.Clock == nil {
		return SystemClock()
	}
	return g.Clock
}

func (g *Guard) log() *slog.Logger {
	if g.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return g.Log
}

// Admit takes the claim a run needs before anything is created or gathered (FR-102).
//
// The decision is bounded (FR-108): exceeding the bound is a refusal naming the backend,
// never a pass.
func (g *Guard) Admit(
	ctx context.Context, book *playbook.Playbook, req Request,
) (*Admitted, error) {
	decide, cancel := bound(ctx, g.clock(), g.Config.DecisionBound)
	defer cancel()

	ask := AcquireRequest{
		Name:    book.Name,
		Holder:  Holder{Host: g.Host, Instance: g.Instance, RunID: req.RunID},
		Expiry:  g.Config.ClaimExpiry,
		Trigger: TriggerRef{Kind: kindOf(req.Kind), DueAt: dueOf(req)},
	}
	// The instant the grant was sent, which the backend's countdown starts no earlier
	// than, and where the stop deadline is anchored (R1).
	sent := g.clock().Monotonic()
	// FR-120: the claim is taken for every playbook. A guard block declares a bound on
	// top of non-concurrency, never an exemption from it.
	claim, err := g.Coordinator.Acquire(decide, ask)
	if err != nil {
		return nil, g.refuse(ctx, book.Name, req, err)
	}
	return &Admitted{Claim: claim, Reach: g.Coordinator.Reach(), sent: sent}, nil
}

// kindOf is what the coordinator is told. Only a scheduled trigger reads and advances a
// name's last tick; a replay and a resume are neither scheduled nor judged against it
// (FR-128).
func kindOf(kind record.TriggerKind) string {
	if kind == record.TriggerSchedule {
		return KindSchedule
	}
	return KindManual
}

// dueOf is the tick handed to the backend: the instant the schedule computed, for a
// scheduled trigger and nothing else (R6).
func dueOf(req Request) time.Time {
	if req.Kind != record.TriggerSchedule {
		return time.Time{}
	}
	return req.DueAt
}

// refuse records why the trigger did not become a run and returns it. The record is
// written under the caller's own context rather than the decision's, which may be the
// thing that just ran out.
func (g *Guard) refuse(ctx context.Context, name string, req Request, err error) error {
	refusal := record.Refusal{
		PlaybookName: name,
		TriggerKind:  req.Kind,
		DueAt:        dueOf(req),
		Mechanism:    mechanismOf(err),
		Detail:       err.Error(),
		RefusedAt:    g.clock().Wall(),
	}
	if writeErr := g.Store.RecordRefusal(ctx, refusal); writeErr != nil {
		// A refusal nobody can read afterwards is SC-108 broken, and the trigger is
		// refused either way: saying so is all that is left.
		g.log().Error("a refusal could not be recorded",
			"playbook", name, "mechanism", string(refusal.Mechanism), "err", writeErr)
	}
	return &Refused{Mechanism: refusal.Mechanism, Detail: refusal.Detail}
}

// mechanismOf is which mechanism refused the trigger (FR-117). Anything that is not a
// refusal the contract names is the backend failing to decide, which FR-107 requires be
// a refusal naming it rather than a quieter guarantee.
func mechanismOf(err error) record.Mechanism {
	switch {
	case errors.Is(err, ErrHeld):
		return record.MechanismClaimHeld
	case errors.Is(err, ErrTickRan):
		return record.MechanismTickAlreadyRan
	default:
		return record.MechanismBackendUnavailable
	}
}

// bound is a context ending d after now on the runtime's own monotonic reading. The
// standard library's own deadline reads the host's clock, which a test cannot move, and
// every bound here is asserted against a subject that exceeds it.
func bound(parent context.Context, clock Clock, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	timer := clock.NewTimer(d)
	go func() {
		select {
		case <-timer.C():
			cancel()
		case <-ctx.Done():
			timer.Stop()
		}
	}()
	return ctx, cancel
}
