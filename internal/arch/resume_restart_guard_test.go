// file: internal/arch/resume_restart_guard_test.go
// version: 1.0.0
// guid: 87f31afd-8f18-4eed-8095-a44c9e9c30bf
// last-edited: 2026-10-30

package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// restartDef is one OperationDef literal that declares ResumePolicy:
// ResumeRestart, with what the scan could tell about its Run function.
type restartDef struct {
	File       string // path relative to the package directory
	ID         string // the ID: field, unquoted when a string literal, else the expression text
	Line       int
	Resolved   bool // the Run: expression was traced to a body in this package
	Checkpoint bool // that body, or a same-package function it reaches, calls .Checkpoint(
	MinCP      bool // the def sets MinCheckpointInterval to something other than the literal 0
}

// pkgFuncs indexes every function and method body of a package by bare name.
// A method and a function that share a name share an entry, which makes the
// reachability walk over-approximate (it may follow a same-named sibling); the
// guard prefers that to missing a Checkpoint call behind a method value.
type pkgFuncs map[string][]ast.Node

func indexFuncs(files []*ast.File) pkgFuncs {
	idx := pkgFuncs{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				idx[fn.Name.Name] = append(idx[fn.Name.Name], fn.Body)
			}
		}
	}
	return idx
}

// reachesCheckpoint reports whether root calls (or passes as a value) a
// .Checkpoint method, or
// names a same-package function or method whose body (transitively) does.
// Naming is enough to follow: a method value handed to a helper
// (p.runBatch passed to a runner) is as good as a call.
func reachesCheckpoint(root ast.Node, idx pkgFuncs) bool {
	seen := map[string]bool{}
	var walk func(n ast.Node) bool
	walk = func(n ast.Node) bool {
		found := false
		ast.Inspect(n, func(c ast.Node) bool {
			if found {
				return false
			}
			switch x := c.(type) {
			case *ast.SelectorExpr:
				// A call (reporter.Checkpoint(s)) or a method value handed to a
				// helper (submit(..., reporter.Checkpoint)); both persist state.
				if x.Sel.Name == "Checkpoint" {
					found = true
					return false
				}
				found = follow(x.Sel.Name, idx, seen, walk)
			case *ast.Ident:
				found = follow(x.Name, idx, seen, walk)
			}
			return !found
		})
		return found
	}
	return walk(root)
}

func follow(name string, idx pkgFuncs, seen map[string]bool, walk func(ast.Node) bool) bool {
	if seen[name] {
		return false
	}
	bodies, ok := idx[name]
	if !ok {
		return false
	}
	seen[name] = true
	for _, b := range bodies {
		if walk(b) {
			return true
		}
	}
	return false
}

// runBody resolves a Run: expression to the node to scan: a function literal,
// or the declaration named by an identifier (run) or selector (p.run).
func runBody(e ast.Expr, idx pkgFuncs) (ast.Node, bool) {
	switch x := e.(type) {
	case *ast.FuncLit:
		return x.Body, true
	case *ast.Ident:
		return named(x.Name, idx)
	case *ast.SelectorExpr:
		return named(x.Sel.Name, idx)
	}
	return nil, false
}

func named(name string, idx pkgFuncs) (ast.Node, bool) {
	bodies := idx[name]
	if len(bodies) == 0 {
		return nil, false
	}
	// Wrap several same-named bodies in one block so they are scanned together.
	return &ast.BlockStmt{List: stmtsOf(bodies)}, true
}

func stmtsOf(bodies []ast.Node) []ast.Stmt {
	var out []ast.Stmt
	for _, b := range bodies {
		out = append(out, b.(*ast.BlockStmt))
	}
	return out
}

func exprText(e ast.Expr) string {
	if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
		if s, err := strconv.Unquote(bl.Value); err == nil {
			return s
		}
	}
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprText(x.X) + "." + x.Sel.Name
	}
	return fmt.Sprintf("%T", e)
}

// scanRestartDefs parses the named non-test files of one package directory and
// returns every composite literal that sets ResumePolicy to a ResumeRestart
// identifier (bare, or qualified as sdk.ResumeRestart / opsregistry.ResumeRestart).
func scanRestartDefs(t *testing.T, dir string, names []string) []restartDef {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	var paths []string
	for _, n := range names {
		path := filepath.Join(dir, n)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, f)
		paths = append(paths, n)
	}
	idx := indexFuncs(files)
	var out []restartDef
	for i, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			fields := map[string]ast.Expr{}
			for _, el := range lit.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok {
						fields[k.Name] = kv.Value
					}
				}
			}
			rp, ok := fields["ResumePolicy"]
			if !ok || !strings.HasSuffix(exprText(rp), "ResumeRestart") {
				return true
			}
			d := restartDef{File: paths[i], Line: fset.Position(lit.Pos()).Line, ID: "?"}
			if id, ok := fields["ID"]; ok {
				d.ID = exprText(id)
			}
			if mc, ok := fields["MinCheckpointInterval"]; ok {
				bl, isLit := mc.(*ast.BasicLit)
				d.MinCP = !(isLit && bl.Value == "0")
			}
			if run, ok := fields["Run"]; ok {
				if body, ok := runBody(run, idx); ok {
					d.Resolved = true
					d.Checkpoint = reachesCheckpoint(body, idx)
				}
			}
			out = append(out, d)
			return true
		})
	}
	return out
}

// restartWithoutCheckpoint lists the ResumeRestart defs that restart from zero
// by design, each with the reason it is safe. A ResumeRestart def with no
// Checkpoint is not automatically wrong: its resume anchor can be state the run
// itself drains (a dirty set, a per-row "done" marker, the operation's own
// result ledger), so re-entering Run from the top does only the work still
// owed. What it must never be is an accident, which is what #3223 found eight
// times: an op declared ResumeRestart, assumed to resume, and silently started
// over because nobody checked. Adding an entry here is that check; write down
// WHY a from-zero restart does no repeated, paid or destructive work.
//
// Keyed by "<package dir>/<file>.go <ID expression>"; a stale key (the def is
// gone, or now checkpoints) fails the test so this list cannot rot.
var restartWithoutCheckpoint = map[string]string{
	// Unregistered stub; the live itunes.import is server.RegisterITunesImportOp.
	"internal/plugins/itunes/import.go itunes.import": "an unregistered plugin stub kept for the SDK shape " +
		"(its Register skips it); it declares MinCheckpointInterval but its Run only returns not-implemented",

	// Drained work sets: a finished item leaves the selection.
	"internal/plugins/maintenance/author_conjunction_repair.go maintenance.author-conjunction-repair": "RESUME AUDIT 2026-09-11 (c): work is selected from current author names and every write removes its row from the selection",
	"internal/plugins/maintenance/author_duplicate_merge.go maintenance.author-duplicate-merge":       "after a merge the group has one live row, so a re-run reports nothing to do; short run",
	"internal/plugins/maintenance/author_id_repair.go maintenance.author-id-repair":                   "after an apply nothing dangles and each name cluster has one row, so a restart re-plans from current state",
	"internal/plugins/maintenance/author_path_link.go maintenance.author-path-link":                   "a linked book has a non-nil scalar author and leaves the population, so a restart re-plans from current state",
	"internal/plugins/maintenance/book_hash_index_repair.go bookHashIndexRepairID":                    "an index check-and-repair that writes only missing keys and deletes only extra ones; documented idempotent",
	"internal/plugins/maintenance/dedupe_book_file_rows.go maintenance.dedupe-book-file-rows":         "a removed duplicate row no longer matches the selection; the ledger of deleted rows is journaled per run",
	"internal/plugins/maintenance/series_phantom_repair.go seriesPhantomRepairOpID":                   "a repaired phantom series id no longer lacks a series row, so the report/repair selection shrinks as it works",
	"internal/plugins/maintenance/repair_transcribe_status.go maintenance.repair-transcribe-status":   "a repaired row leaves the transport-failure state it is selected by; it never calls Whisper",
	"internal/plugins/maintenance/intro_migrate_single_file.go maintenance.intro-migrate-single-file": "copies a transcript onto one row; overwrite is off by default so a re-run skips rows that already carry one",
	"internal/plugins/maintenance/extract_wav_clips.go maintenance.extract-wav-clips":                 "skip_existing (default true) skips a clip already in the cache; pure I/O, no paid call",
	"internal/plugins/maintenance/file_intro_transcribe.go fileIntroOpID":                             "a file is DONE when IntroSourceHash == FileHash, so the remaining work is re-selected on restart (the file's own comment)",
	"internal/plugins/maintenance/reconcile.go itunes.heal":                                           "a reflinked file is no longer missing, so it leaves the heal set; reflink is idempotent",
	"internal/plugins/dedup/window_index_build.go windowIndexBuildOpID":                               "a file with a member list is skipped, so a rerun continues where the last stopped (the op's own comment)",
	"internal/plugins/deluge/link_backfill.go linkBackfillOpID":                                       "a linked row is skipped as already_linked; the run is bounded by limit and reports next_start_after",
	"internal/server/identification_advance_op.go identificationAdvanceOpID":                          "the dirty set IS the resume state: a processed book's mark is gone, so a restart finds only what is still owed",
	"internal/server/purge_soft_deleted_op.go purgeSoftDeletedOpID":                                   "the next run reclassifies the trash; a purged book is gone from it",

	// Ledger-anchored: the operation's own result rows say what is done.
	"internal/server/metadata_ops.go library.bulk-metadata-fetch": "books with an OperationResult row under the fetch chain's ledger key are skipped on resume (metadata_ops.go runBulkMetadataFetchAll)",

	// Re-attach, not re-do.
	"internal/server/aiscan_op.go ai.author-scan": "RESUME AUDIT 2026-09-11 (c): decideResume reads persisted phase rows and re-attaches to a started OpenAI batch; nothing is billed twice",

	// Read-only or cheap and side-effect free.
	"internal/server/catalog_coverage_op.go catalogCoverageOpID":     "a read-only report; a restart simply runs it again",
	"internal/server/collection_refresh_op.go collectionRefreshOpID": "re-evaluates every relative-date collection from stored rows; nothing carries between runs",
}

// restartDefKey is the allowlist key for d in the package directory dir.
func restartDefKey(dir string, d restartDef) string { return dir + "/" + d.File + " " + d.ID }

// TestResumeRestartDefsCheckpoint: every OperationDef literal that declares
// ResumePolicy: ResumeRestart has a Run that reaches a Checkpoint call, or is
// named in restartWithoutCheckpoint with the reason a restart from zero is safe.
//
// It is a static scan of composite literals, not a walk of the registry, so it
// sees a def whether or not its plugin registers it and needs no running
// server. It does not see a def assembled outside a literal (the maintenance
// Job adapter's RestartPolicy, v3_adapter.go) or a Run that is not a function
// literal, a plain identifier or a p.method selector; an unresolvable Run is
// reported rather than passed.
func TestResumeRestartDefsCheckpoint(t *testing.T) {
	root := repoRoot(t)
	pkgs := listPackages(t, root)
	if len(pkgs) < minModulePackages {
		t.Fatalf("the loader is broken, not the code: loaded %d module packages, want at least %d", len(pkgs),
			minModulePackages)
	}
	used := map[string]bool{}
	total, checkpointing := 0, 0
	for _, p := range pkgs {
		dir, ok := rel(p.ImportPath)
		if !ok {
			continue
		}
		for _, d := range scanRestartDefs(t, p.Dir, p.GoFiles) {
			total++
			key := restartDefKey(dir, d)
			if d.Checkpoint {
				checkpointing++
				if _, listed := restartWithoutCheckpoint[key]; listed {
					t.Errorf("%s:%d (%s) now checkpoints; delete its restartWithoutCheckpoint entry", dir+"/"+d.File,
						d.Line, d.ID)
				}
				continue
			}
			if _, listed := restartWithoutCheckpoint[key]; listed {
				used[key] = true
				continue
			}
			why := "its Run reaches no .Checkpoint call"
			if !d.Resolved {
				why = "its Run could not be traced to a function in this package"
			}
			t.Errorf("%s:%d declares %s ResumeRestart but %s, so a restart starts from zero. Checkpoint the work "+
				"that is done (reporter.Checkpoint), switch the policy to ResumeDrop or ResumeRequeue, or, if a "+
				"from-zero restart is provably safe, add %q to restartWithoutCheckpoint with the reason "+
				"(see #3223)", dir+"/"+d.File, d.Line, d.ID, why, key)
		}
	}
	// A scan that finds nothing proves nothing: the module has dozens of
	// ResumeRestart defs and most of them checkpoint.
	if total < 20 || checkpointing < 10 {
		t.Fatalf("the scan is broken, not the code: found %d ResumeRestart defs, %d of them checkpointing; want at "+
			"least 20 and 10", total, checkpointing)
	}
	for key := range restartWithoutCheckpoint {
		if !used[key] {
			t.Errorf("restartWithoutCheckpoint has a stale entry %q: no ResumeRestart def matches it", key)
		}
	}
}

// TestResumeRestartScanReadsDefs: the scanner on a scratch package. Four
// ResumeRestart defs (a checkpointing func literal, a checkpoint one helper
// deep behind a method value, a Checkpoint method value passed on, and one
// that never checkpoints), a ResumeDrop def that must not be reported, and a
// def whose Run it cannot trace.
func TestResumeRestartScanReadsDefs(t *testing.T) {
	dir := t.TempDir()
	src := `package p

type Policy int

const (
	ResumeRestart Policy = iota
	ResumeDrop
)

type Reporter interface{ Checkpoint(any) error }
type Def struct {
	ID                    string
	ResumePolicy          Policy
	Run                   func(Reporter) error
	MinCheckpointInterval int
}
type P struct{}

func (P) defs(ext func(Reporter) error) []Def {
	return []Def{
		{ID: "lit", ResumePolicy: ResumeRestart, Run: func(r Reporter) error { return r.Checkpoint(1) }},
		{ID: "deep", ResumePolicy: ResumeRestart, MinCheckpointInterval: 5, Run: P{}.runDeep},
		{ID: "value", ResumePolicy: ResumeRestart, Run: runValue},
		{ID: "none", ResumePolicy: ResumeRestart, MinCheckpointInterval: 0, Run: runNone},
		{ID: "drop", ResumePolicy: ResumeDrop, Run: runNone},
		{ID: "ext", ResumePolicy: ResumeRestart, Run: ext},
	}
}

func (P) runDeep(r Reporter) error { return helper(r) }
func helper(r Reporter) error      { return r.Checkpoint(2) }
func runValue(r Reporter) error    { return submit(r.Checkpoint) }
func submit(f func(any) error) error { return f(3) }
func runNone(r Reporter) error     { return nil }
`
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := map[string]restartDef{}
	for _, d := range scanRestartDefs(t, dir, []string{"p.go"}) {
		got[d.ID] = d
	}
	want := map[string][3]bool{ // resolved, checkpoint, minCP
		"lit": {true, true, false}, "deep": {true, true, true}, "value": {true, true, false},
		"none": {true, false, false}, "ext": {false, false, false},
	}
	if len(got) != len(want) {
		t.Errorf("scan reported %d defs (%v), want %d", len(got), got, len(want))
	}
	for id, w := range want {
		d, ok := got[id]
		if !ok {
			t.Errorf("def %q not reported", id)
			continue
		}
		if g := [3]bool{d.Resolved, d.Checkpoint, d.MinCP}; g != w {
			t.Errorf("def %q: resolved/checkpoint/minCP = %v, want %v", id, g, w)
		}
	}
}
