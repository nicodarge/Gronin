package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func TestMain(m *testing.M) {
	bintest.Main(m)
}

func TestVersionPrintsSomething(t *testing.T) {
	// The agent is named explicitly. `version` reports the agent it found as well as its
	// own, so without this the test passes on a machine where the real executable is
	// installed and fails everywhere else — which is what it did, on the first CI run
	// after the agent line was added.
	got := bintest.Run(t, "version", "--agent", fakeagent.Build(t))

	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", got.ExitCode, got.Stderr)
	}
	lines := strings.Split(strings.TrimSpace(got.Stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("version printed %d lines, want its own and the agent's: %q", len(lines), got.Stdout)
	}
	if lines[0] == "" {
		t.Fatal("version printed nothing on stdout")
	}
	if lines[0] == "unknown" {
		t.Fatalf("version fell through to its last resort: build info carried no version")
	}
	// The runtime's own version says nothing about whether the bounding flags will be
	// applied, which is why the agent's is on the second line.
	if !strings.Contains(lines[1], "agent") || !strings.Contains(lines[1], "floor") {
		t.Fatalf("the agent line does not report what was found against the floor: %q", lines[1])
	}
}

// A deployment pointed at an executable that is not there says so plainly, rather than
// arming schedules that will each fail one by one.
func TestVersionRefusesAnAgentItCannotFind(t *testing.T) {
	got := bintest.Run(t, "version", "--agent", "/nonexistent/claude")

	if got.ExitCode == 0 {
		t.Fatalf("exit code 0 with no agent executable: %q", got.Stdout)
	}
	if !strings.Contains(got.Stdout, "not found") && !strings.Contains(got.Stderr, "not found") {
		t.Fatalf("stdout = %q, stderr = %q", got.Stdout, got.Stderr)
	}
}

func TestUnknownCommandIsRefused(t *testing.T) {
	got := bintest.Run(t, "definitely-not-a-command")

	if got.ExitCode == 0 {
		t.Fatalf("an unknown command exited 0; stdout = %q", got.Stdout)
	}
	if !strings.Contains(got.Stderr, "definitely-not-a-command") {
		t.Fatalf("the refusal does not name what was refused: %q", got.Stderr)
	}
}

// FR-020. The state directory comes from a flag, an environment variable and two
// fallbacks, and the precedence between them is the part that goes wrong silently — a
// deployment writing somewhere nobody expected looks like a deployment that lost its
// records.
func TestTheStateDirectoryResolvesInOrder(t *testing.T) {
	t.Run("the flag wins", func(t *testing.T) {
		got := bintest.Run(t, "state-dir", "--state-dir", "/srv/gronin")
		if strings.TrimSpace(got.Stdout) != "/srv/gronin" {
			t.Fatalf("state dir = %q", got.Stdout)
		}
	})

	t.Run("then the environment", func(t *testing.T) {
		t.Setenv("GRONIN_STATE_DIR", "/var/lib/gronin")
		got := bintest.Run(t, "state-dir")
		if strings.TrimSpace(got.Stdout) != "/var/lib/gronin" {
			t.Fatalf("state dir = %q", got.Stdout)
		}
	})

	t.Run("then XDG", func(t *testing.T) {
		t.Setenv("GRONIN_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "/home/someone/.state")
		got := bintest.Run(t, "state-dir")
		if want := "/home/someone/.state/gronin"; strings.TrimSpace(got.Stdout) != want {
			t.Fatalf("state dir = %q, want %q", got.Stdout, want)
		}
	})

	t.Run("and it is never empty", func(t *testing.T) {
		t.Setenv("GRONIN_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		got := bintest.Run(t, "state-dir")
		if strings.TrimSpace(got.Stdout) == "" {
			t.Fatal("no state directory was resolved at all")
		}
	})
}

// A configuration the deployment cannot read is a refusal an operator has to be able to
// act on, and `validate` used to exit non-zero with nothing on either stream.
//
// It happened because `validate` wraps what the gate returns in errSilent — the gate has
// already printed the playbook, the field and what would be accepted, so main must not
// print it again — and reading the configuration used to happen inside the gate. A
// failure that the gate never printed was silenced by the wrapper meant for the ones it
// had. Written as a binary-level test because the silence was main's, not the gate's.
func TestValidateSaysWhyItCannotReadTheConfiguration(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "config.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	playbooks := filepath.Join(state, "playbooks")
	if err := os.MkdirAll(playbooks, 0o750); err != nil {
		t.Fatal(err)
	}

	got := bintest.Run(t, "--state-dir", state, "validate", playbooks)

	if got.ExitCode == 0 {
		t.Fatalf("exit code = 0 on a configuration that does not parse; stdout = %q", got.Stdout)
	}
	if strings.TrimSpace(got.Stderr) == "" {
		t.Fatal("exited non-zero and said nothing; an operator has no way to know why")
	}
	if !strings.Contains(got.Stderr, "configuration") {
		t.Errorf("the refusal does not name what it could not read: %q", got.Stderr)
	}
}
