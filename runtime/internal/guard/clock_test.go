package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// A guard with no clock injected reads the system's, and it does so afresh at every use:
// the stop deadline is anchored on one reading and compared with a later one, and a wait's
// length is the difference between two. Every system clock therefore has to read on one
// axis. With an origin of its own each, both readings are near zero, so no deadline is ever
// reached and nothing ever waits any time at all — which every test injecting a clock
// passes.
func TestSystemClocksReadOnOneAxis(t *testing.T) {
	before := guard.SystemClock().Monotonic()
	time.Sleep(50 * time.Millisecond)
	after := guard.SystemClock().Monotonic()
	if elapsed := after.Sub(before); elapsed < 50*time.Millisecond {
		t.Fatalf("two system clocks read %s apart across a 50ms sleep", elapsed)
	}
}
