// See demo_test.go: an unrelated package's own runHelper, never called by demo_test's
// TestMain, that happens to share the name and does clean up.
package demo

import "github.com/nicodarge/Gronin/runtime/internal/fakeagent"

func runHelper(m int) int {
	defer fakeagent.Cleanup()
	return m
}
