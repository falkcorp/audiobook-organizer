// file: internal/plugins/maintenance/deps_jobs_capscan_test.go
// version: 0.1.0
// guid: 7d3e1b94-2c58-4a07-b6f1-e0a95c3d8712
// last-edited: 2026-11-20

package maintenance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// The scan behind TestEveryCapabilityTheJobsResolveIsClassified: for every job,
// the optional capabilities its code resolves from a value, found in the
// functions its Run reaches by the same static walk as the jobs package's write
// oracle (internal/maintenance/jobs/writes_oracle_test.go: closures and
// in-module callees, a call through an interface is not followed, the store
// package's own bodies are not walked). A site is
//
//   - "assert T": a type assertion or type-switch case to an interface with
//     methods, or to a type of the store package (an assertion to string, a map
//     or a context value's concrete type is not a capability);
//   - "AsCapability[T]": the generic resolver, with its instantiated type;
//   - "helper F": a call of a store-package function that resolves a capability
//     from a value passed as any (AsSyncIdentityStore, BooksHoldingFileHash, ...),
//     found by reading the store package's source (helpersOf).
//
// The walk is the oracle's, so it has the oracle's blind spot: a capability
// resolved only inside code reached through an interface call is not seen.

const (
	scanModule = "github.com/falkcorp/audiobook-organizer"
	scanStore  = scanModule + "/internal/books/store"
)

var scanEnv = []string{"GOOS=linux", "CGO_ENABLED=0"}

// scanTags is the production build the oracle walks (see its doc for why
// native_taglib is left out).
const scanTags = "-tags=embed_frontend,fts5"

// siteInfo is what the scan knows of one resolution site: the first function it
// was found in, and the static type of every value it was resolved from. An
// assertion on a value of type any, a store interface or a store is a store
// capability; one on a logger, a progress reporter or a path checker is not.
type siteInfo struct {
	where    string
	operands map[string]bool
	// typ is the capability: the asserted type, or the resolver's type argument.
	typ types.Type
}

var asHelperRe = regexp.MustCompile(`^As[A-Z]`)

// callName is the called function's bare name, looking through an explicit
// instantiation.
func callName(c *ast.CallExpr) string {
	fun := c.Fun
	if ix, ok := fun.(*ast.IndexExpr); ok {
		fun = ix.X
	}
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// resolvesByHand reports whether fd resolves a capability without AsCapability:
// it walks decorators itself (it names StoreUnwrapper), or it is an As*
// resolver that type-asserts (as AsPebbleStore does).
func resolvesByHand(fd *ast.FuncDecl) bool {
	hit := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if x.Name == "StoreUnwrapper" {
				hit = true
			}
		case *ast.TypeAssertExpr, *ast.TypeSwitchStmt:
			if asHelperRe.MatchString(fd.Name.Name) {
				hit = true
			}
		}
		return !hit
	})
	return hit
}

// storeHelpers are the top-level functions of the store package that resolve a
// capability from a value passed as any: those calling AsCapability, an As*
// resolver, or another such helper, to a fixed point, and the hand-rolled
// resolvers they bottom out in.
func storeHelpers(t *testing.T) map[string]bool {
	t.Helper()
	dir := "../../books/store"
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	top := map[string]*ast.FuncDecl{}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
				top[fd.Name.Name] = fd
			}
		}
	}
	helpers := map[string]bool{}
	for name, fd := range top {
		if name != "AsCapability" && resolvesByHand(fd) {
			helpers[name] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for name, fd := range top {
			if helpers[name] || name == "AsCapability" {
				continue
			}
			hit := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					cn := callName(c)
					if cn == "AsCapability" || helpers[cn] || (asHelperRe.MatchString(cn) && top[cn] != nil) {
						hit = true
					}
				}
				return !hit
			})
			if hit {
				helpers[name] = true
				changed = true
			}
		}
	}
	return helpers
}

func qual(p *types.Package) string { return p.Name() }

func isErrorType(t types.Type) bool {
	return types.Identical(t, types.Universe.Lookup("error").Type())
}

// capabilityType reports whether an asserted type is one the scan records: an
// interface with methods (the shape of every capability), or a named type of
// the store package (a concrete store).
func capabilityType(t types.Type) bool {
	if types.IsInterface(t) {
		if it, ok := t.Underlying().(*types.Interface); ok {
			return it.NumMethods() > 0
		}
		return false
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok && n.Obj().Pkg() != nil {
		return n.Obj().Pkg().Path() == scanStore
	}
	return false
}

// wholePackages are scanned function by function, reached or not: the walk
// does not follow a call through an interface, and these are the packages that
// hold the optional-capability resolutions on a store the jobs hand down
// (merge.Service is entered through interfaces and closures the walk loses). A
// site found only here is a capability a job may reach by a path the walk cannot
// see.
var wholePackages = []string{"merge"}

// jobSites is, per job ID, every resolution site its Run reaches, with the
// function it was found in.
func scanJobSites(helpers map[string]bool) (map[string]map[string]*siteInfo, error) {
	cfg := &packages.Config{
		Mode:       packages.LoadAllSyntax,
		BuildFlags: []string{scanTags},
		Env:        append(os.Environ(), scanEnv...),
	}
	pkgs, err := packages.Load(cfg, scanModule+"/internal/maintenance/jobs")
	if err != nil {
		return nil, fmt.Errorf("load jobs package: %w", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("the jobs package does not type-check")
	}
	prog, ssapkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	for _, p := range prog.AllPackages() {
		if strings.HasPrefix(p.Pkg.Path(), scanModule) {
			p.Build()
		}
	}
	jp := ssapkgs[0]
	out := map[string]map[string]*siteInfo{}
	for _, m := range jp.Members {
		tn, ok := m.(*ssa.Type)
		if !ok {
			continue
		}
		ms := prog.MethodSets.MethodSet(types.NewPointer(tn.Type()))
		idSel, runSel := ms.Lookup(nil, "ID"), ms.Lookup(nil, "Run")
		if idSel == nil || runSel == nil {
			continue
		}
		id := constantID(prog.MethodValue(idSel))
		if id == "" {
			return nil, fmt.Errorf("job type %s: ID() does not return a constant string", tn.Name())
		}
		out[id] = jobSitesOf(prog.MethodValue(runSel), helpers)
	}
	for _, pkg := range wholePackages {
		st := newSiteState(helpers)
		for _, p := range prog.AllPackages() {
			if p.Pkg.Path() != scanModule+"/internal/"+pkg {
				continue
			}
			for _, m := range p.Members {
				switch mm := m.(type) {
				case *ssa.Function:
					st.scan(mm, "", nil)
				case *ssa.Type:
					ms := prog.MethodSets.MethodSet(types.NewPointer(mm.Type()))
					for i := 0; i < ms.Len(); i++ {
						if fn := prog.MethodValue(ms.At(i)); fn != nil && fn.Pkg == p {
							st.scan(fn, "", nil)
						}
					}
				}
			}
		}
		out["package "+pkg] = st.sites
	}
	return out, nil
}

func constantID(f *ssa.Function) string {
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if r, ok := in.(*ssa.Return); ok && len(r.Results) == 1 {
				if c, ok := r.Results[0].(*ssa.Const); ok && c.Value != nil && c.Value.String() != "" {
					return strings.Trim(c.Value.String(), `"`)
				}
			}
		}
	}
	return ""
}

func jobSitesOf(run *ssa.Function, helpers map[string]bool) map[string]*siteInfo {
	st := newSiteState(helpers)
	seen := map[*ssa.Function]bool{}
	var walk func(f *ssa.Function)
	walk = func(f *ssa.Function) {
		if f == nil || seen[f] {
			return
		}
		seen[f] = true
		if f.Pkg != nil {
			path := f.Pkg.Pkg.Path()
			if !strings.HasPrefix(path, scanModule) || path == scanStore {
				return
			}
		}
		st.scan(f, "", walk)
	}
	walk(run)
	return st.sites
}

// siteState collects the sites of the functions it scans. A store-package
// helper that resolves a capability is entered, so what it resolves is recorded
// under the capability's own type (BookFileByPathIncludingRetired is
// AsCapability[store.RetiredBookFileReader]), with the helper's name in the
// evidence.
type siteState struct {
	helpers    map[string]bool
	sites      map[string]*siteInfo
	seenHelper map[*ssa.Function]bool
}

func newSiteState(helpers map[string]bool) *siteState {
	return &siteState{helpers: helpers, sites: map[string]*siteInfo{}, seenHelper: map[*ssa.Function]bool{}}
}

// scan records the sites of f's body, closures included. follow, when set,
// receives every in-module callee that is not a store-package function.
func (st *siteState) scan(f *ssa.Function, via string, follow func(*ssa.Function)) {
	note := func(k string, operand, typ types.Type) {
		si := st.sites[k]
		if si == nil {
			where := f.String()
			if via != "" {
				where += " via " + via
			}
			si = &siteInfo{where: where, operands: map[string]bool{}, typ: typ}
			st.sites[k] = si
		}
		if operand != nil {
			si.operands[types.TypeString(operand, qual)] = true
		}
	}
	for _, af := range f.AnonFuncs {
		st.scan(af, via, follow)
	}
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if x, ok := in.(*ssa.TypeAssert); ok && capabilityType(x.AssertedType) && !isErrorType(x.X.Type()) {
				// StoreUnwrapper is the decorator walk every resolver does, not a
				// capability; an assertion on an error is errors.As by hand.
				if n, ok := x.AssertedType.(*types.Named); !ok || n.Obj().Name() != "StoreUnwrapper" {
					note("assert "+types.TypeString(x.AssertedType, qual), x.X.Type(), x.AssertedType)
				}
			}
			ci, ok := in.(ssa.CallInstruction)
			if !ok {
				continue
			}
			callee := ci.Common().StaticCallee()
			if callee == nil {
				continue
			}
			origin := callee
			if o := callee.Origin(); o != nil {
				origin = o
			}
			if origin.Pkg != nil && origin.Pkg.Pkg.Path() == scanStore {
				switch {
				case origin.Name() == "AsCapability" && len(callee.TypeArgs()) == 1:
					var operand types.Type
					if args := ci.Common().Args; len(args) == 1 {
						operand = args[0].Type()
					}
					note("AsCapability["+types.TypeString(callee.TypeArgs()[0], qual)+"]", operand, callee.TypeArgs()[0])
				case origin.Signature.Recv() == nil && st.helpers[origin.Name()] && !st.seenHelper[origin]:
					st.seenHelper[origin] = true
					h := via
					if h == "" {
						h = origin.Name()
					}
					st.scan(origin, h, nil)
				}
				continue
			}
			if follow != nil {
				follow(callee)
			}
		}
	}
}

var (
	scanOnce   sync.Once
	scanResult map[string]map[string]*siteInfo
	scanErr    error
)

// jobSites loads and analyses the jobs package once for every test that asks.
func jobSites(t *testing.T) map[string]map[string]*siteInfo {
	t.Helper()
	scanOnce.Do(func() { scanResult, scanErr = scanJobSites(storeHelpers(t)) })
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	return scanResult
}

func scanSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
