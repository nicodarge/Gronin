// Package testcleanup holds a guard test with no non-test file of its own: it parses
// every other package's _test.go files with go/parser and asserts that a package
// building through fakeagent's or bintest's once-per-binary state also arranges to
// remove what it built, so a leaked build directory (like the one cmd/gronin left
// behind before TestMain wired fakeagent.Cleanup into bintest.Main) is a test failure
// rather than a stale directory in /tmp.
package testcleanup_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"testing"
)

// TestATestMainCleansUpEverythingItsPackageBuilds walks every package under runtime/,
// and for one whose _test.go files call fakeagent.Build, fakeagent.Wrapped,
// bintest.Build, bintest.Run, bintest.RunWithStdin or bintest.Start, checks that its
// TestMain removes what was built: bintest.Build/Run/Start need a TestMain that calls
// bintest.Main, and fakeagent.Build/Wrapped need fakeagent.Cleanup called directly in
// TestMain or passed to bintest.Main as one of its cleanups.
func TestATestMainCleansUpEverythingItsPackageBuilds(t *testing.T) {
	root := runtimeRoot(t)

	dirs := testDirs(t, root)
	if len(dirs) == 0 {
		t.Fatal("walked no directory containing a _test.go file under runtime/; the check below would pass vacuously")
	}

	var sawFakeagentUsage, sawBintestUsage bool
	for _, dir := range dirs {
		pkg := parseTestFiles(t, dir)

		if pkg.usesFakeagent {
			sawFakeagentUsage = true
		}
		if pkg.usesBintest {
			sawBintestUsage = true
		}

		if !pkg.usesFakeagent && !pkg.usesBintest {
			continue
		}
		if pkg.testMain == nil {
			t.Errorf("%s: calls fakeagent.Build/Wrapped or bintest.Build/Run/Start but declares no TestMain", dir)
			continue
		}

		main := inspectTestMain(pkg.testMain)

		if pkg.usesBintest && !main.callsBintestMain {
			t.Errorf("%s: calls bintest.Build/Run/Start but its TestMain never calls bintest.Main, "+
				"so the built executable is never removed", dir)
		}
		if pkg.usesFakeagent && !main.cleansUpFakeagent {
			t.Errorf("%s: calls fakeagent.Build/Wrapped but its TestMain neither calls "+
				"fakeagent.Cleanup directly nor passes it to bintest.Main, so the built stub "+
				"is never removed", dir)
		}
	}

	// A check that never saw the shapes it exists to police would pass on any input,
	// fakeagent/bintest usage included or not.
	if !sawFakeagentUsage {
		t.Fatal("no package's _test.go files were seen calling fakeagent.Build/Wrapped; the guard above never ran")
	}
	if !sawBintestUsage {
		t.Fatal("no package's _test.go files were seen calling bintest.Build/Run/Start; the guard above never ran")
	}
}

// runtimeRoot is the runtime module's root directory: two levels above this file
// (testcleanup/cleanup_guard_test.go -> testcleanup -> internal -> runtime), read from
// this file's own path rather than the process's working directory, which go test sets
// per package and this test does not need to depend on.
func runtimeRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not read this test's own file path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

// testDirs returns every directory under root that contains at least one _test.go
// file.
func testDirs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isTestFile(path) {
			return nil
		}
		dir := filepath.Dir(path)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return dirs
}

func isTestFile(path string) bool {
	return filepath.Ext(path) == ".go" && len(path) > len("_test.go") &&
		path[len(path)-len("_test.go"):] == "_test.go"
}

// pkgFacts is what one directory's _test.go files say about fakeagent/bintest usage
// and, if present, its TestMain declaration.
type pkgFacts struct {
	usesFakeagent bool
	usesBintest   bool
	testMain      *ast.FuncDecl
}

// parseTestFiles parses every _test.go file directly in dir and reports what it found.
func parseTestFiles(t *testing.T, dir string) pkgFacts {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}

	var facts pkgFacts
	fset := token.NewFileSet()
	for _, path := range entries {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case pkgIdent.Name == "fakeagent" && (sel.Sel.Name == "Build" || sel.Sel.Name == "Wrapped"):
				facts.usesFakeagent = true
			case pkgIdent.Name == "bintest" &&
				(sel.Sel.Name == "Build" || sel.Sel.Name == "Run" ||
					sel.Sel.Name == "RunWithStdin" || sel.Sel.Name == "Start"):
				facts.usesBintest = true
			}
			return true
		})

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "TestMain" {
				facts.testMain = fn
			}
		}
	}
	return facts
}

// testMainFacts is what one TestMain's body arranges for cleanup.
type testMainFacts struct {
	callsBintestMain  bool
	cleansUpFakeagent bool
}

// inspectTestMain walks a TestMain's body for the calls that remove a built
// executable: a direct fakeagent.Cleanup() call, or a bintest.Main(...) call — noting
// whether fakeagent.Cleanup was itself passed to it as one of its cleanups.
func inspectTestMain(fn *ast.FuncDecl) testMainFacts {
	var facts testMainFacts
	if fn.Body == nil {
		return facts
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case pkgIdent.Name == "fakeagent" && sel.Sel.Name == "Cleanup":
			facts.cleansUpFakeagent = true
		case pkgIdent.Name == "bintest" && sel.Sel.Name == "Main":
			facts.callsBintestMain = true
			for _, arg := range call.Args {
				argSel, ok := arg.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				argPkgIdent, ok := argSel.X.(*ast.Ident)
				if ok && argPkgIdent.Name == "fakeagent" && argSel.Sel.Name == "Cleanup" {
					facts.cleansUpFakeagent = true
				}
			}
		}
		return true
	})
	return facts
}
