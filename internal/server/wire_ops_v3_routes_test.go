// file: internal/server/wire_ops_v3_routes_test.go
// version: 1.8.1
// guid: f0aca62d-8230-49ce-ab21-61cbd082ce99
// last-edited: 2026-10-30

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opsv3"
)

// TestOpsV3Routes_WiredAndServed checks the oracle the handler tests cannot:
// that setupRoutes registers every /api/v3/ops route on the real router, with
// the opsv3 handler, and that the /api/* -> /api/v1/* compatibility redirect
// leaves them alone.
func TestOpsV3Routes_WiredAndServed(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	want := []struct{ method, path string }{
		{http.MethodGet, "/api/v3/ops/defs"},
		{http.MethodGet, "/api/v3/ops/defs/:id"},
		{http.MethodPost, "/api/v3/ops/defs/:id/runs"},
		{http.MethodGet, "/api/v3/ops/runs"},
		{http.MethodPost, "/api/v3/ops/runs"},
		{http.MethodGet, "/api/v3/ops/runs/:id"},
		{http.MethodGet, "/api/v3/ops/runs/:id/rows"},
		{http.MethodPost, "/api/v3/ops/runs/:id/cancel"},
		{http.MethodPost, "/api/v3/ops/runs/:id/approve"},
		{http.MethodGet, "/api/v3/ops/counts"},
		{http.MethodGet, "/api/v3/ops/timeline"},
		{http.MethodGet, "/api/v3/ops/events"},
	}
	routes := s.router.Routes()
	for _, w := range want {
		var handler string
		for _, ri := range routes {
			if ri.Method == w.method && ri.Path == w.path {
				handler = ri.Handler
			}
		}
		require.NotEmpty(t, handler, "%s %s must be registered", w.method, w.path)
		require.Contains(t, handler, "opsv3", "%s %s handler = %q", w.method, w.path, handler)
	}

	for _, path := range []string{"/api/v3/ops/defs", "/api/v3/ops/counts?group_by=def", "/api/v3/ops/runs", "/api/v3/ops/timeline"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", path, rec.Body.String())
		require.True(t, json.Valid(rec.Body.Bytes()), path)
	}
}

// previewOnlyElsewhere are writing ops whose omitted mode writes nothing
// (write_op_modes.golden records them `preview`) but which have no params key
// /api/v3/ops can drive a preview with, so they declare no Preview and start
// in v3 only with an explicit mode "live":
var previewOnlyElsewhere = map[string]string{
	// No dry_run: its preview is a different op,
	// metadata.bulk-apply-preview (bulk_apply_preview.go), reached through
	// POST /metadata/bulk-apply-preview. A v3 "preview" of this op used to
	// apply metadata and write tags.
	"metadata.batch-apply-cached": "preview is the separate metadata.bulk-apply-preview op",
	// Persists candidate rows on every run; no mode key.
	"metadata.candidate-fetch": "no mode key; fetches and stores candidates on every run",
}

// multiModeKeyFamilies are the ops whose declared params type decodes more
// than one mode-key family (dry_run/dryRun, apply, live). v3 writes every
// one, so a preview is a preview and a live run is live whichever the op
// reads, but two switches for one decision is a shape to justify, not to
// copy: each entry says how the op combines them, and anyWrites must match
// the op's PreviewSpec.AnyFamilyWrites (EnqueueOp's mode-aware dedupe reads a
// caller's body by it). A new op with two families fails until it is listed
// here, and an entry whose op no longer has two fails too.
var multiModeKeyFamilies = map[string]struct {
	anyWrites bool
	how       string
}{
	// vgPrimaryRepairParams: apply is the older spelling of dry_run=false.
	// Run writes when either says write (apply || dry_run==false) and refuses
	// dry_run=true beside apply=true (version_group_primary_repair.go).
	"maintenance.version-group-primary-repair": {anyWrites: true, how: "apply || !dry_run; dry_run=true with apply=true is refused"},
	// repointVersionPrimaryParams via ExecutionPolicy.Params: Run writes only
	// when apply && !dry_run, two fail-closed switches
	// (repoint_version_primary.go).
	"maintenance.repoint-version-primary": {anyWrites: false, how: "apply && !dry_run"},
}

// TestOpsV3_PreviewDeclarationsMatchTheOpsParams checks every registered op's
// Preview declaration against the op's code, not the ledger:
//
//   - the declaration checks (PreviewSpec.Check, by reflection: its mode keys
//     and LiveOneOf switches are bool fields of the params type it names),
//     and that type decodes every key v3 writes under DisallowUnknownFields,
//     so a strict decoder accepts what v3 writes;
//   - /defs reports exactly the declared mode keys for a writer, and no
//     preview for a writer that declares none (fail closed);
//   - an op with more than one mode-key family is in multiModeKeyFamilies;
//   - every writing op is classified: one the ledger records `preview` either
//     declares a preview or is listed in previewOnlyElsewhere with the
//     reason, and one recorded `no-mode` declares none.
//
// It does not prove the declared type is the one Run decodes: that is what
// the TestPreviewBehaviour_* tests do, by running ops with Resolve's output.
func TestOpsV3_PreviewDeclarationsMatchTheOpsParams(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	ledger := readWriteOpModes(t)
	defs := map[string]opsv3.DefInfo{}
	for _, d := range (&opsV3Defs{reg: srv.opRegistry}).ListDefs() {
		defs[d.ID] = d
	}
	require.NotEmpty(t, defs)
	declared := map[string]int{}
	usedElsewhere := map[string]bool{}
	usedMulti := map[string]bool{}
	oneOf := 0
	for _, od := range srv.opRegistry.ActiveDefs() {
		d, ok := defs[od.ID]
		require.True(t, ok, "%s missing from the v3 catalog", od.ID)
		p := od.Preview
		if !p.IsZero() {
			require.NoError(t, p.Check(), "%s: Preview does not match its params type", od.ID)
			require.True(t, canWrite(od), "%s declares a preview but cannot write", od.ID)
			strictDecodes(t, od.ID, p)
			require.True(t, d.HasPreview(), "%s: /defs shows no preview for a declared one", od.ID)
			require.Equal(t, p.ModeKeys(), d.Preview.ModeKeys(), "%s: /defs mode keys", od.ID)
			require.Equal(t, p.LiveOneOf, d.Preview.LiveOneOf, "%s: /defs live_one_of", od.ID)
			for _, f := range p.ModeFamilies() {
				declared[f]++
			}
			if len(p.LiveOneOf) > 0 {
				oneOf++
			}
			if len(p.ModeFamilies()) > 1 {
				entry, listed := multiModeKeyFamilies[od.ID]
				require.True(t, listed, "%s decodes mode keys %v from more than one family; list it in multiModeKeyFamilies with how its Run combines them", od.ID, p.ModeKeys())
				require.Equal(t, entry.anyWrites, p.AnyFamilyWrites, "%s: multiModeKeyFamilies says %q; PreviewSpec.AnyFamilyWrites must agree", od.ID, entry.how)
				usedMulti[od.ID] = true
			}
		} else if d.Writer {
			require.False(t, d.HasPreview(), "%s declares no preview, so it must have none", od.ID)
		}
		if !canWrite(od) || isMaintenanceJobOp(od.ID) {
			continue
		}
		switch ledger[od.ID] {
		case "no-mode":
			require.True(t, p.IsZero(), "%s: the ledger says it always writes, yet it declares a preview (mode keys %v); fix whichever is wrong", od.ID, p.ModeKeys())
		case "preview":
			if p.IsZero() {
				_, listed := previewOnlyElsewhere[od.ID]
				require.True(t, listed, "%s: the ledger says omitted mode writes nothing, but it declares no Preview. Declare the params type its Run decodes (OperationDef.Preview), or list it in previewOnlyElsewhere with the reason", od.ID)
				usedElsewhere[od.ID] = true
			}
		}
	}
	for id := range previewOnlyElsewhere {
		require.True(t, usedElsewhere[id], "previewOnlyElsewhere lists %s, which is not a registered ledger-preview op without a declaration; delete the entry", id)
	}
	for id := range multiModeKeyFamilies {
		require.True(t, usedMulti[id], "multiModeKeyFamilies lists %s, which no longer declares two mode-key families; delete the entry", id)
	}
	for _, k := range []string{"dry_run", "apply", "live"} {
		require.Positive(t, declared[k], "no registered op declares %s; the check did not run", k)
	}
	require.Positive(t, oneOf, "no registered op declares LiveOneOf; the check did not run")
}

// TestOpsV3_DryRunFamilyDecodesBothSpellings is the registry-wide half of the
// dry-run spelling rule (the source scan is TestGuard_DryRunParamsAcceptBothSpellings
// in internal/operations/opmode). For every registered op that declares a
// Preview and decodes a dry-run mode key:
//
//   - it decodes BOTH dry_run and dryRun. encoding/json drops a key the params
//     type has no field for, so an op with one spelling previews (or applies,
//     if its default is the unsafe one) on the other with no warning;
//   - each is a *bool, not a plain bool: a plain bool cannot tell an omitted
//     key from false, so omitted would read as APPLY (opmode.ResolveDryRun maps
//     omitted to preview).
//
// The maintenance-job family (isMaintenanceJobOp) is exempt from the *bool
// half only: its params types are the jobs' own DefaultParams shapes, the mode
// is resolved by the dispatcher through opmode.ResolveDryRunDefault from both
// spellings, and the plain bool is the echo of that resolved value
// (allowPlainBool in the opmode guard says so per struct).
func TestOpsV3_DryRunFamilyDecodesBothSpellings(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	checked := 0
	for _, od := range srv.opRegistry.ActiveDefs() {
		p := od.Preview
		if p.IsZero() || (!p.Decodes(opsregistry.ModeKeyDryRun) && !p.Decodes(opsregistry.ModeKeyDryRunCamel)) {
			continue
		}
		checked++
		require.True(t, p.Decodes(opsregistry.ModeKeyDryRun), "%s: Preview params decode dryRun but not dry_run; a caller sending dry_run is silently dropped", od.ID)
		require.True(t, p.Decodes(opsregistry.ModeKeyDryRunCamel), "%s: Preview params decode dry_run but not dryRun; a caller sending dryRun is silently dropped", od.ID)
		if isMaintenanceJobOp(od.ID) {
			continue
		}
		typ := reflect.TypeOf(p.Params)
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		// VisibleFields includes fields promoted from an embedded struct.
		for _, key := range []string{opsregistry.ModeKeyDryRun, opsregistry.ModeKeyDryRunCamel} {
			for _, f := range reflect.VisibleFields(typ) {
				if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != key {
					continue
				}
				require.Equal(t, reflect.Pointer, f.Type.Kind(), "%s: %s.%s (json %q) must be a *bool so an omitted key reads as preview, not as Go's zero value (APPLY)", od.ID, typ.Name(), f.Name, key)
				require.Equal(t, reflect.Bool, f.Type.Elem().Kind(), "%s: %s.%s must be a *bool", od.ID, typ.Name(), f.Name)
			}
		}
	}
	require.Greater(t, checked, 50, "the check did not run over the registry")
}

// strictDecodes: the declared params type accepts every key v3 can write
// (each mode key and LiveOneOf switch, all true and all false) under
// DisallowUnknownFields.
func strictDecodes(t *testing.T, id string, p opsregistry.PreviewSpec) {
	t.Helper()
	typ := reflect.TypeOf(p.Params)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	keys := append(p.ModeKeys(), p.LiveOneOf...)
	for _, v := range []bool{true, false} {
		obj := map[string]bool{}
		for _, k := range keys {
			obj[k] = v
		}
		raw, err := json.Marshal(obj)
		require.NoError(t, err)
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		require.NoError(t, dec.Decode(reflect.New(typ).Interface()), "%s: a strict decode of the injected %s fails", id, raw)
	}
}

// TestOpsV3_EveryOpThatDeclaresWritesIsAWriter: an op that declares the rows
// it writes (OperationDef.Writes) is a v3 writer whatever its capabilities
// say, so its mode is never ignored. maintenance.transcribe-book-intros
// declared Writes: books with only read capabilities, and v3 passed a
// "preview" to it untouched.
func TestOpsV3_EveryOpThatDeclaresWritesIsAWriter(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	defs := map[string]opsv3.DefInfo{}
	for _, d := range (&opsV3Defs{reg: srv.opRegistry}).ListDefs() {
		defs[d.ID] = d
	}
	n := 0
	for _, od := range srv.opRegistry.ActiveDefs() {
		if len(od.Writes) == 0 {
			continue
		}
		n++
		require.True(t, canWrite(od), "%s declares Writes %v but canWrite is false", od.ID, od.Writes)
		require.True(t, defs[od.ID].Writer, "%s declares Writes %v but v3 treats it as read-only", od.ID, od.Writes)
	}
	require.Positive(t, n, "no registered op declares Writes; the check did not run")
}

// readOnlyByDeclaration is every adapted op v3 treats as read-only (no write
// capability, no Writes), with what it was found to do (re-swept 2026-10-18,
// PR 05-PR9). v3 passes their params through untouched and runs them in
// either mode, so an op wrongly listed here runs for real on a "preview".
// A new op that lands here fails until it is listed, so the claim gets read.
// Ops that declare no writes yet write on every run (candidate rows, caches,
// scan state, paid calls) are not here: they are in writesWithoutPreview,
// which makes v3 treat them as live-only writers until the owner decides
// (todo.d/2026-10-17-ops-v3-approve-and-schema-checker.md).
var readOnlyByDeclaration = map[string]string{
	"catalog.coverage-report":                        "reads ident index and catalog keys",
	"dedup.author-scan":                              "in-memory result cache only",
	"dedup.book-scan":                                "in-memory result cache only",
	"dedup.calibrate-embedding-thresholds":           "reads; logs a report",
	"dedup.label-refinement":                         "enqueues two children with their default (non-writing) params",
	"dedup.series-scan":                              "in-memory result cache only",
	"diagnostics.export":                             "temp ZIP in the OS temp dir",
	"library.size-refresh":                           "in-memory size cache only",
	"maintenance.author-title-fragment-scan":         "reads; logs",
	"maintenance.author-whitespace-collision-report": "reads; logs",
	"maintenance.book-atpath-index-verify":           "snapshot scans only",
	"maintenance.book-shape-report":                  "report TSV under .reports (reportPath can redirect it: see todo)",
	"maintenance.combine-journal-check":              "reads the combine-journal rows; reports each unreadable one by key",
	"maintenance.credit-census":                      "reads; reports",
	"maintenance.file-integrity-check":               "reads and hashes files; reports",
	"maintenance.filepath-collision-report":          "reads; reports",
	"maintenance.metadata-refresh":                   "lists and logs; holds the scan stand-down for its run",
	"maintenance.missing-file-audit":                 "reads; reports",
	"maintenance.missing-file-repair":                "reports; apply is refused",
	"maintenance.orphan-book-files-cleanup":          "reports; delete is refused",
	"maintenance.orphan-book-files-repoint-plan":     "reads; plans",
	"maintenance.unknown-author-audit":               "reads; reports",
	"maintenance.version-group-primary-report":       "reads; reports",
	"metadata.bulk-apply-preview":                    "its own result rows only",
	"metafetch.calibrate-scoring":                    "reads; reports",
	"reconcile.scan":                                 "reads and hashes; the writes are reconcile.apply's",
	"repairs.plan":                                   "fixers' Plan only; writes are in Apply",
}

// TestOpsV3_ReadOnlyOpsAreListed pins readOnlyByDeclaration to the catalog in
// both directions.
func TestOpsV3_ReadOnlyOpsAreListed(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	got := map[string]bool{}
	for _, d := range (&opsV3Defs{reg: srv.opRegistry}).ListDefs() {
		if d.Kind == "adapted" && !d.Writer {
			got[d.ID] = true
			_, listed := readOnlyByDeclaration[d.ID]
			require.True(t, listed, "%s is treated by v3 as read-only (no write capability, no Writes). Check it writes nothing in any mode and list it in readOnlyByDeclaration, or declare what it writes", d.ID)
		}
	}
	for id := range readOnlyByDeclaration {
		require.True(t, got[id], "readOnlyByDeclaration lists %s, which is not a read-only adapted op; delete the entry", id)
	}
}

// TestOpsV3_WritesWithoutPreviewAreLiveOnly: every op in writesWithoutPreview
// is a registered adapted op that v3 shows as a writer with no preview, and
// the router refuses a preview or mode-less start of each with 409. An entry
// whose op has since declared writes or a preview of its own is refused, so
// the list is deleted from rather than left to shadow the declaration.
func TestOpsV3_WritesWithoutPreviewAreLiveOnly(t *testing.T) {
	require.Len(t, writesWithoutPreview, 5, "the list changed; update this count with the owner decision it carries")
	srv, _, _ := bootRegisteredOpIDs(t)
	defs := map[string]opsv3.DefInfo{}
	for _, d := range (&opsV3Defs{reg: srv.opRegistry}).ListDefs() {
		defs[d.ID] = d
	}
	for id, why := range writesWithoutPreview {
		require.NotEmpty(t, why, "%s: say what it writes", id)
		od, ok := srv.opRegistry.Def(id)
		require.True(t, ok, "writesWithoutPreview lists %s, which is not registered; delete the entry", id)
		require.False(t, canWrite(od), "%s now declares what it writes; delete its writesWithoutPreview entry and give it a ledger line", id)
		require.True(t, od.Preview.IsZero(), "%s now declares a preview; delete its writesWithoutPreview entry", id)
		d := defs[id]
		require.Equal(t, "adapted", d.Kind, id)
		require.True(t, d.Writer, "%s: v3 must treat it as a writer", id)
		require.False(t, d.HasPreview(), "%s: v3 must show no preview", id)
		_, alsoReadOnly := readOnlyByDeclaration[id]
		require.False(t, alsoReadOnly, "%s is in both writesWithoutPreview and readOnlyByDeclaration", id)
	}

	s, cleanup := setupTestServer(t)
	defer cleanup()
	routed := 0
	for id := range writesWithoutPreview {
		if _, ok := s.opRegistry.Def(id); !ok {
			continue // not wired on the slim test server; the catalog checks above cover it
		}
		routed++
		for _, body := range []string{`{"mode":"preview"}`, `{}`, `{"mode":"preview","params":{"dry_run":true}}`} {
			req := httptest.NewRequest(http.MethodPost, "/api/v3/ops/defs/"+id+"/runs", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusConflict, rec.Code, "%s %s: %s", id, body, rec.Body.String())
		}
		// The unnested POST /api/v3/ops/runs names the definition in the
		// body and must refuse the same way.
		for _, body := range []string{`{"def":"` + id + `","mode":"preview"}`, `{"def":"` + id + `"}`} {
			req := httptest.NewRequest(http.MethodPost, "/api/v3/ops/runs", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusConflict, rec.Code, "POST /ops/runs %s: %s", body, rec.Body.String())
		}
	}
	require.Positive(t, routed, "no listed op is wired on the test server; the router check did not run")

	// A live start is accepted and its params are queued as sent: no mode
	// key is injected for an op that reads none. Only
	// library.bulk-metadata-fetch is started: the test server's dispatcher
	// runs what is queued, it has no books to fetch for, and the other two
	// wired ops make paid AI calls when a key is configured.
	const liveID = "library.bulk-metadata-fetch"
	_, wired := s.opRegistry.Def(liveID)
	require.True(t, wired, "%s is not wired on the test server; pick another listed op for the live check", liveID)
	for _, path := range []string{"/api/v3/ops/runs", "/api/v3/ops/defs/" + liveID + "/runs"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"def":"`+liveID+`","mode":"live","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusAccepted, rec.Code, "%s live: %s", path, rec.Body.String())
		var started struct {
			RunID string `json:"run_id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
		require.NotEmpty(t, started.RunID, rec.Body.String())
		row, err := s.store.GetOperationV2(started.RunID)
		require.NoError(t, err)
		require.NotNil(t, row, "run %s not stored", started.RunID)
		require.JSONEq(t, `{}`, row.Params, "%s live: params must be queued untouched", path)
	}
}

// TestOpsV3Routes_NoPreviewOpRefusesPreview drives the real router: a
// writing op without a preview shows has_preview=false and a preview (or no
// mode) start is a 409, so it is never queued live under a preview label.
func TestOpsV3Routes_NoPreviewOpRefusesPreview(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/v3/ops/defs", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Items []struct {
			ID         string `json:"id"`
			HasPreview *bool  `json:"has_preview"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	target := ""
	for _, d := range list.Items {
		if d.HasPreview != nil && !*d.HasPreview {
			target = d.ID
			break
		}
	}
	require.NotEmpty(t, target, "no writing op without a preview is registered on the test server")
	for _, body := range []string{`{"mode":"preview"}`, `{}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v3/ops/defs/"+target+"/runs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusConflict, rec.Code, "%s %s: %s", target, body, rec.Body.String())
	}
}

// TestOpsV3Routes_DefsShowTheSchedulerStartAssigns: setupRoutes runs before
// Start() assigns s.scheduler, so the v3 routes must resolve it per request.
// The scheduler is assigned after the routes exist, exactly as Start does,
// and /defs must then show each schedule and its next fire.
func TestOpsV3Routes_DefsShowTheSchedulerStartAssigns(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	require.Nil(t, s.scheduler, "the test server must not have a scheduler before the routes are built")

	yes := func() bool { return true }
	ts := scheduler.NewTaskScheduler(scheduler.SchedulerDeps{
		Store:               func() scheduler.SchedulerStore { return nil },
		OpRegistry:          s.opRegistry,
		HasDedupPlugin:      yes,
		HasMetadataFetchSvc: yes,
		HasActivitySvc:      yes,
		HasBatchPoller:      yes,
	})
	require.NoError(t, ts.RegisterSchedules(s.opRegistry.Catalog()))
	s.scheduler = ts
	t.Cleanup(func() { s.scheduler = nil })

	want := map[string]scheduler.ScheduleStatus{}
	withNext := 0
	for _, st := range ts.ScheduleStatuses() {
		want[st.DefID] = st
		if st.NextFire != nil {
			withNext++
		}
	}
	require.NotEmpty(t, want, "the catalog registered no schedules")
	require.Positive(t, withNext, "no schedule has a next fire to show")

	req := httptest.NewRequest(http.MethodGet, "/api/v3/ops/defs", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Items []struct {
			ID       string `json:"id"`
			Schedule struct {
				Kind     string     `json:"kind"`
				NextFire *time.Time `json:"next_fire"`
			} `json:"schedule"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	shown, shownNext := 0, 0
	for _, d := range list.Items {
		st, ok := want[d.ID]
		if !ok {
			require.Equal(t, "manual", d.Schedule.Kind, d.ID)
			continue
		}
		shown++
		require.Equal(t, st.Kind, d.Schedule.Kind, "%s schedule kind", d.ID)
		require.Equal(t, st.NextFire != nil, d.Schedule.NextFire != nil, "%s next_fire", d.ID)
		if d.Schedule.NextFire != nil {
			shownNext++
		}
	}
	require.Equal(t, len(want), shown, "every scheduled def is listed with its schedule")
	require.Equal(t, withNext, shownNext)
}

// TestOpsV3Routes_EventStreamsEndOnShutdownAndStartLazily: building the
// server starts no event loop (it starts with the first stream), and
// closeOpsV3Streams, which Shutdown calls before the graceful HTTP shutdown,
// ends an open stream at once instead of after the 30s timeout.
func TestOpsV3Routes_EventStreamsEndOnShutdownAndStartLazily(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	require.NotContains(t, s.bgWG.Running(), "ops-v3-events", "the broker loop must not run before anyone streams")

	srv := httptest.NewServer(s.router)
	defer srv.Close()
	ctx := t.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v3/ops/events", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Eventually(t, func() bool { return slices.Contains(s.bgWG.Running(), "ops-v3-events") }, 5*time.Second, 10*time.Millisecond)

	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(ended)
	}()
	s.closeOpsV3Streams()
	s.closeOpsV3Streams() // idempotent
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream outlived closeOpsV3Streams")
	}
}

// TestOpsV3Authz_FirstRunCheckReadOncePerRequest: Holds runs once per
// permission (a definition's set plus the start floor); the first-run check
// behind it is one CountUsers per request, not one per permission.
func TestOpsV3Authz_FirstRunCheckReadOncePerRequest(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.EnableAuth = true
	store := mocks.NewMockStore(t)
	store.EXPECT().CountUsers().Return(3, nil).Times(2)
	s := &Server{store: store}
	authz := s.opsV3Authz()
	for range 2 { // two requests
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		for _, p := range []auth.Permission{auth.PermScanTrigger, auth.PermLibraryEditMetadata, auth.PermSettingsManage} {
			require.False(t, authz.Holds(c, p), "a request with no permissions holds none once users exist")
		}
	}
}

// TestOpsV3Routes_NeedCredentialsWhenAuthIsOn: with auth on and users present,
// every kind of /api/v3/ops route (a read, a start, an approve and the SSE
// feed) answers 401 to a request with no credentials, through the real
// middleware chain.
// Not parallel: it swaps the process-wide config.
func TestOpsV3Routes_NeedCredentialsWhenAuthIsOn(t *testing.T) {
	srv, _, _ := repairsPermServer(t, true, true)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v3/ops/defs"},
		{http.MethodGet, "/api/v3/ops/runs"},
		{http.MethodPost, "/api/v3/ops/defs/library.scan/runs"},
		{http.MethodPost, "/api/v3/ops/runs"},
		{http.MethodPost, "/api/v3/ops/runs/r1/cancel"},
		{http.MethodPost, "/api/v3/ops/runs/r1/approve"},
		{http.MethodGet, "/api/v3/ops/events"},
	} {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "%s %s: %s", r.method, r.path, w.Body.String())
	}
}

// TestOpsV3Routes_ForwardedHeaderCannotApprove: the unsigned
// Cf-Access-Authenticated-User-Email header, naming a real admin, with no
// verified Access JWT, session or token, is not an identity: approve is
// refused (401/403) by the real server chain, never reaching the handler's
// 501.
// Not parallel: it swaps the process-wide config.
func TestOpsV3Routes_ForwardedHeaderCannotApprove(t *testing.T) {
	srv, _, _ := repairsPermServer(t, true, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v3/ops/runs/r1/approve", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cf-Access-Authenticated-User-Email", "admin@x.test")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, w.Code, w.Body.String())
}

// TestOpsV3Routes_ViewerCannotStartARun: the seeded viewer role (no
// scan.trigger) is refused at both start routes by the real permission
// middleware; an editor (scan.trigger) passes the floor.
// Not parallel: it swaps the process-wide config.
func TestOpsV3Routes_ViewerCannotStartARun(t *testing.T) {
	srv, editor, _ := repairsPermServer(t, true, true)
	store := srv.storeForWiring()
	viewerUser, err := store.CreateUser("viewer", "viewer@x.test", "bcrypt", "hash", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	vs, err := store.CreateSession(viewerUser.ID, "192.0.2.10", "test", time.Hour, 0)
	require.NoError(t, err)
	post := func(token, path, body string) *httptest.ResponseRecorder {
		return postRepairs(srv, token, path, body)
	}
	for _, r := range []struct{ path, body string }{
		{"/api/v3/ops/defs/library.scan/runs", `{"mode":"preview"}`},
		{"/api/v3/ops/runs", `{"def":"library.scan","mode":"preview"}`},
	} {
		w := post(vs.ID, r.path, r.body)
		require.Equal(t, http.StatusForbidden, w.Code, "viewer %s: %s", r.path, w.Body.String())
		require.Contains(t, w.Body.String(), "scan.trigger")
	}
	// The editor passes the floor and the permission check and reaches the
	// mode check: library.scan has no preview, so a preview is a 409 and
	// nothing is queued.
	w := post(editor, "/api/v3/ops/defs/library.scan/runs", `{"mode":"preview"}`)
	require.Equal(t, http.StatusConflict, w.Code, "an editor holds scan.trigger: %s", w.Body.String())
}

// TestOpsV3Routes_DeniedInHandlerAnswers403: the permission checks the route
// table cannot make (the definition's own permission on a start, the
// approver's on an approve) run inside the handlers through opsV3Authz.Require,
// which has to write the 403 itself: the handler returns on false and writes
// nothing else, so a Require that only answered the question would leave a
// denied caller with an empty 200. An editor holds the routes' floors
// (scan.trigger, library.view) but not settings.manage, which
// maintenance.file-integrity-check declares and an approve defaults to.
// Not parallel: it swaps the process-wide config.
func TestOpsV3Routes_DeniedInHandlerAnswers403(t *testing.T) {
	srv, _, _ := repairsPermServer(t, true, true)
	store, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok, "the test server runs on a PebbleStore")
	// Approving needs an interactive sign-in (verifiedForApproval), which the
	// helper's plain sessions are not.
	signIn := func(username string) string {
		u, err := store.GetUserByUsername(username)
		require.NoError(t, err)
		require.NotNil(t, u)
		sess, err := store.CreateSessionWithOrigin(u.ID, "192.0.2.10", "test", time.Hour, database.SessionOriginPassword, 0)
		require.NoError(t, err)
		return sess.ID
	}
	editor, admin := signIn("editor"), signIn("admin")
	require.NoError(t, store.InsertRunV3(database.RunRecord{
		SchemaVersion: 3, ID: "run-awaiting-approval", DefID: "maintenance.file-integrity-check",
		Kind: "op", Mode: "live", Trigger: "manual", Params: json.RawMessage(`{}`),
	}))

	for _, r := range []struct{ name, path, body string }{
		{"start by definition", "/api/v3/ops/defs/maintenance.file-integrity-check/runs", `{"mode":"live"}`},
		{"start by body", "/api/v3/ops/runs", `{"def":"maintenance.file-integrity-check","mode":"live"}`},
		{"approve", "/api/v3/ops/runs/run-awaiting-approval/approve", `{}`},
	} {
		w := postRepairs(srv, editor, r.path, r.body)
		require.Equal(t, http.StatusForbidden, w.Code, "editor %s: %s", r.name, w.Body.String())
		require.Contains(t, w.Body.String(), "permission denied: settings.manage", r.name)
	}

	// The admin passes the same check: the approve reaches the handler's own
	// 501 (no approver is wired to a runner).
	w := postRepairs(srv, admin, "/api/v3/ops/runs/run-awaiting-approval/approve", `{}`)
	require.Equal(t, http.StatusNotImplemented, w.Code, "admin approve: %s", w.Body.String())
}
