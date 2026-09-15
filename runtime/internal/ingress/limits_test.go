package ingress_test

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/ingress"
)

// T043, FR-313. The default bounds are the plan's (plan.md, *Constraints*), and the time
// to read a whole request plus the durable step fits inside the answer-write limit —
// research.md §3 found that when it does not, the ingress answers a request it believes
// it has, and the sender never sees it.
func TestTheDurableStepFitsInsideTheWriteLimit(t *testing.T) {
	opts := ingress.DefaultOptions()

	for _, tc := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"HeaderTimeout", opts.HeaderTimeout, ingress.DefaultHeaderTimeout},
		{"RequestTimeout", opts.RequestTimeout, ingress.DefaultRequestTimeout},
		{"AnswerTimeout", opts.AnswerTimeout, ingress.DefaultAnswerTimeout},
		{"DurableStep", opts.DurableStep, ingress.DefaultDurableStep},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %s, want the plan's %s", tc.name, tc.got, tc.want)
		}
	}
	if opts.MaxHeaderBytes != ingress.DefaultMaxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d, want the plan's %d", opts.MaxHeaderBytes, ingress.DefaultMaxHeaderBytes)
	}

	sum := opts.RequestTimeout + opts.DurableStep
	if sum >= opts.AnswerTimeout {
		t.Fatalf("the request timeout (%s) plus the durable step (%s) is %s, which does not "+
			"fit under the answer write limit of %s (research.md §3)",
			opts.RequestTimeout, opts.DurableStep, sum, opts.AnswerTimeout)
	}
}
