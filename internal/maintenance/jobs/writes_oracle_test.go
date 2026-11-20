// file: internal/maintenance/jobs/writes_oracle_test.go
// version: 3.4.0
// guid: 40eadd67-76ac-419f-a3fe-7d1ed5e586e2
// last-edited: 2026-11-19

package jobs_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
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
var oracleNotWalked = regexp.MustCompile(`\.ScanStandDownHold\)\.(Checkpoint|Beat|beat)$`)

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
//
// ReportPathNotify and LedgerPathNotify are listed with the openers although they
// open no report: they create the report (or ledger) directory when it is absent,
// remove write bits from one that has too many, and create and remove a probe file
// in it. A job that resolves its report path and then fails before writing has
// still changed the disk, so it declares the files resource whether or not it gets
// as far as an opener. checkOutsideFileWriters keeps every name in this table
// pointing at a function that exists.
var oracleOutsideFileWriters = map[string]map[string]bool{
	"github.com/falkcorp/mediacore/library": {
		"OpenReportFile": true, "WriteReportFile": true, "PreflightReportFile": true,
		"ReportPathNotify": true, "LedgerPathNotify": true,
	},
}

// checkOutsideFileWriters fails when a name in oracleOutsideFileWriters is not a
// function of a package the load reached. A misspelled or renamed entry would
// otherwise match no call and the oracle would go on reporting a job as writing no
// files, the exact gap the table exists to close.
func checkOutsideFileWriters(prog *ssa.Program) error {
	var errs []error
	for path, names := range oracleOutsideFileWriters {
		pkg := prog.ImportedPackage(path)
		if pkg == nil {
			errs = append(errs, fmt.Errorf("oracleOutsideFileWriters: package %s is not in the jobs package's import graph", path))
			continue
		}
		for name := range names {
			if pkg.Func(name) == nil {
				errs = append(errs, fmt.Errorf("oracleOutsideFileWriters: %s has no function %s", path, name))
			}
		}
	}
	return errors.Join(errs...)
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
	// flows is what the module stores into each package variable, struct field,
	// slice and map, so a function value loaded from one can be named.
	flows *oracleFlows
	// impls caches declaredImpls, cache resolve and ifaceOpen.
	impls *oracleImplCache
	cache *oracleResolveCache
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
	// params are those parameters.
	params []*ssa.Parameter
	// outside: it is a result of a call into code outside the module (the cancel
	// function context.WithCancel returns). Such a value is a closure of that
	// code, which the walk does not enter, so it names no function of the module
	// and is neither resolved nor unresolved: it is the open "calls that leave the
	// module" item, not a gap in the module's own callbacks.
	outside bool
	// viaCall: it is a result of a call to a module function or a function value,
	// whose returned functions the module's own return statements name (or, when
	// they return what outside code made, nothing the walk can follow).
	viaCall bool
	// declared: it was read from a package variable or a field of one, which the
	// module's own stores into it fully describe: a variable nothing in the
	// module stores to (a hook only tests set) holds nothing the walk can follow,
	// and every function of its signature is not an answer for it.
	declared bool
	// name is what the value was read from: the variable's or field's name.
	name string
	// other: it was read from a field, a package variable, a slice or a call
	// result, which only the call graph can resolve.
	other bool
}

func (o *oracleOrigin) merge(p oracleOrigin) {
	o.param = o.param || p.param
	o.params = append(o.params, p.params...)
	o.other = o.other || p.other
	o.outside = o.outside || p.outside
	o.viaCall = o.viaCall || p.viaCall
	o.declared = o.declared || p.declared
	if o.name == "" {
		o.name = p.name
	}
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
		o.params = append(o.params, v)
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
			switch x := v.X.(type) {
			case *ssa.Alloc, *ssa.FreeVar:
				o.merge(oracleOriginOf(v.X, seen))
				return o
			case *ssa.FieldAddr, *ssa.IndexAddr:
				// An element or field of something: of an object built in
				// this function, it is stored here; of an object a caller
				// supplies (a receiver, an options struct), a package
				// variable or anything else, the module's stores into that
				// field or element say, and the call graph is asked too
				// (round-2 review B1 (a): a parameter-rooted load is not a
				// parameter, and leaving it to the caller dropped a
				// callback a constructor's parameter set).
				if oracleBaseOf(x, map[ssa.Value]bool{}) != oracleBaseLocal {
					o.other, o.declared = true, true
				}
				o.name = oracleLoadName(x)
				return o
			case *ssa.Global:
				o.other, o.declared = true, true
				o.name = x.Name()
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
	case *ssa.Extract:
		switch {
		case oracleLeavesModule(v.Tuple):
			o.outside, o.name = true, oracleOutsideName(v.Tuple)
		case isCall(v.Tuple):
			o.other, o.viaCall = true, true
		default:
			// a comma-ok type assertion, a map lookup, a receive
			o.other = true
		}
	case *ssa.Call:
		switch {
		case oracleLeavesModule(v):
			o.outside, o.name = true, oracleOutsideName(v)
		default:
			o.other, o.viaCall = true, true
		}
	default:
		o.other = true
	}
	return o
}

// oracleUnresolvedKey is the call key of a call through a function value the walk
// could not name a callee for: UNRESOLVED.<function>#<what it was read from>.
func oracleUnresolvedKey(f *ssa.Function, what string) string {
	if what == "" {
		what = "value"
	}
	return "UNRESOLVED." + f.String() + "#" + what
}

// oracleLoadName is the name of the field an address of a field or element is
// of, else of what it indexes.
func oracleLoadName(v ssa.Value) string {
	for {
		switch a := v.(type) {
		case *ssa.FieldAddr:
			if fv := oracleFieldVar(a); fv != nil {
				return fv.Name()
			}
			return "field"
		case *ssa.IndexAddr:
			v = a.X
		case *ssa.UnOp:
			v = a.X
		case *ssa.Global:
			return a.Name()
		default:
			return "element"
		}
	}
}

type oracleBase int

const (
	oracleBaseOther oracleBase = iota
	oracleBaseParam
	oracleBaseLocal
)

// oracleBaseOf is what an address of a field or element is rooted in: an object
// a caller supplies (a parameter, a captured variable, or a copy of one), an
// object this function built, or anything else (a package variable, a call
// result).
func oracleBaseOf(v ssa.Value, seen map[ssa.Value]bool) oracleBase {
	if seen[v] {
		return oracleBaseLocal
	}
	seen[v] = true
	switch v := v.(type) {
	case *ssa.Parameter, *ssa.FreeVar:
		return oracleBaseParam
	case *ssa.FieldAddr:
		return oracleBaseOf(v.X, seen)
	case *ssa.IndexAddr:
		return oracleBaseOf(v.X, seen)
	case *ssa.Slice:
		return oracleBaseOf(v.X, seen)
	case *ssa.UnOp:
		if v.Op == token.MUL {
			return oracleBaseOf(v.X, seen)
		}
	case *ssa.Alloc:
		res := oracleBaseLocal
		if parent := v.Parent(); parent != nil {
			for _, b := range parent.Blocks {
				for _, in := range b.Instrs {
					if st, ok := in.(*ssa.Store); ok && st.Addr == v {
						switch oracleBaseOf(st.Val, seen) {
						case oracleBaseParam:
							return oracleBaseParam
						case oracleBaseOther:
							res = oracleBaseOther
						}
					}
				}
			}
		}
		return res
	case *ssa.MakeSlice, *ssa.Const:
		return oracleBaseLocal
	}
	return oracleBaseOther
}

func isCall(v ssa.Value) bool {
	_, ok := v.(*ssa.Call)
	return ok
}

// oracleOutsideName is the outside function a call result was made by:
// "<package path>.<name>".
func oracleOutsideName(v ssa.Value) string {
	c := v.(*ssa.Call)
	owner := oracleOwner(c.Call.StaticCallee())
	return owner.Pkg.Pkg.Path() + "." + owner.Name()
}

// oracleLeavesModule is whether v is a call whose callee is code outside the
// module (a static callee of another module or the standard library).
func oracleLeavesModule(v ssa.Value) bool {
	c, ok := v.(*ssa.Call)
	if !ok {
		return false
	}
	callee := c.Call.StaticCallee()
	if callee == nil {
		return false
	}
	owner := oracleOwner(callee)
	return owner.Pkg != nil && !strings.HasPrefix(owner.Pkg.Pkg.Path(), oracleModule)
}

// oracleFlows is what the module stores into the places a function value is
// loaded from, field-sensitive and flow-insensitive: every store into a package
// variable, into a struct field (by the field's declaration, across every
// struct of that type), into a captured or local cell, and into a slice, array
// or map (an element store, a composite literal, an append, a copy). A function value
// the module loads from one of them, and hands to a shared helper, is one of the
// functions stored there.
type oracleFlows struct {
	globals map[*ssa.Global][]ssa.Value
	fields  map[*types.Var][]ssa.Value
	cells   map[ssa.Value][]ssa.Value
	elems   map[any][]ssa.Value
	// copies are the source containers copy() copied into each container.
	copies map[any][]ssa.Value
}

func oracleFieldVar(fa *ssa.FieldAddr) *types.Var {
	ptr, ok := fa.X.Type().Underlying().(*types.Pointer)
	if !ok {
		return nil
	}
	st, ok := ptr.Elem().Underlying().(*types.Struct)
	if !ok || fa.Field >= st.NumFields() {
		return nil
	}
	return st.Field(fa.Field)
}

// oracleContainer is the identity a container is stored into and loaded from: the
// package variable or struct field it was read from, else the value itself.
func oracleContainer(v ssa.Value) any {
	if u, ok := v.(*ssa.UnOp); ok && u.Op == token.MUL {
		switch x := u.X.(type) {
		case *ssa.Global:
			return x
		case *ssa.FieldAddr:
			if fv := oracleFieldVar(x); fv != nil {
				return fv
			}
		}
	}
	return v
}

func buildOracleFlows(prog *ssa.Program) *oracleFlows {
	fl := &oracleFlows{
		globals: map[*ssa.Global][]ssa.Value{}, fields: map[*types.Var][]ssa.Value{},
		cells: map[ssa.Value][]ssa.Value{}, elems: map[any][]ssa.Value{},
		copies: map[any][]ssa.Value{},
	}
	for f := range ssautil.AllFunctions(prog) {
		if f.Pkg != nil && !strings.HasPrefix(f.Pkg.Pkg.Path(), oracleModule) {
			continue
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch in := in.(type) {
				case *ssa.Store:
					switch a := in.Addr.(type) {
					case *ssa.Global:
						fl.globals[a] = append(fl.globals[a], in.Val)
					case *ssa.FieldAddr:
						if fv := oracleFieldVar(a); fv != nil {
							fl.fields[fv] = append(fl.fields[fv], in.Val)
						}
					case *ssa.IndexAddr:
						k := oracleContainer(a.X)
						fl.elems[k] = append(fl.elems[k], in.Val)
					case *ssa.Alloc, *ssa.FreeVar:
						// A store through a closure's captured variable is
						// a store into the cell it was captured from.
						if cell := oracleCellRoot(a); cell != nil {
							fl.cells[cell] = append(fl.cells[cell], in.Val)
						}
					}
				case *ssa.MapUpdate:
					k := oracleContainer(in.Map)
					fl.elems[k] = append(fl.elems[k], in.Value)
				case *ssa.Call:
					if b, ok := in.Call.Value.(*ssa.Builtin); ok && b.Name() == "copy" && len(in.Call.Args) == 2 {
						k := oracleContainer(in.Call.Args[0])
						fl.copies[k] = append(fl.copies[k], in.Call.Args[1])
					}
				}
			}
		}
	}
	return fl
}

// oracleOpenExported is whether a package variable or struct field with an
// exported name counts as open: the program is the jobs package and what it
// imports, so the wiring that imports the jobs (internal/server, cmd) is not in
// it, and a store it makes into an exported variable or field is invisible.
const oracleOpenExported = true

// oracleRes is what a function value can be, as far as the loaded program says.
//
// The resolver is default-deny: every value it has no rule for, and every value
// that can come from code it cannot see, adds a reason to open, and the walk
// records each reason as UNRESOLVED.<function>#<reason>. A set is trusted as
// complete only when every leaf of it was read from the program.
type oracleRes struct {
	// fns are the functions it can be.
	fns []*ssa.Function
	// params are parameters it can be, of the function that holds the value
	// (or of the function whose closure captures it), reached without passing
	// through a variable, a field or an element: what each caller passes is
	// read where that caller is walked (see derivedWrites).
	params []*ssa.Parameter
	// open are the reasons the set is not known to be complete: a value made by
	// code outside the module ("outside:<function>"), a variable or field
	// nothing in the program stores a function into ("<name>"), one code
	// outside the program can store into ("<name>:exported", "<name>:set by
	// <function>(<parameter>)"), or a shape the resolver has no rule for.
	open []string
	// known is set, when an interface value is resolved (oracleResolver.iface),
	// by a leaf whose concrete type the program shows: a conversion to the
	// interface the call graph sees.
	known bool
}

func (r *oracleRes) merge(o oracleRes) {
	r.known = r.known || o.known
	r.fns = append(r.fns, o.fns...)
	r.params = append(r.params, o.params...)
	r.open = append(r.open, o.open...)
}

func (r *oracleRes) openf(format string, args ...any) {
	r.open = append(r.open, fmt.Sprintf(format, args...))
}

// oracleResolver resolves function values. memo holds each value's result
// (nil while it is being resolved, which breaks a cycle); retStack is the
// functions whose return statements are being read, which breaks recursion;
// leaf is the variable or field being read, which names the reasons a parameter
// reached through it gives.
type oracleResolver struct {
	op       *oracleProgram
	memo     map[oracleMemoKey]*oracleRes
	retStack map[*ssa.Function]bool
	leaf     string
	// iface resolves an interface value instead of a function value: a
	// conversion to the interface is a known leaf, a parameter is always read
	// from its callers, and what is wanted is only whether anything is open.
	iface bool
}

type oracleMemoKey struct {
	v    any
	mode string
}

// resolve is what the function value v can be, read in the function that holds
// it.
func (op *oracleProgram) resolve(v ssa.Value) oracleRes {
	k := oracleCacheKey{op.graph, v, false}
	if got, ok := op.cache.get(k); ok {
		return got
	}
	r := &oracleResolver{op: op, memo: map[oracleMemoKey]*oracleRes{}, retStack: map[*ssa.Function]bool{}}
	out := oracleDedupe(r.value(v, true))
	op.cache.put(k, out)
	return out
}

// oracleResolveCache holds resolve's and ifaceOpen's answers. An answer depends
// only on the value and the call graph (a parameter is read from the graph's
// callers), so it is keyed by both: a test that swaps the graph gets its own.
type oracleResolveCache struct {
	mu sync.Mutex
	m  map[oracleCacheKey]oracleRes
}

type oracleCacheKey struct {
	g     *callgraph.Graph
	v     ssa.Value
	iface bool
}

func (c *oracleResolveCache) get(k oracleCacheKey) (oracleRes, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[k]
	return r, ok
}

func (c *oracleResolveCache) put(k oracleCacheKey, r oracleRes) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = r
}

// ifaceOpen is why the interface value v can hold a type the call graph does not
// see flow to it (it comes from a parameter of an exported function, an exported
// field the wiring fills, code outside the module), or nil when every type it can
// hold is converted to the interface where the program shows it.
func (op *oracleProgram) ifaceOpen(v ssa.Value) []string {
	k := oracleCacheKey{op.graph, v, true}
	if got, ok := op.cache.get(k); ok {
		return got.open
	}
	r := &oracleResolver{op: op, memo: map[oracleMemoKey]*oracleRes{}, retStack: map[*ssa.Function]bool{}, iface: true}
	out := oracleDedupe(r.value(v, false))
	op.cache.put(k, out)
	return out.open
}

// oracleImplCache holds declaredImpls' answers per interface method.
type oracleImplCache struct {
	mu sync.Mutex
	m  map[string][]*ssa.Function
}

// declaredImpls is the method of every named type the module's loaded packages
// declare that implements the interface com is called on: what the wiring can
// pass, whether or not the program converts the type to an interface (CHA knows
// only the types it does, so a type only the unloaded wiring converts would be
// missing). A type declared inside a function, or in a package the program does
// not load, is not seen.
func (op *oracleProgram) declaredImpls(com *ssa.CallCommon) []*ssa.Function {
	iface, ok := com.Value.Type().Underlying().(*types.Interface)
	if !ok {
		return nil // a type parameter: the call graph's answer stands
	}
	key := types.TypeString(com.Value.Type(), nil) + "." + com.Method.Id()
	op.impls.mu.Lock()
	defer op.impls.mu.Unlock()
	if got, ok := op.impls.m[key]; ok {
		return got
	}
	var out []*ssa.Function
	for _, p := range op.prog.AllPackages() {
		if !strings.HasPrefix(p.Pkg.Path(), oracleModule) {
			continue
		}
		scope := p.Pkg.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok || named.TypeParams().Len() > 0 || types.IsInterface(named) {
				continue
			}
			for _, t := range []types.Type{named, types.NewPointer(named)} {
				if types.Implements(t, iface) {
					if f := op.prog.LookupMethod(t, com.Method.Pkg(), com.Method.Name()); f != nil {
						out = append(out, f)
					}
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	op.impls.m[key] = out
	return out
}

func oracleDedupe(r oracleRes) oracleRes {
	out := oracleRes{known: r.known}
	seenF, seenP, seenO := map[*ssa.Function]bool{}, map[*ssa.Parameter]bool{}, map[string]bool{}
	for _, f := range r.fns {
		if !seenF[f] {
			seenF[f] = true
			out.fns = append(out.fns, f)
		}
	}
	for _, p := range r.params {
		if !seenP[p] {
			seenP[p] = true
			out.params = append(out.params, p)
		}
	}
	for _, o := range r.open {
		if !seenO[o] {
			seenO[o] = true
			out.open = append(out.open, o)
		}
	}
	sort.Strings(out.open)
	return out
}

// memoized runs compute once per (v, mode) in this resolver.
func (r *oracleResolver) memoized(v any, mode string, compute func() oracleRes) oracleRes {
	k := oracleMemoKey{v, mode}
	if got, ok := r.memo[k]; ok {
		if got == nil {
			return oracleRes{} // a cycle: what it adds is being collected already
		}
		return *got
	}
	r.memo[k] = nil
	out := compute()
	r.memo[k] = &out
	return out
}

// value is what v can be. direct is false once the walk back from the call has
// passed through a variable, a field or an element, where a parameter it meets
// is no longer the holder's to be asked about but every caller's.
func (r *oracleResolver) value(v ssa.Value, direct bool) oracleRes {
	if v == nil {
		return oracleRes{}
	}
	mode := "value"
	if !direct {
		mode = "stored"
	}
	return r.memoized(v, mode, func() (out oracleRes) {
		switch v := v.(type) {
		case *ssa.Const:
			// nil: calling it panics, it writes nothing
			out.known = r.iface
		case *ssa.MakeInterface:
			if r.iface {
				out.known = true
			} else {
				out = r.value(v.X, direct)
			}
		case *ssa.Function:
			out.fns = append(out.fns, v)
		case *ssa.MakeClosure:
			if f, ok := v.Fn.(*ssa.Function); ok {
				out.fns = append(out.fns, f)
			}
		case *ssa.Parameter:
			if direct && !r.iface {
				out.params = append(out.params, v)
			} else {
				out = r.fromCallers(v, false)
			}
		case *ssa.FreeVar:
			if b := oracleBinding(v); b != nil {
				out = r.value(b, direct)
			} else {
				out.openf("captured %s", v.Name())
			}
		case *ssa.Phi:
			for _, e := range v.Edges {
				out.merge(r.value(e, direct))
			}
		case *ssa.ChangeType:
			out = r.value(v.X, direct)
		case *ssa.Convert:
			out = r.value(v.X, direct)
		case *ssa.ChangeInterface:
			out = r.value(v.X, direct)
		case *ssa.TypeAssert:
			out = r.value(v.X, direct)
		case *ssa.Lookup:
			out = r.elems(v.X)
		case *ssa.Field:
			fv := oracleStructField(v.X.Type(), v.Field)
			if fv == nil {
				out.openf("a field of %s", v.X.Type())
				break
			}
			out = r.leafOf(fv, fv.Name(), r.op.flows.fields[fv], fv.Exported())
		case *ssa.UnOp:
			if v.Op != token.MUL {
				out.openf("received from a channel")
				break
			}
			out = r.load(v.X, direct)
		case *ssa.Call:
			out = r.callResult(v, -1, direct)
		case *ssa.Extract:
			switch t := v.Tuple.(type) {
			case *ssa.Call:
				out = r.callResult(t, v.Index, direct)
			case *ssa.TypeAssert:
				out = r.value(t.X, direct)
			case *ssa.Lookup:
				out = r.elems(t.X)
			case *ssa.Next:
				if rg, ok := t.Iter.(*ssa.Range); ok && v.Index == 2 {
					out = r.elems(rg.X)
				} else {
					out.openf("a range key")
				}
			default:
				out.openf("a %T result", v.Tuple)
			}
		default:
			out.openf("a %T", v)
		}
		return out
	})
}

// load is what was stored at addr, the address a function value is loaded from.
func (r *oracleResolver) load(addr ssa.Value, direct bool) (out oracleRes) {
	switch x := addr.(type) {
	case *ssa.Global:
		return r.leafOf(x, x.Name(), r.op.flows.globals[x], ast.IsExported(x.Name()))
	case *ssa.FieldAddr:
		fv := oracleFieldVar(x)
		if fv == nil {
			out.openf("a field")
			return out
		}
		return r.leafOf(fv, fv.Name(), r.op.flows.fields[fv], fv.Exported())
	case *ssa.IndexAddr:
		return r.elems(x.X)
	case *ssa.Alloc, *ssa.FreeVar:
		cell := oracleCellRoot(x)
		if cell == nil {
			out.openf("a captured variable")
			return out
		}
		if a, ok := cell.(*ssa.Alloc); ok {
			if why := oracleCellEscapes(a); why != "" {
				out.openf("%s:%s", a.Comment, why)
			}
		}
		// A local variable: zero (nil) when nothing is stored, which writes
		// nothing; a parameter stored into it is still the holder's.
		for _, st := range r.op.flows.cells[cell] {
			out.merge(r.value(st, direct))
		}
		return out
	}
	out.openf("loaded through a %T", addr)
	return out
}

// leafOf is what a package variable or struct field (key) holds: every value the
// program stores into it, with a parameter so stored read from its function's
// callers. A leaf nothing stores a function into is a hook only code outside the
// program sets ("<name>"); one with an exported name can be stored into by the
// wiring outside the program ("<name>:exported").
func (r *oracleResolver) leafOf(key any, name string, stores []ssa.Value, exported bool) oracleRes {
	return r.memoized(key, "leaf", func() (out oracleRes) {
		prev := r.leaf
		r.leaf = name
		defer func() { r.leaf = prev }()
		for _, st := range stores {
			out.merge(r.value(st, false))
		}
		if exported && oracleOpenExported {
			out.openf("%s:exported", name)
		}
		if len(out.fns) == 0 && len(out.open) == 0 && len(out.params) == 0 && !out.known {
			out.openf("%s", name)
		}
		return out
	})
}

// fromCallers is what the program's callers pass for a parameter reached through
// a variable, a field or an element (a setter's or a constructor's parameter),
// or the elements of what they pass (elems). Every caller in the program counts,
// not only the job's: the variable is shared. A function code outside the
// program can call (an exported one), or that nothing in the program calls,
// leaves the set open.
func (r *oracleResolver) fromCallers(p *ssa.Parameter, elems bool) oracleRes {
	mode := "callers"
	if elems {
		mode = "callers-elems"
	}
	return r.memoized(p, mode, func() (out oracleRes) {
		fn := p.Parent()
		idx := oracleParamIndex(p)
		what := r.leaf
		if what == "" {
			what = "parameter"
		}
		setBy := fmt.Sprintf("%s:set by %s(%s)", what, fn.Name(), p.Name())
		callers := 0
		if node := r.op.graph.Nodes[fn]; node != nil && idx >= 0 {
			for _, e := range node.In {
				if e.Site == nil {
					continue
				}
				arg, ok := oracleArgAt(e.Site.Common(), idx)
				if !ok {
					out.openf("%s, passed in a way the resolver cannot read", setBy)
					continue
				}
				callers++
				if elems {
					out.merge(r.elems(arg))
				} else {
					out.merge(r.value(arg, false))
				}
			}
		}
		if callers == 0 || oracleCallableOutside(fn) {
			out.openf("%s", setBy)
		}
		return out
	})
}

// elems is what the elements of the slice, array or map c can be.
func (r *oracleResolver) elems(c ssa.Value) oracleRes {
	if c == nil {
		return oracleRes{}
	}
	return r.memoized(c, "elems", func() (out oracleRes) {
		key := oracleContainer(c)
		for _, sv := range r.op.flows.elems[key] {
			out.merge(r.value(sv, false))
		}
		for _, src := range r.op.flows.copies[key] {
			out.merge(r.elems(src))
		}
		switch c := c.(type) {
		case *ssa.UnOp:
			if c.Op != token.MUL {
				out.openf("elements received from a channel")
				break
			}
			var stores []ssa.Value
			name, exported := "", false
			switch x := c.X.(type) {
			case *ssa.Global:
				stores, name, exported = r.op.flows.globals[x], x.Name(), ast.IsExported(x.Name())
			case *ssa.FieldAddr:
				if fv := oracleFieldVar(x); fv != nil {
					stores, name, exported = r.op.flows.fields[fv], fv.Name(), fv.Exported()
				} else {
					out.openf("elements of a field")
				}
			case *ssa.Alloc, *ssa.FreeVar:
				if cell := oracleCellRoot(x); cell != nil {
					stores = r.op.flows.cells[cell]
				} else {
					out.openf("elements of a captured variable")
				}
			default:
				out.openf("elements loaded through a %T", c.X)
			}
			for _, sv := range stores {
				out.merge(r.elems(sv))
			}
			if exported && oracleOpenExported {
				out.openf("%s:exported", name)
			}
		case *ssa.Parameter:
			prev := r.leaf
			r.leaf = c.Name()
			out.merge(r.fromCallers(c, true))
			r.leaf = prev
		case *ssa.FreeVar:
			if b := oracleBinding(c); b != nil {
				out.merge(r.elems(b))
			} else {
				out.openf("elements of captured %s", c.Name())
			}
		case *ssa.Call:
			if b, ok := c.Call.Value.(*ssa.Builtin); ok && b.Name() == "append" {
				for _, a := range c.Call.Args {
					out.merge(r.elems(a))
				}
				break
			}
			out.openf("elements of a call result")
		case *ssa.Slice:
			out.merge(r.elems(c.X))
		case *ssa.ChangeType:
			out.merge(r.elems(c.X))
		case *ssa.Phi:
			for _, e := range c.Edges {
				out.merge(r.elems(e))
			}
		case *ssa.MakeSlice, *ssa.MakeMap, *ssa.Alloc:
			// Built here: what is stored into it (above), unless it is handed
			// to code that can fill it.
			why, into := r.op.containerEscapes(c)
			if why != "" {
				out.openf("%s", why)
			}
			for _, p := range into {
				for _, sv := range r.op.flows.elems[p] {
					out.merge(r.value(sv, false))
				}
				for _, src := range r.op.flows.copies[p] {
					out.merge(r.elems(src))
				}
			}
		case *ssa.Const:
			// a nil slice or map: no elements
		default:
			out.openf("elements of a %T", c)
		}
		return out
	})
}

// callResult is the functions the idx-th result (or the one result, idx < 0) of
// a call can be.
func (r *oracleResolver) callResult(c *ssa.Call, idx int, direct bool) (out oracleRes) {
	com := &c.Call
	if com.IsInvoke() {
		name := oracleIfaceMethodName(com)
		if pkg := com.Method.Pkg(); pkg == nil || !strings.HasPrefix(pkg.Path(), oracleModule) {
			out.openf("outside:%s", name)
			return out
		}
		// A module interface: what the implementations the call graph gives the
		// call return.
		impls := r.op.calleesAt(c)
		if len(impls) == 0 {
			out.openf("call result:%s", name)
		}
		for _, impl := range impls {
			out.merge(r.returnedBy(impl, c, idx, direct))
		}
		return out
	}
	if callee := com.StaticCallee(); callee != nil {
		return r.returnedBy(callee, c, idx, direct)
	}
	if _, ok := com.Value.(*ssa.Builtin); ok {
		return out
	}
	// A call through a function value: what the functions it can be return.
	fv := r.value(com.Value, direct)
	// A parameter called for its result is not the caller's to check (the
	// result is used here): what the program's callers pass, and the
	// reason when that set is not every caller (round-3).
	for _, p := range fv.params {
		fv.merge(r.fromCallers(p, false))
	}
	out.open = append(out.open, fv.open...)
	for _, f := range fv.fns {
		out.merge(r.returnedBy(f, c, idx, direct))
	}
	if len(fv.fns) == 0 && len(fv.open) == 0 && len(fv.params) == 0 {
		out.openf("call result")
	}
	return out
}

// returnedBy is what fn returns at the idx-th result when called at c: what its
// return statements name, with a parameter it returns replaced by what c passes.
func (r *oracleResolver) returnedBy(fn *ssa.Function, c *ssa.Call, idx int, direct bool) (out oracleRes) {
	if owner := oracleOwner(fn); owner.Pkg != nil && !strings.HasPrefix(owner.Pkg.Pkg.Path(), oracleModule) {
		out.openf("outside:%s", oracleFuncName(fn))
		return out
	}
	if len(fn.Blocks) == 0 {
		out.openf("call result:%s", fn.Name())
		return out
	}
	if r.retStack[fn] {
		return out // recursion: the outer reading collects it
	}
	r.retStack[fn] = true
	defer delete(r.retStack, fn)
	sub := &oracleResolver{op: r.op, memo: map[oracleMemoKey]*oracleRes{}, retStack: r.retStack, leaf: r.leaf, iface: r.iface}
	for _, b := range fn.Blocks {
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		for i, res := range ret.Results {
			if idx >= 0 && i != idx {
				continue
			}
			got := sub.value(res, true)
			out.known = out.known || got.known
			out.fns = append(out.fns, got.fns...)
			out.open = append(out.open, got.open...)
			for _, p := range got.params {
				pi := oracleParamIndex(p)
				if p.Parent() != fn || pi < 0 || pi >= len(c.Call.Args) {
					out.openf("call result:%s", fn.Name())
					continue
				}
				out.merge(r.value(c.Call.Args[pi], direct))
			}
		}
	}
	return out
}

// calleesAt is the call graph's callees of one call instruction.
func (op *oracleProgram) calleesAt(c ssa.CallInstruction) []*ssa.Function {
	n := op.graph.Nodes[c.Parent()]
	if n == nil {
		return nil
	}
	var out []*ssa.Function
	for _, e := range n.Out {
		if e.Site == c {
			out = append(out, e.Callee.Func)
		}
	}
	return out
}

// containerEscapes says why a slice, array or map built in a function can hold
// elements the program's element stores into it do not show (it is handed to a
// call through a value, or to a function outside the module, which can fill it),
// or "" when it cannot; and into is the parameters of the module functions it is
// handed to, whose element stores (keyed by the parameter in buildOracleFlows)
// fill it as well.
func (op *oracleProgram) containerEscapes(c ssa.Value) (why string, into []*ssa.Parameter) {
	seen := map[ssa.Value]bool{}
	var visit func(v ssa.Value)
	visit = func(v ssa.Value) {
		if seen[v] || why != "" {
			return
		}
		seen[v] = true
		refs := v.Referrers()
		if refs == nil {
			return
		}
		for _, in := range *refs {
			switch in := in.(type) {
			case *ssa.Slice:
				visit(in)
			case *ssa.ChangeType:
				visit(in)
			case *ssa.Phi:
				visit(in)
			case ssa.CallInstruction:
				com := in.Common()
				if _, ok := com.Value.(*ssa.Builtin); ok {
					continue
				}
				callee := com.StaticCallee()
				switch {
				case callee == nil:
					why = "elements filled by a call through a value"
				case oracleOwner(callee).Pkg != nil && !strings.HasPrefix(oracleOwner(callee).Pkg.Pkg.Path(), oracleModule):
					why = "elements filled by " + oracleFuncName(callee)
				default:
					for i, a := range com.Args {
						if a == v && i < len(callee.Params) {
							into = append(into, callee.Params[i])
						}
					}
				}
			}
		}
	}
	visit(c)
	return why, into
}

// oracleCellEscapes says why a local variable can be set by code the program's
// stores into it do not show (its address is handed on), or "".
func oracleCellEscapes(a *ssa.Alloc) string {
	refs := a.Referrers()
	if refs == nil {
		return ""
	}
	for _, in := range *refs {
		switch in := in.(type) {
		case *ssa.Store:
			if in.Val == a {
				return "its address is stored"
			}
		case *ssa.UnOp, *ssa.DebugRef, *ssa.MakeClosure:
		default:
			return "its address is passed on"
		}
	}
	return ""
}

// oracleBinding is the value a closure's captured variable was bound to where the
// closure was made.
func oracleBinding(fv *ssa.FreeVar) ssa.Value {
	fn := fv.Parent()
	idx := -1
	for i, x := range fn.FreeVars {
		if x == fv {
			idx = i
		}
	}
	if parent := fn.Parent(); parent != nil && idx >= 0 {
		for _, b := range parent.Blocks {
			for _, in := range b.Instrs {
				if mc, ok := in.(*ssa.MakeClosure); ok && mc.Fn == fn {
					return mc.Bindings[idx]
				}
			}
		}
	}
	return nil
}

// oracleCellRoot is the variable cell (an Alloc) a captured variable's address
// refers to, through any number of nested closures; an Alloc is its own root.
func oracleCellRoot(v ssa.Value) ssa.Value {
	for i := 0; i < 64; i++ {
		fv, ok := v.(*ssa.FreeVar)
		if !ok {
			return v
		}
		v = oracleBinding(fv)
		if v == nil {
			return nil
		}
	}
	return nil
}

func oracleParamIndex(p *ssa.Parameter) int {
	for i, q := range p.Parent().Params {
		if q == p {
			return i
		}
	}
	return -1
}

// oracleArgAt is the argument a call passes for the idx-th parameter of its
// callee (an interface call's receiver is not among its Args).
func oracleArgAt(com *ssa.CallCommon, idx int) (ssa.Value, bool) {
	at := idx
	if com.IsInvoke() {
		at = idx - 1
	}
	if at < 0 || at >= len(com.Args) {
		return nil, false
	}
	return com.Args[at], true
}

// oracleCallableOutside is whether code outside the loaded program can call fn:
// an exported function or method.
func oracleCallableOutside(fn *ssa.Function) bool {
	return fn.Parent() == nil && fn.Synthetic == "" && ast.IsExported(fn.Name())
}

// oracleStructField is the declaration of the idx-th field of a struct type.
func oracleStructField(t types.Type, idx int) *types.Var {
	st, ok := t.Underlying().(*types.Struct)
	if !ok || idx >= st.NumFields() {
		return nil
	}
	return st.Field(idx)
}

// oracleFuncName names a function outside the module: "<package path>.<name>",
// or "<package path>.<Type>.<method>".
func oracleFuncName(fn *ssa.Function) string {
	owner := oracleOwner(fn)
	if owner.Pkg == nil {
		return owner.String()
	}
	name := owner.Name()
	if r := owner.Signature.Recv(); r != nil {
		t := r.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			name = n.Obj().Name() + "." + name
		}
	}
	return owner.Pkg.Pkg.Path() + "." + name
}

// oracleIfaceMethodName names an interface method by the interface that declares
// it: "<package path>.<Interface>.<method>" (io.Closer.Close for a Close an
// interface embeds from io.Closer).
func oracleIfaceMethodName(com *ssa.CallCommon) string {
	t := com.Value.Type()
	if sig, ok := com.Method.Type().(*types.Signature); ok && sig.Recv() != nil {
		if _, named := sig.Recv().Type().(*types.Named); named {
			t = sig.Recv().Type()
		}
	}
	return types.TypeString(t, nil) + "." + com.Method.Name()
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

// oracleParamSupplied is whether every chain of walked callers that supplies p
// ends in a caller that passes something other than its own parameter: the
// value the walk resolved and recorded there.
func (op *oracleProgram) oracleParamSupplied(p *ssa.Parameter, walked map[*ssa.Function]bool, visiting map[*ssa.Parameter]bool) bool {
	if visiting[p] {
		return true
	}
	visiting[p] = true
	idx := oracleParamIndex(p)
	node := op.graph.Nodes[p.Parent()]
	if node == nil || idx < 0 {
		return false
	}
	callers := 0
	for _, e := range node.In {
		if e.Site == nil || !walked[e.Site.Parent()] {
			continue
		}
		arg, ok := oracleArgAt(e.Site.Common(), idx)
		if !ok {
			return false
		}
		callers++
		for _, q := range op.resolve(arg).params {
			if !op.oracleParamSupplied(q, walked, visiting) {
				return false
			}
		}
	}
	return callers > 0
}

// oracleIsArg is whether v is the address of one of com's arguments (not its
// callee value).
func oracleIsArg(com *ssa.CallCommon, v *ssa.Value) bool {
	for i := range com.Args {
		if v == &com.Args[i] {
			return true
		}
	}
	return false
}

// oracleUnion is a and the functions of b not in a.
func oracleUnion(a, b []*ssa.Function) []*ssa.Function {
	out := append([]*ssa.Function(nil), a...)
	for _, f := range b {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// oracleStoreKey names a call into the store package for the trace test: a
// function as STOREFN.<package>.<name>, a method as STORECONCRETE.<type>.<method>,
// the package named by its path below internal/books (store, store/aggtest), so
// two store packages that share a function name do not share a key.
func oracleStoreKey(owner *ssa.Function) string {
	path := owner.Pkg.Pkg.Path()
	rel := path[strings.Index(path, "/internal/books/")+len("/internal/books/"):]
	if r := owner.Signature.Recv(); r != nil {
		qual := func(p *types.Package) string {
			q := p.Path()
			return q[strings.Index(q, "/internal/books/")+len("/internal/books/"):]
		}
		return "STORECONCRETE." + types.TypeString(r.Type(), qual) + "." + owner.Name()
	}
	return "STOREFN." + rel + "." + owner.Name()
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
	// walked is the functions whose bodies the walk read.
	walked := map[*ssa.Function]bool{}
	// paramCalls are the calls through a parameter of the function that holds
	// them, checked once the walk is done (see oracleParamSupplied).
	type paramCall struct {
		f *ssa.Function
		p *ssa.Parameter
	}
	var paramCalls []paramCall
	unresolved := func(f *ssa.Function, why string) {
		calls[oracleUnresolvedKey(f, why)] = f.String()
	}
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
		walked[f] = true
		if cur != nil {
			via[f.String()] = cur.String()
		}
		parent := cur
		cur = f
		defer func() { cur = parent }()
		// The graph's callees of each call site in f.
		sites := op.sitesOf(f, false)
		var chaSites map[ssa.CallInstruction][]*ssa.Function
		var operands [16]*ssa.Value
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				ci, isCall := in.(ssa.CallInstruction)
				for _, v := range in.Operands(operands[:0]) {
					if v == nil || *v == nil {
						continue
					}
					if fn, ok := (*v).(*ssa.Function); ok {
						walk(fn)
						continue
					}
					// A function value the module loads from a field, a
					// package variable, a slice, a map or a call and passes
					// on is one of the functions stored there.
					if _, ok := (*v).Type().Underlying().(*types.Signature); !ok {
						continue
					}
					res := op.resolve(*v)
					for _, fn := range res.fns {
						walk(fn)
					}
					// Handed to a callee as an argument: what the resolver
					// cannot vouch for is recorded here, where the value is
					// made, whatever the callee is (a module helper that calls
					// it, a function outside the module that does: round-2
					// review B1 (d), (f)). A parameter passed on is checked
					// where it is finally called.
					if isCall && oracleIsArg(ci.Common(), v) {
						for _, why := range res.open {
							unresolved(f, why)
						}
					}
				}
				if !isCall {
					continue
				}
				com := ci.Common()
				if com.IsInvoke() {
					name := com.Method.Name()
					recv := types.TypeString(com.Value.Type(), func(p *types.Package) string { return p.Name() })
					impls := sites[ci]
					pkg := com.Method.Pkg()
					moduleIface := pkg != nil && strings.HasPrefix(pkg.Path(), oracleModule)
					if moduleIface && len(op.ifaceOpen(com.Value)) > 0 {
						// VTA's set is every type the program converts to the
						// interface on the way here. When the value can come
						// from code the program does not show (an exported
						// field the wiring fills, a parameter of an exported
						// function: the ManualOnlyFilesReader shape), that set
						// can be partial, and a method named like no write
						// (Undo, Rollback) is judged by its bodies alone
						// (round-2 review M1): every implementation the
						// module's loaded packages declare is walked as well.
						impls = oracleUnion(impls, op.declaredImpls(com))
					}
					// The call is judged by the interface method's own name
					// AND its implementations' bodies are walked, and the
					// result is the union. Progress and liveness reporting are
					// not books writes (oracleNotAWrite), so their names are
					// not judged, but what implements them is still walked.
					if _, ok := calls[recv+"."+name]; !ok {
						calls[recv+"."+name] = f.String()
					}
					verb := oracleVerb.MatchString(name)
					if verb && !oracleNotAWrite.MatchString(recv) {
						note(oracleResource(recv, name), recv+"."+name+" in "+f.String())
					}
					// A method named like no write whose bodies the walk
					// cannot all read: of an interface declared outside the
					// module (the walk never enters the implementations
					// outside the module), or of a module interface the
					// program holds no implementation of. Allowed by interface
					// method in oracleOutsideIfaceAllowed, or by key.
					if !verb && (!moduleIface || len(impls) == 0) {
						unresolved(f, "iface:"+oracleIfaceMethodName(com))
					}
					for _, impl := range impls {
						walk(impl)
					}
					continue
				}
				callee := com.StaticCallee()
				if callee == nil {
					if _, builtin := com.Value.(*ssa.Builtin); builtin {
						continue
					}
					// A call through a function value: default-deny. What the
					// resolver reads from the program (the function itself, a
					// closure, the stores into a variable, field, cell or
					// element, what a called function returns), plus the call
					// graph for any value not held in a parameter, plus the
					// signature graph where nothing else knows (a value carried
					// in a context or a registry). Every reason the set may be
					// incomplete is recorded as UNRESOLVED, even when targets
					// were found (a default beside a setter, a parameter beside
					// an unset default: round-2 review B1 (b), (c)). A
					// parameter is the caller's: checked after the walk.
					o := oracleOriginOf(com.Value, map[ssa.Value]bool{})
					res := op.resolve(com.Value)
					targets := res.fns
					// The call graph only for a value read from outside this
					// function (a field, a variable, a call result: o.other);
					// a local slice or cell the resolver reads whole, and VTA
					// keys a slice's elements by type, which would hand this
					// call every function stored in any slice of the type.
					if len(res.params) == 0 && o.other {
						g := sites[ci]
						if len(g) == 0 && len(targets) == 0 && o.other && !o.viaCall && !o.declared {
							// A function of a common signature (func())
							// matches everything, so not for a call result nor
							// for a package variable or field, whose stores the
							// resolver reads.
							if chaSites == nil {
								chaSites = op.sitesOf(f, true)
							}
							g = chaSites[ci]
						}
						targets = append(targets, g...)
					}
					for _, why := range res.open {
						unresolved(f, why)
					}
					for _, p := range res.params {
						paramCalls = append(paramCalls, paramCall{f, p})
					}
					for _, target := range targets {
						walk(target)
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
				// An instance of a generic function has no package of its own:
				// it is the store's when the generic is.
				owner := oracleOwner(callee)
				inStore := owner.Pkg != nil && strings.Contains(owner.Pkg.Pkg.Path(), "/internal/books/store")
				if inStore {
					key := oracleStoreKey(owner)
					if _, ok := calls[key]; !ok {
						calls[key] = f.String()
					}
					// A concrete store type called directly (not through an
					// interface) is judged like an interface call: the store
					// package's own bodies are not walked.
					if owner.Signature.Recv() != nil && oracleVerb.MatchString(owner.Name()) {
						recv := types.TypeString(owner.Signature.Recv().Type(), func(p *types.Package) string { return p.Name() })
						if !oracleNotAWrite.MatchString(recv) {
							note(oracleResource(recv, owner.Name()), recv+"."+owner.Name()+" in "+f.String())
						}
					}
				}
				walk(callee)
			}
		}
	}
	walk(runFn)
	// A call through a parameter: what each walked caller passes was resolved,
	// walked and recorded at that caller's operand scan; a caller that passes its
	// own parameter on is followed to its callers, at any depth. What is left is
	// a parameter no walked caller supplies (code outside the module calls the
	// function, or the walk reached it only as a value).
	for _, pc := range paramCalls {
		if !op.oracleParamSupplied(pc.p, walked, map[*ssa.Parameter]bool{}) {
			unresolved(pc.f, "parameter:"+pc.p.Name())
		}
	}
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
// does not). overlay is extra source files, by absolute path, that the load sees
// as if they were in the tree: the probe test uses it to add a function whose
// only effect is one call, which the real jobs do not isolate.
func loadOracleProgram(checkBuild bool, overlay map[string][]byte, patterns ...string) (*oracleProgram, error) {
	cfg := &packages.Config{
		Mode:       packages.LoadAllSyntax,
		BuildFlags: []string{oracleBuildTags},
		Env:        append(os.Environ(), oracleEnv...),
		Overlay:    overlay,
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
	if err := checkOutsideFileWriters(prog); err != nil {
		return nil, err
	}
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
	return &oracleProgram{prog: prog, graph: g, cha: c, roots: ssapkgs, flows: buildOracleFlows(prog), impls: &oracleImplCache{m: map[string][]*ssa.Function{}},
		cache: &oracleResolveCache{m: map[oracleCacheKey]oracleRes{}}}, nil
}

func loadOracleJobs() (map[string]oracleJob, error) {
	op, err := loadOracleProgram(true, nil, "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs")
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

// oracleProbeSource is a function added to the jobs package by overlay. Each of
// its calls is the only thing the function does, so what the oracle derives for it
// is what that one call is judged to be. No real job isolates a report-path
// resolution from the write that follows it (the backfill resolves, then opens), so
// without the probe a table entry for ReportPathNotify or LedgerPathNotify could be
// deleted and nothing would notice.
const oracleProbeSource = `package jobs

import "github.com/falkcorp/mediacore/library"

func oracleProbeReportPathNotify() {
	_, _ = library.ReportPathNotify("", "", "", nil)
}

func oracleProbeLedgerPathNotify() {
	_, _ = library.LedgerPathNotify("", "", "", nil)
}

func oracleProbeNothing() {
	_ = library.Mounted("")
}
`

// TestOracle_PathResolversCountAsFileWriters pins that the oracle reads a call to
// ReportPathNotify or LedgerPathNotify as a write to the files resource (they
// create the directory, change its mode and create and remove a probe file), and
// that a call to a library function that writes nothing is not read as one.
func TestOracle_PathResolversCountAsFileWriters(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	op, err := loadOracleProgram(true, map[string][]byte{
		filepath.Join(dir, "zz_oracle_probe.go"): []byte(oracleProbeSource),
	}, "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs")
	if err != nil {
		t.Fatal(err)
	}
	jp := op.roots[0]
	for _, tc := range []struct {
		fn   string
		want []string
	}{
		{"oracleProbeReportPathNotify", []string{"files"}},
		{"oracleProbeLedgerPathNotify", []string{"files"}},
		{"oracleProbeNothing", nil},
	} {
		f := jp.Func(tc.fn)
		if f == nil {
			t.Fatalf("%s: the probe function is not in the loaded package", tc.fn)
		}
		got, why, _, _, _ := derivedWrites(op, f)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: derived writes %v, want %v (evidence %v)", tc.fn, got, tc.want, why)
		}
	}
}

// oracleUnresolvedAllowed are the calls through a function value for which the
// walk finds no callee, and why that is safe. Each is keyed by what
// oracleUnresolvedKey records: UNRESOLVED.<function>#<what it was read from>. The
// walk records every such call instead of skipping it, so a function value the
// walk cannot see (a callback loaded from a variable the module never stores to,
// a registry filled by code outside the program) fails TestJobWrites_NoUnresolvedCalls
// until someone has read it and said here what it does.
var oracleUnresolvedAllowed = map[string]string{
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/deluge.Client).callCtx#iface:io.Closer.Close":                                                                                 "resp.Body.Close on the net/http response of the Deluge RPC call: closes a network body",
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs.hashBackfillReport).close#iface:io.Closer.Close":                                                             "r.f.Close on the report file hbOpenFile opened (a *os.File from mediacore library.OpenReportFile): closes the job's report file, whose writes are recorded where it is opened",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs.openHashBackfillReport#iface:io.Closer.Close":                                                                  "f.Close on the report file hbOpenFile just opened (a *os.File) when vetting or truncating it fails",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs.openHashBackfillReport#iface:io.Seeker.Seek":                                                                   "f.Seek on the report file hbOpenFile opened (a *os.File): moves its offset to the last good line",
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp.attemptBody).Close#iface:io.Closer.Close":                                                               "b.ReadCloser.Close on the wrapped net/http response body: closes a network body",
	"UNRESOLVED.(struct{io.Reader; io.Closer}).Close#iface:io.Closer.Close":                                                                                                                      "ai peekQuota429's re-wrapped response body, whose Closer is the original net/http response body: closes a network body",
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/maintenance.ProgressAdapter).Log#iface:github.com/falkcorp/audiobook-organizer/internal/operations.ProgressReporter.Log":      "ProgressAdapter.Ops is filled by server maintenance_job_op.go with registryProgressAdapter (a package the program does not load), whose Log forwards to the run reporter's Log: the run's own op-log rows (oracleReporterOwn)",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/authorcredit.authorityOf#iface:github.com/falkcorp/audiobook-organizer/internal/authorcredit.AuthoritySource.AuthorityLookup":   "the one implementation is server authorityEvidenceProvider (a package the program does not load), whose AuthorityLookup returns the loaded authority.Lookup: a getter",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/bookfileaudio.warnUnavailableOnce#iface:github.com/falkcorp/audiobook-organizer/internal/bookfileaudio.warnDemoter.DemotesWarn": "the one implementation is plugins/maintenance demotedWarnLog (a package the program does not load), whose DemotesWarn returns true: a constant",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/merge.CompletePendingUserStateRepairs#parameter:set by CompletePendingUserStateRepairs(lock)":                                   "exported, but the only callers outside tests are in merge itself (completePendingInvolving passes nil, PendingRepairLoop a closure returning UnlockMergeRMW); the walk reads both",
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/openlibrary.OLStore).CloseWithin#closeDB:set by NewOLStoreOnEngine(closeDB)":                                                  "exported for tests: outside tests only NewOLStore calls NewOLStoreOnEngine, passing the close function database.NewOwnedGatedDB returns, which the walk reads",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs.hbHook#hbCommitHook":                                                                                           "a test seam: production never sets hbCommitHook, so the call is nil and does nothing",
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs.mergeChapterGroupsJob).applyOne$1#chapterPrecheckTestHook":                                                   "a test hook: set only by tests, nil in production",
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/operations/registry.Registry).livenessClock#LivenessNow:exported":                                                             "a test clock: only registry tests set Options.LivenessNow; nil in production, where the watchdog uses time.Now",
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/operations/registry.dbReporter).TouchLiveness#touchProgressFn:set by NewDBReporterForTestWithTouch(touchFn)":                  "a test seam: only tests build a reporter with NewDBReporterForTestWithTouch; production reporters use the default the walk reads",
	"UNRESOLVED.(github.com/falkcorp/audiobook-organizer/internal/versionprimary.Loader).Load#Probe:exported":                                                                                    "every versionprimary.Loader a jobs run reaches is built inside the program, where its stores are read and walked; the stores outside the program (plugins/maintenance fixers, reconcile, server reconcile, books restore stager) fill other operations' Loaders, never a jobs run's",
	"UNRESOLVED.(github.com/falkcorp/audiobook-organizer/internal/versionprimary.Loader).Load#Stat:exported":                                                                                     "every versionprimary.Loader a jobs run reaches is built inside the program, where its stores are read and walked; the stores outside the program (plugins/maintenance duplicate-copies fixer) fill other operations' Loaders, never a jobs run's",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/bookfileaudio.boundedProbe$1#probe:set by SetProbeForTesting(fn)":                                                               "a test seam: only tests call SetProbeForTesting; production probes with the default the walk reads",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/dedup.MergeSplitBookClusterRechecked$1#Precheck:exported":                                                                       "SplitMergeOptions.Precheck has two stores, both inside the program (jobs merge_chapter_groups.go, dedup split_book_recheck.go), and both are read and walked",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/dedup.MergeSplitBookClusterWithOptions#Precheck:exported":                                                                       "SplitMergeOptions.Precheck has two stores, both inside the program (jobs merge_chapter_groups.go, dedup split_book_recheck.go), and both are read and walked",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/maintenance.SetResult#outside:context.Context.Value":                                                                            "the run's result setter: put on the context by jobs kinds.go (resultBox.set, an in-memory box the walk reads) and server maintenance_job_op.go (ReporterSetResult, the operation's own result row): neither is a library store write",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/operations/registry.RunItems[string]$1#CheckpointStateFn:exported":                                                              "every RunItemsOptions a jobs run reaches is built inside the program, where its stores are read and walked; the stores outside the program (plugins/maintenance kinds, server kinds_t7, acoustid window backfill) fill other operations' options, never a jobs run's",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/operations/registry.RunItems[string]$3#Label:exported":                                                                          "every RunItemsOptions a jobs run reaches is built inside the program, where its stores are read and walked; the stores outside the program (the plugins' and reconcile's RunItems calls) fill other operations' options, never a jobs run's",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/operations/registry.runItemsSeq[string]#CheckpointFn:exported":                                                                  "every RunItemsOptions a jobs run reaches is built inside the program, where its stores are read and walked; the stores outside the program (plugins/maintenance kinds, server kinds_t7, transcription auto-match) fill other operations' options, never a jobs run's",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/scanner.DetectChapterGroupsWithOptions#Exclude:exported":                                                                        "scanner chapter-group Options.Exclude has one store, inside the program (jobs chapter_groups_common.go), read and walked",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/scanner.evaluateSeqBucket#Protected:exported":                                                                                   "scanner chapter-group Options.Protected has one store, inside the program (jobs chapter_groups_common.go), read and walked",
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/versionprimary.EnsureSinglePrimary#MayWrite:exported":                                                                           "every versionprimary.Env a jobs run reaches is built inside the program, where its stores are read and walked; the stores outside the program (plugins/maintenance fixers, reconcile, audiobooks revert-settle) fill other operations' Envs, never a jobs run's",
}

// oracleOutsideClosureAllowed are the functions outside the module whose
// returned closures the jobs call, keyed by "<package path>.<name>" (the key
// ends #outside:<that>), and why calling the closure is not a write.
var oracleOutsideClosureAllowed = map[string]string{
	"context.WithCancel":  "the cancel function releases a context",
	"context.WithTimeout": "the cancel function releases a context and its timer",
	"strings.SplitSeq":    "the iterator walks a string in memory, calling the loop body (a range-over-func yield, read where it is written)",
	"strings.FieldsSeq":   "the iterator walks a string in memory, calling the loop body (a range-over-func yield, read where it is written)",
	"slices.Backward":     "the iterator walks a slice in memory, calling the loop body (a range-over-func yield, read where it is written)",
	"sync.OnceValues":     "the closure calls only the function handed to sync.OnceValues, which the walk reads and walks where it is handed over",
}

// oracleOutsideIfaceAllowed are the methods, named like no write, of interfaces
// declared outside the module that the jobs call, keyed by
// "<package path>.<interface>.<method>" (the key ends #iface:<that>), and why a
// call is not a write. Every implementation the module's loaded packages declare
// is walked beside the call graph's set; what is allowed here is the
// implementations outside the module, which cannot reach the store except by
// calling a module function value handed to them, and that hand-over is
// recorded where it is made.
var oracleOutsideIfaceAllowed = map[string]string{
	"context.Context.Deadline":                 "a context's deadline",
	"context.Context.Done":                     "a context's cancellation channel",
	"context.Context.Err":                      "a context's cancellation cause",
	"context.Context.Value":                    "a context value lookup; a function value it returns is resolved where it is called",
	"error.Error":                              "an error's message",
	"github.com/dhowden/tag.Metadata.FileType": "a parsed tag's file type, in memory",
	"github.com/dhowden/tag.Metadata.Raw":      "a parsed tag's raw frames, in memory",
	"hash.Hash.Sum":                            "a hash digest, in memory",
	"hash.Hash32.Sum32":                        "a hash digest, in memory",
	"io/fs.DirEntry.IsDir":                     "a directory entry's mode bit",
	"io/fs.DirEntry.Name":                      "a directory entry's name",
	"io/fs.FileInfo.IsDir":                     "a stat result's mode bit",
	"io/fs.FileInfo.Mode":                      "a stat result's mode",
	"io/fs.FileInfo.Size":                      "a stat result's size",
	"io/fs.FileInfo.Sys":                       "a stat result's system data",
	"reflect.Type.Kind":                        "a type's kind",
}

// TestJobWrites_NoUnresolvedCalls: no job reaches a call through a function value
// that the walk could not resolve, unless it is allowed above with a reason, and
// every allowance is used (an allowance for a call no job makes any more is a
// hole left open).
func TestJobWrites_NoUnresolvedCalls(t *testing.T) {
	used := map[string]bool{}
	for id, j := range oracleJobs(t) {
		for key := range j.calls {
			if !strings.HasPrefix(key, "UNRESOLVED.") {
				continue
			}
			if _, ok := oracleUnresolvedAllowed[key]; ok {
				used[key] = true
				continue
			}
			if i := strings.Index(key, "#outside:"); i >= 0 {
				name := key[i+len("#outside:"):]
				if _, ok := oracleOutsideClosureAllowed[name]; ok {
					used[name] = true
					continue
				}
			}
			if i := strings.Index(key, "#iface:"); i >= 0 {
				name := key[i+len("#iface:"):]
				if _, ok := oracleOutsideIfaceAllowed[name]; ok {
					used["iface:"+name] = true
					continue
				}
			}
			t.Errorf("%s: a call through a function value the walk cannot resolve, %s (in %s): read what it can be and allow it in oracleUnresolvedAllowed with the reason", id, key, j.calls[key])
		}
	}
	for key := range oracleUnresolvedAllowed {
		if !used[key] {
			t.Errorf("oracleUnresolvedAllowed has %s, but no job makes that call any more: remove it", key)
		}
	}
	for name := range oracleOutsideClosureAllowed {
		if !used[name] {
			t.Errorf("oracleOutsideClosureAllowed has %s, but no job calls a closure it made any more: remove it", name)
		}
	}
	for name := range oracleOutsideIfaceAllowed {
		if !used["iface:"+name] {
			t.Errorf("oracleOutsideIfaceAllowed has %s, but no job calls that method any more: remove it", name)
		}
	}
}
