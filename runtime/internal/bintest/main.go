package bintest

import (
	"os"
	"testing"
)

// Main runs a package's tests, then removes the built executable and runs cleanups in
// order, then exits with the tests' code. A package using Build must call it from
// TestMain: the executable is shared by every test in the package, so it outlives any
// one t.TempDir(). Pass any other package's own cleanup (fakeagent.Cleanup, say) that
// this package's tests also need — a package building both through Build and through
// another package's own once-per-binary state has to arrange both removals itself,
// since neither package can see the other's build directory.
func Main(m *testing.M, cleanups ...func()) {
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	for _, cleanup := range cleanups {
		cleanup()
	}
	os.Exit(code)
}
