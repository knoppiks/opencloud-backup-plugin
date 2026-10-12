package publicapi

import (
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const repoRoot = "../.."

func TestPublicPackagesExposeNoInternalTypes(t *testing.T) {
	public := goList(t, "./pkg/...")
	if len(public) == 0 {
		t.Fatal("no packages under pkg/")
	}
	imp := exportImporter(t, "./pkg/...")
	for _, path := range public {
		for _, leak := range internalTypesIn(t, imp, path) {
			t.Errorf("%s: %s", path, leak)
		}
	}
}

// A control: an internal package whose API does name other internal types
// must be reported, or the check above could pass by finding nothing at all.
func TestTheCheckFindsInternalTypes(t *testing.T) {
	const pkg = "./internal/takeout/remote"
	path := goList(t, pkg)[0]
	if leaks := internalTypesIn(t, exportImporter(t, pkg), path); len(leaks) == 0 {
		t.Fatalf("%s names internal/snapshot and internal/objstore types in its API, but none were found", path)
	}
}

// internalTypesIn lists every exported identifier of the package at path whose
// API mentions a type declared under an internal/ directory.
func internalTypesIn(t *testing.T, imp types.Importer, path string) []string {
	t.Helper()
	pkg, err := imp.Import(path)
	if err != nil {
		t.Fatalf("import %s: %v", path, err)
	}

	var leaks []string
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		w := walker{seen: map[types.Type]bool{}}
		w.object(obj)
		for _, found := range w.found {
			leaks = append(leaks, name+" uses "+found)
		}
	}
	return leaks
}

// walker collects the internal named types reachable through a declaration's
// API: signatures, exported fields, exported methods, element types.
type walker struct {
	seen  map[types.Type]bool
	found []string
}

func (w *walker) object(obj types.Object) {
	switch obj := obj.(type) {
	case *types.TypeName:
		named, ok := obj.Type().(*types.Named)
		if !ok { // an alias
			w.typ(obj.Type())
			return
		}
		w.typ(named.Underlying())
		for m := range named.Methods() {
			if m.Exported() {
				w.typ(m.Type())
			}
		}
	default: // *types.Func, *types.Var, *types.Const
		w.typ(obj.Type())
	}
}

func (w *walker) typ(t types.Type) {
	if t == nil || w.seen[t] {
		return
	}
	w.seen[t] = true

	switch t := t.(type) {
	case *types.Named:
		if pkg := t.Obj().Pkg(); pkg != nil && isInternal(pkg.Path()) {
			name := pkg.Path() + "." + t.Obj().Name()
			if !slices.Contains(w.found, name) {
				w.found = append(w.found, name)
			}
		}
		for arg := range t.TypeArgs().Types() {
			w.typ(arg)
		}
	case *types.Alias:
		w.typ(types.Unalias(t))
	case *types.Pointer:
		w.typ(t.Elem())
	case *types.Slice:
		w.typ(t.Elem())
	case *types.Array:
		w.typ(t.Elem())
	case *types.Chan:
		w.typ(t.Elem())
	case *types.Map:
		w.typ(t.Key())
		w.typ(t.Elem())
	case *types.Signature:
		w.tuple(t.Params())
		w.tuple(t.Results())
	case *types.Struct:
		for f := range t.Fields() {
			if f.Exported() || f.Embedded() {
				w.typ(f.Type())
			}
		}
	case *types.Interface:
		for m := range t.Methods() {
			if m.Exported() {
				w.typ(m.Type())
			}
		}
		for e := range t.EmbeddedTypes() {
			w.typ(e)
		}
	}
}

func (w *walker) tuple(tup *types.Tuple) {
	for v := range tup.Variables() {
		w.typ(v.Type())
	}
}

// isInternal reports whether an import path has an internal/ element, which
// is what makes it unimportable from another module.
func isInternal(path string) bool {
	return slices.Contains(strings.Split(path, "/"), "internal")
}

// exportImporter type-checks nothing itself: it reads the export data the go
// command compiled for pattern and all its dependencies.
func exportImporter(t *testing.T, pattern string) types.Importer {
	t.Helper()
	exports := map[string]string{}
	for _, line := range goList(t, "-export", "-deps", "-f", "{{.ImportPath}}\t{{.Export}}", pattern) {
		path, file, _ := strings.Cut(line, "\t")
		if file != "" {
			exports[path] = file
		}
	}
	return importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return os.Open(file)
	})
}

// goList runs `go list` in the repository root and returns its output lines.
// It skips the test without a go toolchain, since then the question cannot be
// asked.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	cmd := exec.Command(goBin, append([]string{"list"}, args...)...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}
