package guard_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// A guard with no clock injected builds one per reading, and a deadline compares two of them.
func TestSystemClocksReadOnOneAxis(t *testing.T) {
	before := guard.SystemClock().Monotonic()
	time.Sleep(50 * time.Millisecond)
	after := guard.SystemClock().Monotonic()
	if elapsed := after.Sub(before); elapsed < 50*time.Millisecond {
		t.Fatalf("two system clocks read %s apart across a 50ms sleep", elapsed)
	}
}
