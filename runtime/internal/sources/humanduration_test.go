package sources

import (
	"testing"
	"time"
)

// humanDuration mirrors internal/guard's HumanDuration by hand (sources.go says why it
// is not imported); this holds the copy to the same cases guard/duration_test.go's
// TestHumanDuration does, so the two cannot drift apart silently. A white-box test,
// because the function it covers is unexported.
func TestHumanDuration(t *testing.T) {
	for in, want := range map[time.Duration]string{
		0:                               "0s",
		400 * time.Millisecond:          "0s",
		1523 * time.Millisecond:         "2s",
		30 * time.Minute:                "30m",
		12*time.Minute + 14*time.Second: "12m14s",
		time.Hour:                       "1h",
		2*time.Hour + 5*time.Second:     "2h5s",
		time.Hour + 30*time.Minute + 499*time.Millisecond: "1h30m",
	} {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", in, got, want)
		}
	}
}
