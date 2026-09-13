// Fixture for TestGuardOnFixtures: fakeagent dot-imported, so Build appears as a bare
// identifier the guard cannot resolve — it must refuse this outright rather than pass
// it silently. Never built — see alias_without_cleanup/main_test.go.
package dotimportwithoutcleanup_test

import (
	"os"
	"testing"

	. "github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestSomething(t *testing.T) {
	_ = Build(t)
}
