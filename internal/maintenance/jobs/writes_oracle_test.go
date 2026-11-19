// file: internal/maintenance/jobs/writes_oracle_test.go
// version: 2.1.0
// guid: 40eadd67-76ac-419f-a3fe-7d1ed5e586e2
// last-edited: 2026-11-19

package jobs_test

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/mediacore/tasks"
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// oracleModule is the module whose code the oracle walks into; a call that
// leaves it is judged by name only (os file mutators), never followed.
const oracleModule = "github.com/falkcorp/audiobook-organizer"

// oracleBuildTags and oracleEnv are the build the oracle walks: the production
// binary's (Dockerfile and Makefile build -tags "embed_frontend fts5
// native_taglib" for linux), not the host's. Without them the walk follows
// reflink_darwin.go and fix_file_modes_unix.go on a Mac and the !native_taglib
// files, which are not what ships.
//
// native_taglib is the one production tag left out: taglib_cgo.go imports "C"
// and needs the vendored static libraries and a linux C toolchain to load, which
// a developer machine does not have, so the walk would not type-check (the
// !native_taglib variant, taglib_support.go, is walked instead). Both variants
// reach the same store surface from scan-composer-tags (ResolvePathForWrite,
// fileops.WriteTagsSafe, VerifyWritten), checked by reading both. CGO_ENABLED=0
// keeps the load from invoking cgo for the same reason.
const oracleBuildTags = "-tags=embed_frontend,fts5"

var oracleEnv = []string{"GOOS=linux", "CGO_ENABLED=0"}

// oracleVerb is what makes an interface method call a write. A method that
// does not start with one of these is a read.
var oracleVerb = regexp.MustCompile(`^(Create|Update|Delete|Set|Save|Add|Remove|Put|Modify|Move|Reassign|Prune|Recompute|Upsert|Write|Mark|Batch|Reset|Purge|Insert|Apply|Retire|Clear|Replace|Merge|Rename|Link|Unlink|Store|Record|Append|Touch|Increment|Mint|Sweep|Revert)`)

// oracleNotAWrite names receivers whose "writes" are not a books resource:
// progress and liveness reporting, hashing, and the activity log's own sink.
var oracleNotAWrite = regexp.MustCompile(`Reporter|LivenessToucher|hash\.Hash|ActivityLogWriter`)

// oracleNotWalked are functions the walk does not follow into, though they are
// inside the module: the scan stand-down hold's heartbeat. RunItems calls
// (*ScanStandDownHold).Checkpoint for a run that holds the stand-down; it renews
// the marker the registry owns (RenewScanStandDown, a settings row) through an
// interface the graph resolves. That is the hold's own liveness, the same kind of
// thing oracleNotAWrite excludes for progress reporting, not a write the job makes.
var oracleNotWalked = regexp.MustCompile(`\.ScanStandDownHold\)\.`)

// oracleFileWriters are the os functions that change a file or directory.
var oracleFileWriters = map[string]bool{
	"Remove": true, "RemoveAll": true, "Rename": true, "WriteFile": true, "Chmod": true,
	"Chown": true, "Chtimes": true, "Mkdir": true, "MkdirAll": true, "Create": true,
	"OpenFile": true, "Truncate": true, "Symlink": true, "Link": true,
}

// oracleOutsideFileWriters are functions outside the module that create or
// write a file, keyed by package path. The walk never follows a call out of the
// module, so a job that writes its report through core's confined openers
// (library.OpenReportFile and its siblings, which use openat under the
// .reports root rather than os.OpenFile) would otherwise read as writing no
// files on disk.
var oracleOutsideFileWriters = map[string]map[string]bool{
	"github.com/falkcorp/mediacore/library": {
		"OpenReportFile": true, "WriteReportFile": true, "PreflightReportFile": true,
	},
}

// oracleResource names the resource kind an interface write touches, from the
// receiver's type name and the method name alone. It is deliberately not the
// table's vocabulary applied to a job: it reads what the code calls.
func oracleResource(recv, method string) string {
	if r := oracleNoun(method); r != "" {
		return r
	}
	if r := oracleNoun(recv); r != "" {
		return r
	}
	return "UNCLASSIFIED(" + recv + "." + method + ")"
}

// oracleNoun is the resource a name speaks of, or "" when it names none. The
// method is asked before the receiver, so ModifyBook on a type called
// tagAuthorStore is still a book write.
func oracleNoun(name string) string {
	switch {
	case strings.Contains(name, "Sync"):
		return "sync_identity"
	case strings.Contains(name, "Metric"):
		return "metrics"
	case strings.Contains(name, "Setting"):
		return "settings"
	case strings.Contains(name, "ExternalID"):
		return "external_ids"
	case strings.Contains(name, "Operation"), strings.Contains(name, "checkpointer"),
		strings.Contains(name, "Journal"), strings.Contains(name, "pendingRepair"), strings.Contains(name, "retentionKV"):
		return "operations"
	case strings.Contains(name, "UserBookState"), strings.Contains(name, "UserPosition"),
		strings.Contains(name, "UserTag"), strings.Contains(name, "Bookmark"), strings.Contains(name, "UserProgress"):
		return "user_state"
	case strings.Contains(name, "FileEvent"):
		// The provenance ledger (file_prov:* keys) is its own resource, not a
		// book_files row: ops.ResFileProvenance, named by the kind it declares.
		return "file_provenance"
	case strings.Contains(name, "BookFile"):
		return "book_files"
	case strings.Contains(name, "Author"):
		return "authors"
	case strings.Contains(name, "Series"):
		return "series"
	case strings.Contains(name, "Book"):
		return "books"
	}
	return ""
}

// oracleProgram is an SSA program of the module's code with the call graph the
// walk follows. The graph is VTA seeded by CHA (the same construction
// tools/fixerstores uses): an interface call resolves to the implementations
// whose types flow to it, and a call through a function value (a struct field, a
// slice of steps, a package variable) resolves to the functions stored there.
type oracleProgram struct {
	prog  *ssa.Program
	graph *callgraph.Graph
	// cha is the class-hierarchy graph the VTA graph was seeded with: every
	// implementation of an interface method that the program contains.
	cha *callgraph.Graph
	// roots are the SSA packages of the patterns the program was loaded for.
	roots []*ssa.Package
}

// oracleCallGraph is VTA over every function of the module (and every synthetic
// wrapper, which is how a promoted method or a bound method value reaches its
// target), seeded by CHA. Functions outside the module are left out of the VTA
// set: their bodies are not built, and the walk never follows a call out of the
// module (see walk).
func oracleCallGraph(prog *ssa.Program) (vtaGraph, chaGraph *callgraph.Graph) {
	funcs := map[*ssa.Function]bool{}
	for f := range ssautil.AllFunctions(prog) {
		if f.Pkg == nil || strings.HasPrefix(f.Pkg.Pkg.Path(), oracleModule) {
			funcs[f] = true
		}
	}
	chaGraph = cha.CallGraph(prog)
	return vta.CallGraph(funcs, chaGraph), chaGraph
}

// oracleOrigin says where a function value in call position came from.
type oracleOrigin struct {
	// param: some caller supplies it (a parameter, or a captured parameter).
	param bool
	// other: it was read from a field, a package variable, a slice or a call
	// result, which only the call graph can resolve.
	other bool
}

func (o *oracleOrigin) merge(p oracleOrigin) {
	o.param = o.param || p.param
	o.other = o.other || p.other
}

// oracleOriginOf follows a function value back through the variables it passed
// through inside the function that holds it and the functions that created the
// closures capturing it. A value that is, or was stored from, a literal function
// or a closure is known here: the walk reads it where it is mentioned.
func oracleOriginOf(v ssa.Value, seen map[ssa.Value]bool) (o oracleOrigin) {
	if seen[v] {
		return o
	}
	seen[v] = true
	switch v := v.(type) {
	case *ssa.Parameter:
		o.param = true
	case *ssa.Function, *ssa.MakeClosure:
		// known where it is mentioned
	case *ssa.FreeVar:
		fn, idx := v.Parent(), -1
		for i, fv := range fn.FreeVars {
			if fv == v {
				idx = i
			}
		}
		found := false
		if parent := fn.Parent(); parent != nil && idx >= 0 {
			for _, b := range parent.Blocks {
				for _, in := range b.Instrs {
					if mc, ok := in.(*ssa.MakeClosure); ok && mc.Fn == fn {
						found = true
						o.merge(oracleOriginOf(mc.Bindings[idx], seen))
					}
				}
			}
		}
		if !found {
			o.other = true
		}
	case *ssa.Alloc:
		// the address of a captured or local variable: what was stored into it
		stores := 0
		if parent := v.Parent(); parent != nil {
			for _, b := range parent.Blocks {
				for _, in := range b.Instrs {
					if st, ok := in.(*ssa.Store); ok && st.Addr == v {
						stores++
						o.merge(oracleOriginOf(st.Val, seen))
					}
				}
			}
		}
		if stores == 0 {
			o.other = true
		}
	case *ssa.UnOp:
		if v.Op == token.MUL {
			switch v.X.(type) {
			case *ssa.Alloc, *ssa.FreeVar:
				o.merge(oracleOriginOf(v.X, seen))
				return o
			}
		}
		o.other = true
	case *ssa.ChangeType:
		o.merge(oracleOriginOf(v.X, seen))
	case *ssa.Phi:
		for _, e := range v.Edges {
			o.merge(oracleOriginOf(e, seen))
		}
	default:
		o.other = true
	}
	return o
}

// oracleGraphResolves is whether the call graph should resolve a call through
// the function value v: when it is read from a field, a package variable, a
// slice or a call result, and no caller supplies it. A value some caller
// supplies (a parameter) is not the graph's to resolve: VTA is context
// insensitive, so its callees are every function any caller anywhere passes
// there. The caller that passes the callback mentions it as a value, and the
// walk read it there, with only this job's callers in view.
func oracleGraphResolves(v ssa.Value) bool {
	o := oracleOriginOf(v, map[ssa.Value]bool{})
	return o.other && !o.param
}

// sitesOf is the callees of each call site in f, from the VTA graph or the CHA
// graph.
func (op *oracleProgram) sitesOf(f *ssa.Function, useCHA bool) map[ssa.CallInstruction][]*ssa.Function {
	g := op.graph
	if useCHA {
		g = op.cha
	}
	n := g.Nodes[f]
	if n == nil {
		return nil
	}
	sites := map[ssa.CallInstruction][]*ssa.Function{}
	for _, e := range n.Out {
		if e.Site != nil {
			sites[e.Site] = append(sites[e.Site], e.Callee.Func)
		}
	}
	return sites
}

// oracleFollows is whether the walk goes into f's body: module code other than
// the store package, and any synthetic wrapper (a promoted method, a bound
// method value), because it only forwards. An instance of a generic function,
// and the closures inside it, belong to the package of the generic they were
// instantiated from (slices.Backward[...] is the standard library's, whatever
// instantiated it). The scan stand-down hold's heartbeat is left out
// (oracleNotWalked).
func oracleFollows(f *ssa.Function) bool {
	if pkg := oracleOwner(f).Pkg; pkg != nil {
		path := pkg.Pkg.Path()
		if !strings.HasPrefix(path, oracleModule) || strings.Contains(path, "/internal/books/store") {
			return false
		}
	}
	return !oracleNotWalked.MatchString(f.String())
}

// oracleOwner is the top-level, non-instantiated function a function belongs to:
// the closure's enclosing function, and the generic an instance was made from.
func oracleOwner(f *ssa.Function) *ssa.Function {
	for f.Parent() != nil {
		f = f.Parent()
	}
	if o := f.Origin(); o != nil {
		f = o
	}
	return f
}

// derivedWrites walks every function a job's Run reaches (closures, function
// values and the implementations of the interface calls it makes included,
// inside the module only) and returns the resource kinds it writes.
//
// What it follows, and why each is needed:
//   - a static call, as the compiler resolved it;
//   - an interface call or a call through a function value, to every callee the
//     VTA graph gives that call site (a store write made by an implementation of
//     an interface whose method is not named like a write, such as a wrapper's
//     Revert, is not dropped any more);
//   - every function a body mentions as a value (passed as a callback, stored in
//     a field), whether or not the module's own code is what finally calls it: a
//     callback handed to code outside the module is invoked there, where the walk
//     does not go.
//
// It does not follow a call into the store package (the store's bodies are the
// shared table's domain, judged by the method's name) nor out of the module.
func derivedWrites(op *oracleProgram, runFn *ssa.Function) (kinds []string, evidence map[string]string, checkpoints bool, calls map[string]string, via map[string]string) {
	via = map[string]string{}
	set := map[string]bool{}
	calls = map[string]string{}
	evidence = map[string]string{}
	note := func(kind, why string) {
		set[kind] = true
		if _, ok := evidence[kind]; !ok {
			evidence[kind] = why
		}
	}
	seen := map[*ssa.Function]bool{}
	var walk func(f *ssa.Function)
	var cur *ssa.Function
	walk = func(f *ssa.Function) {
		if f == nil || seen[f] {
			return
		}
		seen[f] = true
		if !oracleFollows(f) {
			return
		}
		if cur != nil {
			via[f.String()] = cur.String()
		}
		parent := cur
		cur = f
		defer func() { cur = parent }()
		for _, af := range f.AnonFuncs {
			walk(af)
		}
		// The graph's callees of each call site in f.
		sites := op.sitesOf(f, false)
		var chaSites map[ssa.CallInstruction][]*ssa.Function
		var operands [16]*ssa.Value
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				for _, v := range in.Operands(operands[:0]) {
					if fn, ok := (*v).(*ssa.Function); ok {
						walk(fn)
					}
				}
				ci, ok := in.(ssa.CallInstruction)
				if !ok {
					continue
				}
				com := ci.Common()
				if com.IsInvoke() {
					name := com.Method.Name()
					recv := types.TypeString(com.Value.Type(), func(p *types.Package) string { return p.Name() })
					// Progress and liveness reporting are not books writes (the
					// oracleNotAWrite rule), and neither is what implements them.
					var impls []*ssa.Function
					if !oracleNotAWrite.MatchString(recv) {
						impls = sites[ci]
						if pkg := com.Method.Pkg(); len(impls) == 0 && pkg != nil && strings.HasPrefix(pkg.Path(), oracleModule) {
							// VTA found no type that flows to this interface
							// value: the value comes from code outside the loaded
							// program (the wiring that hands a job its
							// collaborators). Every implementation of a module
							// interface that the program contains is then the
							// answer. Not for an interface of the standard library
							// or a dependency (error, fs.FileInfo, io.Closer):
							// every type in the module implements one.
							if chaSites == nil {
								chaSites = op.sitesOf(f, true)
							}
							impls = chaSites[ci]
						}
					}
					// When every implementation is module code the walk follows,
					// what the call writes is what those bodies do, and the
					// interface method's own name says nothing (a wrapper's
					// ImportPath is the UpdateBookFile it ends in). Any other
					// implementation (the store, whose bodies are not walked, or
					// code outside the module) leaves the call judged by name.
					byBody := len(impls) > 0
					for _, impl := range impls {
						if !oracleFollows(impl) {
							byBody = false
						}
					}
					if !byBody {
						if _, ok := calls[recv+"."+name]; !ok {
							calls[recv+"."+name] = f.String()
						}
						if oracleVerb.MatchString(name) && !oracleNotAWrite.MatchString(recv) {
							note(oracleResource(recv, name), recv+"."+name+" in "+f.String())
						}
					}
					for _, impl := range impls {
						walk(impl)
					}
					continue
				}
				callee := com.StaticCallee()
				if callee == nil {
					// A call through a function value (see oracleGraphResolves).
					if oracleGraphResolves(com.Value) {
						for _, target := range sites[ci] {
							walk(target)
						}
					}
					continue
				}
				// A job restores a checkpoint only through checkpointerFrom (or
				// the v1 helpers it falls back to when that returns nil).
				if callee.Name() == "checkpointerFrom" {
					checkpoints = true
				}
				if callee.Pkg != nil && callee.Pkg.Pkg.Path() == "os" && oracleFileWriters[callee.Name()] {
					note("files", "os."+callee.Name()+" in "+f.String())
				}
				if callee.Pkg != nil && oracleOutsideFileWriters[callee.Pkg.Pkg.Path()][callee.Name()] {
					note("files", callee.Pkg.Pkg.Path()+"."+callee.Name()+" in "+f.String())
				}
				if callee.Pkg != nil && strings.Contains(callee.Pkg.Pkg.Path(), "/internal/books/store") {
					key := "STOREFN." + callee.Name()
					if callee.Signature.Recv() != nil {
						key = "STORECONCRETE." + types.TypeString(callee.Signature.Recv().Type(), func(p *types.Package) string { return p.Name() }) + "." + callee.Name()
					}
					if _, ok := calls[key]; !ok {
						calls[key] = f.String()
					}
				}
				// A concrete store type called directly (not through an
				// interface) is judged like an interface call: the store
				// package's own bodies are not walked.
				if callee.Pkg != nil && strings.Contains(callee.Pkg.Pkg.Path(), "/internal/books/store") && callee.Signature.Recv() != nil && oracleVerb.MatchString(callee.Name()) {
					recv := types.TypeString(callee.Signature.Recv().Type(), func(p *types.Package) string { return p.Name() })
					if !oracleNotAWrite.MatchString(recv) {
						note(oracleResource(recv, callee.Name()), recv+"."+callee.Name()+" in "+f.String())
					}
				}
				walk(callee)
			}
		}
	}
	walk(runFn)
	for k := range set {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds, evidence, checkpoints, calls, via
}

// oracleJobWrites loads the jobs package with its dependencies' syntax and
// returns, per job ID, the write set derived from its code.
// oracleJob is what the oracle derives from one job's code.
type oracleJob struct {
	writes      []string
	evidence    map[string]string
	checkpoints bool
	// calls is every method the job's Run reaches on an interface, and every
	// function or method of the store package it calls directly, as
	// "<receiver>.<method>" (STOREFN.<name> for a store package function), with
	// the function it was found in. The names are unfiltered: writes_trace_test.go
	// decides which of them write.
	calls map[string]string
	// via maps each function the walk visited to the function it was first
	// reached from, so a surprising write can be traced back to Run.
	via map[string]string
}

// chain is the route the walk took from the job's Run to fn, innermost first.
func (j oracleJob) chain(fn string) string {
	out := fn
	for i := 0; i < 64; i++ {
		parent, ok := j.via[fn]
		if !ok {
			break
		}
		out += " <- " + parent
		fn = parent
	}
	return out
}

// checkProductionBuild fails when the load did not pick the production build: the
// linux variant of a file the jobs reach through internal/fileops, and not the
// darwin one the host would give. A Config that silently stopped applying the
// build flags would otherwise walk the wrong files and still pass.
func checkProductionBuild(pkgs []*packages.Package) error {
	var found bool
	var bad error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.PkgPath != oracleModule+"/internal/fileops" {
			return
		}
		found = true
		var linux, darwin bool
		for _, f := range p.GoFiles {
			switch {
			case strings.HasSuffix(f, "reflink_linux.go"):
				linux = true
			case strings.HasSuffix(f, "reflink_darwin.go"):
				darwin = true
			}
		}
		if !linux || darwin {
			bad = fmt.Errorf("the oracle did not load the linux build of internal/fileops (reflink_linux.go=%v, reflink_darwin.go=%v)", linux, darwin)
		}
	})
	if !found {
		return fmt.Errorf("internal/fileops is not in the jobs package's import graph; pick another file to check the build with")
	}
	return bad
}

// loadOracleProgram loads the patterns with their dependencies' syntax and
// builds the SSA program and call graph. checkBuild asks for the production
// build check (the jobs package's import graph has internal/fileops; a fixture's
// does not).
func loadOracleProgram(checkBuild bool, patterns ...string) (*oracleProgram, error) {
	cfg := &packages.Config{
		Mode:       packages.LoadAllSyntax,
		BuildFlags: []string{oracleBuildTags},
		Env:        append(os.Environ(), oracleEnv...),
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("load %v: %w", patterns, err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("%v does not type-check", patterns)
	}
	if checkBuild {
		if err := checkProductionBuild(pkgs); err != nil {
			return nil, err
		}
	}
	prog, ssapkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	for _, p := range ssapkgs {
		if p != nil && strings.HasPrefix(p.Pkg.Path(), oracleModule) {
			p.Build()
		}
	}
	// Packages of the module that the roots reach are built too, but only
	// ssautil's roots were; build every module package SSA created.
	for _, p := range prog.AllPackages() {
		if strings.HasPrefix(p.Pkg.Path(), oracleModule) {
			p.Build()
		}
	}
	g, c := oracleCallGraph(prog)
	return &oracleProgram{prog: prog, graph: g, cha: c, roots: ssapkgs}, nil
}

func loadOracleJobs() (map[string]oracleJob, error) {
	op, err := loadOracleProgram(true, "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs")
	if err != nil {
		return nil, err
	}
	oracleJobsProg = op
	prog, jp := op.prog, op.roots[0]
	out := map[string]oracleJob{}
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
		id := constantReturn(prog.MethodValue(idSel))
		if id == "" {
			return nil, fmt.Errorf("job type %s: ID() does not return a constant string", tn.Name())
		}
		w, why, cp, calls, via := derivedWrites(op, prog.MethodValue(runSel))
		out[id] = oracleJob{writes: w, evidence: why, checkpoints: cp, calls: calls, via: via}
	}
	return out, nil
}

// oracleJobsProg is the program loadOracleJobs walked, for the tests that ask
// about code the jobs reach beyond the jobs themselves.
var oracleJobsProg *oracleProgram

var (
	oracleOnce   sync.Once
	oracleResult map[string]oracleJob
	oracleErr    error
)

// oracleJobs loads and analyses the jobs package once for every test that asks.
func oracleJobs(t *testing.T) map[string]oracleJob {
	t.Helper()
	oracleOnce.Do(func() { oracleResult, oracleErr = loadOracleJobs() })
	if oracleErr != nil {
		t.Fatal(oracleErr)
	}
	return oracleResult
}

// oracleJobsProgram is the program the oracle walked the jobs in.
func oracleJobsProgram(t *testing.T) *oracleProgram {
	t.Helper()
	oracleJobs(t)
	return oracleJobsProg
}

// oracleKinds is every job's native kind, built with the real write sets.
func oracleKinds(t *testing.T) []tasks.Definition {
	t.Helper()
	defs, err := jobs.Kinds(jobs.KindDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

// constantReturn is the string a one-statement method returns.
func constantReturn(f *ssa.Function) string {
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if r, ok := in.(*ssa.Return); ok && len(r.Results) == 1 {
				if c, ok := r.Results[0].(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
					return constant.StringVal(c.Value)
				}
			}
		}
	}
	return ""
}

// TestJobWrites_MatchSSAOracle derives every job's write set from its code by
// NAME (the interface writes and file mutations its Run reaches, classified by
// the receiver's and the method's noun) and requires the Effects the native kind
// declares to cover it, kind for kind. It is a floor: it stops at the store and
// reads names, so it cannot see what a store method writes underneath (the stale
// stamp and the ident: rows of ModifyBook, the books aggregate behind a file-row
// method). Equality with what the store writes is TestJobWrites_DerivedFromCodeAndSharedTable
// in writes_trace_test.go, which reads the shared method-to-resource table.
// Before 2026-11-16 this test demanded equality with the name classes, which
// pinned the table to the oracle's blind spot (stack review M2).
func TestJobWrites_MatchSSAOracle(t *testing.T) {
	if os.Getenv("JOBWRITES_ORACLE_DUMP") != "" {
		for id, j := range oracleJobs(t) {
			fmt.Printf("ORACLE %s: %v checkpoints=%v %v\n", id, j.writes, j.checkpoints, j.evidence)
			for c, in := range j.calls {
				fmt.Printf("CALL %s: %s <- %s\n", id, c, in)
			}
			if os.Getenv("JOBWRITES_ORACLE_DUMP") == "paths" {
				for fn := range j.via {
					fmt.Printf("PATH %s: %s\n", id, j.chain(fn))
				}
			}
		}
		return
	}
	derived := oracleJobs(t)
	defs := oracleKinds(t)
	if len(defs) != len(derived) {
		t.Errorf("%d kinds but the oracle found %d jobs", len(defs), len(derived))
	}
	for _, d := range defs {
		id := strings.TrimPrefix(string(d.Kind()), "books.maintenance.")
		dj, ok := derived[id]
		if !ok {
			t.Errorf("%s: the oracle found no job of that ID", id)
			continue
		}
		fx := tasks.CommonOf(d).Effects
		var got []string
		for _, r := range fx.WriteSet() {
			got = append(got, r.Kind)
		}
		sort.Strings(got)
		for _, w := range dj.writes {
			if !slices.Contains(got, w) {
				t.Errorf("%s: declared writes %v lack %s, which the code names (evidence %v)", id, got, w, dj.evidence)
			}
		}
	}
}

// TestKindShell_ResumeContinueMeansCheckpoint: a kind resumes (ResumeContinue)
// exactly when the code of its job restores a checkpoint, and every other job
// that HEAD restarts runs again from zero. The set of checkpointing jobs is
// read from the code, not from checkpointingJobs.
func TestKindShell_ResumeContinueMeansCheckpoint(t *testing.T) {
	derived := oracleJobs(t)
	for _, d := range oracleKinds(t) {
		id := strings.TrimPrefix(string(d.Kind()), "books.maintenance.")
		got := tasks.CommonOf(d).Resume
		if got == tasks.ResumeContinue && !derived[id].checkpoints {
			t.Errorf("%s resumes with ResumeContinue but its Run reaches no checkpoint", id)
		}
		if derived[id].checkpoints && got != tasks.ResumeContinue {
			t.Errorf("%s checkpoints but its resume policy is %v, not continue", id, got)
		}
	}
	var cp []string
	for id, j := range derived {
		if j.checkpoints {
			cp = append(cp, id)
		}
	}
	sort.Strings(cp)
	if want := []string{"backfill-file-hashes", "recompute-book-aggregates"}; !slices.Equal(cp, want) {
		t.Errorf("jobs that checkpoint = %v, want %v", cp, want)
	}
}
