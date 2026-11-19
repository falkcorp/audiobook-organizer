// file: internal/maintenance/jobs/taglib_variants_test.go
// version: 1.0.0
// guid: 5a0d9c7e-2f41-4b86-9d3a-c1e8f7b20a64
// last-edited: 2026-11-19

package jobs_test

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// What this guards. The write-set oracle walks the build the jobs package gets
// WITHOUT native_taglib (internal/metadata/taglib_support.go), because the
// production build (native_taglib, taglib_cgo.go) imports "C" and cannot be loaded
// without the vendored static libraries and a C toolchain. Everything the oracle
// concludes about scan-composer-tags and every other job that writes tags is
// therefore a conclusion about the variant that does not ship, unless the two
// reach the same write helpers. They are two implementations of one set of
// functions, written separately: one calls tagger.WriteTagsSafe, the other calls
// fileops.WriteTagsSafe, tagger.ResolvePathForWrite and deps.VerifyWritten
// itself. This test reads taglib_cgo.go as syntax (no type-check, so no cgo) and
// compares what each variant's write functions reach.

const metadataDir = "../../metadata"

// taglibWriteEntries are the functions both variants define that write a file.
var taglibWriteEntries = []string{
	"writeMetadataWithTaglib", "writeMetadataWithTaglibInPlace",
	"writeSingleTagWithTaglibVia", "writePropertiesWithTaglib",
}

// taglibHelper is a write helper in the module's other packages, named
// "<package>.<function>" or "<package>.<Type>.<method>".
type taglibHelper string

// taglibWriteHelpers are the helpers whose presence or absence makes a tag write
// safe or not: the protected-path pre-flight (resolvePath, which tagger's exported
// ResolvePathForWrite and its own WriteTagsSafe both call), the
// temp-copy-and-rename file write, the read-back verification, and the recording
// of the written file's hashes. A variant's own raw tag write (tagger.WriteTagsInPlace,
// or the native TagLib call) is not on the list: it is the one thing the two
// variants implement differently on purpose.
var taglibWriteHelpers = []taglibHelper{
	"tagger.resolvePath", "fileops.WriteTagsSafe", "tagger.SafeWriteDeps.VerifyWritten", "fileops.HashOptionsForPath",
}

// taglibSafeWriters are the entry points that run the whole safe write; the
// in-place writer is the caller-already-holds-a-temp-copy contract and reaches
// none of the helpers.
var taglibSafeWriters = []string{"writeMetadataWithTaglib", "writeSingleTagWithTaglibVia", "writePropertiesWithTaglib"}

// helperOf names the helper an SSA function is, or "".
func helperOf(f *ssa.Function) taglibHelper {
	if f == nil || f.Pkg == nil {
		return ""
	}
	path := f.Pkg.Pkg.Path()
	var pkg string
	switch path {
	case oracleModule + "/internal/tagger":
		pkg = "tagger"
	case oracleModule + "/internal/fileops":
		pkg = "fileops"
	default:
		return ""
	}
	name := f.Name()
	if r := f.Signature.Recv(); r != nil {
		t := r.Type().String()
		t = strings.TrimPrefix(t, "*")
		name = t[strings.LastIndex(t, ".")+1:] + "." + name
	}
	h := taglibHelper(pkg + "." + name)
	for _, want := range taglibWriteHelpers {
		if h == want {
			return h
		}
	}
	return ""
}

// taglibVariantFiles parses the files of internal/metadata the given variant
// builds (linux, with the oracle's tags, native_taglib on or off, cgo on or off
// to match), as syntax only.
func taglibVariantFiles(t *testing.T, native bool) []*ast.File {
	t.Helper()
	ctxt := build.Default
	ctxt.GOOS, ctxt.GOARCH = "linux", "amd64"
	ctxt.CgoEnabled = native
	ctxt.BuildTags = []string{"embed_frontend", "fts5"}
	if native {
		ctxt.BuildTags = append(ctxt.BuildTags, "native_taglib")
	}
	entries, err := filepath.Glob(filepath.Join(metadataDir, "*.go"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no Go files in %s: %v", metadataDir, err)
	}
	var files []*ast.File
	fset := token.NewFileSet()
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		ok, err := ctxt.MatchFile(filepath.Dir(path), filepath.Base(path))
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, f)
	}
	return files
}

// taglibCalls is what a function of internal/metadata calls: the functions of the
// same package (by name), the functions of other packages it imports
// ("<import path>.<name>"), and method names ("." + name) whose receiver the
// syntax does not give.
func taglibCalls(fd *ast.FuncDecl, imports map[string]string) (local, qualified, methods []string) {
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			local = append(local, fn.Name)
		case *ast.SelectorExpr:
			if id, ok := fn.X.(*ast.Ident); ok && id.Obj == nil {
				if path, ok := imports[id.Name]; ok {
					qualified = append(qualified, path+"."+fn.Sel.Name)
					return true
				}
			}
			methods = append(methods, fn.Sel.Name)
		}
		return true
	})
	return local, qualified, methods
}

// taglibVariantReach is, per write entry point of a variant of internal/metadata,
// the write helpers it reaches: through the variant's own functions (followed as
// syntax), into the helpers' packages (followed as the program's SSA).
func taglibVariantReach(t *testing.T, op *oracleProgram, idx *taglibIndex, native bool) map[string][]taglibHelper {
	t.Helper()
	decls := map[string]*ast.FuncDecl{}
	imports := map[string]string{}
	for _, f := range taglibVariantFiles(t, native) {
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			name := path[strings.LastIndex(path, "/")+1:]
			if im.Name != nil {
				name = im.Name.Name
			}
			imports[name] = path
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				// Methods are matched by name: the syntax gives no receiver type,
				// and a name shared by two types only widens what is reached.
				decls[fd.Name.Name] = fd
			}
		}
	}
	out := map[string][]taglibHelper{}
	for _, entry := range taglibWriteEntries {
		fd := decls[entry]
		if fd == nil {
			t.Errorf("the %s variant of internal/metadata has no %s", variantName(native), entry)
			continue
		}
		seen := map[string]bool{entry: true}
		queue := []string{entry}
		var qualified, methods []string
		for len(queue) > 0 {
			name := queue[0]
			queue = queue[1:]
			l, q, m := taglibCalls(decls[name], imports)
			qualified, methods = append(qualified, q...), append(methods, m...)
			for _, callee := range l {
				if _, ok := decls[callee]; ok && !seen[callee] {
					seen[callee] = true
					queue = append(queue, callee)
				}
			}
			for _, callee := range m {
				if _, ok := decls[callee]; ok && !seen[callee] {
					seen[callee] = true
					queue = append(queue, callee)
				}
			}
		}
		reached := map[taglibHelper]bool{}
		walk := func(f *ssa.Function) {
			_, _, _, _, via := derivedWrites(op, f)
			if h := helperOf(f); h != "" {
				reached[h] = true
			}
			for fn := range via {
				if h := idx.helperByString[fn]; h != "" {
					reached[h] = true
				}
			}
		}
		for _, q := range qualified {
			i := strings.LastIndex(q, ".")
			pkg := op.prog.ImportedPackage(q[:i])
			if pkg == nil {
				continue
			}
			if f := pkg.Func(q[i+1:]); f != nil {
				walk(f)
			}
		}
		for _, m := range methods {
			for _, f := range idx.methodsNamed[m] {
				walk(f)
			}
		}
		for h := range reached {
			out[entry] = append(out[entry], h)
		}
		sort.Slice(out[entry], func(i, j int) bool { return out[entry][i] < out[entry][j] })
	}
	return out
}

// taglibIndex is the SSA functions the reach needs to look up: the write helpers
// by their string form, and the methods of the packages a variant calls into, by
// name.
type taglibIndex struct {
	helperByString map[string]taglibHelper
	methodsNamed   map[string][]*ssa.Function
}

func newTaglibIndex(op *oracleProgram) *taglibIndex {
	idx := &taglibIndex{helperByString: map[string]taglibHelper{}, methodsNamed: map[string][]*ssa.Function{}}
	for f := range ssautil.AllFunctions(op.prog) {
		if f.Pkg == nil || !strings.HasPrefix(f.Pkg.Pkg.Path(), oracleModule) {
			continue
		}
		if h := helperOf(f); h != "" {
			idx.helperByString[f.String()] = h
		}
		if f.Signature.Recv() != nil {
			idx.methodsNamed[f.Name()] = append(idx.methodsNamed[f.Name()], f)
		}
	}
	return idx
}

func variantName(native bool) string {
	if native {
		return "native_taglib (taglib_cgo.go)"
	}
	return "default (taglib_support.go)"
}

// TestTaglibVariants_ReachTheSameWriteHelpers: for each function both variants
// define that writes a file, taglib_cgo.go and taglib_support.go reach the same
// set of write helpers, and the safe writers reach every one of them (path
// pre-flight, temp-copy write, read-back verification, hash recording). The
// in-place writer reaches none in either variant: its contract is that the caller
// already holds a temp copy and resolved the path.
func TestTaglibVariants_ReachTheSameWriteHelpers(t *testing.T) {
	op := oracleJobsProgram(t)
	idx := newTaglibIndex(op)
	support := taglibVariantReach(t, op, idx, false)
	cgo := taglibVariantReach(t, op, idx, true)
	all := make([]taglibHelper, len(taglibWriteHelpers))
	copy(all, taglibWriteHelpers)
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	for _, entry := range taglibWriteEntries {
		s, c := fmt.Sprint(support[entry]), fmt.Sprint(cgo[entry])
		if s != c {
			t.Errorf("%s reaches %s in taglib_support.go and %s in taglib_cgo.go; the oracle walks only the first, production ships the second", entry, s, c)
		}
		want := fmt.Sprint([]taglibHelper(nil))
		if slices.Contains(taglibSafeWriters, entry) {
			want = fmt.Sprint(all)
		}
		for name, got := range map[string]string{"taglib_support.go": s, "taglib_cgo.go": c} {
			if got != want {
				t.Errorf("%s in %s reaches %s, want %s", entry, name, got, want)
			}
		}
	}
}
