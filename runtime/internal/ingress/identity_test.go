package ingress_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/ingress"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// T040, SC-308. A source declaring /id: two bodies differing only outside it are one
// hand-off; a body with no id is 400 and a delivery refusal with its body; /id holding an
// object is refused likewise; and two bodies whose id are the integers
// 9007199254740993 and 9007199254740992 are two hand-offs (FR-316's float-precision
// case). With no identity declared, reordering a body's keys makes a new delivery, since
// the digest falls back to the exact bytes.
func TestADeclaredIdentity(t *testing.T) {
	t.Run("TwoBodiesDifferingOutsideItAreOneHandOff", testDeclaredIdentityCollapsesRepeats)
	t.Run("AnAbsentIdentityIsRefused", testDeclaredIdentityAbsentIsRefused)
	t.Run("AnObjectIdentityIsRefused", testDeclaredIdentityObjectIsRefused)
	t.Run("TwoNearbyLargeIntegersAreTwoHandOffs", testDeclaredIdentityIntegerPrecision)
	t.Run("WithNoIdentityDeclaredReorderingKeysIsNew", testDigestIdentityKeyOrderMakesNew)
}

// newDeclaredIdentityHandler builds a handler for one source bound to one playbook, its
// identity declared at pointer (empty for the digest default).
func newDeclaredIdentityHandler(
	t *testing.T, pointer string,
) (*ingress.Handler, *record.Store, *recordingDispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const secret = "s3cr3t-identity"
	catalog := testCatalog(t, testSource{name: "alerts", secret: secret, identity: pointer, window: 10 * time.Minute})
	book := webhookBook("alert-triage", "alerts", nil)
	dispatcher := &recordingDispatcher{}
	h := &ingress.Handler{
		Store: store, Sources: catalog, Secret: catalog.Secret,
		Loaded: playbook.Loaded{Playbooks: []*playbook.Playbook{book}}, Dispatcher: dispatcher,
		Clock: guard.SystemClock(), Instance: "instance-a", Options: ingress.DefaultOptions(),
	}
	return h, store, dispatcher, secret
}

func testDeclaredIdentityCollapsesRepeats(t *testing.T) {
	h, store, dispatcher, secret := newDeclaredIdentityHandler(t, "/id")

	for _, body := range [][]byte{
		[]byte(`{"id":"abc","note":"first"}`),
		[]byte(`{"id":"abc","note":"second, and different outside /id"}`),
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, sign("alerts", secret, body))
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
		}
	}
	pollUntil(t, time.Second, func() bool { return len(dispatcher.Calls()) == 1 })
	deliveries, err := store.ListDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Repeats != 1 {
		t.Fatalf("deliveries = %+v, err = %v; want one delivery with one repeat", deliveries, err)
	}
}

func testDeclaredIdentityAbsentIsRefused(t *testing.T) {
	h, store, dispatcher, secret := newDeclaredIdentityHandler(t, "/id")

	body := []byte(`{"note":"no id in this body"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, sign("alerts", secret, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), string(record.ReasonIdentityAbsent)) {
		t.Fatalf("body = %q, want it to name %s", w.Body.String(), record.ReasonIdentityAbsent)
	}
	if len(dispatcher.Calls()) != 0 {
		t.Fatalf("dispatched %+v for a delivery that was never accepted", dispatcher.Calls())
	}

	refusals, err := store.ListDeliveryRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 {
		t.Fatalf("refusals = %+v, err = %v", refusals, err)
	}
	if refusals[0].Reason != record.ReasonIdentityAbsent || refusals[0].BodyRef == "" {
		t.Fatalf("refusal = %+v, want reason %s and a stored body", refusals[0], record.ReasonIdentityAbsent)
	}
	stored, err := store.Blobs().Get(refusals[0].BodyRef)
	if err != nil || string(stored) != string(body) {
		t.Fatalf("stored body = %q, err = %v; want %q", stored, err, body)
	}
}

func testDeclaredIdentityObjectIsRefused(t *testing.T) {
	h, store, _, secret := newDeclaredIdentityHandler(t, "/id")

	body := []byte(`{"id":{"nested":"object"}}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, sign("alerts", secret, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), string(record.ReasonIdentityNotSingle)) {
		t.Fatalf("body = %q, want it to name %s", w.Body.String(), record.ReasonIdentityNotSingle)
	}
	refusals, err := store.ListDeliveryRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 || refusals[0].Reason != record.ReasonIdentityNotSingle {
		t.Fatalf("refusals = %+v, err = %v", refusals, err)
	}
}

// FR-316: a number is taken as the literal the sender wrote, never re-rendered from a
// decoded float, so two identities that differ only past a float's precision stay two —
// 9007199254740993 is 2^53+1, the first integer an IEEE-754 float64 cannot represent
// exactly (it would round to 9007199254740992, its neighbour here).
func testDeclaredIdentityIntegerPrecision(t *testing.T) {
	h, store, dispatcher, secret := newDeclaredIdentityHandler(t, "/id")

	for _, body := range [][]byte{
		[]byte(`{"id":9007199254740993}`),
		[]byte(`{"id":9007199254740992}`),
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, sign("alerts", secret, body))
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
		}
	}
	pollUntil(t, time.Second, func() bool { return len(dispatcher.Calls()) == 2 })
	deliveries, err := store.ListDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("deliveries = %+v, err = %v; want two", deliveries, err)
	}
}

func testDigestIdentityKeyOrderMakesNew(t *testing.T) {
	h, store, dispatcher, secret := newDeclaredIdentityHandler(t, "")

	for _, body := range [][]byte{
		[]byte(`{"a":1,"b":2}`),
		[]byte(`{"b":2,"a":1}`),
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, sign("alerts", secret, body))
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
		}
	}
	pollUntil(t, time.Second, func() bool { return len(dispatcher.Calls()) == 2 })
	deliveries, err := store.ListDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("deliveries = %+v, err = %v; want two — the digest of two byte-for-byte "+
			"different bodies, whatever their keys mean", deliveries, err)
	}
}
