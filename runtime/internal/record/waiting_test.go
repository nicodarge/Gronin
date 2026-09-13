package record_test

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// waitingAt is a manual trigger of drift-check accepted at the instant given, by the
// instance given.
func waitingAt(accepted time.Time, instance string) record.WaitingTrigger {
	return record.WaitingTrigger{
		PlaybookName: "drift-check", PlaybookPath: "/srv/gronin/playbooks/book.yaml",
		TriggerKind: record.TriggerManual, AcceptedAt: accepted,
		ExpiresAt: accepted.Add(30 * time.Minute), Instance: instance,
	}
}

// FR-111 and FR-127. Each manual invocation is a process of its own, so the slot is held by
// the database both of them open and not by whichever process checks first: two stores on
// one directory race to accept, and exactly one does.
func TestTwoStoresCannotBothAcceptAWaitingTrigger(t *testing.T) {
	dir := t.TempDir()
	stores := []*record.Store{openStoreAt(t, dir), openStoreAt(t, dir)}
	accepted := time.Date(2026, 9, 10, 6, 1, 2, 0, time.UTC)

	var (
		wg      sync.WaitGroup
		results = make([]error, len(stores))
	)
	for at, store := range stores {
		wg.Go(func() {
			_, results[at] = store.AcceptWaiting(t.Context(),
				waitingAt(accepted, []string{"instance-a", "instance-b"}[at]), nil)
		})
	}
	wg.Wait()

	won, full := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, record.ErrWaitingSlotFull):
			full++
		default:
			t.Fatalf("accepting failed for a reason that is not the slot: %v", err)
		}
	}
	if won != 1 || full != 1 {
		t.Fatalf("%d accepted and %d refused as full, want one of each", won, full)
	}

	// Once the one waiting has ended, the slot is free again.
	waiting, err := stores[0].StillWaiting(t.Context())
	if err != nil || len(waiting) != 1 {
		t.Fatalf("still waiting = %+v, err = %v", waiting, err)
	}
	if _, err := stores[1].EndWait(t.Context(), waiting[0].ID, record.WaitEnd{
		Outcome: record.WaitExpired, At: accepted.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].AcceptWaiting(t.Context(), waitingAt(accepted.Add(time.Hour), "instance-c"), nil); err != nil {
		t.Fatalf("the slot was not freed by the end of the wait that held it: %v", err)
	}
}

// FR-127: acceptance is on disk when AcceptWaiting returns, readable by another process
// before anything else happens — which is what a kill straight afterwards leaves behind.
func TestAcceptingWritesTheRowBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	accepting, reading := openStoreAt(t, dir), openStoreAt(t, dir)
	accepted := time.Date(2026, 9, 10, 6, 1, 2, 0, time.UTC)

	trigger, err := accepting.AcceptWaiting(t.Context(), waitingAt(accepted, "instance-a"),
		map[string]string{"host": "web-1.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if trigger.ID == "" {
		t.Fatal("an accepted trigger has no identifier")
	}

	waiting, err := reading.StillWaiting(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 || waiting[0].ID != trigger.ID {
		t.Fatalf("another store reads %+v, want the trigger just accepted", waiting)
	}
	got := waiting[0]
	if got.Outcome != record.WaitWaiting || got.Instance != "instance-a" ||
		!got.AcceptedAt.Equal(accepted) || !got.ExpiresAt.Equal(accepted.Add(30*time.Minute)) ||
		got.PlaybookPath != "/srv/gronin/playbooks/book.yaml" || got.TriggerKind != record.TriggerManual {
		t.Fatalf("read back as %+v", got)
	}
	// The trigger's values are kept under the waiting trigger's own identifier: no run
	// exists yet to key them by.
	if got.TriggerRef == "" {
		t.Fatal("the trigger's values were not recorded")
	}
	values, err := reading.Blobs().Get(got.TriggerRef)
	if err != nil || string(values) != `{"host":"web-1.example.com"}` {
		t.Fatalf("trigger values = %q, err = %v", values, err)
	}
}

// A wait ends once. The call that ends it writes its refusal in the same step, and a second
// call — a reconciliation racing the process that owned the row — changes nothing and
// writes no second refusal.
func TestAWaitEndsOnceWithItsRefusal(t *testing.T) {
	store := openStoreAt(t, t.TempDir())
	accepted := time.Date(2026, 9, 10, 6, 1, 2, 0, time.UTC)
	trigger, err := store.AcceptWaiting(t.Context(), waitingAt(accepted, "instance-a"), nil)
	if err != nil {
		t.Fatal(err)
	}

	end := func() (bool, error) {
		return store.EndWait(t.Context(), trigger.ID, record.WaitEnd{
			Outcome: record.WaitDropped, At: accepted.Add(time.Minute),
			Refusal: &record.Refusal{
				PlaybookName: "drift-check", TriggerKind: record.TriggerManual,
				WaitingTriggerID: trigger.ID, Mechanism: record.MechanismDropped,
				Detail: "process instance-a ended before it ran", RefusedAt: accepted.Add(time.Minute),
			},
		})
	}
	if ended, err := end(); err != nil || !ended {
		t.Fatalf("ending the wait: ended = %v, err = %v", ended, err)
	}
	if ended, err := end(); err != nil || ended {
		t.Fatalf("a wait already ended was ended again: ended = %v, err = %v", ended, err)
	}

	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 1 || refusals[0].WaitingTriggerID != trigger.ID {
		t.Fatalf("refusals = %+v, want the one naming the wait it ended", refusals)
	}
	got, err := store.GetWaitingTrigger(t.Context(), trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != record.WaitDropped || !got.OutcomeAt.Equal(accepted.Add(time.Minute)) {
		t.Fatalf("read back as %+v", got)
	}
}

func openStoreAt(t *testing.T, dir string) *record.Store {
	t.Helper()
	store, err := record.Open(t.Context(), filepath.Join(dir, "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
