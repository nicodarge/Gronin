package testcleanup_test

import (
	"path/filepath"
	"testing"
)

// TestGuardOnFixtures runs analyzeDir and verdict — the same logic
// TestATestMainCleansUpEverythingItsPackageBuilds applies to every real package —
// against fixtures under testdata/fixtures, each built to exercise one shape the guard
// has to get right: an aliased import, a dot import, a call sitting in a non-test
// helper file, a same-named helper declared in an unrelated package sharing the
// directory, and a local declaration shadowing the fakeagent import — refused (no
// cleanup arranged, or resolved to the wrong declaration) and accepted (cleanup
// arranged, in each recognised shape) versions of each.
func TestGuardOnFixtures(t *testing.T) {
	root := runtimeRoot(t)
	fixtures := filepath.Join(root, "internal", "testcleanup", "testdata", "fixtures")

	cases := []struct {
		name        string
		dir         string
		wantRefused bool
	}{
		{
			name:        "an aliased fakeagent import with no cleanup is refused",
			dir:         "alias_without_cleanup",
			wantRefused: true,
		},
		{
			name:        "a dot-imported fakeagent is refused outright, cleanup or not",
			dir:         "dot_import_without_cleanup",
			wantRefused: true,
		},
		{
			name:        "an aliased fakeagent import cleaned up directly is accepted",
			dir:         "alias_with_direct_cleanup",
			wantRefused: false,
		},
		{
			name:        "an aliased fakeagent.Cleanup passed to an aliased bintest.Main is accepted",
			dir:         "alias_bintest_passed_cleanup",
			wantRefused: false,
		},
		{
			name:        "a fakeagent.Build call in a non-test helper file is still seen",
			dir:         "helper_in_nontest_file",
			wantRefused: true,
		},
		{
			name:        "fakeagent.Cleanup deferred in a helper TestMain calls is accepted",
			dir:         "helper_defers_cleanup",
			wantRefused: false,
		},
		{
			name:        "a same-named helper in an unrelated package sharing the directory is not followed",
			dir:         "cross_package_helper_name_collision",
			wantRefused: true,
		},
		{
			name:        "a local variable shadowing the fakeagent import is not mistaken for it",
			dir:         "local_var_shadows_fakeagent",
			wantRefused: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(fixtures, c.dir)
			pkg := analyzeDir(t, dir)
			msgs := verdict(dir, pkg)

			switch {
			case c.wantRefused && len(msgs) == 0:
				t.Fatalf("%s: expected the guard to refuse this fixture, it passed silently", dir)
			case !c.wantRefused && len(msgs) != 0:
				t.Fatalf("%s: expected the guard to accept this fixture, it reported: %v", dir, msgs)
			}
		})
	}
}
