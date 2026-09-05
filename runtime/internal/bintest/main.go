package bintest

import (
	"os"
	"testing"
)

// Main runs a package's tests and removes the built executable afterwards. A package
// using Build must call it from TestMain: the executable is shared by every test in
// the package, so it outlives any one t.TempDir().
func Main(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}
