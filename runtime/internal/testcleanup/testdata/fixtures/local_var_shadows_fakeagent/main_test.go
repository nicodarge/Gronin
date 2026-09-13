// Fixture for TestGuardOnFixtures: TestMain declares a local variable named
// "fakeagent" (an unrelated type with its own Cleanup method) that shadows the
// imported package of the same name within that scope — entirely legal Go, since
// nothing else in this function needs the package. A guard matching a call by its
// identifier's text against the file's import table, without checking whether that
// identifier actually refers to the import at this point, mistakes
// fakeagent.Cleanup() (the local variable's method) for the real one. Never built —
// see ../alias_without_cleanup/main_test.go.
package shadowedcleanup_test

import (
	"os"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

type notReallyFakeagent struct{}

func (notReallyFakeagent) Cleanup() {}

func TestMain(m *testing.M) {
	fakeagent := notReallyFakeagent{}
	fakeagent.Cleanup()
	os.Exit(m.Run())
}

func TestSomething(t *testing.T) {
	_ = fakeagent.Build(t)
}
