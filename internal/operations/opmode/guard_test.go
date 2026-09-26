// file: internal/operations/opmode/guard_test.go
// version: 1.1.0
// guid: 4e03d07d-8575-4704-af45-0699789f2293
// last-edited: 2026-09-25

package opmode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestGuard_NoPlainBoolDryRunParams is the guard for the owner's 2026-09-25
// rule: an operation that writes runs as a PREVIEW unless its request says
// dry_run=false. A params field declared as a plain `bool` tagged dry_run or
// dryRun makes the opposite true -- Go's zero value is false, so an omitted
// key is a LIVE run -- and that is exactly the shape that shipped in
// itunes.path-repair and dedup.split-book-bulk-merge until this date.
//
// The op registry cannot decode params generically (OperationDef.Run takes raw
// JSON and each op decodes its own struct), so this scans the source instead:
// every struct field in internal/ and pkg/ whose JSON name is dry_run or
// dryRun must be a *bool (resolved by ResolveDryRun), unless
//
//   - its struct is an OUTPUT type -- the name ends in one of
//     outputTypeSuffixes, so it reports a mode rather than choosing one;
//   - it is an anonymous struct returned by a maintenance job's DefaultParams,
//     which TestMaintenanceJobs_PreviewByDefault in internal/maintenance/jobs
//     covers by iterating the job registry itself; or
//   - it is on allowPlainBool below, each entry with the reason it is not an
//     op params struct or why omission there is not a live run.
//
// A new op that adds `DryRun bool `json:"dry_run"“ fails here. The fix is
// a *bool plus ResolveDryRun, not an allowlist entry.
func TestGuard_NoPlainBoolDryRunParams(t *testing.T) {
	root := repoRoot(t)
	var violations []string
	seen := map[string]bool{}
	for _, dir := range []string{"internal", "pkg"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == "testdata" || name == "mocks" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for _, v := range scanFile(t, path, rel) {
				key := v.file + ":" + v.owner
				if _, ok := allowPlainBool[key]; ok {
					seen[key] = true
					continue
				}
				violations = append(violations, v.String())
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("plain bool dry-run param (omitted = LIVE): %s -- use *bool and opmode.ResolveDryRun", v)
	}
	// A stale allowlist entry is its own failure: an exemption whose code is
	// gone is an exemption waiting for the next plain bool to reuse its name.
	for key := range allowPlainBool {
		if !seen[key] {
			t.Errorf("allowPlainBool entry %q matched nothing; delete it", key)
		}
	}
}

// outputTypeSuffixes name structs that REPORT a mode (a run's result, a plan it
// printed, an HTTP response) rather than choose one. A plain bool is correct
// there: the value is always set by the code that ran.
var outputTypeSuffixes = []string{"Result", "Report", "Response", "Plan", "Preview", "Summary", "Outcome"}

// allowPlainBool exempts specific plain-bool dry_run fields, keyed by
// "<repo-relative file>:<owner>", where owner is the named struct type, or
// "func <Name>" for an anonymous struct declared inside that function.
//
// Every entry here is NOT an operation's params struct. The HTTP handlers are
// listed in docs/audits/2026-09-25-op-preview-default-inventory.md as an open
// owner decision: the 2026-09-25 rule was stated for operations, and flipping
// an endpoint's default changes what its existing UI buttons do.
var allowPlainBool = map[string]string{
	// maintenanceJobOpParams.DryRun is overwritten from a separate *bool decode
	// and the job's advertised default before it is read (maintenance_job_op.go).
	"internal/server/maintenance_job_op.go:maintenanceJobOpParams": "overwritten by opmode.ResolveDryRunDefault (both spellings, advertised default) before use",

	// Legacy v1 param shapes. Nothing decodes or encodes them any more; they
	// survive only as exported names. See the inventory doc.
	"internal/operations/state.go:ComposerScanParams":       "unused legacy v1 params shape",
	"internal/operations/state.go:BackfillFileHashesParams": "unused legacy v1 params shape",
	"internal/operations/state.go:MissingFileRepairParams":  "unused legacy v1 params shape",
	"internal/operations/state.go:BulkImportDelugeParams":   "unused legacy v1 params shape",

	// Maintenance-job custom-param structs. The job's dry-run is the Run
	// argument the dispatcher resolved (advertised default when omitted); these
	// structs are also the job's DefaultParams, whose dry_run the jobs guard
	// test pins to true. Their own DryRun field is never read as the mode.
	"internal/maintenance/jobs/chapter_groups_common.go:chapterGroupParams":            "job custom params; mode comes from the dispatcher-resolved Run argument",
	"internal/maintenance/jobs/recompute_book_aggregates.go:recomputeAggregatesParams": "job custom params; mode comes from the dispatcher-resolved Run argument",
	"internal/maintenance/jobs/prune_book_snapshots.go:pbs_params":                     "job custom params; mode comes from the dispatcher-resolved Run argument",
	"internal/maintenance/jobs/bulk_deluge_import.go:bdi_params":                       "job custom params; mode comes from the dispatcher-resolved Run argument",
	"internal/maintenance/jobs/scan_composer_tags.go:sct_params":                       "job custom params; mode comes from the dispatcher-resolved Run argument",
	"internal/maintenance/jobs/refetch_missing_authors.go:rma_params":                  "job custom params; mode comes from the dispatcher-resolved Run argument",
	"internal/maintenance/jobs/repoint_version_primary.go:repointVersionPrimaryParams": "job custom params; mode comes from the dispatcher-resolved Run argument",

	// HTTP handlers (not ops). Open owner decision; see the inventory doc.
	"internal/server/maintenance_fixups.go:func handleWipe":                     "HTTP handler; pre-filled true before bind, and requires confirm=WIPE",
	"internal/server/deluge_discovery.go:func handleDiscoveryImport":            "HTTP handler; omitted dry_run is LIVE -- owner decision pending",
	"internal/server/itl_pid.go:func pidRepairDryRun":                           "HTTP handler; ORed with ?dry_run, fails toward preview on true only -- owner decision pending",
	"internal/server/handlers/metadata/handler.go:func handleBulkWriteBackImpl": "HTTP handler; omitted dry_run enqueues library.bulk-write-back -- owner decision pending",
}

// previewWhenTrueTags are the JSON names of a mode flag whose TRUE value is the
// preview. A plain bool under any of them is live when omitted. dry_run and
// dryRun are the two spellings in use; the rest are the other ways the same
// flag gets spelled, so a new op cannot pass this scan by picking one of them.
// Flags whose TRUE value is live (apply, live, commit, execute) are safe as a
// plain bool: omitted is false, which is the preview. Their failure shape is a
// pre-fill, which TestGuard_NoLiveDefaultPrefill covers.
var previewWhenTrueTags = map[string]bool{
	"dry_run": true, "dryRun": true, "dryrun": true, "dry": true,
	"preview": true, "preview_only": true, "previewOnly": true,
}

type plainBoolField struct {
	file, owner, field, tag string
	line                    int
}

func (v plainBoolField) String() string {
	return v.file + ":" + strconv.Itoa(v.line) + " " + v.owner + "." + v.field + " `json:\"" + v.tag + "\"`"
}

func scanFile(t *testing.T, path, rel string) []plainBoolField {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var out []plainBoolField
	check := func(st *ast.StructType, owner string) {
		for _, fld := range st.Fields.List {
			if fld.Tag == nil {
				continue
			}
			ident, ok := fld.Type.(*ast.Ident)
			if !ok || ident.Name != "bool" {
				continue
			}
			tagVal, err := strconv.Unquote(fld.Tag.Value)
			if err != nil {
				continue
			}
			name := strings.Split(reflect.StructTag(tagVal).Get("json"), ",")[0]
			if !previewWhenTrueTags[name] {
				continue
			}
			fieldName := "(embedded)"
			if len(fld.Names) > 0 {
				fieldName = fld.Names[0].Name
			}
			out = append(out, plainBoolField{file: rel, owner: owner, field: fieldName, tag: name, line: fset.Position(fld.Pos()).Line})
		}
	}
	isOutput := func(name string) bool {
		for _, s := range outputTypeSuffixes {
			if strings.HasSuffix(name, s) {
				return true
			}
		}
		return false
	}
	// Named struct types.
	named := map[*ast.StructType]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		if st, ok := ts.Type.(*ast.StructType); ok {
			named[st] = true
			if !isOutput(ts.Name.Name) {
				check(st, ts.Name.Name)
			}
		}
		return true
	})
	// Anonymous struct types, attributed to their enclosing function.
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if fn.Name.Name == "DefaultParams" {
			continue // covered by the maintenance-jobs registry guard
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok || named[st] {
				return true
			}
			// An anonymous struct literal being RETURNED or written as a
			// response is output; one being decoded into is input. The
			// scan cannot tell them apart syntactically, so an anonymous
			// output struct whose only plain-bool dry_run is echoed back
			// is skipped when every such field sits in a struct that also
			// has a Results/Preview-style sibling field.
			if anonymousLooksLikeOutput(st) {
				return true
			}
			check(st, "func "+fn.Name.Name)
			return true
		})
	}
	return out
}

// anonymousLooksLikeOutput reports whether an anonymous struct is a response
// body: it carries a field named like an output payload next to dry_run.
func anonymousLooksLikeOutput(st *ast.StructType) bool {
	for _, fld := range st.Fields.List {
		for _, n := range fld.Names {
			switch n.Name {
			case "Results", "Preview", "Incomplete", "Plan", "Report", "Summary":
				return true
			}
		}
	}
	return false
}

// TestGuard_NoLiveDefaultPrefill catches the other shape of a live default:
// a params value PRE-FILLED live before it is decoded into. json.Unmarshal
// leaves a missing key alone, so
//
//	params := RescoreParams{Apply: true}
//	json.Unmarshal(raw, &params)
//
// runs live on `{}`, `null` and a requeue's empty params, whatever the field's
// type or spelling. dedup.rescore shipped exactly that until 2026-09-25, and
// TestGuard_NoPlainBoolDryRunParams could not see it: that scan checks field
// declarations, and this is a literal at the decode site.
//
// For every function in internal/ and pkg/ that decodes into &v (json.Unmarshal
// or a Decoder's Decode), the composite literal v is initialised from must not
// key a live-when-true field (Apply, Live, Commit, Execute) to true, or a
// preview-when-true field (DryRun, DryRunCamel, DryRunSnake, Preview) to false
// or to a Live() pointer. The fix is to drop the pre-fill so omission
// previews, and have callers that must run live say so.
func TestGuard_NoLiveDefaultPrefill(t *testing.T) {
	root := repoRoot(t)
	var violations []string
	seen := map[string]bool{}
	for _, dir := range []string{"internal", "pkg"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == "testdata" || name == "mocks" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for _, v := range scanPrefills(t, path, rel) {
				if _, ok := allowLivePrefill[v.key()]; ok {
					seen[v.key()] = true
					continue
				}
				violations = append(violations, v.String())
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("params pre-filled LIVE before decode (omitted = LIVE): %s -- drop the pre-fill so omission previews", v)
	}
	for key := range allowLivePrefill {
		if !seen[key] {
			t.Errorf("allowLivePrefill entry %q matched nothing; delete it", key)
		}
	}
}

// allowLivePrefill exempts specific pre-fills, keyed "<file>:func <Name>.<Field>".
// Empty on purpose: every pre-fill found when this guard was written was a
// live default and was removed.
var allowLivePrefill = map[string]string{}

// liveWhenTrue and previewWhenTrue name mode fields by their Go field name.
var (
	liveWhenTrue    = map[string]bool{"Apply": true, "Live": true, "Commit": true, "Execute": true}
	previewWhenTrue = map[string]bool{"DryRun": true, "DryRunCamel": true, "DryRunSnake": true, "Preview": true}
)

type livePrefill struct {
	file, fn, field string
	line            int
}

func (v livePrefill) key() string { return v.file + ":func " + v.fn + "." + v.field }
func (v livePrefill) String() string {
	return v.file + ":" + strconv.Itoa(v.line) + " func " + v.fn + " pre-fills " + v.field
}

func scanPrefills(t *testing.T, path, rel string) []livePrefill {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var out []livePrefill
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// Variables decoded into: json.Unmarshal(_, &v) or x.Decode(&v).
		targets := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			var arg ast.Expr
			switch {
			case sel.Sel.Name == "Unmarshal" && len(call.Args) == 2:
				arg = call.Args[1]
			case sel.Sel.Name == "Decode" && len(call.Args) == 1:
				arg = call.Args[0]
			default:
				return true
			}
			if u, ok := arg.(*ast.UnaryExpr); ok && u.Op == token.AND {
				if id, ok := u.X.(*ast.Ident); ok {
					targets[id.Name] = true
				}
			}
			return true
		})
		if len(targets) == 0 {
			continue
		}
		checkLit := func(lhs string, rhs ast.Expr) {
			if !targets[lhs] {
				return
			}
			if u, ok := rhs.(*ast.UnaryExpr); ok && u.Op == token.AND {
				rhs = u.X
			}
			lit, ok := rhs.(*ast.CompositeLit)
			if !ok {
				return
			}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				if (liveWhenTrue[key.Name] && isIdent(kv.Value, "true")) ||
					(previewWhenTrue[key.Name] && (isIdent(kv.Value, "false") || isLiveCall(kv.Value))) {
					out = append(out, livePrefill{file: rel, fn: fn.Name.Name, field: key.Name, line: fset.Position(kv.Pos()).Line})
				}
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				if len(s.Lhs) == len(s.Rhs) {
					for i, l := range s.Lhs {
						if id, ok := l.(*ast.Ident); ok {
							checkLit(id.Name, s.Rhs[i])
						}
					}
				}
			case *ast.ValueSpec:
				if len(s.Names) == len(s.Values) {
					for i, id := range s.Names {
						checkLit(id.Name, s.Values[i])
					}
				}
			}
			return true
		})
	}
	return out
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// isLiveCall matches opmode.Live() / Live() and a bool-pointer helper called
// with false (boolPtr(false), ptr(false)).
func isLiveCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if fun.Sel.Name == "Live" {
			return true
		}
	case *ast.Ident:
		if fun.Name == "Live" {
			return true
		}
	}
	return len(call.Args) == 1 && isIdent(call.Args[0], "false")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/operations/opmode/guard_test.go -> repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
