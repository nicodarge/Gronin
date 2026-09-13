// Fixture for TestGuardOnFixtures: both fakeagent and bintest imported under aliases,
// with the aliased fakeagent.Cleanup passed into the aliased bintest.Main. Never
// built — see alias_without_cleanup/main_test.go.
package aliasbintestpassedcleanup_test

import (
	"testing"

	bt "github.com/nicodarge/Gronin/runtime/internal/bintest"
	fa "github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func TestMain(m *testing.M) {
	bt.Main(m, fa.Cleanup)
}

func TestSomething(t *testing.T) {
	_ = bt.Run(t, "version", "--agent", fa.Build(t))
}
