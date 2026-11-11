// file: core/internal/storagehttp/routes_test.go
// version: 1.0.0
// guid: bb49cbb2-e178-4f4e-bdfe-c55385b0d521
// last-edited: 2026-11-10

package storagehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// ---- doubles ---------------------------------------------------------------

const (
	bruceID = "01GOTHAMBRUCEWAYNE0000000"
	// batcaveRoot is the library root the cases confirm.
	batcaveRoot = "/batcave/library"
)

// checker is an rbac.Checker holding exactly its permissions.
type checker []rbac.Permission

func (c checker) Has(p rbac.Permission) bool { return slices.Contains(c, p) }
func (c checker) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindPublic:
		return true
	case rbac.KindAllOf:
		return len(ps) > 0 && !slices.ContainsFunc(ps, func(p rbac.Permission) bool { return !c.Has(p) })
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, c.Has)
	}
	return false
}
func (checker) Confined() bool                { return false }
func (c checker) Concrete() []rbac.Permission { return slices.Clone(c) }

// as is a principal authenticated by m holding perms; every method but
// MethodAuthDisabled gets a user.
func as(m auth.Method, perms ...rbac.Permission) *auth.Principal {
	p := &auth.Principal{Method: m, Perms: checker(perms), CredentialID: "cred-gotham", ClientAddr: netip.MustParseAddr("192.0.2.7")}
	if m != auth.MethodAuthDisabled {
		p.User = &auth.User{ID: bruceID, Username: "bruce.wayne"}
	}
	return p
}

// owner is an interactive core:admin session with the owner assurance.
func owner() *auth.Principal {
	p := as(auth.MethodSession, rbac.CoreAdmin)
	p.Assurance = auth.AssuranceOwner
	return p
}

// events is the shared order of calls the doubles saw.
type events struct {
	mu  sync.Mutex
	seq []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq = append(e.seq, s)
}

func (e *events) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.seq)
}

// stager is a Stager double recording what the route passes it.
type stager struct {
	ev    *events
	calls int
	by    string
	req   storage.ResetRequest
	ctx   context.Context
	res   storage.ResetStaged
	err   error
}

func (s *stager) StageReset(ctx context.Context, by string, req storage.ResetRequest) (storage.ResetStaged, error) {
	s.ev.add("stage")
	s.calls++
	s.by, s.req, s.ctx = by, req, ctx
	return s.res, s.err
}

// recorder is a Recorder double.
type recorder struct {
	ev      *events
	records []audit.Event
	failOn  int // the 1-based call that fails; 0 = none
	err     error
}

func (r *recorder) Record(_ context.Context, e audit.Event) error {
	r.ev.add("record")
	r.records = append(r.records, e)
	if r.failOn == len(r.records) {
		return r.err
	}
	return nil
}

// rig is the route over the doubles.
type rig struct {
	t    *testing.T
	st   *stager
	rec  *recorder
	api  *api
	logs *bytes.Buffer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	ev := &events{}
	r := &rig{t: t, st: &stager{ev: ev}, rec: &recorder{ev: ev, err: errors.New("audit disk is full")}, logs: &bytes.Buffer{}}
	r.api = &api{stager: r.st, audit: r.rec, log: slog.New(slog.NewTextHandler(r.logs, nil))}
	return r
}

// reply is a recorded response.
type reply struct {
	status      int
	contentType string
	body        string
}

// post sends body as POST /system/factory-reset as p (nil: no principal).
func (r *rig) post(p *auth.Principal, body string) reply {
	r.t.Helper()
	return r.postWith(p, "application/json", body)
}

func (r *rig) postWith(p *auth.Principal, contentType, body string) reply {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/core/v1/system/factory-reset", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "batcomputer/1.0")
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	rec := httptest.NewRecorder()
	r.api.factoryReset(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return reply{status: res.StatusCode, contentType: res.Header.Get("Content-Type"), body: string(b)}
}

func problemOf(t *testing.T, rep reply) problemBody {
	t.Helper()
	var pb problemBody
	if err := json.Unmarshal([]byte(rep.body), &pb); err != nil {
		t.Fatalf("body is not a problem: %v: %s", err, rep.body)
	}
	return pb
}

// untouched fails t when the stager or the audit log was reached.
func (r *rig) untouched(what string) {
	r.t.Helper()
	if r.st.calls != 0 || len(r.rec.records) != 0 {
		r.t.Errorf("%s: %d StageReset calls, %d audit records, want none", what, r.st.calls, len(r.rec.records))
	}
}

// ---- the request flows unchanged ------------------------------------------

// TestResetRequestFlowsUnchanged (route half; R1-25 owns the offline half):
// the one storage.ResetRequest reaches Stager.StageReset exactly as the body
// carried it, with the requester's user ID, and kind alone is defaulted.
func TestResetRequestFlowsUnchanged(t *testing.T) {
	cases := []struct {
		name, body string
		want       storage.ResetRequest
	}{
		{"every member", fmt.Sprintf(`{"kind":"factory","confirm":"RESET","delete_library_files":true,"library_root":%q,"library_file_count":12}`, batcaveRoot),
			storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: "RESET", DeleteLibraryFiles: true, LibraryRoot: batcaveRoot, LibraryFileCount: 12}},
		{"kind omitted defaults to factory", `{"confirm":"RESET"}`,
			storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: "RESET"}},
		{"store", `{"kind":"store","confirm":"RESET"}`,
			storage.ResetRequest{Kind: restorejournal.ResetStore, Confirm: "RESET"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.st.res = storage.ResetStaged{ID: "rst-arkham"}
			rep := r.post(owner(), c.body)
			if rep.status != http.StatusOK {
				t.Fatalf("status = %d: %s", rep.status, rep.body)
			}
			if r.st.calls != 1 || r.st.req != c.want || r.st.by != bruceID {
				t.Errorf("stager saw %d calls, by %q, %+v; want 1, %q, %+v", r.st.calls, r.st.by, r.st.req, bruceID, c.want)
			}
		})
	}
}

// TestFactoryResetNativeRouteKeepsLibraryFilesByDefault: without
// delete_library_files the request asks for no wipe and the answer names no
// root or count; with it, a stale or missing root or count is a 422
// library_files_confirm_required carrying the current values (and nothing
// staged, which is the Stager's refusal), matching values are a 200 naming
// them, kind store with the flag is a 422, and a missing or wrong confirm is a
// 422 that never reaches the Stager or the audit log (A-17, R9-6).
func TestFactoryResetNativeRouteKeepsLibraryFilesByDefault(t *testing.T) {
	t.Run("default asks for no wipe", func(t *testing.T) {
		r := newRig(t)
		// Even a Stager that reports a root answers none without the flag.
		r.st.res = storage.ResetStaged{ID: "rst-arkham", LibraryRoot: batcaveRoot, LibraryFileCount: 9}
		rep := r.post(owner(), `{"confirm":"RESET"}`)
		if rep.status != http.StatusOK || rep.contentType != "application/json" {
			t.Fatalf("reply = %d %s: %s", rep.status, rep.contentType, rep.body)
		}
		if r.st.req.DeleteLibraryFiles || r.st.req.LibraryRoot != "" || r.st.req.LibraryFileCount != 0 {
			t.Errorf("request = %+v, want no library wipe", r.st.req)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(rep.body), &got); err != nil {
			t.Fatal(err)
		}
		if got["reset_id"] != "rst-arkham" || got["message"] != resetMessage || len(got) != 2 {
			t.Errorf("body = %v, want only reset_id and message", got)
		}
		if d := r.rec.records[0].Details; d["delete_library_files"] != false || d["library_root"] != nil {
			t.Errorf("audit details = %v", d)
		}
	})

	t.Run("a stale confirmation is 422 with the current values", func(t *testing.T) {
		for _, files := range []int{7, 0} {
			r := newRig(t)
			r.st.err = fmt.Errorf("preflight: %w", &storage.LibraryConfirmError{Root: batcaveRoot, Files: files})
			rep := r.post(owner(), `{"confirm":"RESET","delete_library_files":true,"library_root":"/batcave/old","library_file_count":3}`)
			if rep.status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d: %s", rep.status, rep.body)
			}
			pb := problemOf(t, rep)
			if pb.Code != "library_files_confirm_required" || pb.LibraryRoot != batcaveRoot || pb.LibraryFileCount == nil || *pb.LibraryFileCount != files {
				t.Errorf("problem = %+v, want root %s and count %d", pb, batcaveRoot, files)
			}
			if !strings.Contains(rep.body, fmt.Sprintf(`"library_file_count":%d`, files)) {
				t.Errorf("count %d is not in the body: %s", files, rep.body)
			}
			if len(pb.Errors) != 2 || pb.Type != problemTypeBase+"library_files_confirm_required" {
				t.Errorf("problem = %+v", pb)
			}
		}
	})

	t.Run("matching values are 200 naming the root and count", func(t *testing.T) {
		r := newRig(t)
		r.st.res = storage.ResetStaged{ID: "rst-arkham", LibraryRoot: batcaveRoot, LibraryFileCount: 12}
		rep := r.post(owner(), fmt.Sprintf(`{"confirm":"RESET","delete_library_files":true,"library_root":%q,"library_file_count":12}`, batcaveRoot))
		var got FactoryResetResponse
		if err := json.Unmarshal([]byte(rep.body), &got); err != nil {
			t.Fatal(err)
		}
		if rep.status != http.StatusOK || got.ResetID != "rst-arkham" || got.LibraryRoot != batcaveRoot || got.LibraryFileCount == nil || *got.LibraryFileCount != 12 {
			t.Errorf("reply = %d %s", rep.status, rep.body)
		}
	})

	t.Run("a zero count is still named", func(t *testing.T) {
		r := newRig(t)
		r.st.res = storage.ResetStaged{ID: "rst-arkham", LibraryRoot: batcaveRoot}
		rep := r.post(owner(), fmt.Sprintf(`{"confirm":"RESET","delete_library_files":true,"library_root":%q}`, batcaveRoot))
		if rep.status != http.StatusOK || !strings.Contains(rep.body, `"library_file_count":0`) {
			t.Errorf("reply = %d %s", rep.status, rep.body)
		}
	})

	t.Run("kind store with the flag is 422 and stages nothing", func(t *testing.T) {
		r := newRig(t)
		rep := r.post(owner(), `{"kind":"store","confirm":"RESET","delete_library_files":true}`)
		if rep.status != http.StatusUnprocessableEntity || problemOf(t, rep).Code != "reset_request_invalid" {
			t.Errorf("reply = %d %s", rep.status, rep.body)
		}
		r.untouched("kind store with the flag")
	})

	t.Run("an unknown kind is 422 and quoted nowhere", func(t *testing.T) {
		r := newRig(t)
		rep := r.post(owner(), `{"kind":"gotham-wipe","confirm":"RESET"}`)
		if rep.status != http.StatusUnprocessableEntity || strings.Contains(rep.body, "gotham-wipe") {
			t.Errorf("reply = %d %s", rep.status, rep.body)
		}
		r.untouched("an unknown kind")
	})

	for _, body := range []string{`{}`, `{"confirm":""}`, `{"confirm":"reset"}`, `{"kind":"store","confirm":"yes"}`} {
		t.Run("confirm_required "+body, func(t *testing.T) {
			r := newRig(t)
			rep := r.post(owner(), body)
			pb := problemOf(t, rep)
			if rep.status != http.StatusUnprocessableEntity || pb.Code != "confirm_required" || len(pb.Errors) != 1 || pb.Errors[0].Field != "/body/confirm" {
				t.Errorf("reply = %d %s", rep.status, rep.body)
			}
			r.untouched("a missing or wrong confirm")
		})
	}
}

// TestFactoryResetCoreRouteRecordsHookArgs (route half; the intent's
// hook_args and hook_paths are the kernel's StageReset, which receives the one
// request): the Stager is called once with the request, the requester and a
// context carrying the audit actor, after the audit record of the request's
// kind, flag, root and count, which never holds the confirm token.
func TestFactoryResetCoreRouteRecordsHookArgs(t *testing.T) {
	r := newRig(t)
	r.st.res = storage.ResetStaged{ID: "rst-arkham", LibraryRoot: batcaveRoot, LibraryFileCount: 4}
	rep := r.post(owner(), fmt.Sprintf(`{"kind":"factory","confirm":"RESET","delete_library_files":true,"library_root":%q,"library_file_count":4}`, batcaveRoot))
	if rep.status != http.StatusOK {
		t.Fatalf("status = %d: %s", rep.status, rep.body)
	}
	if got := r.st.ev.get(); !slices.Equal(got, []string{"record", "stage"}) {
		t.Errorf("call order = %v, want the audit record first", got)
	}
	if len(r.rec.records) != 1 {
		t.Fatalf("%d audit records, want 1", len(r.rec.records))
	}
	e := r.rec.records[0]
	if e.Action != "core.system.factory_reset" || e.Outcome != audit.Success || e.Route != "POST /api/core/v1/system/factory-reset" {
		t.Errorf("event = %+v", e)
	}
	wantDetails := map[string]any{"kind": "factory", "delete_library_files": true, "library_root": batcaveRoot, "library_file_count": 4}
	if fmt.Sprint(e.Details) != fmt.Sprint(wantDetails) {
		t.Errorf("details = %v, want %v", e.Details, wantDetails)
	}
	if e.Actor.UserID != bruceID || e.Actor.Username != "bruce.wayne" || e.Actor.Method != string(auth.MethodSession) ||
		e.Actor.CredentialID != "cred-gotham" || e.Actor.ClientAddr != "192.0.2.7" || e.Actor.UserAgent != "batcomputer/1.0" {
		t.Errorf("actor = %+v", e.Actor)
	}
	if b, _ := json.Marshal(e); strings.Contains(string(b), "RESET") && strings.Contains(string(b), `"confirm"`) {
		t.Errorf("the confirm token is recorded: %s", b)
	}
	if a, ok := audit.ActorFrom(r.st.ctx); !ok || a.UserID != bruceID {
		t.Errorf("the Stager's context carries actor %+v (bound %v)", a, ok)
	}
}

// ---- guards ----------------------------------------------------------------

// TestFactoryReset_GuardsRefuseBeforeAnythingIsRead: the handler repeats the
// route's permission and guards; every refusal answers its status without
// reading the body, staging or recording.
func TestFactoryReset_GuardsRefuseBeforeAnythingIsRead(t *testing.T) {
	admin := func(m auth.Method) *auth.Principal { return as(m, rbac.CoreAdmin) }
	cases := []struct {
		name         string
		p            *auth.Principal
		ownerNeeded  func() bool
		status       int
		code         string
		wantRequired string
	}{
		{"no principal", nil, nil, 401, "authentication_required", ""},
		{"auth disabled", as(auth.MethodAuthDisabled, rbac.CoreAdmin), nil, 401, "authentication_required", ""},
		{"no user id", &auth.Principal{Method: auth.MethodSession, User: &auth.User{}, Perms: checker{rbac.CoreAdmin}}, nil, 401, "authentication_required", ""},
		{"no checker", &auth.Principal{Method: auth.MethodSession, User: &auth.User{ID: bruceID}}, nil, 403, "permission_denied", string(rbac.CoreAdmin)},
		{"not an admin", as(auth.MethodSession, "core:settings.manage"), nil, 403, "permission_denied", string(rbac.CoreAdmin)},
		{"api key", admin(auth.MethodAPIKey), func() bool { return false }, 403, "interactive_required", ""},
		{"admin without the owner", admin(auth.MethodSession), nil, 403, "owner_required", ""},
		{"owner required explicitly", admin(auth.MethodSession), func() bool { return true }, 403, "owner_required", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.api.ownerRequired = c.ownerNeeded
			rep := r.post(c.p, `not even json`)
			pb := problemOf(t, rep)
			if rep.status != c.status || pb.Code != c.code || pb.Required != c.wantRequired {
				t.Errorf("reply = %d %s, want %d %s (required %q)", rep.status, rep.body, c.status, c.code, c.wantRequired)
			}
			r.untouched(c.name)
		})
	}
}

// TestFactoryReset_OwnerGateFallsBackOnlyWhenNotRequired: with
// core.auth.owner.required false (no OwnerVerifier or no owner set, SEC4-14)
// an interactive core:admin session stages the reset; while it is true only a
// principal holding the owner assurance does.
func TestFactoryReset_OwnerGateFallsBackOnlyWhenNotRequired(t *testing.T) {
	body := `{"confirm":"RESET"}`
	r := newRig(t)
	r.api.ownerRequired = func() bool { return false }
	if rep := r.post(as(auth.MethodSession, rbac.CoreAdmin), body); rep.status != http.StatusOK {
		t.Errorf("owner not required: %d %s", rep.status, rep.body)
	}
	if rep := r.post(as(auth.MethodAPIKey, rbac.CoreAdmin), body); rep.status != http.StatusForbidden || problemOf(t, rep).Code != "interactive_required" {
		t.Errorf("an API key with the owner gate off: %d %s, want 403 interactive_required", rep.status, rep.body)
	}
	r.api.ownerRequired = func() bool { return true }
	if rep := r.post(owner(), body); rep.status != http.StatusOK {
		t.Errorf("owner required, owner held: %d %s", rep.status, rep.body)
	}
	delegated := owner()
	delegated.Method = auth.MethodSessionDelegated
	if rep := r.post(delegated, body); rep.status != http.StatusOK {
		t.Errorf("a delegated session holding the owner assurance: %d %s", rep.status, rep.body)
	}
}

// TestFactoryReset_FailsClosedWhenMountedWithoutItsParts: a missing Stager or
// audit log answers 503 after the caller is authorised, and stages nothing.
func TestFactoryReset_FailsClosedWhenMountedWithoutItsParts(t *testing.T) {
	r := newRig(t)
	r.api.audit = nil
	if rep := r.post(owner(), `{"confirm":"RESET"}`); rep.status != http.StatusServiceUnavailable {
		t.Errorf("no audit log: %d %s", rep.status, rep.body)
	}
	if r.st.calls != 0 {
		t.Error("a reset was staged with no audit log")
	}
	r = newRig(t)
	r.api.stager = nil
	if rep := r.post(owner(), `{"confirm":"RESET"}`); rep.status != http.StatusServiceUnavailable || len(r.rec.records) != 0 {
		t.Errorf("no stager: %d %s, %d records", rep.status, rep.body, len(r.rec.records))
	}
	// An unauthorised caller learns nothing about availability.
	r.api.audit = nil
	if rep := r.post(as(auth.MethodSession), `{"confirm":"RESET"}`); rep.status != http.StatusForbidden {
		t.Errorf("unauthorised caller with nothing mounted: %d %s, want 403", rep.status, rep.body)
	}
}

// ---- audit -----------------------------------------------------------------

// TestFactoryReset_AuditIsFailClosed: a failed audit write answers 503 with the
// Stager never called and the write's text withheld; a reset recorded and then
// refused or failed at staging gets a second record naming the failure.
func TestFactoryReset_AuditIsFailClosed(t *testing.T) {
	r := newRig(t)
	r.rec.failOn = 1
	rep := r.post(owner(), `{"confirm":"RESET"}`)
	if rep.status != http.StatusServiceUnavailable || r.st.calls != 0 || strings.Contains(rep.body, "disk is full") {
		t.Errorf("reply = %d %s, %d StageReset calls", rep.status, rep.body, r.st.calls)
	}
	if !strings.Contains(r.logs.String(), "disk is full") {
		t.Error("the audit failure is not in the operator log")
	}

	cases := []struct {
		name    string
		err     error
		status  int
		code    string
		outcome audit.Outcome
		reason  string
	}{
		{"refused by the app", fmt.Errorf("%w: RootDir /batcave/root is dangerous", storage.ErrResetRefused), 400, "reset_refused", audit.Failure, "staging_refused"},
		{"swap pending", fmt.Errorf("%w (in flight: 01HX)", storage.ErrSwapPending), 409, "swap_pending", audit.Failure, "staging_refused"},
		{"rename refused", fmt.Errorf("%w: /batcave/aux", storage.ErrRenameRefused), 409, "rename_refused", audit.Failure, "staging_refused"},
		{"request invalid from the kernel", fmt.Errorf("%w: no reset hook", storage.ErrResetRequestInvalid), 422, "reset_request_invalid", audit.Failure, "staging_refused"},
		{"confirm from the kernel", storage.ErrResetConfirmRequired, 422, "confirm_required", audit.Failure, "staging_refused"},
		{"bare library confirmation", fmt.Errorf("%w", storage.ErrLibraryConfirmRequired), 422, "library_files_confirm_required", audit.Failure, "staging_refused"},
		{"storage failure", errors.New("fsync /batcave/state: disk on fire"), 500, "internal", audit.Error, "staging_failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.st.err = c.err
			rep := r.post(owner(), `{"confirm":"RESET"}`)
			if rep.status != c.status || problemOf(t, rep).Code != c.code {
				t.Fatalf("reply = %d %s, want %d %s", rep.status, rep.body, c.status, c.code)
			}
			for _, leak := range []string{"/batcave", "dangerous", "on fire", "01HX"} {
				if strings.Contains(rep.body, leak) {
					t.Errorf("the body leaks %q: %s", leak, rep.body)
				}
			}
			if len(r.rec.records) != 2 || r.rec.records[0].Outcome != audit.Success ||
				r.rec.records[1].Outcome != c.outcome || r.rec.records[1].Reason != c.reason || r.rec.records[1].Action != actionFactoryReset {
				t.Errorf("records = %+v, want a request record then a %s %s record", r.rec.records, c.outcome, c.reason)
			}
		})
	}

	// A failure record that cannot be written changes nothing for the caller.
	r = newRig(t)
	r.st.err = storage.ErrSwapPending
	r.rec.failOn = 2
	if rep := r.post(owner(), `{"confirm":"RESET"}`); rep.status != http.StatusConflict {
		t.Errorf("reply = %d %s, want the staging refusal", rep.status, rep.body)
	}
	if !strings.Contains(r.logs.String(), "audit failure record") {
		t.Error("the lost failure record is not logged")
	}
}

// ---- request decoding ------------------------------------------------------

// TestFactoryReset_RequestDecodingIsStrict: the body is application/json,
// bounded, an object of the five members with typed values; every refusal is a
// fixed message that quotes nothing and reaches neither the Stager nor the
// audit log.
func TestFactoryReset_RequestDecodingIsStrict(t *testing.T) {
	cases := []struct {
		name, contentType, body string
		status                  int
		code                    string
	}{
		{"no content type", "", `{"confirm":"RESET"}`, 415, "unsupported_media_type"},
		{"text", "text/plain", `{"confirm":"RESET"}`, 415, "unsupported_media_type"},
		{"too large", "application/json", `{"confirm":"` + strings.Repeat("R", maxBody) + `"}`, 413, "body_too_large"},
		{"not an object", "application/json", `[1]`, 400, "malformed_json"},
		{"empty", "application/json", ``, 400, "malformed_json"},
		{"trailing data", "application/json", `{"confirm":"RESET"} {}`, 400, "malformed_json"},
		{"unknown member", "application/json", `{"confirm":"RESET","wipe_everything":true}`, 400, "unknown_field"},
		{"confirm of the wrong type", "application/json", `{"confirm":1}`, 422, "validation_failed"},
		{"flag of the wrong type", "application/json", `{"confirm":"RESET","delete_library_files":"yes"}`, 422, "validation_failed"},
		{"count of the wrong type", "application/json", `{"confirm":"RESET","library_file_count":"many"}`, 422, "validation_failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			rep := r.postWith(owner(), c.contentType, c.body)
			if rep.status != c.status || problemOf(t, rep).Code != c.code {
				t.Errorf("reply = %d %.200s, want %d %s", rep.status, rep.body, c.status, c.code)
			}
			for _, leak := range []string{"wipe_everything", "many", "yes"} {
				if strings.Contains(rep.body, leak) {
					t.Errorf("the body quotes %q: %.200s", leak, rep.body)
				}
			}
			r.untouched(c.name)
		})
	}

	r := newRig(t)
	if rep := r.postWith(owner(), "application/json; charset=utf-8", `{"confirm":"RESET"}`); rep.status != http.StatusOK {
		t.Errorf("a charset parameter: %d %s", rep.status, rep.body)
	}
	if got := userAgent(httptest.NewRequest(http.MethodPost, "/", nil)); got != "" {
		t.Errorf("no agent = %q", got)
	}
	long := httptest.NewRequest(http.MethodPost, "/", nil)
	long.Header.Set("User-Agent", strings.Repeat("é", 200))
	if got := userAgent(long); len(got) > 256 || len(got) < 254 || strings.ToValidUTF8(got, "?") != got {
		t.Errorf("long agent: len %d", len(got))
	}
}

// ---- the route table -------------------------------------------------------

func registered(t *testing.T) *httpapi.Router {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	Register(r.CoreGroup(), Deps{})
	return r
}

// TestRegister_RouteTable pins the one route: method, path, name, core:admin,
// Guards.Interactive and Guards.Owner, the default rate class and a raw kind
// (it writes its own problem members). A dropped guard fails this test.
func TestRegister_RouteTable(t *testing.T) {
	var got []httpapi.RouteInfo
	for ri := range registered(t).Routes() {
		got = append(got, ri)
	}
	if len(got) != 1 {
		t.Fatalf("%d routes, want 1: %+v", len(got), got)
	}
	ri := got[0]
	if ri.Owner != "core" || ri.Kind != httpapi.RouteKindRaw || ri.Method != http.MethodPost || ri.Pattern != "/api/core/v1/system/factory-reset" || ri.Name != "factoryReset" {
		t.Errorf("route = %s %s %s %s (kind %d)", ri.Owner, ri.Method, ri.Pattern, ri.Name, ri.Kind)
	}
	if ri.Perm.IsZero() || ri.Perm.String() != rbac.Need(rbac.CoreAdmin).String() {
		t.Errorf("requires %s, want %s", ri.Perm, rbac.Need(rbac.CoreAdmin))
	}
	if !ri.Guards.Interactive || !ri.Guards.Owner {
		t.Errorf("guards = %+v, want Interactive and Owner", ri.Guards)
	}
	if ri.RateClass != httpapi.RateDefault {
		t.Errorf("rate class = %d, want default", ri.RateClass)
	}
}

// TestRegister_BuildFailsOnlyForWantOfAuth: until F4-08 a router refuses every
// non-Public native route with ErrNoAuthService; a bad binding, a body httpapi
// cannot describe, a non-snake_case name or a duplicate would be a further
// error.
func TestRegister_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	_, err := registered(t).Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Build error %T does not join its problems", err)
	}
	problems := joined.Unwrap()
	if len(problems) != 1 {
		t.Errorf("Build reported %d problems, want 1 (ErrNoAuthService): %v", len(problems), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}
