package main

import (
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
)

func TestMain(m *testing.M) {
	bintest.Main(m)
}

func TestVersionPrintsSomething(t *testing.T) {
	got := bintest.Run(t, "version")

	if got.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", got.ExitCode, got.Stderr)
	}
	line := strings.TrimSpace(got.Stdout)
	if line == "" {
		t.Fatal("version printed nothing on stdout")
	}
	if line == "unknown" {
		t.Fatalf("version fell through to its last resort: build info carried no version")
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("version printed more than one line: %q", got.Stdout)
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
