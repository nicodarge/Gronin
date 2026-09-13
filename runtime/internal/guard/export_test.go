package guard

import "testing"

// OnProbe sets what alive calls while it holds a dead instance's lock, until the test ends.
func OnProbe(t testing.TB, hook func(id string)) {
	probed = hook
	t.Cleanup(func() { probed = nil })
}
