package ingress_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nicodarge/Gronin/runtime/internal/ingress"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// rawDB opens the record's database file beside the store, the way accept.go (T049,
// not built yet) will write a delivery and its hand-offs. Until then, a test seeds them
// directly — the way internal/record/webhook_test.go does for the same reason.
func rawDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "record.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// recordingDispatcher is ingress.Dispatcher for a test: it never reaches a guard or an
// executor, only records what it was asked to dispatch and returns HandOffHandedOff.
// Its own mutex is what makes it safe to read from the test goroutine while the
// handler's hand-off runs from one of its own (T050): a synchronous caller never
// notices it.
type recordingDispatcher struct {
	mu    sync.Mutex
	calls []dispatched
}

type dispatched struct {
	playbook string
	values   map[string]string
}

func (d *recordingDispatcher) Dispatch(
	_ context.Context, book *playbook.Playbook, _ record.Delivery, values map[string]string,
) record.HandOffState {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, dispatched{playbook: book.Name, values: values})
	return record.HandOffHandedOff
}

// Calls is a snapshot of every dispatch seen so far.
func (d *recordingDispatcher) Calls() []dispatched {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]dispatched(nil), d.calls...)
}

// seedDelivery writes a delivery's body through the blob store, and the delivery and its
// hand-offs directly — what T049's Accept will do in one transaction, once it exists.
func seedDelivery(
	t *testing.T, store *record.Store, dir, id, source string, body []byte, boundPlaybooks []string,
) record.Delivery {
	t.Helper()
	ctx := t.Context()

	bodyRef, err := store.Blobs().Put(id, "body", body)
	if err != nil {
		t.Fatal(err)
	}
	received := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	if _, err := rawDB(t, dir).ExecContext(ctx, `
		INSERT INTO deliveries (id, source, identity, identity_kind, received_at, peer,
		                        body_ref, body_sha256, repeats, instance, state)
		VALUES (?, ?, ?, 'digest', ?, '192.0.2.10', ?, 'sha', 0, 'instance-a', 'accepted')`,
		id, source, "digest-"+id, received.Format(time.RFC3339Nano), bodyRef); err != nil {
		t.Fatal(err)
	}
	for _, name := range boundPlaybooks {
		if _, err := rawDB(t, dir).ExecContext(ctx, `
			INSERT INTO handoffs (delivery_id, playbook_name, state) VALUES (?, ?, 'pending')`,
			id, name); err != nil {
			t.Fatal(err)
		}
	}

	delivery, err := store.GetDelivery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func webhookBook(name, source string, values map[string]playbook.TriggerValue) *playbook.Playbook {
	return &playbook.Playbook{
		Name:    name,
		Trigger: playbook.Trigger{Type: "webhook", Source: source, Values: values},
	}
}

// T020, SC-317 and SC-320. A delivery bound to two playbooks on its source: one whose
// declared value the body fails, one whose declared value it satisfies, and a third
// loaded playbook bound to a different source. Naming the third in the body's own
// "playbook" field changes nothing (FR-321) — nothing in the body selects among them.
func TestAHandOffChecksEveryBoundPlaybookOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	refuses := webhookBook("alert-page", "alerts", map[string]playbook.TriggerValue{
		"alertname": {At: "/alertname", Pattern: "[a-z ]+", MaxLength: 40},
	})
	accepts := webhookBook("alert-triage", "alerts", map[string]playbook.TriggerValue{
		"sev": {At: "/sev", Pattern: "[a-z]+", MaxLength: 20},
	})
	unbound := webhookBook("other-playbook", "other", map[string]playbook.TriggerValue{
		"x": {At: "/x", Pattern: ".+", MaxLength: 10},
	})
	loaded := playbook.Loaded{Playbooks: []*playbook.Playbook{refuses, accepts, unbound}}

	body := []byte(`{"alertname":"DISK FULL!!","sev":"critical","playbook":"other-playbook"}`)
	delivery := seedDelivery(t, store, dir, "delivery-1", "alerts", body,
		[]string{"alert-page", "alert-triage"})

	dispatcher := &recordingDispatcher{}
	if err := ingress.HandOff(t.Context(), store, dispatcher, loaded, delivery,
		func() time.Time { return time.Date(2026, 9, 12, 8, 0, 5, 0, time.UTC) }); err != nil {
		t.Fatal(err)
	}

	// The refused playbook dispatches nothing and leaves a refusal naming it and the
	// value.
	calls := dispatcher.Calls()
	if len(calls) != 1 {
		t.Fatalf("dispatched %d times, want 1: %+v", len(calls), calls)
	}
	if calls[0].playbook != "alert-triage" {
		t.Fatalf("dispatched %q, want alert-triage", calls[0].playbook)
	}
	if len(calls[0].values) != 1 || calls[0].values["sev"] != "critical" {
		t.Fatalf("values = %+v, want exactly {sev: critical}", calls[0].values)
	}

	refusals, err := store.ListDeliveryRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 {
		t.Fatalf("refusals = %+v, err = %v", refusals, err)
	}
	if refusals[0].PlaybookName != "alert-page" || refusals[0].ValueName != "alertname" ||
		refusals[0].DeliveryID != "delivery-1" {
		t.Fatalf("refusal = %+v", refusals[0])
	}

	handoffs, err := store.HandOffsOf(t.Context(), "delivery-1")
	if err != nil || len(handoffs) != 2 {
		t.Fatalf("hand-offs = %+v, err = %v", handoffs, err)
	}
	states := map[string]record.HandOffState{}
	for _, handoff := range handoffs {
		states[handoff.PlaybookName] = handoff.State
	}
	if states["alert-page"] != record.HandOffRefused {
		t.Fatalf("alert-page's hand-off = %s, want refused", states["alert-page"])
	}
	if states["alert-triage"] != record.HandOffHandedOff {
		t.Fatalf("alert-triage's hand-off = %s, want handed_off", states["alert-triage"])
	}

	// Every hand-off is decided, so the delivery itself reads handed off.
	again, err := store.GetDelivery(t.Context(), "delivery-1")
	if err != nil || again.State != record.DeliveryHandedOff {
		t.Fatalf("delivery = %+v, err = %v", again, err)
	}
}

// A delivery whose source no loaded playbook is bound to hands off to nothing, and the
// record says so rather than the delivery vanishing (spec.md, *Edge Cases*).
func TestAnUnboundDeliveryIsRecordedUnbound(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	loaded := playbook.Loaded{Playbooks: []*playbook.Playbook{
		webhookBook("alert-triage", "alerts", nil),
	}}
	delivery := seedDelivery(t, store, dir, "delivery-2", "quiet", []byte(`{}`), nil)

	dispatcher := &recordingDispatcher{}
	if err := ingress.HandOff(t.Context(), store, dispatcher, loaded, delivery, time.Now); err != nil {
		t.Fatal(err)
	}
	if calls := dispatcher.Calls(); len(calls) != 0 {
		t.Fatalf("dispatched %v for a delivery bound to nothing", calls)
	}
	again, err := store.GetDelivery(t.Context(), "delivery-2")
	if err != nil || again.State != record.DeliveryUnbound {
		t.Fatalf("delivery = %+v, err = %v", again, err)
	}
}
