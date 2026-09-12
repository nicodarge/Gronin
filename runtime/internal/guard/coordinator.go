package guard

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Coordinator holds claims on playbook names for one deployment.
type Coordinator interface {
	// Acquire takes the claim on req.Name, one of its rate slots when req.Rate is set, and
	// for a scheduled trigger the name's last tick, together or not at all. It returns
	// within ctx's deadline.
	Acquire(ctx context.Context, req AcquireRequest) (Claim, error)

	// Released returns once the claim on name has been observed free, or when ctx ends. It
	// may return early; the caller acquires again rather than trusting it.
	Released(ctx context.Context, name string) error

	// Reach is "cross-host" or "single-host", printed by `serve` at startup (FR-109).
	Reach() string
}

// AcquireRequest is what a claim is asked for.
type AcquireRequest struct {
	Name    string
	Holder  Holder        // host, process instance, run identifier: what a refusal names
	Expiry  time.Duration // whole seconds
	Rate    *RateLimit    // nil when the playbook declares none
	Trigger TriggerRef    // kind, and for a scheduled trigger the instant it was due
}

// Claim is one held claim on a name.
type Claim interface {
	Token() int64                    // the fencing token
	Renew(ctx context.Context) error // one attempt, bounded by ctx
	Fence(ctx context.Context) error // is this still the current claim on its name?
	Release(ctx context.Context) error
	// Expiry is the expiry the backend actually granted, which may be longer than the
	// one asked for and is never shorter (C12). Zero for a claim that does not expire.
	Expiry() time.Duration
}

// Holder is who holds a claim: what a refusal names.
type Holder struct{ Host, Instance, RunID string }

func (h Holder) String() string {
	return fmt.Sprintf("run %s on %s, process %s", h.RunID, h.Host, h.Instance)
}

// RateLimit is a playbook's declared limit of runs per window (FR-114).
type RateLimit struct {
	Runs int
	Per  time.Duration
}

// TriggerRef says how the run that would hold the claim was triggered.
type TriggerRef struct {
	Kind  string    // "schedule" or "manual"
	DueAt time.Time // the instant the schedule computed; zero for any other kind
}

// The trigger kinds a claim is taken for. Only a scheduled one reads and advances the
// name's last tick.
const (
	KindSchedule = "schedule"
	KindManual   = "manual"
)

// Reaches a Coordinator states.
const (
	ReachCrossHost  = "cross-host"
	ReachSingleHost = "single-host"
)

// The refusals and failures a Coordinator reports.
var (
	ErrHeld        = errors.New("claim held")         // wrapped with the holder
	ErrRateLimited = errors.New("rate limit reached") // wrapped with the limit
	ErrUnavailable = errors.New("backend unavailable")
	ErrLost        = errors.New("claim lost")
	ErrTickRan     = errors.New("tick already ran") // wrapped with the recorded tick and its holder
)

// HeldBy is ErrHeld naming the holder.
func HeldBy(holder Holder) error {
	return fmt.Errorf("%w by %s", ErrHeld, holder)
}

// TickRanAs is ErrTickRan naming the recorded tick and the holder that took it.
func TickRanAs(tick time.Time, holder Holder) error {
	return fmt.Errorf("%w: tick %s ran as %s", ErrTickRan, tick.UTC().Format(time.RFC3339), holder)
}

// Seam is called by a coordinator between reading a name's last tick and sending the
// transaction that would take the claim. Nil outside the contract suite, which sets one to
// choose an interleaving that two calls in sequence cannot produce (C13).
type Seam func(ctx context.Context, name string)

// Check refuses a request no coordinator can act on. A scheduled trigger without its due
// instant is refused rather than let through: judged as the zero time it would never be
// later than a recorded tick, and skipped it would run one tick twice without a word.
func (r AcquireRequest) Check() error {
	if r.Name == "" {
		return errors.New("a claim was asked for with no name")
	}
	if r.Trigger.Kind == KindSchedule && r.Trigger.DueAt.IsZero() {
		return fmt.Errorf("a scheduled trigger of %s carries no due instant", r.Name)
	}
	return nil
}
