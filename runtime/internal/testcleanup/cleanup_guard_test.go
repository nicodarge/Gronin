// Package testcleanup holds a guard test with no non-test file of its own: it parses
// every other package's .go files with go/parser and asserts that a package building
// through fakeagent's or bintest's once-per-binary state also arranges to remove what
// it built, so a leaked build directory (like the one cmd/gronin left behind before
// TestMain wired fakeagent.Cleanup into bintest.Main) is a test failure rather than a
// stale directory in /tmp.
package testcleanup_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The two packages this guard polices, by import path rather than by the identifier a
// file happens to call them through: a file can alias either ("fa", "bt", anything) or
// dot-import it, and an identifier match alone would silently miss both — which is
// exactly how a previous version of this guard passed on an aliased or dot-imported
// call that arranged no cleanup at all.
const (
	fakeagentImportPath = "github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	bintestImportPath   = "github.com/nicodarge/Gronin/runtime/internal/bintest"
)

var fakeagentUsageFuncs = map[string]bool{"Build": true, "Wrapped": true}
var bintestUsageFuncs = map[string]bool{"Build": true, "Run": true, "RunWithStdin": true, "Start": true}

// TestATestMainCleansUpEverythingItsPackageBuilds walks every package under runtime/
// with at least one _test.go file (testdata/ fixtures aside — those are inputs to
// TestGuardOnFixtures below, not real packages), and for one whose .go files call
// fakeagent.Build, fakeagent.Wrapped, bintest.Build, bintest.Run, bintest.RunWithStdin
// or bintest.Start under any name, checks that its TestMain removes what was built:
// bintest usage needs a TestMain that calls bintest.Main, and fakeagent usage needs
// fakeagent.Cleanup called directly in TestMain (or in a local helper function TestMain
// calls, however many levels deep — os.Exit never returns, so the only way a deferred
// cleanup runs at all on the ordinary path is a defer inside a helper TestMain calls,
// never a defer in TestMain's own body) or passed to bintest.Main as one of its
// cleanups.
func TestATestMainCleansUpEverythingItsPackageBuilds(t *testing.T) {
	root := runtimeRoot(t)

	dirs := testDirs(t, root)
	if len(dirs) == 0 {
		t.Fatal("walked no directory containing a _test.go file under runtime/; the check below would pass vacuously")
	}

	var sawFakeagentUsage, sawBintestUsage bool
	for _, dir := range dirs {
		pkg := analyzeDir(t, dir)

		if pkg.usesFakeagent {
			sawFakeagentUsage = true
		}
		if pkg.usesBintest {
			sawBintestUsage = true
		}

		for _, msg := range verdict(dir, pkg) {
			t.Error(msg)
		}
	}

	// A check that never saw the shapes it exists to police would pass on any input,
	// fakeagent/bintest usage included or not.
	if !sawFakeagentUsage {
		t.Fatal("no package's .go files were seen calling fakeagent.Build/Wrapped; the guard above never ran")
	}
	if !sawBintestUsage {
		t.Fatal("no package's .go files were seen calling bintest.Build/Run/Start; the guard above never ran")
	}
}

// verdict reports every way dir's package fails the guard, or nil if it does not. It is
// shared between the whole-repository walk above, which turns each message into a
// t.Error, and the fixture table below, which instead asserts on the message count —
// both exercise the identical logic.
func verdict(dir string, pkg pkgFacts) []string {
	// A dot import of either package makes an unqualified call indistinguishable from
	// any other identifier in the file; this guard cannot resolve it and refuses the
	// package outright rather than silently missing whatever it dot-imported for.
	if len(pkg.dotImportViolations) > 0 {
		return pkg.dotImportViolations
	}
	if !pkg.usesFakeagent && !pkg.usesBintest {
		return nil
	}
	if pkg.testMain == nil {
		return []string{dir + ": calls fakeagent.Build/Wrapped or bintest.Build/Run/Start but declares no TestMain"}
	}

	main := inspectTestMain(pkg)
	var msgs []string
	if pkg.usesBintest && !main.callsBintestMain {
		msgs = append(msgs, dir+": calls bintest.Build/Run/Start but its TestMain never calls bintest.Main, "+
			"so the built executable is never removed")
	}
	if pkg.usesFakeagent && !main.cleansUpFakeagent {
		msgs = append(msgs, dir+": calls fakeagent.Build/Wrapped but its TestMain (nor any local helper it "+
			"calls) neither calls fakeagent.Cleanup directly nor passes it to bintest.Main, so the built "+
			"stub is never removed")
	}
	return msgs
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
// file, skipping any directory named testdata: this guard's own fixtures live under
// one, and every fixture is deliberately either broken or built purely to be parsed —
// neither is a real package for the walk to hold to its own standard.
func testDirs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var dirs []string
	err := filepath.WalkDir(root, func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !isTestFile(fpath) {
			return nil
		}
		dir := filepath.Dir(fpath)
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

func isTestFile(fpath string) bool {
	return strings.HasSuffix(fpath, "_test.go")
}

// pkgFacts is what one directory's .go files say about fakeagent/bintest usage: the
// TestMain declaration (if any, only ever legal in a _test.go file) and, for resolving
// every call reachable from it, every package-level function declared anywhere in the
// directory together with the import names visible in the file that declares it — a
// file's import aliases apply only within that file.
type pkgFacts struct {
	usesFakeagent       bool
	usesBintest         bool
	testMain            *ast.FuncDecl
	dotImportViolations []string
	funcs               map[string]*ast.FuncDecl
	namesOf             map[*ast.FuncDecl]map[string]string
}

// analyzeDir parses every .go file directly in dir — non-test files included, since a
// package can call fakeagent.Build from a test-support helper that does not itself end
// in _test.go, only Test functions do — and reports what it found.
func analyzeDir(t *testing.T, dir string) pkgFacts {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}

	facts := pkgFacts{
		funcs:   map[string]*ast.FuncDecl{},
		namesOf: map[*ast.FuncDecl]map[string]string{},
	}
	fset := token.NewFileSet()
	for _, fpath := range entries {
		file, err := parser.ParseFile(fset, fpath, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", fpath, err)
		}

		names, dotImports := importNames(t, fpath, file)
		for _, importPath := range dotImports {
			facts.dotImportViolations = append(facts.dotImportViolations,
				fpath+": dot-imports "+importPath+
					"; this guard cannot tell an unqualified call to it apart from any other "+
					"identifier in the file — import it under a name instead")
		}

		if callsAny(file, names, fakeagentImportPath, fakeagentUsageFuncs) {
			facts.usesFakeagent = true
		}
		if callsAny(file, names, bintestImportPath, bintestUsageFuncs) {
			facts.usesBintest = true
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			facts.funcs[fn.Name.Name] = fn
			facts.namesOf[fn] = names
			if isTestFile(fpath) && fn.Name.Name == "TestMain" {
				facts.testMain = fn
			}
		}
	}
	return facts
}

// importNames maps each file-local import name to the import path it resolves to — the
// default (the import path's last segment) unless the file names it explicitly — and
// separately reports every import path among fakeagentImportPath/bintestImportPath that
// the file dot-imports, which resolution cannot help with at all.
func importNames(t *testing.T, fpath string, file *ast.File) (names map[string]string, dotImportsOfTargets []string) {
	t.Helper()
	names = map[string]string{}
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("import path in %s: %v", fpath, err)
		}
		local := path.Base(importPath)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		switch local {
		case "_":
			// A blank import cannot be called through, so it cannot be the usage or
			// the cleanup this guard looks for.
			continue
		case ".":
			if importPath == fakeagentImportPath || importPath == bintestImportPath {
				dotImportsOfTargets = append(dotImportsOfTargets, importPath)
			}
		default:
			names[local] = importPath
		}
	}
	return names, dotImportsOfTargets
}

// callsAny reports whether file calls importPath.name(...) for some name in funcNames,
// under whatever local identifier names resolves that import to.
func callsAny(file *ast.File, names map[string]string, importPath string, funcNames map[string]bool) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		sel, ok := selectorCall(n)
		if !ok {
			return true
		}
		if names[sel.pkg] == importPath && funcNames[sel.name] {
			found = true
		}
		return true
	})
	return found
}

// selectorCall reports the package identifier and method name of n, if n is a call of
// the shape pkg.Name(...).
func selectorCall(n ast.Node) (sel struct{ pkg, name string }, ok bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return sel, false
	}
	selExpr, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return sel, false
	}
	pkgIdent, ok := selExpr.X.(*ast.Ident)
	if !ok {
		return sel, false
	}
	return struct{ pkg, name string }{pkgIdent.Name, selExpr.Sel.Name}, true
}

// testMainFacts is what TestMain, and every local helper function it reaches (however
// many calls deep), arranges for cleanup.
type testMainFacts struct {
	callsBintestMain  bool
	cleansUpFakeagent bool
}

// inspectTestMain walks pkg.testMain's body, and the body of every package-level
// function it calls unqualified (transitively, tracking what has already been visited
// so a cycle cannot loop forever), resolving each call through the import names of
// whichever file declared the function being inspected. It looks for the calls that
// remove a built executable: a direct fakeagent.Cleanup() call, or a bintest.Main(...)
// call — noting whether fakeagent.Cleanup was itself passed to it as one of its
// cleanups. Following local calls is what lets the required shape pass: TestMain
// calling os.Exit(runTests(m)), with runTests deferring the cleanup around m.Run() —
// os.Exit never returns, so a defer in TestMain's own body would never run at all, and
// every real TestMain in this repository defers the cleanup in a helper for exactly
// that reason.
func inspectTestMain(pkg pkgFacts) testMainFacts {
	var facts testMainFacts
	visited := map[*ast.FuncDecl]bool{}
	var queue []*ast.FuncDecl
	queue = append(queue, pkg.testMain)

	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		if fn == nil || fn.Body == nil || visited[fn] {
			continue
		}
		visited[fn] = true
		names := pkg.namesOf[fn]

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				pkgIdent, ok := fun.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case names[pkgIdent.Name] == fakeagentImportPath && fun.Sel.Name == "Cleanup":
					facts.cleansUpFakeagent = true
				case names[pkgIdent.Name] == bintestImportPath && fun.Sel.Name == "Main":
					facts.callsBintestMain = true
					for _, arg := range call.Args {
						argSel, ok := arg.(*ast.SelectorExpr)
						if !ok {
							continue
						}
						argPkgIdent, ok := argSel.X.(*ast.Ident)
						if ok && names[argPkgIdent.Name] == fakeagentImportPath && argSel.Sel.Name == "Cleanup" {
							facts.cleansUpFakeagent = true
						}
					}
				}
			case *ast.Ident:
				// An unqualified call: if it names a function declared elsewhere in
				// this same directory, follow it too — a deferred cleanup commonly
				// lives in a small helper TestMain calls, not in TestMain itself.
				if callee, ok := pkg.funcs[fun.Name]; ok {
					queue = append(queue, callee)
				}
			}
			return true
		})
	}
	return facts
}
