package guardtest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
)

// C8, under the exact interleaving a sequential contract clause cannot produce: B checks
// the rate slots, finds one free, and is held there — after the check and before the
// commit that would take it — while A takes and releases the same slot. B is then let go.
// A slot does not free on release (C9), so the window is still full when B resumes, and
// only a re-check at the commit itself (not merely at the first check) can catch it. This
// is what the many-goroutines version of this test could only hit by chance: a reviewer
// measured a granted second claim in 7 of 200 runs under -race, which a mutation harness
// running the command once cannot rely on.
func TestFakeRateSlotIsAtomicWithTheClaimUnderConcurrency(t *testing.T) {
	start := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	fake := guardtest.NewFake(guardtest.NewClock(start), guardtest.NewClock(start))
	limit := &guard.RateLimit{Runs: 1, Per: time.Hour}

	var reached sync.Once
	atSeam, proceed := make(chan struct{}), make(chan struct{})
	b := fake.Host(nil)
	b.OnRateCheck(func(ctx context.Context, _ string) {
		reached.Do(func() { close(atSeam) })
		select {
		case <-proceed:
		case <-ctx.Done():
		}
	})
	a := fake.Host(nil)

	req := func(runID string) guard.AcquireRequest {
		return guard.AcquireRequest{
			Name:    "race",
			Holder:  guard.Holder{RunID: runID},
			Expiry:  30 * time.Second,
			Rate:    limit,
			Trigger: guard.TriggerRef{Kind: guard.KindManual},
		}
	}

	result := make(chan error, 1)
	go func() {
		claim, err := b.Acquire(context.Background(), req("run-b"))
		if err == nil {
			_ = claim.Release(context.Background())
		}
		result <- err
	}()
	select {
	case <-atSeam:
	case <-time.After(5 * time.Second):
		t.Fatal("B never reached the window between its check and its commit")
	}

	claimA, err := a.Acquire(context.Background(), req("run-a"))
	if err != nil {
		t.Fatalf("A was refused: %v", err)
	}
	if err := claimA.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(proceed)

	select {
	case errB := <-result:
		if !errors.Is(errB, guard.ErrRateLimited) {
			t.Fatalf("B, racing A for the rate slot A had already taken and released, "+
				"was not refused for rate: %v", errB)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B did not return after being let go")
	}
}
