package ingress_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/ingress"
	"github.com/nicodarge/Gronin/runtime/internal/ingress/ingresstest"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/sources"
)

// signatureHeader is the one every test source in this package declares (contracts/cli.md
// leaves the name to the source; which one is fixed does not matter to any test here).
const signatureHeader = "X-Signature"

// testSource is one entry a testCatalog builds.
type testSource struct {
	name     string
	secret   string
	identity string
	window   time.Duration
}

// testCatalog builds a sources.Catalog holding every src, its secret resolved through a
// config of its own (contracts/cli.md's shape) — what every ingress handler test needs
// to sign a request the handler will actually verify.
func testCatalog(t *testing.T, srcs ...testSource) *sources.Catalog {
	t.Helper()
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	entries := map[string]map[string]string{}
	for i, src := range srcs {
		key := "secret" + string(rune('a'+i))
		if err := cfg.Set(key, config.Value{Value: src.secret, Secret: true}); err != nil {
			t.Fatal(err)
		}
		entry := map[string]string{
			"secret": "${config." + key + "}", "signature_header": signatureHeader,
		}
		if src.identity != "" {
			entry["identity"] = src.identity
		}
		if src.window > 0 {
			entry["replay_window"] = src.window.String()
		}
		entries[src.name] = entry
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sources.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := sources.Load(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// withDurableStep is ingress.DefaultOptions with only its durable step's bound changed.
func withDurableStep(d time.Duration) ingress.Options {
	opts := ingress.DefaultOptions()
	opts.DurableStep = d
	return opts
}

// sign builds a signed POST to /hooks/<source>, ready for Handler.ServeHTTP directly —
// no socket, since that is what the durable-step tests need to measure.
func sign(source, secret string, body []byte) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/hooks/"+source, bytes.NewReader(body))
	req.Header.Set(signatureHeader, ingresstest.Sign(secret, "", body))
	return req
}

// pollUntil polls check every 5ms until it reports true, failing t once deadline passes.
// Never a fixed sleep-then-assert: what it waits for is asynchronous (a hand-off run from
// a goroutine the handler starts once the acceptance completes), and a slow but correct
// outcome must not read as a failure.
func pollUntil(t *testing.T, deadline time.Duration, check func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for {
		if check() {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("condition did not become true within %s", deadline)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// T036, SC-302 and SC-303, and contracts/ingress.md's A1 and A2: against the handler with
// no socket, a durable-step bound of 200ms and the write lock held throughout by
// ingresstest.HoldWrites, no answer is written while the lock is held, and at the bound
// the answer is 503 — checked from this test's own watchdog against the bound plus slack,
// since the handler's own timer is what is under test here.
func TestTheAnswerWaitsForTheRecord(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const secret = "s3cr3t-one"
	catalog := testCatalog(t, testSource{name: "alerts", secret: secret, window: 10 * time.Minute})
	h := &ingress.Handler{
		Store: store, Sources: catalog, Secret: catalog.Secret,
		Loaded: playbook.Loaded{}, Dispatcher: &recordingDispatcher{},
		Clock: guard.SystemClock(), Instance: "instance-a",
		Options: withDurableStep(200 * time.Millisecond),
	}

	release := ingresstest.HoldWrites(t, dir)
	t.Cleanup(release)

	req := sign("alerts", secret, []byte(`{"id":"one"}`))
	w := httptest.NewRecorder()

	start := time.Now()
	h.ServeHTTP(w, req)
	elapsed := time.Since(start)

	const bound, slack = 200 * time.Millisecond, 300 * time.Millisecond
	if elapsed < bound {
		t.Fatalf("answered after %s, before its %s bound", elapsed, bound)
	}
	if elapsed > bound+slack {
		t.Fatalf("answered after %s, past its %s bound plus %s slack", elapsed, bound, slack)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

// T036, SC-302 and SC-303, and contracts/ingress.md's A3: the write lock is released 500ms
// after the 503, well inside the record store's own busy timeout — so the acceptance that
// answered 503 still lands, its one hand-off is dispatched exactly once, and the same body
// sent again is 202 and dispatches nothing more. A store that failed outright would prove
// nothing about the ordering, which is why the write lands late rather than not at all.
func TestALateWriteStillRuns(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const secret = "s3cr3t-two"
	catalog := testCatalog(t, testSource{name: "alerts", secret: secret, window: 10 * time.Minute})
	book := webhookBook("alert-triage", "alerts", nil)
	dispatcher := &recordingDispatcher{}
	h := &ingress.Handler{
		Store: store, Sources: catalog, Secret: catalog.Secret,
		Loaded: playbook.Loaded{Playbooks: []*playbook.Playbook{book}}, Dispatcher: dispatcher,
		Clock: guard.SystemClock(), Instance: "instance-a",
		Options: withDurableStep(200 * time.Millisecond),
	}

	release := ingresstest.HoldWrites(t, dir)

	// A cancellable context, cancelled the moment ServeHTTP returns — what a real
	// http.Server does once it finishes handling a request. The acceptance must not be
	// on this context (research.md §3): only a context detached from it survives past
	// this point to see the late write land.
	reqCtx, cancel := context.WithCancel(context.Background())
	body := []byte(`{"id":"one"}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, sign("alerts", secret, body).WithContext(reqCtx))
	cancel()
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	// Deliberately late, not absent: the hold outlives the durable step's bound but not
	// the store's own busy timeout (research.md §3).
	time.Sleep(500 * time.Millisecond)
	release()

	pollUntil(t, 3*time.Second, func() bool { return len(dispatcher.Calls()) == 1 })
	if dispatcher.Calls()[0].playbook != "alert-triage" {
		t.Fatalf("dispatched %+v, want alert-triage", dispatcher.Calls())
	}
	pollUntil(t, 3*time.Second, func() bool {
		deliveries, err := store.ListDeliveries(t.Context(), 10)
		return err == nil && len(deliveries) == 1 && deliveries[0].State == record.DeliveryHandedOff
	})

	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, sign("alerts", secret, body))
	if w2.Code != http.StatusAccepted {
		t.Fatalf("repeat status = %d, want %d", w2.Code, http.StatusAccepted)
	}
	if len(dispatcher.Calls()) != 1 {
		t.Fatalf("the repeat dispatched %+v, want no further calls", dispatcher.Calls())
	}
	deliveries, err := store.ListDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Repeats != 1 {
		t.Fatalf("deliveries = %+v, err = %v; want one delivery with one repeat", deliveries, err)
	}
}

// A pre-PR review finding: nothing had gone through Handler.ServeHTTP itself with
// playbooks loaded for two different sources to prove boundPlaybookNames (T050), which
// decides the Playbooks Accept records a delivery's hand-offs for, filters by source —
// TestAHandOffChecksEveryBoundPlaybookOnItsOwn and TestAnUnboundDeliveryIsRecordedUnbound
// both call HandOff directly, past that decision. A delivery for "alerts" must be handed
// only to the playbook bound to "alerts": never the one bound to "other", in the row
// Accept records or the dispatch HandOff runs.
func TestASourceIsHandedOnlyToItsOwnPlaybooks(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const secret = "s3cr3t-source"
	catalog := testCatalog(t, testSource{name: "alerts", secret: secret, window: 10 * time.Minute})
	loaded := playbook.Loaded{Playbooks: []*playbook.Playbook{
		webhookBook("alert-triage", "alerts", nil),
		webhookBook("other-playbook", "other", nil),
	}}
	dispatcher := &recordingDispatcher{}
	h := &ingress.Handler{
		Store: store, Sources: catalog, Secret: catalog.Secret, Loaded: loaded,
		Dispatcher: dispatcher, Clock: guard.SystemClock(), Instance: "instance-a",
		Options: ingress.DefaultOptions(),
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, sign("alerts", secret, []byte(`{"id":"one"}`)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}

	pollUntil(t, time.Second, func() bool { return len(dispatcher.Calls()) == 1 })
	calls := dispatcher.Calls()
	if len(calls) != 1 || calls[0].playbook != "alert-triage" {
		t.Fatalf("dispatched %+v, want exactly one call to alert-triage", calls)
	}

	deliveries, err := store.ListDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries = %+v, err = %v", deliveries, err)
	}

	// The row assertion, not the dispatch one, is what catches a boundPlaybookNames
	// that stopped filtering by source: HandOff's own source check would still refuse
	// to dispatch other-playbook, but Accept would have recorded a row for it anyway.
	handoffs, err := store.HandOffsOf(t.Context(), deliveries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(handoffs) != 1 || handoffs[0].PlaybookName != "alert-triage" {
		t.Fatalf("hand-offs = %+v, want exactly one, for alert-triage", handoffs)
	}
}

// A pre-PR review finding: the identity-refusal write (step 6) runs on a context
// detached from the request, like the acceptance (step 7), so that a client
// disconnecting cannot lose the refusal record the 400 answer names — untested until
// now. The request's own context is cancelled before it is ever sent, as it would be for
// a client already gone by the time this handler runs, while the write lock is held; the
// refusal still lands once the lock is released.
func TestALateIdentityRefusalStillLands(t *testing.T) {
	dir := t.TempDir()
	store, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const secret = "s3cr3t-refusal"
	catalog := testCatalog(t, testSource{name: "alerts", secret: secret, identity: "/id", window: 10 * time.Minute})
	h := &ingress.Handler{
		Store: store, Sources: catalog, Secret: catalog.Secret,
		Loaded: playbook.Loaded{}, Dispatcher: &recordingDispatcher{},
		Clock: guard.SystemClock(), Instance: "instance-a", Options: ingress.DefaultOptions(),
	}

	release := ingresstest.HoldWrites(t, dir)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()
	body := []byte(`{"note":"no id in this body"}`)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, sign("alerts", secret, body).WithContext(reqCtx))
		close(done)
	}()

	// Deliberately late, not absent (contracts/ingress.md, A3's reasoning applied to
	// step 6): the hold outlives however long ServeHTTP takes to reach the write, but
	// not the store's own busy timeout.
	time.Sleep(200 * time.Millisecond)
	release()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHTTP never returned after the lock was released")
	}

	refusals, err := store.ListDeliveryRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 || refusals[0].Reason != record.ReasonIdentityAbsent {
		t.Fatalf("refusals = %+v, err = %v; want exactly one, identity_absent, despite the "+
			"cancelled request", refusals, err)
	}
}
