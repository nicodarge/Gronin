package ingress_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath      = "github.com/nicodarge/Gronin/runtime"
	ingressPath     = modulePath + "/internal/ingress"
	apiPath         = modulePath + "/internal/api"
	recordPath      = modulePath + "/internal/record"
	ingresstestPath = ingressPath + "/ingresstest"
)

// TestTheIngressAndTheAPIShareOnlyTheRecord parses the imports of every non-test file in
// internal/ingress and internal/api with go/parser and asserts neither imports the
// other, and that no non-test file anywhere under runtime/ imports ingresstest. Two
// packages serving the two route sets are what keep a future change from putting them
// back behind one listener without anyone deciding to (plan.md, *Project Structure*).
func TestTheIngressAndTheAPIShareOnlyTheRecord(t *testing.T) {
	root := runtimeRoot(t)

	ingressFiles, ingressImports := nonTestImports(t, filepath.Join(root, "internal", "ingress"))
	apiFiles, apiImports := nonTestImports(t, filepath.Join(root, "internal", "api"))

	// A check over nothing passes: if either package parsed no file, the assertions
	// below would hold vacuously and prove nothing.
	if ingressFiles == 0 {
		t.Fatal("no non-test file was parsed in internal/ingress")
	}
	if apiFiles == 0 {
		t.Fatal("no non-test file was parsed in internal/api")
	}
	if !apiImports[recordPath] {
		t.Fatal("internal/api's import of internal/record was never seen; the check over nothing would pass")
	}

	if ingressImports[apiPath] {
		t.Error("a non-test file in internal/ingress imports internal/api")
	}
	if apiImports[ingressPath] {
		t.Error("a non-test file in internal/api imports internal/ingress")
	}

	ingresstestDir := filepath.Join(root, "internal", "ingress", "ingresstest")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// ingresstest importing its own package, if it ever does, is not the thing
		// under test; every other non-test file is.
		if strings.HasPrefix(path, ingresstestDir+string(filepath.Separator)) {
			return nil
		}
		if fileImports(t, path)[ingresstestPath] {
			t.Errorf("%s imports ingresstest outside a _test.go file", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}

// runtimeRoot is the runtime module's root directory: three levels above this file
// (ingress/imports_test.go -> ingress -> internal -> runtime), read from this file's own
// path rather than the process's working directory, which go test sets per package and
// this test does not need to depend on.
func runtimeRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not read this test's own file path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

// nonTestImports returns how many non-test .go files sit directly in dir and the union
// of every import path found among them.
func nonTestImports(t *testing.T, dir string) (files int, imports map[string]bool) {
	t.Helper()
	imports = map[string]bool{}
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		files++
		for imp := range fileImports(t, path) {
			imports[imp] = true
		}
	}
	return files, imports
}

// fileImports parses only the import block of the Go source file at path.
func fileImports(t *testing.T, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, imp := range file.Imports {
		unquoted, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("import path in %s: %v", path, err)
		}
		out[unquoted] = true
	}
	return out
}
