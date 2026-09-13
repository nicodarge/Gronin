package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SC-112's second half, FR-122. Held rather than severed: a severed backend fails at
// once, so a test that only severs passes against a renewal with no bound at all.
func TestRenewalsThatHangEndTheRunBeforeItsClaimCanLapse(t *testing.T) {
	var (
		backend = guardtest.NewClock(start)
		runtime = guardtest.NewClock(start)
		fake    = guardtest.NewFake(backend, runtime)
		cfg     = testConfig()
	)
	host := watching(fake, runtime, 0)
	subject := &guard.Guard{
		Coordinator: host, Store: newStore(t), Config: cfg, Clock: runtime,
		Host: "host-a.example.com", Instance: "instance-a",
	}

	admitted, err := subject.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-a", Kind: record.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan guard.Instant, 1)
	since := runtime.Set()
	hold := subject.Hold(admitted, func() { stopped <- runtime.Monotonic() })
	t.Cleanup(hold.Done)

	host.Hold()
	// Three attempts, each abandoned at its bound: sent at 5, 10 and 15, ending at 9, 14
	// and 19. Nothing ever succeeds, so the deadline at 18 stands where the grant put it.
	for _, attempt := range []time.Duration{5, 10, 15} {
		since = advanceTo(t, runtime, backend, since, attempt*time.Second)
		advanceTo(t, runtime, backend, since, attempt*time.Second+cfg.RenewBound)
		since = host.waitForAttempt(t)
	}

	select {
	case at := <-stopped:
		// Before the claim can lapse is what matters, and the backend is what judges
		// that: the claim was granted at the backend's zero and lasts its whole expiry.
		if elapsed := backend.Monotonic().Sub(guard.InstantAt(0)); elapsed >= cfg.ClaimExpiry {
			t.Fatalf("the run was stopped %s in, after the claim's %s expiry", elapsed, cfg.ClaimExpiry)
		}
		if got := at.Sub(guard.InstantAt(0)); got < 18*time.Second {
			t.Fatalf("the run was stopped at %s, before the 18s deadline", got)
		}
	case <-time.After(patience):
		t.Fatal("renewals that hang never stopped the run, so its claim would have lapsed under it")
	}
}

// The half that makes the bound testable at all: an attempt abandoned at its bound is
// followed by another, and a run whose later renewals are answered keeps going. A test
// that only ever holds every renewal passes with the bound removed.
func TestRenewalsResumeAfterOneIsAbandonedAtItsBound(t *testing.T) {
	var (
		backend = guardtest.NewClock(start)
		runtime = guardtest.NewClock(start)
		fake    = guardtest.NewFake(backend, runtime)
		cfg     = testConfig()
	)
	host := watching(fake, runtime, 0)
	subject := &guard.Guard{
		Coordinator: host, Store: newStore(t), Config: cfg, Clock: runtime,
		Host: "host-a.example.com", Instance: "instance-a",
	}

	admitted, err := subject.Admit(t.Context(), book("drift-check"), guard.Request{
		RunID: "run-a", Kind: record.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan guard.Instant, 1)
	since := runtime.Set()
	hold := subject.Hold(admitted, func() { stopped <- runtime.Monotonic() })
	t.Cleanup(hold.Done)

	host.Hold()
	since = advanceTo(t, runtime, backend, since, 5*time.Second)
	advanceTo(t, runtime, backend, since, 5*time.Second+cfg.RenewBound)
	since = host.waitForAttempt(t)
	host.Resume()

	// Every later attempt succeeds, so the deadline keeps moving and the run outlives
	// the one the first grant gave it.
	for _, attempt := range []time.Duration{10, 15, 20, 25} {
		advanceTo(t, runtime, backend, since, attempt*time.Second)
		since = host.waitForAttempt(t)
	}
	select {
	case at := <-stopped:
		t.Fatalf("the run was stopped at %s although its renewals were answered again", at.Sub(guard.InstantAt(0)))
	default:
	}
	if hold.Stopped() {
		t.Fatal("the hold reports having stopped a run whose renewals were answered")
	}
}
