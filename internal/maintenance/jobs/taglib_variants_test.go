// file: internal/maintenance/jobs/taglib_variants_test.go
// version: 2.1.0
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
// compares what each variant's write functions reach: the write helpers, the
// resource kinds and the classified calls of everything they call into the rest
// of the module, and the file writers of package os that the variant's own bodies
// call.

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
// ("<import path>.<name>"), and method names (the syntax gives no receiver).
// refs is every "<import path>.<name>" the body names, called or not (os.Rename
// handed on as a value is a file writer too).
func taglibCalls(fd *ast.FuncDecl, imports map[string]string) (local, qualified, methods, refs []string) {
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil {
				if path, ok := imports[id.Name]; ok {
					refs = append(refs, path+"."+sel.Sel.Name)
				}
			}
		}
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
	return local, qualified, methods, refs
}

// taglibReach is what one write entry point of one variant reaches.
type taglibReach struct {
	// helpers are the write helpers reached.
	helpers []taglibHelper
	// kinds are the resource kinds of everything the entry calls into the rest of
	// the module, and calls those calls' keys the oracle records for them.
	kinds, calls []string
	// osWriters are the file writers (of package os, and of the packages the
	// oracle treats as file writers) the variant's own bodies call or name.
	osWriters []string
	// verbMethods are the method names, named like a write, the variant's own
	// bodies call: a store or interface write made from the variant itself,
	// which the syntax cannot resolve to a receiver (round-2 review m1).
	verbMethods []string
}

// taglibVariantReach is, per write entry point of a variant of internal/metadata,
// what it reaches: through the variant's own functions (followed as syntax), into
// the module's packages (followed as the program's SSA, with the oracle's walk).
func taglibVariantReach(t *testing.T, op *oracleProgram, idx *taglibIndex, native bool) map[string]taglibReach {
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
				// Methods are matched by name among the variant's own
				// declarations only: the syntax gives no receiver type, and a
				// name shared by two types only widens what is reached.
				decls[fd.Name.Name] = fd
			}
		}
	}
	out := map[string]taglibReach{}
	for _, entry := range taglibWriteEntries {
		fd := decls[entry]
		if fd == nil {
			t.Errorf("the %s variant of internal/metadata has no %s", variantName(native), entry)
			continue
		}
		seen := map[string]bool{entry: true}
		queue := []string{entry}
		var qualified, methods, refs []string
		for len(queue) > 0 {
			name := queue[0]
			queue = queue[1:]
			l, q, m, r := taglibCalls(decls[name], imports)
			qualified, methods, refs = append(qualified, q...), append(methods, m...), append(refs, r...)
			for _, callee := range append(l, m...) {
				if _, ok := decls[callee]; ok && !seen[callee] {
					seen[callee] = true
					queue = append(queue, callee)
				}
			}
		}
		reached := map[taglibHelper]bool{}
		kinds, calls, writers := map[string]bool{}, map[string]bool{}, map[string]bool{}
		walk := func(f *ssa.Function) {
			k, _, _, c, via := derivedWrites(op, f)
			for _, kind := range k {
				kinds[kind] = true
			}
			for key := range c {
				// An UNRESOLVED key says where a walk could not follow a
				// value, relative to where the walk started: a helper walked
				// from here has parameters no walked caller supplies (the
				// variant's caller is syntax, not SSA), so it names the start,
				// not a write. TestJobWrites_NoUnresolvedCalls holds the
				// jobs' keys to a reason.
				if !strings.HasPrefix(key, "UNRESOLVED.") {
					calls[key] = true
				}
			}
			if h := helperOf(f); h != "" {
				reached[h] = true
			}
			for fn := range via {
				if h := idx.helperByString[fn]; h != "" {
					reached[h] = true
				}
			}
		}
		for _, q := range refs {
			i := strings.LastIndex(q, ".")
			pkgPath, name := q[:i], q[i+1:]
			if pkgPath == "os" && oracleFileWriters[name] || oracleOutsideFileWriters[pkgPath][name] {
				writers[q] = true
			}
		}
		for _, q := range qualified {
			i := strings.LastIndex(q, ".")
			pkgPath, name := q[:i], q[i+1:]
			pkg := op.prog.ImportedPackage(pkgPath)
			if pkg == nil {
				continue
			}
			if f := pkg.Func(name); f != nil {
				// The walk stops at once for a store function, so the key is
				// recorded here, where the variant calls it, as the walk
				// records it at a call site (round-2 review m1).
				if strings.Contains(pkgPath, "/internal/books/store") {
					calls[oracleStoreKey(f)] = true
				}
				walk(f)
			}
		}
		verbs := map[string]bool{}
		for _, m := range methods {
			if oracleVerb.MatchString(m) {
				verbs[m] = true
			}
		}
		// A method call is resolved only to the write helpers, by the helper's
		// own method name (VerifyWritten): not to every method of that name in
		// the module.
		for _, m := range methods {
			if f := idx.helperMethods[m]; f != nil {
				walk(f)
			}
		}
		r := taglibReach{kinds: sortedKeys(kinds), calls: sortedKeys(calls), osWriters: sortedKeys(writers), verbMethods: sortedKeys(verbs)}
		for h := range reached {
			r.helpers = append(r.helpers, h)
		}
		sort.Slice(r.helpers, func(i, j int) bool { return r.helpers[i] < r.helpers[j] })
		out[entry] = r
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// taglibIndex is the SSA functions the reach needs to look up: the write helpers
// by their string form, and the helpers that are methods by their method name.
type taglibIndex struct {
	helperByString map[string]taglibHelper
	helperMethods  map[string]*ssa.Function
}

func newTaglibIndex(op *oracleProgram) *taglibIndex {
	idx := &taglibIndex{helperByString: map[string]taglibHelper{}, helperMethods: map[string]*ssa.Function{}}
	for f := range ssautil.AllFunctions(op.prog) {
		if f.Pkg == nil || !strings.HasPrefix(f.Pkg.Pkg.Path(), oracleModule) {
			continue
		}
		if h := helperOf(f); h != "" {
			idx.helperByString[f.String()] = h
			if f.Signature.Recv() != nil {
				idx.helperMethods[f.Name()] = f
			}
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
		s, c := support[entry], cgo[entry]
		for what, pair := range map[string][2]string{
			"write helpers":          {fmt.Sprint(s.helpers), fmt.Sprint(c.helpers)},
			"resource kinds":         {fmt.Sprint(s.kinds), fmt.Sprint(c.kinds)},
			"classified calls":       {fmt.Sprint(s.calls), fmt.Sprint(c.calls)},
			"file writers (os etc.)": {fmt.Sprint(s.osWriters), fmt.Sprint(c.osWriters)},
			"write-named methods":    {fmt.Sprint(s.verbMethods), fmt.Sprint(c.verbMethods)},
		} {
			if pair[0] != pair[1] {
				t.Errorf("%s reaches %s %s in taglib_support.go and %s in taglib_cgo.go; the oracle walks only the first, production ships the second", entry, what, pair[0], what+" "+pair[1])
			}
		}
		want := fmt.Sprint([]taglibHelper(nil))
		if slices.Contains(taglibSafeWriters, entry) {
			want = fmt.Sprint(all)
		}
		for name, got := range map[string]string{"taglib_support.go": fmt.Sprint(s.helpers), "taglib_cgo.go": fmt.Sprint(c.helpers)} {
			if got != want {
				t.Errorf("%s in %s reaches %s, want %s", entry, name, got, want)
			}
		}
	}
}
