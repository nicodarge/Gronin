package ingress_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/ingress"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// T038, SC-305. The guard's fake clock (G013) injected, a source with a 10-minute window.
// The same body twice inside it is one hand-off and a repeat count of one; the clock
// moved to one second past the window, the same body is a second hand-off; and the clock
// moved to exactly one window after that second acceptance, a third (research.md §5: "an
// acceptance exactly one window old is a new delivery"). Every request carries a body with
// its own "timestamp" field and a Date header skewed one way or the other — neither ever
// changes an outcome, because the window is judged on the injected clock's wall reading
// alone (FR-320).
func TestTheReplayWindow(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const secret = "s3cr3t-window"
	const window = 10 * time.Minute
	catalog := testCatalog(t, testSource{name: "alerts", secret: secret, window: window})
	book := webhookBook("alert-triage", "alerts", nil)
	dispatcher := &recordingDispatcher{}

	start := time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)
	clock := guardtest.NewClock(start)

	h := &ingress.Handler{
		Store: store, Sources: catalog, Secret: catalog.Secret,
		Loaded: playbook.Loaded{Playbooks: []*playbook.Playbook{book}}, Dispatcher: dispatcher,
		Clock: clock, Instance: "instance-a", Options: ingress.DefaultOptions(),
	}

	body := []byte(`{"id":"one","timestamp":"2026-09-12T05:00:00Z"}`)

	send := func(dateSkew time.Time) int {
		t.Helper()
		req := sign("alerts", secret, body)
		req.Header.Set("Date", dateSkew.Format(http.TimeFormat))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}

	// An hour past the window, on the request that starts the whole sequence.
	if code := send(start.Add(window + time.Hour)); code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d, want %d", code, http.StatusAccepted)
	}
	pollUntil(t, time.Second, func() bool { return len(dispatcher.Calls()) == 1 })

	// Same body, inside the window: a repeat, no second hand-off. An hour before the
	// first acceptance, this time.
	if code := send(start.Add(-time.Hour)); code != http.StatusAccepted {
		t.Fatalf("repeat status = %d, want %d", code, http.StatusAccepted)
	}
	if len(dispatcher.Calls()) != 1 {
		t.Fatalf("dispatched %d times for a repeat inside the window", len(dispatcher.Calls()))
	}
	deliveries, err := store.ListDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Repeats != 1 {
		t.Fatalf("deliveries = %+v, err = %v; want one delivery with one repeat", deliveries, err)
	}

	// One second past the window: a second, new delivery.
	clock.Advance(window + time.Second)
	if code := send(start.Add(window + time.Hour)); code != http.StatusAccepted {
		t.Fatalf("post-window status = %d, want %d", code, http.StatusAccepted)
	}
	pollUntil(t, time.Second, func() bool { return len(dispatcher.Calls()) == 2 })
	deliveries, err = store.ListDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("deliveries = %+v, err = %v; want two", deliveries, err)
	}

	// Exactly one window after that second acceptance: new again.
	clock.Advance(window)
	if code := send(start.Add(-time.Hour)); code != http.StatusAccepted {
		t.Fatalf("third delivery status = %d, want %d", code, http.StatusAccepted)
	}
	pollUntil(t, time.Second, func() bool { return len(dispatcher.Calls()) == 3 })
	deliveries, err = store.ListDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 3 {
		t.Fatalf("deliveries = %+v, err = %v; want three", deliveries, err)
	}
}
