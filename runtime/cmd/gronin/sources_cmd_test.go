package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/testsecret"
)

func TestSourcesListSaysWhenNothingIsConfigured(t *testing.T) {
	got := bintest.Run(t, "--state-dir", t.TempDir(), "sources", "list")

	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", got.ExitCode, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "no sources") {
		t.Fatalf("stdout = %q", got.Stdout)
	}
}

// T013, SC-316: the sentinel is set with `gronin config set --secret` on standard input,
// sources.json references it, and `gronin sources list` names every source and holds no
// sentinel. The same value given as an argument is refused, which the runtime core
// already does; asserted here too so SC-316 fails if that ever regresses —
// scripts/run-suite.sh's scan of the suite's output for the sentinel covers every other
// line, but not a bug in this command's own refusal path.
func TestSourcesAreListedWithoutTheirSecret(t *testing.T) {
	state := t.TempDir()

	set := bintest.RunWithStdin(t, testsecret.Value,
		"--state-dir", state, "config", "set", "--secret", "alerts_hook_secret")
	if set.ExitCode != 0 {
		t.Fatalf("config set exit code = %d, stderr = %q", set.ExitCode, set.Stderr)
	}

	document := `{
	  "alerts": {
	    "secret": "${config.alerts_hook_secret}",
	    "signature_header": "X-Grafana-Alerting-Signature"
	  },
	  "forge": {
	    "secret": "${config.alerts_hook_secret}",
	    "signature_header": "X-Forgejo-Signature",
	    "identity": "/delivery_uuid",
	    "replay_window": "30m"
	  }
	}`
	if err := os.WriteFile(filepath.Join(state, "sources.json"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	got := bintest.Run(t, "--state-dir", state, "sources", "list")
	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", got.ExitCode, got.Stderr)
	}
	// The exact layout contracts/cli.md documents — not merely a substring match, which
	// "window 30m0s" would also satisfy.
	want := "alerts   header X-Grafana-Alerting-Signature   identity digest of the body   window 10m\n" +
		"forge    header X-Forgejo-Signature            identity /delivery_uuid       window 30m\n"
	if got.Stdout != want {
		t.Fatalf("stdout =\n%q\nwant:\n%q", got.Stdout, want)
	}
	if strings.Contains(got.Stdout, testsecret.Value) {
		t.Fatalf("the secret is in the listing: %q", got.Stdout)
	}

	// The same value given as an argument is refused (the runtime core's own rule).
	direct := bintest.Run(t, "--state-dir", state, "config", "set", "--secret",
		"alerts_hook_secret", testsecret.Value)
	if direct.ExitCode == 0 {
		t.Fatalf("a secret given on the command line was accepted")
	}
	if strings.Contains(direct.Stdout, testsecret.Value) || strings.Contains(direct.Stderr, testsecret.Value) {
		t.Fatalf("the refusal echoed the secret: stdout=%q stderr=%q", direct.Stdout, direct.Stderr)
	}
}
