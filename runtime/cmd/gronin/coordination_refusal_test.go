package main

import (
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

// SC-112 at the operator's surface, FR-123: durations that cannot hold stop `serve`
// before anything is armed, and the refusal names each of them and the expiry they
// exceed — as contracts/cli.md shows it.
//
// The set below is refused only because of the stop bound: 5 + 4 + 10 + 2 = 21 against
// 20. One refused on the other terms alone would pass an implementation that leaves the
// stop bound out of the sum.
func TestServeRefusesDurationsThatCannotHold(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	stateDir, _ := oneHost(t, "http://127.0.0.1:9/unreachable")
	writeCoordinationFile(t, stateDir, map[string]any{
		"etcd":         map[string]any{"endpoints": []string{"unix:///dev/null"}, "prefix": "gronin/"},
		"claim_expiry": "20s",
	})

	got := bintest.Run(t, "serve", "--state-dir", stateDir,
		"--agent", fakeagent.Build(t), "--api-address", "127.0.0.1:0")

	if got.ExitCode == 0 {
		t.Fatalf("serve armed a deployment whose durations cannot hold: %q", got.Stdout)
	}
	if !strings.Contains(got.Stderr, "Nothing was armed.") {
		t.Fatalf("the output does not say nothing was armed:\n%s", got.Stderr)
	}
	for _, named := range []string{
		"coordination.json", "renew_every 5s", "renew_bound 4s", "stop_bound 10s", "claim_expiry 20s",
	} {
		if !strings.Contains(got.Stderr, named) {
			t.Fatalf("the refusal does not name %q:\n%s", named, got.Stderr)
		}
	}
	// Nothing was armed, which is the half the exit code alone does not say.
	if strings.Contains(got.Stdout, "armed") || strings.Contains(got.Stdout, "API on") {
		t.Fatalf("serve got as far as arming or listening:\n%s", got.Stdout)
	}
}
