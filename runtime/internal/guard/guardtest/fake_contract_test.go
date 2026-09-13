package guardtest_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
)

// The fake is the runtime's belief about the backend. Held to the contract the etcd
// adapter is held to, a clause it satisfies and etcd does not fails in the other run.
func TestFakeContract(t *testing.T) {
	start := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	var (
		backend = guardtest.NewClock(start)
		runtime = guardtest.NewClock(start)
		fake    = guardtest.NewFake(backend, runtime)
	)
	host := func(seam guard.Seam) *guardtest.FakeHost { return fake.Host(seam) }

	guardtest.Contract(t, guardtest.Subject{
		New: func(_ *testing.T, seam guard.Seam) guardtest.Node {
			h := host(seam)
			return guardtest.Node{Coordinator: h, Hold: h.Hold}
		},
		Seams: true,
		NewWatching: func(_ *testing.T, watching func(name string)) guardtest.Node {
			h := host(nil)
			h.OnWatch(watching)
			return guardtest.Node{Coordinator: h, Hold: h.Hold}
		},
		Unreachable: func(*testing.T) guard.Coordinator {
			h := host(nil)
			h.Sever()
			return h
		},
		Lapse:       func(_ *testing.T, claim guard.Claim) { fake.Expire(claim) },
		Elapse:      func(_ *testing.T, d time.Duration) { backend.Advance(d) },
		StepRuntime: runtime.Advance,
		ShortGrant: func(*testing.T) guard.Coordinator {
			h := host(nil)
			h.Grant(func(asked time.Duration) time.Duration { return asked - time.Second })
			return h
		},
		LongGrant: func(*testing.T) (guard.Coordinator, time.Duration, time.Duration) {
			h := host(nil)
			h.Grant(func(asked time.Duration) time.Duration { return asked + time.Second })
			return h, 5 * time.Second, 6 * time.Second
		},
		Expiry: 30 * time.Second,
		Slack:  3 * time.Second,
	})
}

// A test that advances the clock before the code under test waits on it asserts
// nothing, so the fake clock's own guarantees are pinned: the wall reading steps alone,
// and timers fire on the monotonic one.
func TestTheFakeClockKeepsItsReadingsApart(t *testing.T) {
	clock := guardtest.NewClock(time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC))
	before := clock.Monotonic()
	timer := clock.NewTimer(10 * time.Second)

	clock.StepWall(-time.Hour)
	if got := clock.Monotonic(); got != before {
		t.Fatalf("stepping the wall reading moved the monotonic one by %s", got.Sub(before))
	}
	if !clock.Wall().Equal(time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("wall = %v", clock.Wall())
	}

	clock.Advance(9 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("the timer fired before its deadline")
	default:
	}
	clock.Advance(time.Second)
	select {
	case <-timer.C():
	default:
		t.Fatal("the timer did not fire at its deadline")
	}
	if timer.Stop() {
		t.Fatal("a fired timer reported being stopped")
	}
}
