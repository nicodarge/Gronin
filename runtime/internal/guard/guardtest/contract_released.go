package guardtest

import (
	"context"
	"sync"
	"testing"
	"time"
)

// C10, the other half. A coordinator whose backend can say that a claim was released waits
// for it: one whose Released returned at once would pass the half that releases, and leave a
// waiting trigger asking the backend for the claim in a busy loop.
func releasedWaitsWhileHeld(t *testing.T, s Subject) {
	if s.NewWatching == nil {
		t.Skipf("Released returns at each poll on this subject: %s", s.ReleasedPolls)
	}
	holder := s.New(t, nil)
	claim := acquire(t, s, holder, manual("c10-held", "run-a"))

	watching := make(chan struct{})
	var once sync.Once
	waiter := s.NewWatching(t, func(string) { once.Do(func() { close(watching) }) })

	ctx, cancel := context.WithTimeout(t.Context(), callBound)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- waiter.Released(ctx, "c10-held") }()

	select {
	case err := <-returned:
		t.Fatalf("Released returned while the claim was held: %v", err)
	case <-watching:
	}
	select {
	case err := <-returned:
		t.Fatalf("Released returned while the claim was held: %v", err)
	default:
	}

	releaseClaim(t, claim)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Released failed once the claim was released: %v", err)
		}
	case <-time.After(callBound):
		t.Fatal("Released did not return once the claim was released")
	}
}
