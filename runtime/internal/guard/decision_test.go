package guard_test

import (
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// decisionBound is short enough for a test to wait out and long enough that only a
// decision that ignores its bound reaches the watchdog below.
const decisionBound = 500 * time.Millisecond

// watchdogSlack is how long past its bound a decision may take before it is called
// unbounded.
const watchdogSlack = 250 * time.Millisecond

// SC-116, FR-108. The backend holds its answer rather than refusing promptly: against
// one that answers quickly the criterion is met whether or not any bound is enforced.
//
// The runtime's own clock here is the system's, because what is measured is real
// elapsed time, and the watchdog is the test's own — go test's timeout would take ten
// minutes to notice a decision that never returns.
func TestTheDecisionBound(t *testing.T) {
	var (
		backend = guardtest.NewClock(start)
		runtime = guardtest.NewClock(start)
		fake    = guardtest.NewFake(backend, runtime)
	)
	host := fake.Host(nil)
	host.Hold()

	cfg := testConfig()
	cfg.DecisionBound = decisionBound
	subject := &guard.Guard{
		Coordinator: host, Store: newStore(t), Config: cfg,
		Host: "host-a.example.com", Instance: "instance-a",
	}

	decided := make(chan error, 1)
	go func() {
		_, err := subject.Admit(t.Context(), book("drift-check"), guard.Request{
			RunID: "run-a", Kind: record.TriggerManual,
		})
		decided <- err
	}()

	select {
	case err := <-decided:
		if !refusedWith(err, record.MechanismBackendUnavailable) {
			t.Fatalf("a decision that could not be reached was not a backend refusal: %v", err)
		}
		// It names what would not answer, rather than leaving an operator to guess which
		// of the deployment's parts was the quiet one.
		if !strings.Contains(err.Error(), "fake") {
			t.Fatalf("the refusal does not name the backend: %v", err)
		}
	case <-time.After(decisionBound + watchdogSlack):
		t.Fatalf("the decision had not returned %s after a bound of %s", watchdogSlack, decisionBound)
	}
}
