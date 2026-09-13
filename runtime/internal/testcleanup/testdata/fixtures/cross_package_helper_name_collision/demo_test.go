// Fixture for TestGuardOnFixtures: TestMain calls a local, unqualified runHelper that
// does not clean up. An unrelated package sharing this directory (see helper.go,
// package demo — the common package/package_test split) happens to declare its own
// runHelper that does, and a guard that follows local calls by bare name alone,
// ignoring which package declared the caller, can be led to the wrong one. Never
// built — see ../alias_without_cleanup/main_test.go.
package demo_test

import (
	"os"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func TestMain(m *testing.M) {
	os.Exit(runHelper(m))
}

func runHelper(m *testing.M) int {
	return m.Run()
}

func TestSomething(t *testing.T) {
	_ = fakeagent.Build(t)
}
