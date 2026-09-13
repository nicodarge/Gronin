// Fixture for TestGuardOnFixtures: the fakeagent.Build call sits in a non-test helper
// file, not in a _test.go file, so a guard that only parses _test.go files never sees
// it. Never built — see ../alias_without_cleanup/main_test.go.
package helperinnontestfile

import (
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func buildStub(t *testing.T) string {
	return fakeagent.Build(t)
}
