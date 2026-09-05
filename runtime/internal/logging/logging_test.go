package logging_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/logging"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/testsecret"
)

// SC-005's third surface. The store's boundary was asserted in Phase 2; this is the log,
// which is where a value goes when a stage has something to say about a failure — and a
// failure is usually the thing that failed to authenticate.
func TestNoSecretReachesTheLog(t *testing.T) {
	var out bytes.Buffer
	log := logging.New(&out, record.NewRedactor([]string{testsecret.Value}), slog.LevelDebug)

	log.Info("authenticating with " + testsecret.Value)
	log.Info("delivering", "webhook", testsecret.Value)
	log.Error("the sink refused", "err", errors.New("401 for "+testsecret.Value))
	log.With("credential", testsecret.Value).Info("with an attribute carried along")
	log.WithGroup("sink").Info("grouped", "url", testsecret.Value)

	if strings.Contains(out.String(), testsecret.Value) {
		t.Fatalf("a secret reached the log:\n%s", out.String())
	}
	if !strings.Contains(out.String(), record.Placeholder) {
		t.Fatalf("nothing was redacted at all, so the test proves nothing:\n%s", out.String())
	}
}

func TestTheLogStillSaysWhatHappened(t *testing.T) {
	var out bytes.Buffer
	log := logging.New(&out, record.NewRedactor([]string{"t0ken"}), slog.LevelInfo)

	log.Info("run finished", "playbook", "drift-check", "status", "succeeded")

	line := out.String()
	for _, want := range []string{"run finished", "drift-check", "succeeded"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line does not carry %q: %s", want, line)
		}
	}
}

func TestDiscardWritesNothing(t *testing.T) {
	logging.Discard().Info("nothing to see")
}
