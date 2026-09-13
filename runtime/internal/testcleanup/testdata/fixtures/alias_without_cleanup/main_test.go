// Fixture for TestGuardOnFixtures: fakeagent imported under an alias, and never
// cleaned up. Never built — go/parser only checks syntax, and testdata/ is excluded
// from every real build and test run anyway.
package aliaswithoutcleanup_test

import (
	"os"
	"testing"

	fa "github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestSomething(t *testing.T) {
	_ = fa.Build(t)
}
