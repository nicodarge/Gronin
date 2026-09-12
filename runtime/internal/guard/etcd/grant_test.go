package etcd

import (
	"errors"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// C12's refusing half. The embedded server can only grant longer than it was asked, so
// that half is unreachable through it; the contract exercises it on the fake, and this is
// where the adapter's own comparison is shown able to fail.
func TestGrantedExpiry(t *testing.T) {
	for name, probe := range map[string]struct {
		asked, granted time.Duration
		refused        bool
	}{
		"a second short":         {30 * time.Second, 29 * time.Second, true},
		"nothing at all":         {30 * time.Second, 0, true},
		"half of what was asked": {30 * time.Second, 15 * time.Second, true},
		"exactly what was asked": {30 * time.Second, 30 * time.Second, false},
		"a second longer":        {30 * time.Second, 31 * time.Second, false},
		"twice as long":          {2 * time.Second, 4 * time.Second, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckGrant(probe.asked, probe.granted)
			switch {
			case probe.refused && err == nil:
				t.Fatalf("a grant of %s for %s asked was accepted", probe.granted, probe.asked)
			case probe.refused && !errors.Is(err, guard.ErrUnavailable):
				t.Fatalf("a short grant was refused as %v, not as an unavailable backend", err)
			case !probe.refused && err != nil:
				t.Fatalf("a grant of %s for %s asked was refused: %v", probe.granted, probe.asked, err)
			}
		})
	}
}
