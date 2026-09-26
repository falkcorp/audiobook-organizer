// file: internal/maintenance/jobs/preview_default_guard_test.go
// version: 1.1.0
// guid: 0c7d2b1e-5f3a-4e68-9a41-7d2e8b6c3f15
// last-edited: 2026-09-25

package jobs

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// noPreviewMode lists the maintenance jobs that do NOT advertise
// dry_run:true, each with the reason. Every other registered job must.
//
// The dispatcher (server/maintenance_dispatcher.go) and the v2 Run closure
// (server/maintenance_job_op.go) both fall back to the job's ADVERTISED
// dry_run when a request omits it. So a job advertising false runs LIVE on an
// empty body. The owner's 2026-09-25 rule is that every writing operation
// previews unless told otherwise; this test enforces it over the job registry
// itself, so a new job cannot ship with a live default without an entry here.
//
// Advertising true for a job whose Run ignores dryRun would be worse than
// false: the UI would offer a "preview" that writes. So jobs with no preview
// mode stay listed here rather than flipped.
var noPreviewMode = map[string]string{
	// Run(..., _ bool): read-only reports that record results only.
	"relink-report":           "read-only report; Run ignores dryRun",
	"scan-duplicate-files":    "read-only scan; Run ignores dryRun",
	"scan-duration-mismatch":  "read-only scan; Run ignores dryRun",
	"scan-metadata-hash-dups": "read-only scan; Run ignores dryRun",

	// No dry_run key in DefaultParams at all.
	"bulk-fetch-metadata": "no preview mode: Run names dryRun but never reads it, so it always fetches and applies",
	"scan-chapter-groups": "read-only scan; Run ignores dryRun; the writing twin merge-chapter-groups previews by default",

	// revert-metadata-fetch and generate-itl-tests were listed here until
	// 2026-09-25 as having "no preview mode". Both Run bodies branch on dryRun,
	// so both had a working preview and defaulted live. They now advertise
	// dry_run:true, and TestMaintenanceJobs_NoPreviewModeJobsIgnoreDryRun
	// fails if a job whose Run reads dryRun is listed here again.
}

func TestMaintenanceJobs_PreviewByDefault(t *testing.T) {
	all := maintenance.All()
	if len(all) == 0 {
		t.Fatal("no maintenance jobs registered; the guard would pass vacuously")
	}
	seen := map[string]bool{}
	for _, job := range all {
		id := job.ID()
		raw, err := json.Marshal(job.DefaultParams())
		if err != nil {
			t.Errorf("%s: marshal DefaultParams: %v", id, err)
			continue
		}
		var dp struct {
			DryRun *bool `json:"dry_run"`
		}
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &dp); err != nil {
				t.Errorf("%s: DefaultParams is not a JSON object: %s", id, raw)
				continue
			}
		}
		advertisesPreview := dp.DryRun != nil && *dp.DryRun
		reason, exempt := noPreviewMode[id]
		switch {
		case exempt && advertisesPreview:
			t.Errorf("%s advertises dry_run:true but is listed in noPreviewMode (%q); remove the entry", id, reason)
		case !exempt && !advertisesPreview:
			t.Errorf("%s: DefaultParams %s does not advertise dry_run:true, so an empty request runs it LIVE. "+
				"Advertise dry_run:true (and honor dryRun in Run), or list it in noPreviewMode with the reason it has no preview mode", id, raw)
		}
		seen[id] = true
	}
	for id := range noPreviewMode {
		if !seen[id] {
			t.Errorf("noPreviewMode lists %q but no such job is registered; delete the entry", id)
		}
	}
}

// TestMaintenanceJobs_NoPreviewModeJobsIgnoreDryRun checks each noPreviewMode
// reason against the code. The exemption is only true of a job whose Run never
// reads its dryRun parameter. A job whose Run does read it has a working
// preview, so leaving it exempt keeps it running live on an empty request.
// That is how revert-metadata-fetch and generate-itl-tests stayed live-default
// until 2026-09-25. It parses this package's sources, maps each job type's ID()
// literal to its Run method, and fails on any exempt job whose Run body
// references its last parameter.
func TestMaintenanceJobs_NoPreviewModeJobsIgnoreDryRun(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	idByType := map[string]string{}
	runReadsDryRun := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			ident, ok := recv.(*ast.Ident)
			if !ok {
				continue
			}
			switch fn.Name.Name {
			case "ID":
				if len(fn.Body.List) != 1 {
					continue
				}
				ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
				if !ok || len(ret.Results) != 1 {
					continue
				}
				lit, ok := ret.Results[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if v, uerr := strconv.Unquote(lit.Value); uerr == nil {
					idByType[ident.Name] = v
				}
			case "Run":
				params := fn.Type.Params.List
				if len(params) == 0 {
					continue
				}
				last := params[len(params)-1]
				if len(last.Names) == 0 {
					runReadsDryRun[ident.Name] = false
					continue
				}
				pname := last.Names[len(last.Names)-1].Name
				reads := false
				if pname != "_" {
					ast.Inspect(fn.Body, func(n ast.Node) bool {
						if id, ok := n.(*ast.Ident); ok && id.Name == pname {
							reads = true
							return false
						}
						return true
					})
				}
				runReadsDryRun[ident.Name] = reads
			}
		}
	}
	typeByID := map[string]string{}
	for typ, id := range idByType {
		typeByID[id] = typ
	}
	for id, reason := range noPreviewMode {
		typ, ok := typeByID[id]
		if !ok {
			t.Errorf("noPreviewMode lists %q but no job type in this package returns it from a literal ID(); the guard cannot check its reason", id)
			continue
		}
		reads, ok := runReadsDryRun[typ]
		if !ok {
			t.Errorf("%s (%s): no Run method found; the guard cannot check its reason", id, typ)
			continue
		}
		if reads {
			t.Errorf("%s is in noPreviewMode (%q) but %s.Run reads its dryRun parameter, so it has a preview mode. "+
				"Advertise dry_run:true in DefaultParams and delete the entry", id, reason, typ)
		}
	}
}
