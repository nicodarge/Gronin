package bintest

import (
	"os"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	defer fakeagent.Cleanup()
	return runAndCleanUp(m, nil)
}
