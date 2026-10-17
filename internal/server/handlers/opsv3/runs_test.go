// file: internal/server/handlers/opsv3/runs_test.go
// version: 1.1.1
// guid: 162c063a-7da8-4b81-a598-3e457d048c20
// last-edited: 2026-10-17

package opsv3

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

const bookIDsSchema = `{
  "type": "object",
  "required": ["book_ids"],
  "additionalProperties": false,
  "properties": {
    "book_ids": {"type": "array", "minItems": 1, "items": {"type": "string", "minLength": 1}},
    "limit":    {"type": "integer", "minimum": 1, "maximum": 100},
    "dry_run":  {"type": "boolean"}
  }
}`

func startHarness(defs map[string]DefInfo, ready map[string]string) (*fakeEnq, *fakeApprover, *memRuns, *Handler) {
	enq := &fakeEnq{}
	appr := &fakeApprover{}
	mem := &memRuns{runs: map[string]database.RunRecord{}}
	h := New(Deps{Runs: mem, Defs: &fakeDefs{defs: defs, ready: ready}, Enqueuer: enq, Approver: appr})
	return enq, appr, mem, h
}

func TestRunRejectsSchemaInvalidParamsBeforeQueueing(t *testing.T) {
	enq, _, _, h := startHarness(map[string]DefInfo{
		"test.apply": {ID: "test.apply", Title: "Apply", Kind: "batch", Permissions: []auth.Permission{auth.PermLibraryEditMetadata}, ParamsSchema: []byte(bookIDsSchema)},
	}, nil)
	r := newRouter(h)
	editor := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger + "," + auth.PermLibraryEditMetadata, user: "u-ed", method: string(auth.MethodSession)}

	w := do(t, r, editor, http.MethodPost, "/api/v3/ops/defs/test.apply/runs",
		`{"params":{"book_ids":[],"limit":500,"extra":1}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("schema-invalid params = %d, want 400: %s", w.Code, w.Body.String())
	}
	var body struct {
		Fields []Violation `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, f := range body.Fields {
		paths[f.Path] = true
	}
	for _, want := range []string{"/book_ids", "/limit", "/extra"} {
		if !paths[want] {
			t.Errorf("400 does not name field %s; fields=%+v", want, body.Fields)
		}
	}
	if n := enq.count(); n != 0 {
		t.Fatalf("the enqueuer saw %d calls for invalid params, want 0", n)
	}

	// Missing required field.
	w = do(t, r, editor, http.MethodPost, "/api/v3/ops/defs/test.apply/runs", `{"params":{}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "/book_ids") {
		t.Fatalf("missing required = %d %s", w.Code, w.Body.String())
	}
	// params that are not an object.
	if w := do(t, r, editor, http.MethodPost, "/api/v3/ops/defs/test.apply/runs", `{"params":[1]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("array params = %d, want 400", w.Code)
	}
	if n := enq.count(); n != 0 {
		t.Fatalf("enqueuer saw %d calls", n)
	}

	// Valid params queue, and the caller is recorded as the actor.
	w = do(t, r, editor, http.MethodPost, "/api/v3/ops/defs/test.apply/runs", `{"params":{"book_ids":["b1"]}}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("valid params = %d, want 202: %s", w.Code, w.Body.String())
	}
	if got := decode[map[string]string](t, w)["run_id"]; got == "" {
		t.Error("202 without run_id")
	}
	if enq.count() != 1 {
		t.Fatalf("enqueuer calls = %d, want 1", enq.count())
	}
	if o := enq.calls[0].opts; o.ActorUserID != "u-ed" || o.ActorAuthMethod != "session" {
		t.Errorf("actor = %q/%q, want u-ed/session", o.ActorUserID, o.ActorAuthMethod)
	}
}

func TestRunUnreadyDefReturns409(t *testing.T) {
	enq, _, _, h := startHarness(
		map[string]DefInfo{"test.scan": {ID: "test.scan", Title: "Scan", Kind: "task", Permissions: []auth.Permission{auth.PermScanTrigger}}},
		map[string]string{"test.scan": "waiting for startup warmup"})
	r := newRouter(h)
	scanner := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger, user: "u", method: string(auth.MethodSession)}
	w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/defs/test.scan/runs", `{}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "waiting for startup warmup") {
		t.Fatalf("unready def = %d %s, want 409 naming the reason", w.Code, w.Body.String())
	}
	if enq.count() != 0 {
		t.Fatal("an unready def was queued")
	}
}

func TestEmptyPermissionRequiresSettingsManage(t *testing.T) {
	enq, _, _, h := startHarness(map[string]DefInfo{
		"test.nopermission": {ID: "test.nopermission", Title: "No permission", Kind: "task"},
		"test.blank":        {ID: "test.blank", Title: "Blank string", Kind: "task", Permissions: []auth.Permission{""}},
	}, nil)
	r := newRouter(h)
	scanner := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger + "," + auth.PermLibraryEditMetadata, user: "u", method: string(auth.MethodSession)}
	for _, id := range []string{"test.nopermission", "test.blank"} {
		if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/defs/"+id+"/runs", `{}`); w.Code != http.StatusForbidden {
			t.Errorf("%s without settings.manage = %d, want 403", id, w.Code)
		}
		if w := do(t, r, admin, http.MethodPost, "/api/v3/ops/defs/"+id+"/runs", `{}`); w.Code != http.StatusAccepted {
			t.Errorf("%s with settings.manage = %d, want 202: %s", id, w.Code, w.Body.String())
		}
	}
	if enq.count() != 2 {
		t.Errorf("enqueued %d, want 2 (the admin's)", enq.count())
	}
	// The permission shown to the UI says the same.
	got := decode[DefView](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/defs/test.nopermission", nil))
	if got.Permission != auth.PermSettingsManage {
		t.Errorf("defs shows permission %q for an empty one, want settings.manage", got.Permission)
	}
}

func TestStartUnnestedRouteAndUnknownDef(t *testing.T) {
	enq, _, _, h := startHarness(map[string]DefInfo{"test.a": {ID: "test.a", Title: "A", Kind: "task", Permissions: []auth.Permission{auth.PermScanTrigger}}}, nil)
	r := newRouter(h)
	scanner := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger, user: "u", method: string(auth.MethodSession)}
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/runs", `{"def":"test.a"}`); w.Code != http.StatusAccepted {
		t.Fatalf("POST /ops/runs {def} = %d: %s", w.Code, w.Body.String())
	}
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/runs", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no def = %d, want 400", w.Code)
	}
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/defs/nope/runs", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown def = %d, want 404", w.Code)
	}
	// A body-less POST is an empty params, not a malformed body.
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/defs/test.a/runs", nil); w.Code != http.StatusAccepted {
		t.Fatalf("empty body = %d: %s", w.Code, w.Body.String())
	}
	if enq.count() != 2 {
		t.Errorf("enqueued %d, want 2", enq.count())
	}
}

func TestStartModeIsExplicitAndConsistent(t *testing.T) {
	enq, _, _, h := startHarness(map[string]DefInfo{"test.w": {ID: "test.w", Title: "W", Kind: "batch", Permissions: []auth.Permission{auth.PermScanTrigger}, Writer: true, HasPreview: true}}, nil)
	r := newRouter(h)
	scanner := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger, user: "u", method: string(auth.MethodSession)}
	post := func(body string) *int {
		w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/defs/test.w/runs", body)
		return &w.Code
	}
	if c := post(`{"mode":"live"}`); *c != http.StatusAccepted {
		t.Fatalf("live = %d", *c)
	}
	if c := post(`{"mode":"preview","params":{"k":1}}`); *c != http.StatusAccepted {
		t.Fatalf("preview = %d", *c)
	}
	if c := post(`{"params":{"k":1}}`); *c != http.StatusAccepted {
		t.Fatalf("no mode = %d", *c)
	}
	for _, bad := range []string{`{"mode":"yolo"}`, `{"mode":"live","params":{"dry_run":true}}`, `{"params":{"dry_run":true,"dryRun":false}}`} {
		if c := post(bad); *c != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, *c)
		}
	}
	if enq.count() != 3 {
		t.Fatalf("enqueued %d, want 3", enq.count())
	}
	mode := func(i int) (string, bool) {
		var m map[string]any
		_ = json.Unmarshal(enq.calls[i].params, &m)
		v, ok := m["dry_run"]
		if !ok {
			return "", false
		}
		return map[bool]string{true: "preview", false: "live"}[v.(bool)], true
	}
	if m, _ := mode(0); m != "live" {
		t.Errorf("mode=live queued %s", m)
	}
	if m, _ := mode(1); m != "preview" {
		t.Errorf("mode=preview queued %s", m)
	}
	// No mode sent to a definition with a preview: a preview, written into
	// params so the op's own default cannot decide.
	if m, _ := mode(2); m != "preview" {
		t.Errorf("no mode queued %q, want preview", m)
	}
}

// TestStartNoPreviewDefNeedsExplicitLive: an adapted v2 op recorded `no-mode`
// ignores dry_run, so a "preview" (or a request with no mode) would run live.
// Both are refused with 409 and nothing is queued; only mode "live" starts it,
// with params passed through as sent.
func TestStartNoPreviewDefNeedsExplicitLive(t *testing.T) {
	enq, _, _, h := startHarness(map[string]DefInfo{
		"dedup.book-merge": {ID: "dedup.book-merge", Title: "Merge", Kind: "adapted", Permissions: []auth.Permission{auth.PermScanTrigger}, Writer: true},
		"test.report":      {ID: "test.report", Title: "Report", Kind: "adapted", Permissions: []auth.Permission{auth.PermScanTrigger}},
	}, nil)
	r := newRouter(h)
	scanner := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger, user: "u", method: string(auth.MethodSession)}
	for _, body := range []string{`{"mode":"preview"}`, `{}`, `{"params":{"dry_run":true}}`, `{"params":{"dry_run":false}}`} {
		w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/defs/dedup.book-merge/runs", body)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `mode \"live\"`) {
			t.Errorf("%s on a no-preview def = %d %s, want 409 asking for mode live", body, w.Code, w.Body.String())
		}
	}
	if enq.count() != 0 {
		t.Fatalf("a refused preview queued %d runs", enq.count())
	}
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/defs/dedup.book-merge/runs", `{"mode":"live","params":{"dry_run":true}}`); w.Code != http.StatusBadRequest {
		t.Errorf("live with dry_run=true = %d, want 400", w.Code)
	}
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/defs/dedup.book-merge/runs", `{"mode":"live","params":{"keep":"b1"}}`); w.Code != http.StatusAccepted {
		t.Fatalf("explicit live = %d: %s", w.Code, w.Body.String())
	}
	if enq.count() != 1 || string(enq.calls[0].params) != `{"keep":"b1"}` {
		t.Fatalf("live run params = %s, want them untouched", enq.calls[0].params)
	}
	// A read-only def is not gated: the mode changes nothing.
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/defs/test.report/runs", `{}`); w.Code != http.StatusAccepted {
		t.Errorf("read-only def with no mode = %d", w.Code)
	}
	// /defs says which is which: false for no preview, null for read-only.
	raw := do(t, r, viewer, http.MethodGet, "/api/v3/ops/defs/dedup.book-merge", nil).Body.String()
	if !strings.Contains(raw, `"has_preview":false`) {
		t.Errorf("no-preview def view lacks has_preview=false: %s", raw)
	}
	raw = do(t, r, viewer, http.MethodGet, "/api/v3/ops/defs/test.report", nil).Body.String()
	if !strings.Contains(raw, `"has_preview":null`) {
		t.Errorf("read-only def view lacks has_preview=null: %s", raw)
	}
}

func TestApproveRefusedWithoutVerifiedAuth(t *testing.T) {
	_, appr, mem, h := startHarness(map[string]DefInfo{"test.fix": {ID: "test.fix", Title: "Fix", Kind: "fixer"}}, nil)
	mem.runs["run1"] = database.RunRecord{ID: "run1", DefID: "test.fix", State: state.V3AwaitingDecision}
	r := newRouter(h)

	// Authorized by permission but only by a forwarded header: no verified
	// method.
	for name, who := range map[string]caller{
		"forwarded header only": headers,
		"delegated session":     {perms: headers.perms, user: "u", method: string(auth.MethodSessionDelegated)},
		"abs token":             {perms: headers.perms, user: "u", method: string(auth.MethodABS)},
	} {
		w := do(t, r, who, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{}`)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: approve = %d, want 403: %s", name, w.Code, w.Body.String())
		}
	}
	if len(appr.calls) != 0 {
		t.Fatalf("an unverified approval reached the approver: %+v", appr.calls)
	}
	got, _ := mem.GetRunV3("run1")
	if got.State != state.V3AwaitingDecision || got.Approval != nil {
		t.Fatalf("run changed: state=%s approval=%v", got.State, got.Approval)
	}

	// Verified, but without the approver permission.
	if w := do(t, r, viewer, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{}`); w.Code != http.StatusForbidden {
		t.Errorf("verified without approver permission = %d, want 403", w.Code)
	}
	if len(appr.calls) != 0 {
		t.Fatal("approver called without permission")
	}

	// Verified with the permission: recorded with who and how.
	if w := do(t, r, admin, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{"note":"looks right"}`); w.Code != http.StatusAccepted {
		t.Fatalf("verified approve = %d: %s", w.Code, w.Body.String())
	}
	if len(appr.calls) != 1 {
		t.Fatalf("approver calls = %d", len(appr.calls))
	}
	rec := appr.calls[0]
	if rec.ApprovedBy.UserID != "u-admin" || rec.ApprovedBy.AuthMethod != "session" || rec.Decision != "approve" || rec.Note != "looks right" {
		t.Errorf("approval record = %+v", rec)
	}
	// An API token counts as verified.
	tok := caller{perms: admin.perms, user: "svc", method: string(auth.MethodAPIKey)}
	if w := do(t, r, tok, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{"decision":"reject"}`); w.Code != http.StatusAccepted {
		t.Errorf("api-key approve = %d", w.Code)
	}
	if w := do(t, r, admin, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{"decision":"maybe"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad decision = %d, want 400", w.Code)
	}
	if w := do(t, r, admin, http.MethodPost, "/api/v3/ops/runs/nope/approve", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown run = %d, want 404", w.Code)
	}
}

func TestApproveUsesTheDefinitionsApproverPermission(t *testing.T) {
	_, appr, mem, h := startHarness(map[string]DefInfo{
		"test.fix": {ID: "test.fix", Title: "Fix", Kind: "fixer", Approver: auth.PermLibraryEditMetadata},
	}, nil)
	mem.runs["run1"] = database.RunRecord{ID: "run1", DefID: "test.fix"}
	r := newRouter(h)
	editor := caller{perms: auth.PermLibraryView + "," + auth.PermLibraryEditMetadata, user: "e", method: string(auth.MethodSession)}
	if w := do(t, r, editor, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{}`); w.Code != http.StatusAccepted {
		t.Fatalf("editor (the approver permission) = %d: %s", w.Code, w.Body.String())
	}
	if w := do(t, r, admin, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("settings.manage is not this definition's approver permission, got %d", w.Code)
	}
	if len(appr.calls) != 1 {
		t.Fatalf("approver calls = %d, want 1", len(appr.calls))
	}
	appr.err = ErrRunNotApprovable
	if w := do(t, r, editor, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{}`); w.Code != http.StatusConflict {
		t.Errorf("not approvable = %d, want 409", w.Code)
	}
}

func TestApproveWithoutRunnerBackendIs501(t *testing.T) {
	mem := &memRuns{runs: map[string]database.RunRecord{"run1": {ID: "run1", DefID: "test.fix"}}}
	r := newRouter(New(Deps{Runs: mem}))
	if w := do(t, r, admin, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{}`); w.Code != http.StatusNotImplemented {
		t.Fatalf("no approver wired = %d, want 501", w.Code)
	}
	// 501 only after the checks: an unverified caller still gets 403.
	if w := do(t, r, headers, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("unverified with no approver wired = %d, want 403", w.Code)
	}
}

func TestCancelNeedsDefinitionPermissionOrBeingTheStarter(t *testing.T) {
	enq, _, mem, h := startHarness(map[string]DefInfo{
		"test.scan": {ID: "test.scan", Title: "Scan", Kind: "task", Permissions: []auth.Permission{auth.PermScanTrigger}},
	}, nil)
	mem.runs["mine"] = database.RunRecord{ID: "mine", DefID: "test.scan", State: state.V3Running, Actor: database.Actor{UserID: "u-view"}}
	mem.runs["theirs"] = database.RunRecord{ID: "theirs", DefID: "test.scan", State: state.V3Running, Actor: database.Actor{UserID: "someone-else"}}
	mem.runs["done"] = database.RunRecord{ID: "done", DefID: "test.scan", State: state.V3Succeeded, Actor: database.Actor{UserID: "u-view"}}
	r := newRouter(h)
	scanner := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger, user: "s", method: string(auth.MethodSession)}

	if w := do(t, r, viewer, http.MethodPost, "/api/v3/ops/runs/mine/cancel", nil); w.Code != http.StatusAccepted {
		t.Errorf("starter cancelling own run = %d: %s", w.Code, w.Body.String())
	}
	if w := do(t, r, viewer, http.MethodPost, "/api/v3/ops/runs/theirs/cancel", nil); w.Code != http.StatusForbidden {
		t.Errorf("non-starter without the permission = %d, want 403", w.Code)
	}
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/runs/theirs/cancel", nil); w.Code != http.StatusAccepted {
		t.Errorf("holder of the definition's permission = %d: %s", w.Code, w.Body.String())
	}
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/runs/done/cancel", nil); w.Code != http.StatusConflict {
		t.Errorf("finished run = %d, want 409", w.Code)
	}
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/runs/nope/cancel", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown run = %d, want 404", w.Code)
	}
	if got := strings.Join(enq.cancelled, ","); got != "mine,theirs" {
		t.Errorf("cancelled %q, want mine,theirs", got)
	}
	enq.cancelErr = opsregistry.ErrOpNotFound
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/runs/theirs/cancel", nil); w.Code != http.StatusNotFound {
		t.Errorf("registry does not know the run = %d, want 404", w.Code)
	}
}

func TestRunViewCarriesChunksAndWorkers(t *testing.T) {
	total := int64(200)
	started := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	rec := database.RunRecord{
		ID: "batch1", DefID: "test.batch", State: state.V3Running, Mode: "live", StartedAt: &started,
		Actor: database.Actor{UserID: "u-admin"},
		Progress: database.ProgressSnapshot{
			Done: 120, Total: &total, Failed: 3,
			Chunks: &database.ChunkProgress{Done: 6, Total: 10, Size: 20},
			Workers: []database.WorkerProgress{
				{Worker: 0, Chunk: 6, Item: "book-a", ItemIndex: 4, ChunkSize: 20},
				{Worker: 1, Chunk: -1},
				{Worker: 2, Chunk: 7, Item: "book-b", Stuck: true},
			},
		},
	}
	mem := &memRuns{runs: map[string]database.RunRecord{"batch1": rec, "task1": {ID: "task1", DefID: "test.task", State: state.V3Running}}}
	r := newRouter(New(Deps{Runs: mem}))
	v := decode[RunView](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/batch1", nil))
	if v.Progress.Chunks == nil || v.Progress.Chunks.Done != 6 || v.Progress.Chunks.Total != 10 {
		t.Fatalf("chunks = %+v, want 6/10", v.Progress.Chunks)
	}
	if len(v.Progress.Workers) != 3 || !v.Progress.Workers[2].Stuck || v.Progress.Workers[0].Item != "book-a" {
		t.Fatalf("workers = %+v", v.Progress.Workers)
	}
	if v.Progress.Done != 120 || v.Progress.Total == nil || *v.Progress.Total != 200 || v.StartedBy != "u-admin" || v.Mode != "live" || v.Outcome != "" {
		t.Errorf("view = %+v", v)
	}
	// A Task has no chunks (null) and an empty workers list, and an unknown
	// total stays null.
	raw := do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/task1", nil).Body.String()
	for _, want := range []string{`"chunks":null`, `"workers":[]`, `"total":null`} {
		if !strings.Contains(raw, want) {
			t.Errorf("task run JSON lacks %s: %s", want, raw)
		}
	}
	if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/missing", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown run = %d, want 404", w.Code)
	}
}

func TestZombieFlagAndStoppingState(t *testing.T) {
	// The registry says run z is a zombie even though its record still reads
	// running (the record can lag by one write).
	mem := &memRuns{runs: map[string]database.RunRecord{
		"z":  {ID: "z", DefID: "test.slow", State: state.V3Running},
		"ok": {ID: "ok", DefID: "test.slow", State: state.V3Running},
		"s":  {ID: "s", DefID: "test.slow", State: state.V3Stopping, Zombie: true},
	}}
	r := newRouter(New(Deps{Runs: mem, Zombies: fakeZombies{{OpID: "z", DefID: "test.slow"}}}))
	z := decode[RunView](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/z", nil))
	if z.State != "stopping" || !z.Zombie || z.Outcome != "" {
		t.Errorf("zombie = state %q zombie %v outcome %q, want stopping/true/empty", z.State, z.Zombie, z.Outcome)
	}
	if ok := decode[RunView](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/ok", nil)); ok.State != "running" || ok.Zombie {
		t.Errorf("healthy run = %+v", ok)
	}
	if s := decode[RunView](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/s", nil)); s.State != "stopping" || !s.Zombie {
		t.Errorf("record-level zombie = %+v", s)
	}
}

func TestDefsListEffectsScheduleReadinessAndLastRun(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	next := now.Add(time.Hour)
	fin := now.Add(-time.Hour)
	mem := &memRuns{
		done: []database.RunRecord{
			{ID: "old", DefID: "test.nightly", State: state.V3Failed, CompletedAt: ptr(fin.Add(-time.Hour))},
			{ID: "new", DefID: "test.nightly", State: state.V3Succeeded, CompletedAt: &fin},
		},
		runs:  map[string]database.RunRecord{"viaSched": {ID: "viaSched", DefID: "test.weekly", State: state.V3Canceled, CompletedAt: &fin}},
		sched: map[string]database.SchedState{"test.weekly": {DefID: "test.weekly", LastRunID: "viaSched"}},
	}
	defs := &fakeDefs{
		defs: map[string]DefInfo{
			"test.nightly": {ID: "test.nightly", Title: "Nightly", Kind: "batch", Permissions: []auth.Permission{auth.PermScanTrigger}, Writes: []string{"book"}},
			"test.weekly":  {ID: "test.weekly", Title: "Weekly", Kind: "task", ReadOnly: true, Reads: []string{"book"}},
			"test.manual":  {ID: "test.manual", Title: "Manual", Kind: "fixer"},
		},
		ready: map[string]string{"test.manual": "waiting for startup warmup"},
	}
	sched := fakeSched{"test.nightly": {Kind: "daily_at", Active: true, Live: true, NextFire: &next}}
	r := newRouter(New(Deps{Runs: mem, Defs: defs, Sched: sched, Now: func() time.Time { return now }}))
	type listDefs struct {
		Items []DefView `json:"items"`
		Total int       `json:"total"`
	}
	got := decode[listDefs](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/defs", nil))
	by := map[string]DefView{}
	for _, d := range got.Items {
		by[d.ID] = d
	}
	if got.Total != 3 || len(by) != 3 {
		t.Fatalf("defs = %+v", got)
	}
	n := by["test.nightly"]
	if n.Schedule.Kind != "daily_at" || !n.Schedule.Active || !n.Schedule.Live || n.Schedule.NextFire == nil || !n.Schedule.NextFire.Equal(next) {
		t.Errorf("nightly schedule = %+v", n.Schedule)
	}
	if n.LastRun == nil || n.LastRun.ID != "new" || n.LastRun.Outcome != "succeeded" {
		t.Errorf("nightly last_run = %+v, want the newest finished run", n.LastRun)
	}
	if len(n.Effects.Writes) != 1 || n.Effects.ReadOnly || n.Permission != auth.PermScanTrigger {
		t.Errorf("nightly effects/permission = %+v / %s", n.Effects, n.Permission)
	}
	w := by["test.weekly"]
	if w.LastRun == nil || w.LastRun.ID != "viaSched" || w.LastRun.Outcome != "canceled" {
		t.Errorf("weekly last_run = %+v, want the scheduler's recorded run", w.LastRun)
	}
	if !w.Effects.ReadOnly || w.Schedule.Kind != "manual" || w.Schedule.NextFire != nil {
		t.Errorf("weekly = %+v", w)
	}
	m := by["test.manual"]
	if m.Ready.OK || m.Ready.Reason != "waiting for startup warmup" || m.LastRun != nil {
		t.Errorf("manual = %+v", m)
	}
	if !n.Ready.OK {
		t.Errorf("nightly should be ready: %+v", n.Ready)
	}
	fixers := decode[listDefs](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/defs?kind=fixer", nil))
	if fixers.Total != 1 || fixers.Items[0].ID != "test.manual" {
		t.Errorf("kind=fixer = %+v", fixers)
	}
	if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/defs/nope", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown def = %d", w.Code)
	}
}

type fakeSched map[string]ScheduleInfo

func (f fakeSched) Schedules() map[string]ScheduleInfo { return f }

func ptr[T any](v T) *T { return &v }

// TestStartAndCancelNeedScanTriggerFloor: a definition that declares only
// library.view cannot be started by a viewer (v1's POST /operations/v2 floor
// is scan.trigger), on either start route, and a viewer cannot cancel someone
// else's run of it. The starter may still cancel their own run.
func TestStartAndCancelNeedScanTriggerFloor(t *testing.T) {
	enq, _, mem, h := startHarness(map[string]DefInfo{
		"test.peek": {ID: "test.peek", Title: "Peek", Kind: "task", Permissions: []auth.Permission{auth.PermLibraryView}},
	}, nil)
	mem.runs["theirs"] = database.RunRecord{ID: "theirs", DefID: "test.peek", State: state.V3Running, Actor: database.Actor{UserID: "someone-else"}}
	mem.runs["mine"] = database.RunRecord{ID: "mine", DefID: "test.peek", State: state.V3Running, Actor: database.Actor{UserID: viewer.user}}
	r := newRouter(h)
	for _, req := range []struct{ path, body string }{
		{"/api/v3/ops/defs/test.peek/runs", `{}`},
		{"/api/v3/ops/runs", `{"def":"test.peek"}`},
	} {
		if w := do(t, r, viewer, http.MethodPost, req.path, req.body); w.Code != http.StatusForbidden {
			t.Errorf("viewer POST %s = %d, want 403", req.path, w.Code)
		}
	}
	if enq.count() != 0 {
		t.Fatalf("a viewer queued %d runs", enq.count())
	}
	trigger := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger, user: "u-t", method: string(auth.MethodSession)}
	if w := do(t, r, trigger, http.MethodPost, "/api/v3/ops/defs/test.peek/runs", `{}`); w.Code != http.StatusAccepted {
		t.Fatalf("scan.trigger + the def's permission = %d: %s", w.Code, w.Body.String())
	}
	if w := do(t, r, viewer, http.MethodPost, "/api/v3/ops/runs/theirs/cancel", nil); w.Code != http.StatusForbidden {
		t.Errorf("viewer cancelling another user's run = %d, want 403", w.Code)
	}
	if w := do(t, r, viewer, http.MethodPost, "/api/v3/ops/runs/mine/cancel", nil); w.Code != http.StatusAccepted {
		t.Errorf("starter cancelling own run = %d, want 202", w.Code)
	}
	if w := do(t, r, trigger, http.MethodPost, "/api/v3/ops/runs/theirs/cancel", nil); w.Code != http.StatusAccepted {
		t.Errorf("scan.trigger + def permission cancelling = %d, want 202", w.Code)
	}
	if got := strings.Join(enq.cancelled, ","); got != "mine,theirs" {
		t.Errorf("cancelled %q, want mine,theirs", got)
	}
}
