package guard_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// contracts/cli.md writes a duration rounded to the second, unit by unit, leaving out every
// unit that is zero.
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
		if got := guard.HumanDuration(in); got != want {
			t.Errorf("HumanDuration(%s) = %q, want %q", in, got, want)
		}
	}
}

// A claim_held refusal names its holder as contracts/cli.md writes it, and stays ErrHeld to
// anything that asks.
func TestAHeldClaimNamesItsHolderAsTheContractDoes(t *testing.T) {
	err := guard.HeldBy(guard.Holder{
		Host: "host-b.example.com", Instance: "8f2c1a0b", RunID: "20260910T060000Z-3f9a1c0b2e4d",
	})
	if want := "held by run 20260910T060000Z-3f9a1c0b2e4d on host-b.example.com, process 8f2c1a0b"; err.Error() != want {
		t.Fatalf("the refusal reads %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, guard.ErrHeld) {
		t.Fatal("a held claim is not ErrHeld")
	}
}

// A rate_limited refusal names the limit it hit as contracts/cli.md writes it, and stays
// ErrRateLimited to anything that asks.
func TestARateLimitedRefusalNamesTheLimitAsTheContractDoes(t *testing.T) {
	err := guard.RateLimitedBy(guard.RateLimit{Runs: 2, Per: 5 * time.Minute})
	if want := "rate limit reached: 2 runs per 5m"; err.Error() != want {
		t.Fatalf("the refusal reads %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, guard.ErrRateLimited) {
		t.Fatal("a rate-limited refusal is not ErrRateLimited")
	}
}
