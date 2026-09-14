package ingress

import (
	"context"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// Dispatcher turns one bound playbook's checked values into a decision for one delivery.
// cmd/gronin's dispatcher (T054) is the only implementation that reaches the guard and
// the executor — the one package the plan allows to know both — so this package
// declares only the shape, never importing either.
type Dispatcher interface {
	// Dispatch hands book its checked values for delivery. It returns the hand-off's
	// decided state — HandOffHandedOff once a run has started, or HandOffRefused for a
	// guard refusal other than dropped — or HandOffWaiting when the guard accepted the
	// trigger into its waiting slot, which is not yet a decision
	// (specs/004-webhook/data-model.md, *Hand-off*).
	Dispatch(
		ctx context.Context, book *playbook.Playbook, delivery record.Delivery,
		values map[string]string,
	) record.HandOffState
}

// reasonOf translates why Check refused a declared value into the reason a delivery
// refusal names (data-model.md, *Delivery refusal*).
func reasonOf(kind playbook.ValueRefusalKind) record.DeliveryRefusalReason {
	switch kind {
	case playbook.ValueAbsent:
		return record.ReasonValueAbsent
	case playbook.ValueNotSingle:
		return record.ReasonValueNotSingle
	case playbook.ValueTooLong:
		return record.ReasonValueTooLong
	case playbook.ValueLeadingDash:
		return record.ReasonValueLeadingDash
	default:
		return record.ReasonValueNoMatch
	}
}

// HandOff decides one delivery against every loaded playbook bound to its source
// (FR-321): nothing in the body, the headers or the query selects among them — only the
// source the acceptance already recorded. Each bound playbook's declared values are
// extracted and checked independently (FR-322, FR-326), so one playbook's refusal never
// touches another's hand-off.
func HandOff(
	ctx context.Context, store *record.Store, dispatcher Dispatcher, loaded playbook.Loaded,
	delivery record.Delivery, now func() time.Time,
) error {
	body, err := store.Blobs().Get(delivery.BodyRef)
	if err != nil {
		return err
	}

	var bound bool
	for _, book := range loaded.Playbooks {
		if book.Trigger.Type != "webhook" || book.Trigger.Source != delivery.Source {
			continue
		}
		bound = true

		values := playbook.Extract(book.Trigger, body)
		checked, refusals := playbook.Check(book, values)
		if len(refusals) > 0 {
			// One row: the first declared value Check found wrong, in the deterministic
			// order Check itself applies. A delivery with several bad values is still
			// refused once per playbook — an operator reading `gronin deliveries show`
			// fixes the first and the rest surface on the retry.
			first := refusals[0]
			if _, err := store.AddDeliveryRefusal(ctx, record.DeliveryRefusal{
				Source: delivery.Source, Reason: reasonOf(first.Kind),
				ValueName: first.Name, PlaybookName: book.Name,
				DeliveryID: delivery.ID, ReceivedAt: now(), Peer: delivery.Peer,
			}, nil); err != nil {
				return err
			}
			if err := store.SetHandOffState(ctx, delivery.ID, book.Name, record.HandOffRefused, now()); err != nil {
				return err
			}
			continue
		}

		state := dispatcher.Dispatch(ctx, book, delivery, checked)
		decidedAt := now()
		if state == record.HandOffWaiting {
			// Not yet a decision (data-model.md, *Hand-off*): a waiting trigger does
			// not survive its own process, so it is recorded with no decided time.
			decidedAt = time.Time{}
		}
		if err := store.SetHandOffState(ctx, delivery.ID, book.Name, state, decidedAt); err != nil {
			return err
		}
	}

	return finishDelivery(ctx, store, delivery.ID, bound)
}

// finishDelivery writes a delivery's own state from its hand-offs (data-model.md,
// *Hand-off*): unbound when nothing was bound to its source; otherwise dropped once any
// hand-off is, waiting while any is undecided, and handed off once every one is decided.
func finishDelivery(ctx context.Context, store *record.Store, deliveryID string, bound bool) error {
	if !bound {
		return store.SetDeliveryState(ctx, deliveryID, record.DeliveryUnbound)
	}
	handoffs, err := store.HandOffsOf(ctx, deliveryID)
	if err != nil {
		return err
	}

	state := record.DeliveryHandedOff
	for _, handoff := range handoffs {
		switch handoff.State {
		case record.HandOffDropped:
			state = record.DeliveryDropped
		case record.HandOffHandedOff, record.HandOffRefused:
			// Already decided; does not change a dropped or waiting reading a state
			// seen earlier in this loop already set.
		default:
			// pending or waiting: undecided. Dropped, once seen, is not downgraded by
			// a later undecided hand-off.
			if state != record.DeliveryDropped {
				state = record.DeliveryWaiting
			}
		}
	}
	return store.SetDeliveryState(ctx, deliveryID, state)
}
