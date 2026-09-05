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
