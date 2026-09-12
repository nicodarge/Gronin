package run_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// expiresAfterTheFirstFence lets the fence before the agent stage pass and then takes the
// claim away, which puts the loss exactly where R3's test wants it: between the agent
// stage and the first sink.
type expiresAfterTheFirstFence struct {
	guard.Coordinator
	fake *guardtest.Fake
}

func (c *expiresAfterTheFirstFence) Acquire(
	ctx context.Context, req guard.AcquireRequest,
) (guard.Claim, error) {
	held, err := c.Coordinator.Acquire(ctx, req)
	if err != nil {
		return nil, err
	}
	return &expiringClaim{Claim: held, fake: c.fake}, nil
}

type expiringClaim struct {
	guard.Claim
	fake *guardtest.Fake
	once sync.Once
}

func (c *expiringClaim) Fence(ctx context.Context) error {
	if err := c.Claim.Fence(ctx); err != nil {
		return err
	}
	c.once.Do(func() { c.fake.Expire(c.Claim) })
	return nil
}

// R3: a fence before every side effect. The claim is gone by the time the first sink
// would deliver, so it does not — and the run is recorded claim_lost rather than failed.
//
// The delivering run beside it is what makes the absence mean something: the endpoint
// receives exactly one message when the claim holds, and none when it does not.
func TestFenceStopsTheSinksWhenTheClaimIsGone(t *testing.T) {
	for name, probe := range map[string]struct {
		expires  bool
		posts    int
		expected record.Status
	}{
		"the claim holds":         {expires: false, posts: 1, expected: record.StatusSucceeded},
		"the claim is taken away": {expires: true, posts: 0, expected: record.StatusClaimLost},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, fakeagent.ModeSuccess)
			h.executor.AgentEnv = append(h.executor.AgentEnv,
				fakeagent.ResultVar+`={"findings":[{"id":"one"}]}`)

			now := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
			fake := guardtest.NewFake(guardtest.NewClock(now), guardtest.NewClock(now))
			var coordinator guard.Coordinator = fake.Host(nil)
			if probe.expires {
				coordinator = &expiresAfterTheFirstFence{Coordinator: coordinator, fake: fake}
			}
			h.executor.Guard = &guard.Guard{
				Coordinator: coordinator, Store: h.store, Config: guard.DefaultConfig(),
				Host: "host-a.example.com", Instance: "instance-a",
			}

			book := h.playbook(t, goodPlaybook)
			got, err := h.executor.Execute(t.Context(), book, run.Trigger{
				Kind: record.TriggerManual,
			})
			if err != nil {
				t.Fatalf("the run did not happen at all: %v", err)
			}
			if got.Status != probe.expected {
				t.Fatalf("status = %q, want %q (%s)", got.Status, probe.expected, got.Error)
			}
			if posted := h.posted.all(); len(posted) != probe.posts {
				t.Fatalf("the sink's endpoint received %d message(s), want %d: %q",
					len(posted), probe.posts, posted)
			}

			// The record is the authoritative copy, and it is where an operator reads
			// which guarantee the run ended under.
			stored, err := h.store.GetRun(t.Context(), got.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != probe.expected {
				t.Fatalf("the record says %q, want %q", stored.Status, probe.expected)
			}
		})
	}
}
