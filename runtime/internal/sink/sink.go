// Package sink defines what a sink is, the cap contract every creating sink must
// honour, and the per-sink outcome recorded for a run.
//
// A sink is the only code path allowed to touch anything outside a run's working
// directory. That is Principle II's runtime half: the agent produces a report and the
// runtime acts on it, so a run that goes wrong produces a bad report rather than a bad
// action — and a bad report is recoverable.
package sink

import (
	"context"
	"errors"
	"fmt"
)

// Status is what one sink did for one run.
type Status string

const (
	// StatusDelivered is a messaging sink that sent its message.
	StatusDelivered Status = "delivered"
	// StatusCreated is a creating sink that created something.
	StatusCreated Status = "created"
	// StatusSkipped is a sink that had nothing to do.
	StatusSkipped Status = "skipped"
	// StatusCapped is a creating sink that stopped at its ceiling. Not a failure: the
	// cap is the point, and a run that hits it is working as declared.
	StatusCapped Status = "capped"
	// StatusFailed is a sink that could not do its work.
	StatusFailed Status = "failed"
)

// Delivery is what a sink is given. It carries the report the agent produced, or the
// reason the runtime refused it — never both, and never the malformed content.
type Delivery struct {
	PlaybookName string
	RunID        string

	// Report is the agent's answer, already validated against the declared schema.
	// Empty when Failure is set.
	Report []byte

	// Failure, when set, is why there is no report. A sink sends this instead: posting
	// content the runtime has just decided it cannot read is how a bad run becomes a
	// bad action.
	Failure error
}

// Outcome is what one sink did, recorded per sink so one failing does not make the
// run's other deliveries unknowable.
type Outcome struct {
	Sink         string
	Status       Status
	ItemsCreated int
	ItemsSkipped int
	Detail       string
}

// Sink is one destination.
type Sink interface {
	// Name is how this sink appears in the record.
	Name() string
	// Creates reports whether this sink brings things into existence somewhere else. A
	// creating sink must declare a cap, and the load gate refuses one that does not.
	Creates() bool
	// Cap is the ceiling a creating sink declared, and whether it declared one.
	Cap() (int, bool)
	// Deliver does the work. An error is the sink's own failure; it must not discard
	// the report, and the caller records it per sink.
	Deliver(ctx context.Context, delivery Delivery) (Outcome, error)
}

// ErrNoCap is what the load gate reports for a creating sink that declared no ceiling.
// An uncapped creator gets muted within a month, and the useful signal is lost with the
// noise.
var ErrNoCap = errors.New("a sink that creates things must declare a cap")

// CheckCap applies FR-006 to a built sink.
//
// It is a function rather than a line inside Build so it can be probed against a sink
// that creates things, which this deployment does not yet ship — a check no test can
// reach is a comment, and the comment claiming Build enforced this was exactly that
// until a review looked for the code behind it.
func CheckCap(one Sink) error {
	if !one.Creates() {
		return nil
	}
	ceiling, declared := one.Cap()
	if !declared {
		return fmt.Errorf("%w", ErrNoCap)
	}
	if ceiling <= 0 {
		return fmt.Errorf("%w: %d creates nothing, which is not a ceiling", ErrNoCap, ceiling)
	}
	return nil
}

// ErrUnknownType is what the load gate reports for a sink type this deployment does not
// implement (FR-038). Accepting it and doing nothing would leave a playbook that looks
// delivered and is not.
var ErrUnknownType = errors.New("this deployment implements no sink of that type")

// DeliverAll runs every sink and returns what each one did.
//
// It does not stop at the first failure and it does not return early: FR-025 requires a
// sink failure to be recorded per sink and not to discard the report, and a second sink
// has no reason to be punished for the first one's outage.
func DeliverAll(ctx context.Context, sinks []Sink, delivery Delivery) []Outcome {
	outcomes := make([]Outcome, 0, len(sinks))
	for _, one := range sinks {
		outcome, err := one.Deliver(ctx, delivery)
		outcome.Sink = one.Name()
		if err != nil {
			outcome.Status = StatusFailed
			if outcome.Detail == "" {
				outcome.Detail = err.Error()
			} else {
				outcome.Detail = fmt.Sprintf("%s: %s", outcome.Detail, err)
			}
		}
		if outcome.Status == "" {
			outcome.Status = StatusSkipped
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}
