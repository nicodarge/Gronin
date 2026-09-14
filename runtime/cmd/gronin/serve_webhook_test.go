package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

// T023, FR-305: this deployment has no ingress at all yet — --ingress-address does not
// exist until T054 — so a loaded webhook playbook refuses `serve` outright, before
// anything is armed, naming the playbook and the missing ingress. The same directory
// with the webhook playbook removed arms and serves normally, which is what makes the
// refusal about the webhook trigger and not about the directory.
func TestServeRefusesAWebhookPlaybookWithNoIngress(t *testing.T) {
	stateDir, playbooksDir := deploymentWithSource(t, webhookPlaybook)

	got := bintest.Run(t, "serve", "--state-dir", stateDir, "--agent", fakeagent.Build(t),
		"--api-address", "127.0.0.1:0")
	if got.ExitCode == 0 {
		t.Fatalf("serve armed a webhook playbook with no ingress: %q", got.Stdout)
	}
	if strings.Contains(got.Stdout, "armed") {
		t.Fatalf("it reported arming something: %q", got.Stdout)
	}
	if !strings.Contains(got.Stderr, "alert-triage") {
		t.Fatalf("the refusal does not name the playbook: %q", got.Stderr)
	}
	if !strings.Contains(got.Stderr, "ingress") {
		t.Fatalf("the refusal does not say what is missing: %q", got.Stderr)
	}

	// The same directory, the webhook playbook removed, arms and would serve — proving
	// the refusal above was about the trigger and not the directory. serve is killed at
	// cleanup once it has said so; nothing here sends it a delivery.
	if err := os.Remove(filepath.Join(playbooksDir, "book.yaml")); err != nil {
		t.Fatal(err)
	}
	process := bintest.Start(t, "serve", "--state-dir", stateDir, "--agent", fakeagent.Build(t),
		"--api-address", "127.0.0.1:0")
	line := process.Expect(t, "armed", 10*time.Second)
	if !strings.Contains(line, "armed 0 schedule(s) of 0 playbook(s)") {
		t.Fatalf("startup line = %q", line)
	}
}
