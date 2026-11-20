// file: internal/maintenance/jobs/writes_oracle_test.go
// version: 3.1.0
// guid: 40eadd67-76ac-419f-a3fe-7d1ed5e586e2
// last-edited: 2026-11-19

package jobs_test

import (
	"errors"
	"fmt"
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
				// An element or field of something. Of an object a caller
				// supplies (an options struct, a receiver), the callback is
				// the caller's, read where that caller builds the object. Of
				// an object built in this function, it is stored here. Of a
				// package variable or anything else, only the module's
				// stores can say.
				switch oracleBaseOf(x, map[ssa.Value]bool{}) {
				case oracleBaseParam:
					o.param = true
				case oracleBaseLocal:
				default:
					o.other, o.declared = true, true
					o.name = oracleLoadName(x)
				}
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
// or map (an element store, a composite literal, an append). A function value
// the module loads from one of them, and hands to a shared helper, is one of the
// functions stored there.
type oracleFlows struct {
	globals map[*ssa.Global][]ssa.Value
	fields  map[*types.Var][]ssa.Value
	cells   map[ssa.Value][]ssa.Value
	elems   map[any][]ssa.Value
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
						fl.cells[a] = append(fl.cells[a], in.Val)
					}
				case *ssa.MapUpdate:
					k := oracleContainer(in.Map)
					fl.elems[k] = append(fl.elems[k], in.Value)
				}
			}
		}
	}
	return fl
}

// oracleCellOf is the variable cell a captured variable refers to.
func oracleCellOf(fv *ssa.FreeVar) ssa.Value {
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

// funcs is the functions a function value might be, as far as the module's own
// stores say: the function itself, a closure's function, the functions stored
// into the package variable, field, cell, slice or map it was loaded from, and
// the functions a called function returns. A parameter is the caller's to say,
// so it names none.
func (fl *oracleFlows) funcs(v ssa.Value, seen map[ssa.Value]bool) (out []*ssa.Function) {
	if v == nil || seen[v] {
		return nil
	}
	seen[v] = true
	switch v := v.(type) {
	case *ssa.Function:
		return []*ssa.Function{v}
	case *ssa.MakeClosure:
		if f, ok := v.Fn.(*ssa.Function); ok {
			return []*ssa.Function{f}
		}
	case *ssa.Phi:
		for _, e := range v.Edges {
			out = append(out, fl.funcs(e, seen)...)
		}
	case *ssa.ChangeType:
		return fl.funcs(v.X, seen)
	case *ssa.Convert:
		return fl.funcs(v.X, seen)
	case *ssa.MakeInterface:
		return fl.funcs(v.X, seen)
	case *ssa.TypeAssert:
		return fl.funcs(v.X, seen)
	case *ssa.Lookup:
		return fl.elemFuncs(v.X, seen)
	case *ssa.UnOp:
		if v.Op != token.MUL {
			return nil
		}
		var stored []ssa.Value
		switch x := v.X.(type) {
		case *ssa.Global:
			stored = fl.globals[x]
		case *ssa.FieldAddr:
			if fv := oracleFieldVar(x); fv != nil {
				stored = fl.fields[fv]
			}
		case *ssa.IndexAddr:
			return fl.elemFuncs(x.X, seen)
		case *ssa.Alloc:
			stored = fl.cells[x]
		case *ssa.FreeVar:
			if cell := oracleCellOf(x); cell != nil {
				stored = fl.cells[cell]
			}
		}
		for _, sv := range stored {
			out = append(out, fl.funcs(sv, seen)...)
		}
	case *ssa.Call:
		out = append(out, fl.returned(v, -1, seen)...)
	case *ssa.Extract:
		if c, ok := v.Tuple.(*ssa.Call); ok {
			out = append(out, fl.returned(c, v.Index, seen)...)
		}
	}
	return out
}

// returned is the functions a statically called module function returns (the
// idx-th result, or the one result for idx < 0).
func (fl *oracleFlows) returned(c *ssa.Call, idx int, seen map[ssa.Value]bool) (out []*ssa.Function) {
	callee := c.Call.StaticCallee()
	if callee == nil {
		return nil
	}
	for _, b := range callee.Blocks {
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		for i, r := range ret.Results {
			if idx < 0 || i == idx {
				out = append(out, fl.funcs(r, seen)...)
			}
		}
	}
	return out
}

// elemFuncs is the functions that might be elements of a slice, array or map
// value: what was stored into the container it was loaded from, appended to it,
// or written into the literal that built it.
func (fl *oracleFlows) elemFuncs(c ssa.Value, seen map[ssa.Value]bool) (out []*ssa.Function) {
	if c == nil || seen[c] {
		return nil
	}
	seen[c] = true
	for _, sv := range fl.elems[oracleContainer(c)] {
		out = append(out, fl.funcs(sv, seen)...)
	}
	switch c := c.(type) {
	case *ssa.UnOp:
		if c.Op != token.MUL {
			return out
		}
		var stored []ssa.Value
		switch x := c.X.(type) {
		case *ssa.Global:
			stored = fl.globals[x]
		case *ssa.FieldAddr:
			if fv := oracleFieldVar(x); fv != nil {
				stored = fl.fields[fv]
			}
		case *ssa.Alloc:
			stored = fl.cells[x]
		}
		for _, sv := range stored {
			out = append(out, fl.elemFuncs(sv, seen)...)
		}
	case *ssa.Call:
		if b, ok := c.Call.Value.(*ssa.Builtin); ok && b.Name() == "append" {
			for _, a := range c.Call.Args {
				out = append(out, fl.elemFuncs(a, seen)...)
			}
		}
	case *ssa.Slice:
		out = append(out, fl.elemFuncs(c.X, seen)...)
	case *ssa.ChangeType:
		out = append(out, fl.elemFuncs(c.X, seen)...)
	case *ssa.Phi:
		for _, e := range c.Edges {
			out = append(out, fl.elemFuncs(e, seen)...)
		}
	}
	return out
}

// oracleParamsUnresolved is whether some caller of the function a parameter
// belongs to hands it a function value the walk cannot name: not a function, a
// closure, nil, another parameter, or something the module's stores resolve.
func (op *oracleProgram) oracleParamsUnresolved(params []*ssa.Parameter) bool {
	for _, p := range params {
		fn := p.Parent()
		idx := -1
		for i, q := range fn.Params {
			if q == p {
				idx = i
			}
		}
		node := op.graph.Nodes[fn]
		if idx < 0 || node == nil {
			continue
		}
		for _, e := range node.In {
			if e.Site == nil {
				continue
			}
			com := e.Site.Common()
			args, at := com.Args, idx
			if com.IsInvoke() {
				at = idx - 1
			}
			if at < 0 || at >= len(args) || (!com.IsInvoke() && len(args) != len(fn.Params)) {
				continue
			}
			switch a := args[at].(type) {
			case *ssa.Function, *ssa.MakeClosure, *ssa.Const:
				continue
			default:
				// A value the caller itself takes from a parameter or builds
				// is that caller's callers' business (or was read where it
				// was built); a result of a call is the callee's return
				// statements'. What is left is a value read from a variable
				// or a field the module never stores a function into, or
				// from outside the program.
				if o := oracleOriginOf(a, map[ssa.Value]bool{}); o.param || o.outside || o.viaCall || !o.other {
					continue
				}
				if len(op.flows.funcs(a, map[ssa.Value]bool{})) == 0 {
					return true
				}
			}
		}
	}
	return false
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
		// The graph's callees of each call site in f.
		sites := op.sitesOf(f, false)
		var chaSites map[ssa.CallInstruction][]*ssa.Function
		var operands [16]*ssa.Value
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
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
					if _, ok := (*v).Type().Underlying().(*types.Signature); ok {
						for _, fn := range op.flows.funcs(*v, map[ssa.Value]bool{}) {
							walk(fn)
						}
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
					impls := sites[ci]
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
					// The call is judged by the interface method's own name
					// AND its implementations' bodies are walked, and the
					// result is the union. Neither alone is enough: the set of
					// implementations the program knows can be partial (an
					// in-program type beside a store the wiring passes in),
					// and a body can write through code the walk does not
					// enter. Progress and liveness reporting are not books
					// writes (oracleNotAWrite), so their names are not judged,
					// but what implements them is still walked.
					if _, ok := calls[recv+"."+name]; !ok {
						calls[recv+"."+name] = f.String()
					}
					if oracleVerb.MatchString(name) && !oracleNotAWrite.MatchString(recv) {
						note(oracleResource(recv, name), recv+"."+name+" in "+f.String())
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
					// A call through a function value. What the module's
					// stores say it can be (a package variable's or a field's
					// functions, the default of a nil-means-default
					// parameter), plus, for a value some caller supplies, what
					// the callers pass (read where each is mentioned); for a
					// value no caller supplies, the VTA graph, then the
					// signature-based CHA graph where VTA knows nothing (a
					// value set by code outside the program).
					o := oracleOriginOf(com.Value, map[ssa.Value]bool{})
					targets := op.flows.funcs(com.Value, map[ssa.Value]bool{})
					switch {
					case o.outside && !o.other && !o.param && len(targets) == 0:
						// a closure of code outside the module
						calls[oracleUnresolvedKey(f, "outside:"+o.name)] = f.String()
					case o.other && !o.param:
						g := sites[ci]
						if len(g) == 0 && len(targets) == 0 && !o.viaCall && !o.declared {
							// A value set by code outside the program (a
							// context value, a registry): every function of
							// that signature, as for an interface value. Not
							// for a call result (the module's return
							// statements are the answer there) nor for a
							// package variable or field (the module's stores
							// are): a function of a common signature (func())
							// matches everything.
							if chaSites == nil {
								chaSites = op.sitesOf(f, true)
							}
							g = chaSites[ci]
						}
						targets = append(targets, g...)
						if len(targets) == 0 {
							what := o.name
							if o.viaCall {
								what = "call result"
							}
							calls[oracleUnresolvedKey(f, what)] = f.String()
						}
					case o.param && op.oracleParamsUnresolved(o.params):
						calls[oracleUnresolvedKey(f, "parameter")] = f.String()
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
	return &oracleProgram{prog: prog, graph: g, cha: c, roots: ssapkgs, flows: buildOracleFlows(prog)}, nil
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
	"UNRESOLVED.github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs.hbHook#hbCommitHook":                                         "a test seam: production never sets hbCommitHook, so the call is nil and does nothing",
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs.mergeChapterGroupsJob).applyOne$1#chapterPrecheckTestHook": "a test hook: set only by tests, nil in production",
	"UNRESOLVED.(*github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp.throttled).RoundTrip$2$1#call result":                 "cancel of attemptContext, a variable holding context.WithTimeout: the standard library's cancel function, which releases a context and writes nothing",
}

// oracleOutsideClosureAllowed are the functions outside the module whose
// returned closures the jobs call, keyed by "<package path>.<name>" (the key
// ends #outside:<that>), and why calling the closure is not a write.
var oracleOutsideClosureAllowed = map[string]string{
	"context.WithCancel":  "the cancel function releases a context",
	"context.WithTimeout": "the cancel function releases a context and its timer",
	"strings.SplitSeq":    "the iterator walks a string in memory, calling the loop body (a range-over-func yield, read where it is written)",
	"strings.FieldsSeq":   "the iterator walks a string in memory, calling the loop body (a range-over-func yield, read where it is written)",
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
}
