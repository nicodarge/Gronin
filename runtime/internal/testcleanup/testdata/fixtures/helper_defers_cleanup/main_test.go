// Fixture for TestGuardOnFixtures: the actual shape this repository's own TestMains
// use — a helper function TestMain calls, which defers the cleanup around m.Run(),
// since os.Exit never returns and a defer in TestMain's own body would never run on the
// ordinary, non-panicking path. Never built — see
// ../alias_without_cleanup/main_test.go.
package helperdeferscleanup_test

import (
	"os"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func TestMain(m *testing.M) {
	os.Exit(runAndCleanUp(m))
}

func runAndCleanUp(m *testing.M) (code int) {
	defer fakeagent.Cleanup()
	return m.Run()
}

func TestSomething(t *testing.T) {
	_ = fakeagent.Build(t)
}
