// Fixture for TestGuardOnFixtures — see helper.go. TestMain here never sees fakeagent
// imported at all, since the call is in a sibling non-test file; it must still be
// caught. Never built — see ../alias_without_cleanup/main_test.go.
package helperinnontestfile

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestSomething(t *testing.T) {
	_ = buildStub(t)
}
