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
//
// The removal and the cleanups run from a defer around m.Run() alone, in the helper
// below, and not around the call to os.Exit: os.Exit never returns, so anything
// deferred in the same function that calls it would never run at all, on any path.
// Deferring around m.Run() instead still lets a panic that unwinds THIS goroutine —
// m.Run() itself, or setup before it returns — run this cleanup on the way up, with
// nothing here recovering it, so the panic keeps propagating afterwards.
//
// It does not reach a panic from inside an actual Test function: go test runs each one
// in its own goroutine (testing.tRunner), and an unrecovered panic there crashes the
// whole process immediately — Go gives no other goroutine, this one included, a chance
// to run its deferred functions first. That gap pre-dates this defer and is not one it
// closes; verified with a forced panic inside a Test function, which still leaked both
// build directories.
func Main(m *testing.M, cleanups ...func()) {
	os.Exit(runAndCleanUp(m, cleanups))
}

func runAndCleanUp(m *testing.M, cleanups []func()) (code int) {
	defer func() {
		if buildDir != "" {
			_ = os.RemoveAll(buildDir)
		}
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()
	return m.Run()
}
