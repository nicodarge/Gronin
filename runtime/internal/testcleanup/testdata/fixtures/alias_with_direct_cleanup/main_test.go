// Fixture for TestGuardOnFixtures: fakeagent imported under an alias, cleaned up
// directly in TestMain. Never built — see alias_without_cleanup/main_test.go.
package aliaswithdirectcleanup_test

import (
	"os"
	"testing"

	fa "github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

func TestMain(m *testing.M) {
	code := m.Run()
	fa.Cleanup()
	os.Exit(code)
}

func TestSomething(t *testing.T) {
	_ = fa.Build(t)
}
