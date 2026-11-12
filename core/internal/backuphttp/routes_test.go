// file: core/internal/backuphttp/routes_test.go
// version: 1.0.2
// guid: 71e57c75-3bcb-4631-a4cb-85a8d13a48d1
// last-edited: 2026-11-12

package backuphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// ---- doubles ---------------------------------------------------------------

const (
	bhBruceID = "01GOTHAMBRUCEWAYNE0000000"
	// bhArkham is a well-formed archive ID (a ULID shape).
	bhArkham backup.ArchiveID = "01ARKHAMASYLUM0000000000AA"
	bhBase                    = "/api/core/v1"
)

// bhChecker is an rbac.Checker holding exactly its permissions; core:admin
// holds every one, as the real checker's does for an interactive principal.
type bhChecker []rbac.Permission

func (c bhChecker) Has(p rbac.Permission) bool {
	return slices.Contains(c, p) || slices.Contains(c, rbac.CoreAdmin)
}

func (c bhChecker) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindPublic, rbac.KindAuthenticated:
		return true
	case rbac.KindAllOf:
		return len(ps) > 0 && !slices.ContainsFunc(ps, func(p rbac.Permission) bool { return !c.Has(p) })
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, c.Has)
	}
	return false
}
func (bhChecker) Confined() bool                { return false }
func (c bhChecker) Concrete() []rbac.Permission { return slices.Clone(c) }

// bhAs is a principal authenticated by m holding perms.
func bhAs(m auth.Method, perms ...rbac.Permission) *auth.Principal {
	p := &auth.Principal{Method: m, Perms: bhChecker(perms), CredentialID: "cred-gotham", ClientAddr: netip.MustParseAddr("192.0.2.7")}
	if m != auth.MethodAuthDisabled {
		p.User = &auth.User{ID: bruceID(), Username: "bruce.wayne"}
	}
	return p
}

func bruceID() auth.UserID { return auth.UserID(bhBruceID) }

// bhOwner is an interactive session with the owner assurance holding perms.
func bhOwner(perms ...rbac.Permission) *auth.Principal {
	p := bhAs(auth.MethodSession, perms...)
	p.Assurance = auth.AssuranceOwner
	return p
}

// bhEvents is the shared order of calls the doubles saw.
type bhEvents struct {
	mu  sync.Mutex
	seq []string
}

func (e *bhEvents) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq = append(e.seq, s)
}

func (e *bhEvents) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.seq)
}

func (e *bhEvents) count(s string) int {
	n := 0
	for _, x := range e.get() {
		if x == s {
			n++
		}
	}
	return n
}

type bhCat struct {
	ev           *bhEvents
	archives     []backup.Archive
	listErr      error
	verifyRes    backup.VerifyResult
	verifyErr    error
	lastLevel    backup.VerifyLevel
	lastVerifyID backup.ArchiveID
	importErr    error
	importName   string
	importData   []byte
	importRes    backup.Archive
	downloadBody string
	downloadErr  error
	closed       bool
	deleteErr    error
}

func (c *bhCat) List(context.Context) ([]backup.Archive, error) {
	c.ev.add("list")
	return slices.Clone(c.archives), c.listErr
}

func (c *bhCat) Verify(_ context.Context, id backup.ArchiveID, l backup.VerifyLevel) (backup.VerifyResult, error) {
	c.ev.add("verify")
	c.lastLevel, c.lastVerifyID = l, id
	return c.verifyRes, c.verifyErr
}

func (c *bhCat) Import(_ context.Context, name string, r io.Reader) (backup.Archive, error) {
	c.ev.add("import")
	c.importName = name
	data, err := io.ReadAll(r)
	c.importData = data
	if err != nil {
		return backup.Archive{}, fmt.Errorf("import %s: %w", name, err)
	}
	return c.importRes, c.importErr
}

type bhBody struct {
	io.Reader
	c *bhCat
}

func (b bhBody) Close() error { b.c.closed = true; return nil }

func (c *bhCat) Download(context.Context, backup.ArchiveID) (io.ReadCloser, error) {
	c.ev.add("download")
	if c.downloadErr != nil {
		return nil, c.downloadErr
	}
	return bhBody{Reader: strings.NewReader(c.downloadBody), c: c}, nil
}

func (c *bhCat) Delete(context.Context, backup.ArchiveID) error {
	c.ev.add("delete")
	return c.deleteErr
}

type bhRst struct {
	ev                      *bhEvents
	stageRes                backup.PendingRestore
	stageErr                error
	stageID                 backup.ArchiveID
	stageOpts               backup.RestoreOptions
	revertOpts              backup.RestoreOptions
	stageProof, revertProof backup.RestoreProof
	revertErr               error
	cancelErr               error
	pend                    *backup.PendingRestore
	pendErr                 error
}

func (s *bhRst) StageRestore(ctx context.Context, id backup.ArchiveID, o backup.RestoreOptions) (backup.PendingRestore, error) {
	s.ev.add("stage")
	s.stageID, s.stageOpts, s.stageProof = id, o, backup.RestoreProofFrom(ctx)
	return s.stageRes, s.stageErr
}

func (s *bhRst) CancelRestore(context.Context) error { s.ev.add("cancel"); return s.cancelErr }

func (s *bhRst) Pending(context.Context) (*backup.PendingRestore, error) {
	s.ev.add("pending")
	return s.pend, s.pendErr
}

func (s *bhRst) RevertLastRestore(ctx context.Context, o backup.RestoreOptions) (backup.PendingRestore, error) {
	s.ev.add("revert")
	s.revertOpts, s.revertProof = o, backup.RestoreProofFrom(ctx)
	return s.stageRes, s.revertErr
}

type bhRw struct {
	ev  *bhEvents
	res backup.Archive
	err error
}

func (w *bhRw) Rewrap(context.Context, backup.ArchiveID) (backup.Archive, error) {
	w.ev.add("rewrap")
	return w.res, w.err
}

type bhEnq struct {
	ev    *bhEvents
	opts  backup.CreateOptions
	by    string
	id    string
	err   error
	calls int
}

func (e *bhEnq) EnqueueCreate(_ context.Context, o backup.CreateOptions, by string) (string, error) {
	e.ev.add("enqueue")
	e.calls++
	e.opts, e.by = o, by
	return e.id, e.err
}

type bhRec struct {
	ev      *bhEvents
	records []audit.Event
	failOn  int // the 1-based call that fails; 0 = none
}

func (r *bhRec) Record(_ context.Context, e audit.Event) error {
	r.ev.add("record")
	r.records = append(r.records, e)
	if r.failOn == len(r.records) {
		return errors.New("audit disk is full")
	}
	return nil
}

// bhRig is the routes over the doubles, mounted from the real route table.
type bhRig struct {
	t    *testing.T
	ev   *bhEvents
	cat  *bhCat
	rst  *bhRst
	rw   *bhRw
	enq  *bhEnq
	rec  *bhRec
	logs *bytes.Buffer
	mux  *http.ServeMux
	// ownerRequired is what core.auth.owner.required reads; nil means required.
	ownerRequired func() bool
}

func newBHRig(t *testing.T) *bhRig {
	t.Helper()
	if !backup.ValidArchiveID(string(bhArkham)) {
		t.Fatalf("fixture ID %q is not a valid archive ID", bhArkham)
	}
	ev := &bhEvents{}
	arkham := backup.Archive{ID: bhArkham, Name: "arkham-asylum.tar.zst", Size: 42, CreatedAt: time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC),
		Format: backup.FormatMediacore, Provenance: backup.Own, Manifest: &backup.Manifest{Reason: "manual", AppVersion: "1.0.0", CoreVersion: "2.0.0"}}
	r := &bhRig{t: t, ev: ev, logs: &bytes.Buffer{}}
	r.cat = &bhCat{ev: ev, archives: []backup.Archive{arkham}, importRes: arkham, downloadBody: "The Killing Joke",
		verifyRes: backup.VerifyResult{At: time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC), Level: backup.VerifyEntries, OK: true}}
	r.rst = &bhRst{ev: ev, stageRes: backup.PendingRestore{ArchiveID: bhArkham, Kind: journal.KindArchive, RequestedBy: bhBruceID,
		StagedAt: time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC), Warnings: []string{"module tv is dormant"}}}
	r.rw = &bhRw{ev: ev, res: arkham}
	r.enq = &bhEnq{ev: ev, id: "task-the-long-halloween"}
	r.rec = &bhRec{ev: ev}
	r.mount(Deps{Catalogue: r.cat, Restorer: r.rst, Rewrapper: r.rw, Enqueuer: r.enq, Audit: r.rec})
	return r
}

// mount replaces the routes with ones over d (its Log and OwnerRequired are
// the rig's).
func (r *bhRig) mount(d Deps) {
	d.Log = slog.New(slog.NewTextHandler(r.logs, nil))
	d.OwnerRequired = func() bool { return r.ownerRequired == nil || r.ownerRequired() }
	r.mux = http.NewServeMux()
	for _, def := range newAPI(d).routeTable() {
		r.mux.Handle(def.method+" "+bhBase+def.path, def.handler)
	}
}

type bhReply struct {
	status int
	header http.Header
	body   string
}

func (r *bhRig) do(p *auth.Principal, method, target, contentType, body string, tweak ...func(*http.Request)) bhReply {
	r.t.Helper()
	req := httptest.NewRequest(method, bhBase+target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "batcomputer/1.0")
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	for _, f := range tweak {
		f(req)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return bhReply{status: res.StatusCode, header: res.Header, body: string(b)}
}

func bhProblem(t *testing.T, rep bhReply) httpapi.Problem {
	t.Helper()
	var pb httpapi.Problem
	if err := json.Unmarshal([]byte(rep.body), &pb); err != nil {
		t.Fatalf("body is not a problem: %v: %s", err, rep.body)
	}
	return pb
}

// untouched fails t when a service, the enqueuer or the audit log was reached.
func (r *bhRig) untouched(what string) {
	r.t.Helper()
	if got := r.ev.get(); len(got) != 0 {
		r.t.Errorf("%s: calls %v, want none", what, got)
	}
}

// ---- the route table under test -------------------------------------------

type bhCase struct {
	name        string
	method      string
	target      string
	ctype, body string
	perm        rbac.Permission
	interactive bool
	owner       bool
	action      string // the audit action written, "" for none
	ok          int
}

func bhCases() []bhCase {
	id := string(bhArkham)
	return []bhCase{
		{name: "list", method: "GET", target: "/backups", perm: permSystemView, ok: 200},
		{name: "get", method: "GET", target: "/backups/" + id, perm: permSystemView, ok: 200},
		{name: "verify", method: "POST", target: "/backups/" + id + "/verify", perm: permSystemManage, ok: 200},
		{name: "create", method: "POST", target: "/backups", ctype: "application/json", body: `{"reason":"before the Joker"}`, perm: permSystemManage, action: actionCreate, ok: 202},
		{name: "delete", method: "DELETE", target: "/backups/" + id, perm: permSystemManage, interactive: true, ok: 204},
		{name: "import", method: "POST", target: "/backups/import?name=gotham.tar.zst", ctype: "application/octet-stream", body: "Year One", perm: permSystemManage, interactive: true, action: actionImport, ok: 201},
		{name: "download", method: "GET", target: "/backups/" + id + "/download", perm: rbac.CoreAdmin, interactive: true, owner: true, action: actionDownload, ok: 200},
		{name: "restore", method: "POST", target: "/backups/" + id + "/restore", perm: permBackupRestore, interactive: true, owner: true, ok: 202},
		{name: "revert", method: "POST", target: "/restore/revert", perm: permBackupRestore, interactive: true, owner: true, ok: 202},
		{name: "cancel", method: "DELETE", target: "/restore/pending", perm: permBackupRestore, interactive: true, owner: true, ok: 204},
		{name: "pending", method: "GET", target: "/restore/pending", perm: permBackupRestore, ok: 204},
		{name: "rewrap", method: "POST", target: "/backups/" + id + "/rewrap", perm: permSystemManage, interactive: true, ok: 200},
	}
}

// grant is the principal that holds exactly what c needs.
func (c bhCase) grant() *auth.Principal {
	if c.owner {
		return bhOwner(c.perm)
	}
	if c.interactive {
		return bhAs(auth.MethodSession, c.perm)
	}
	return bhAs(auth.MethodAPIKey, c.perm)
}

func (r *bhRig) run(p *auth.Principal, c bhCase) bhReply {
	r.t.Helper()
	return r.do(p, c.method, c.target, c.ctype, c.body)
}

// TestBackupRoutesPermissions drives every route of 05-backup §5B.3.8 through
// its permission, each guard, the owner fallback and the audit order.
func TestBackupRoutesPermissions(t *testing.T) {
	for _, c := range bhCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Run("no principal", func(t *testing.T) {
				r := newBHRig(t)
				if rep := r.run(nil, c); rep.status != 401 {
					t.Errorf("status = %d, want 401: %s", rep.status, rep.body)
				}
				r.untouched("no principal")
			})
			t.Run("lacking the permission", func(t *testing.T) {
				r := newBHRig(t)
				other := bhOwner("core:users.view")
				rep := r.run(other, c)
				if rep.status != 403 || bhProblem(t, rep).Code != "permission_denied" {
					t.Errorf("= %d %s, want 403 permission_denied", rep.status, rep.body)
				}
				r.untouched("lacking the permission")
			})
			if c.interactive {
				t.Run("non-interactive credential", func(t *testing.T) {
					r := newBHRig(t)
					p := bhAs(auth.MethodAPIKey, c.perm)
					p.Assurance = auth.AssuranceOwner
					rep := r.run(p, c)
					if rep.status != 403 || bhProblem(t, rep).Code != "interactive_required" {
						t.Errorf("= %d %s, want 403 interactive_required", rep.status, rep.body)
					}
					r.untouched("non-interactive")
				})
			}
			if c.owner {
				t.Run("owner assurance missing", func(t *testing.T) {
					r := newBHRig(t)
					rep := r.run(bhAs(auth.MethodSession, c.perm), c)
					if rep.status != 403 || bhProblem(t, rep).Code != "owner_required" {
						t.Errorf("= %d %s, want 403 owner_required", rep.status, rep.body)
					}
					r.untouched("owner missing")
				})
				t.Run("owner not required falls back to interactive plus core:admin", func(t *testing.T) {
					r := newBHRig(t)
					r.ownerRequired = func() bool { return false }
					// Interactive, holds the route's scope, no admin, no assurance.
					lacking := &auth.Principal{Method: auth.MethodSession, Perms: scopedOnly{perm: c.perm}, User: &auth.User{ID: bruceID(), Username: "bruce.wayne"}}
					if c.perm != rbac.CoreAdmin {
						rep := r.run(lacking, c)
						if rep.status != 403 || bhProblem(t, rep).Code != "permission_denied" || bhProblem(t, rep).Required != string(rbac.CoreAdmin) {
							t.Errorf("without admin = %d %s, want 403 permission_denied naming core:admin", rep.status, rep.body)
						}
						r.untouched("fallback without admin")
					}
					admin := bhAs(auth.MethodSession, rbac.CoreAdmin)
					if rep := r.run(admin, c); rep.status != c.ok {
						t.Errorf("admin without assurance = %d %s, want %d", rep.status, rep.body, c.ok)
					}
					api := bhAs(auth.MethodAPIKey, rbac.CoreAdmin)
					r2 := newBHRig(t)
					r2.ownerRequired = func() bool { return false }
					if rep := r2.run(api, c); rep.status != 403 || bhProblem(t, rep).Code != "interactive_required" {
						t.Errorf("admin key = %d %s, want 403 interactive_required", rep.status, rep.body)
					}
				})
			}
			t.Run("granted", func(t *testing.T) {
				r := newBHRig(t)
				rep := r.run(c.grant(), c)
				if rep.status != c.ok {
					t.Fatalf("status = %d, want %d: %s", rep.status, c.ok, rep.body)
				}
				if c.action == "" {
					if len(r.rec.records) != 0 {
						t.Errorf("%d audit records, want none", len(r.rec.records))
					}
					return
				}
				if len(r.rec.records) != 1 || r.rec.records[0].Action != c.action || r.rec.records[0].Outcome != audit.Success {
					t.Fatalf("records = %+v, want exactly one %s success", r.rec.records, c.action)
				}
				if seq := r.ev.get(); seq[0] != "record" || r.ev.count("record") != 1 {
					t.Errorf("events = %v, want the one record before any service call", seq)
				}
				if got := r.rec.records[0].Actor.UserID; got != bhBruceID {
					t.Errorf("actor = %q, want the user's ID", got)
				}
			})
		})
	}
}

// scopedOnly holds exactly one scope, with no admin, as the real checker
// would for a role that grants it.
type scopedOnly struct{ perm rbac.Permission }

func (s scopedOnly) Has(p rbac.Permission) bool { return p == s.perm }
func (s scopedOnly) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	return len(ps) == 1 && ps[0] == s.perm
}
func (scopedOnly) Confined() bool                { return false }
func (s scopedOnly) Concrete() []rbac.Permission { return []rbac.Permission{s.perm} }

// TestBackupRoutesRestoreForeign: a foreign restore also needs core:admin.
func TestBackupRoutesRestoreForeign(t *testing.T) {
	target := "/backups/" + string(bhArkham) + "/restore"
	t.Run("without admin", func(t *testing.T) {
		r := newBHRig(t)
		p := bhOwner(permBackupRestore)
		p.Perms = scopedOnly{perm: permBackupRestore}
		rep := r.do(p, "POST", target, "application/json", `{"foreign":true}`)
		if rep.status != 403 || bhProblem(t, rep).Code != "permission_denied" || bhProblem(t, rep).Required != "core:admin" {
			t.Fatalf("= %d %s, want 403 permission_denied naming core:admin", rep.status, rep.body)
		}
		r.untouched("foreign without admin")
		rep = r.do(p, "POST", target, "application/json", `{"foreign":false}`)
		if rep.status != 202 || r.ev.count("stage") != 1 || r.rst.stageOpts.Foreign {
			t.Errorf("foreign=false = %d (stage calls %d, Foreign %v), want 202 and one non-foreign stage", rep.status, r.ev.count("stage"), r.rst.stageOpts.Foreign)
		}
		// The route passed the owner proof it established, and no more: this
		// holder is not core:admin, so the service's own gate refuses a foreign
		// restore whatever else reaches it.
		if got := r.rst.stageProof; got != (backup.RestoreProof{Owner: true}) {
			t.Errorf("proof = %+v, want owner without admin", got)
		}
	})
	t.Run("with admin", func(t *testing.T) {
		r := newBHRig(t)
		rep := r.do(bhOwner(rbac.CoreAdmin), "POST", target, "application/json", `{"foreign":true}`)
		if rep.status != 202 || !r.rst.stageOpts.Foreign {
			t.Errorf("= %d (Foreign %v), want 202 with Foreign", rep.status, r.rst.stageOpts.Foreign)
		}
		if got := r.rst.stageProof; got != (backup.RestoreProof{Owner: true, Admin: true}) {
			t.Errorf("proof = %+v, want owner and admin", got)
		}
	})
}

// TestBackupDownload_SystemManageOnly403: manage is not enough to take an
// archive, and a view-class scope cannot verify or create.
func TestBackupDownload_SystemManageOnly403(t *testing.T) {
	r := newBHRig(t)
	p := bhOwner(permSystemManage)
	p.Perms = scopedOnly{perm: permSystemManage}
	rep := r.do(p, "GET", "/backups/"+string(bhArkham)+"/download", "", "")
	if rep.status != 403 {
		t.Fatalf("download = %d %s, want 403", rep.status, rep.body)
	}
	if r.ev.count("download") != 0 || len(r.rec.records) != 0 {
		t.Errorf("events %v: Download or the audit log was reached", r.ev.get())
	}
	view := bhOwner(permSystemView)
	view.Perms = scopedOnly{perm: permSystemView}
	for _, c := range []bhCase{bhCases()[2], bhCases()[3]} { // verify, create
		if rep := r.run(view, c); rep.status != 403 {
			t.Errorf("%s as view = %d %s, want 403", c.name, rep.status, rep.body)
		}
	}
	r.untouched("view-class principal")
}

// TestBackupRestoreOptionsFlow: the body reaches StageRestore as
// backup.RestoreOptions, verify defaults to open, and a revert drops foreign.
func TestBackupRestoreOptionsFlow(t *testing.T) {
	target := "/backups/" + string(bhArkham) + "/restore"
	r := newBHRig(t)
	p := bhOwner(rbac.CoreAdmin)
	rep := r.do(p, "POST", target, "application/json", `{"verify":"entries","allow_weak_verify":true,"foreign":true,"replace_config":true,"accept_unreadable_secrets":true,"restart":true}`)
	want := backup.RestoreOptions{Verify: backup.VerifyEntries, AllowWeakVerify: true, Foreign: true, ReplaceConfig: true, AcceptUnreadableSecrets: true, Restart: true}
	if rep.status != 202 || r.rst.stageOpts != want || r.rst.stageID != bhArkham {
		t.Fatalf("= %d, opts %+v id %q, want 202 and %+v", rep.status, r.rst.stageOpts, r.rst.stageID, want)
	}
	var got restoreResponse
	if err := json.Unmarshal([]byte(rep.body), &got); err != nil || !got.RestartRequired || got.Pending.ArchiveID != string(bhArkham) || got.Pending.Kind != "archive" || len(got.Pending.Warnings) != 1 {
		t.Errorf("body = %s (%v), want pending and restart_required", rep.body, err)
	}
	if rep := r.do(p, "POST", target, "", ""); rep.status != 202 || r.rst.stageOpts != (backup.RestoreOptions{Verify: backup.VerifyOpen}) {
		t.Errorf("empty body = %d opts %+v, want 202 and the open default", rep.status, r.rst.stageOpts)
	}
	if rep := r.do(p, "POST", target, "application/json", `{"verify":"thorough"}`); rep.status != 422 {
		t.Errorf("bad verify = %d, want 422", rep.status)
	}
	if rep := r.do(p, "POST", target, "application/json", `{"surprise":true}`); rep.status != 400 || bhProblem(t, rep).Code != "unknown_field" {
		t.Errorf("unknown member = %d %s, want 400 unknown_field", rep.status, rep.body)
	}
	if rep := r.do(p, "POST", target, "text/plain", `{}`); rep.status != 415 {
		t.Errorf("wrong content type = %d, want 415", rep.status)
	}
	// Revert: foreign is accepted and never passed on, and needs no admin.
	r2 := newBHRig(t)
	pr := bhOwner(permBackupRestore)
	pr.Perms = scopedOnly{perm: permBackupRestore}
	rep = r2.do(pr, "POST", "/restore/revert", "application/json", `{"foreign":true,"restart":true}`)
	if rep.status != 202 || r2.rst.revertOpts.Foreign || !r2.rst.revertOpts.Restart || r2.rst.revertOpts.Verify != backup.VerifyOpen {
		t.Errorf("revert = %d opts %+v, want 202, Foreign dropped, Restart kept", rep.status, r2.rst.revertOpts)
	}
	if got := r2.rst.revertProof; got != (backup.RestoreProof{Owner: true}) {
		t.Errorf("revert proof = %+v, want owner without admin", got)
	}
	if got := r2.rec.records; len(got) != 0 {
		t.Errorf("%d route audit records, want none (the service audits)", len(got))
	}
}

func TestBackupCreateFlow(t *testing.T) {
	p := bhAs(auth.MethodAPIKey, permSystemManage)
	t.Run("options reach the enqueuer", func(t *testing.T) {
		r := newBHRig(t)
		rep := r.do(p, "POST", "/backups", "application/json", `{"reason":"before Arkham","include_derived":true,"include_recreatable":true}`)
		if rep.status != 202 || !strings.Contains(rep.body, `"task_id":"task-the-long-halloween"`) {
			t.Fatalf("= %d %s, want 202 with the task ID", rep.status, rep.body)
		}
		if r.enq.opts != (backup.CreateOptions{Reason: "before Arkham", IncludeDerived: true, IncludeRecreatable: true}) || r.enq.by != bhBruceID {
			t.Errorf("enqueued %+v by %q", r.enq.opts, r.enq.by)
		}
		if d := r.rec.records[0].Details; d["reason"] != "before Arkham" || d["include_derived"] != true {
			t.Errorf("details = %v", d)
		}
	})
	t.Run("defaults", func(t *testing.T) {
		r := newBHRig(t)
		if rep := r.do(p, "POST", "/backups", "", ""); rep.status != 202 || r.enq.opts.Reason != "manual" {
			t.Errorf("= %d reason %q, want 202 and manual", rep.status, r.enq.opts.Reason)
		}
	})
	t.Run("a bad reason is refused unrecorded", func(t *testing.T) {
		r := newBHRig(t)
		for _, body := range []string{`{"reason":"a\nb"}`, `{"reason":"` + strings.Repeat("x", 129) + `"}`} {
			if rep := r.do(p, "POST", "/backups", "application/json", body); rep.status != 422 {
				t.Errorf("%q = %d, want 422", body, rep.status)
			}
		}
		r.untouched("bad reason")
	})
	t.Run("a failed audit write stops it", func(t *testing.T) {
		r := newBHRig(t)
		r.rec.failOn = 1
		rep := r.do(p, "POST", "/backups", "", "")
		if rep.status != 503 || r.enq.calls != 0 {
			t.Errorf("= %d, enqueue calls %d, want 503 and none", rep.status, r.enq.calls)
		}
		if strings.Contains(rep.body, "disk is full") || !strings.Contains(r.logs.String(), "disk is full") {
			t.Errorf("the audit cause must be logged and not sent: body %s", rep.body)
		}
	})
	t.Run("a refused enqueue is recorded twice and mapped", func(t *testing.T) {
		r := newBHRig(t)
		r.enq.err = backup.ErrBackupInProgress
		rep := r.do(p, "POST", "/backups", "", "")
		if rep.status != 409 || bhProblem(t, rep).Code != "backup_in_progress" {
			t.Errorf("= %d %s, want 409 backup_in_progress", rep.status, rep.body)
		}
		if len(r.rec.records) != 2 || r.rec.records[1].Outcome != audit.Failure {
			t.Errorf("records = %+v, want the request and a failure record", r.rec.records)
		}
	})
}

func TestBackupVerifyLevels(t *testing.T) {
	p := bhAs(auth.MethodAPIKey, permSystemManage)
	target := "/backups/" + string(bhArkham) + "/verify"
	for _, c := range []struct {
		body string
		want backup.VerifyLevel
	}{{"", backup.VerifyEntries}, {`{}`, backup.VerifyEntries}, {`{"level":"checksum"}`, backup.VerifyChecksum}, {`{"level":"open"}`, backup.VerifyOpen}} {
		r := newBHRig(t)
		rep := r.do(p, "POST", target, "application/json", c.body)
		if rep.status != 200 || r.cat.lastLevel != c.want || r.cat.lastVerifyID != bhArkham {
			t.Errorf("%q = %d level %v, want 200 %v", c.body, rep.status, r.cat.lastLevel, c.want)
		}
		if !strings.Contains(rep.body, `"ok":true`) || !strings.Contains(rep.body, `"level":"entries"`) {
			t.Errorf("body = %s", rep.body)
		}
	}
	r := newBHRig(t)
	if rep := r.do(p, "POST", target, "application/json", `{"level":"deep"}`); rep.status != 422 || r.ev.count("verify") != 0 {
		t.Errorf("bad level = %d, want 422 with no Verify call", rep.status)
	}
}

func TestBackupReads(t *testing.T) {
	r := newBHRig(t)
	view := bhAs(auth.MethodAPIKey, permSystemView)
	rep := r.do(view, "GET", "/backups", "", "")
	var list listResponse
	if err := json.Unmarshal([]byte(rep.body), &list); err != nil || len(list.Backups) != 1 {
		t.Fatalf("list = %d %s (%v)", rep.status, rep.body, err)
	}
	a := list.Backups[0]
	if a.ID != string(bhArkham) || a.Name != "arkham-asylum.tar.zst" || a.Provenance != "own" || a.Format != backup.FormatMediacore || a.Reason != "manual" {
		t.Errorf("entry = %+v", a)
	}
	for _, leak := range []string{"install", "mac", "key_id"} {
		if strings.Contains(rep.body, leak) {
			t.Errorf("list body mentions %q: %s", leak, rep.body)
		}
	}
	// The auth-disabled principal may read the view routes (no user to name).
	nobody := bhAs(auth.MethodAuthDisabled, permSystemView)
	if rep := r.do(nobody, "GET", "/backups", "", ""); rep.status != 200 {
		t.Errorf("auth-disabled list = %d, want 200", rep.status)
	}
	if rep := r.do(view, "GET", "/backups/01JOKERSLAUGH000000000ARKM", "", ""); rep.status != 404 || bhProblem(t, rep).Code != "archive_not_found" {
		t.Errorf("unknown ID = %d %s, want 404 archive_not_found", rep.status, rep.body)
	}
	if rep := r.do(view, "GET", "/backups/..%2Fetc", "", ""); rep.status != 404 || bhProblem(t, rep).Code != "archive_not_found" {
		t.Errorf("malformed ID = %d %s, want 404 archive_not_found", rep.status, rep.body)
	}
}

func TestBackupPending(t *testing.T) {
	p := bhAs(auth.MethodAPIKey, permBackupRestore)
	t.Run("nothing pending is 204", func(t *testing.T) {
		r := newBHRig(t)
		if rep := r.do(p, "GET", "/restore/pending", "", ""); rep.status != 204 || rep.body != "" {
			t.Errorf("= %d %q, want 204 and no body", rep.status, rep.body)
		}
	})
	t.Run("a staged restore is 200", func(t *testing.T) {
		r := newBHRig(t)
		pr := r.rst.stageRes
		r.rst.pend = &pr
		rep := r.do(p, "GET", "/restore/pending", "", "")
		var got pendingDTO
		if err := json.Unmarshal([]byte(rep.body), &got); rep.status != 200 || err != nil || got.ArchiveID != string(bhArkham) || got.Kind != "archive" || got.RequestedBy != bhBruceID {
			t.Errorf("= %d %s (%v)", rep.status, rep.body, err)
		}
	})
}

// ---- error mapping ---------------------------------------------------------

func TestBackupErrorMapping(t *testing.T) {
	const secret = "/var/lib/batcave/keys/joker.key"
	wrap := func(e error) error { return fmt.Errorf("%w: %s", e, secret) }
	floor := &storage.ErrUpgradeFloor{Owner: "books", Have: 1, Floor: 3, InstallFirst: "v9.9.9"}
	for _, c := range []struct {
		name   string
		err    error
		status int
		code   string
		detail string // a substring the detail must carry
	}{
		{"not found", wrap(backup.ErrNotFound), 404, "archive_not_found", ""},
		{"foreign", wrap(backup.ErrForeignArchive), 403, "foreign_archive_requires_flag", ""},
		{"option without proof", wrap(backup.ErrRestoreNotAuthorized), 403, "restore_option_not_authorized", ""},
		{"floor protected", wrap(backup.ErrProtectedByFloor), 409, "protected_by_floor", ""},
		{"checksum", wrap(backup.ErrChecksumMismatch), 409, "archive_verification_failed", ""},
		{"manifest", wrap(backup.ErrManifest), 409, "archive_verification_failed", ""},
		{"no checksum", wrap(backup.ErrNoChecksum), 400, "archive_has_no_checksum", ""},
		{"wrong app", wrap(backup.ErrWrongApp), 409, "archive_incompatible", ""},
		{"too new", wrap(backup.ErrBackupTooNew), 409, "archive_incompatible", ""},
		{"upgrade floor", fmt.Errorf("stage: %w (%s)", floor, secret), 409, "archive_incompatible", "v9.9.9"},
		{"unreadable secrets", wrap(backup.ErrUnreadableSecrets), 409, "unreadable_secrets", ""},
		{"swap pending", wrap(restorejournal.ErrSwapPending), 409, "swap_pending", ""},
		{"swap pending via storage", wrap(storage.ErrSwapPending), 409, "swap_pending", ""},
		{"backup in progress", wrap(backup.ErrBackupInProgress), 409, "backup_in_progress", ""},
		{"insufficient space", wrap(backup.ErrInsufficientSpace), 507, "insufficient_space", ""},
		{"stale restore", wrap(backup.ErrRestoreStale), 409, "restore_stale", ""},
		{"rename refused", wrap(restorejournal.ErrRenameRefused), 409, "rename_refused", ""},
		{"cross device", wrap(backup.ErrCrossDevice), 500, "staging_cross_device", ""},
		{"no undo point", wrap(backup.ErrNoUndoPoint), 404, "no_undo_point", ""},
		{"anything else", errors.New("pebble: corrupt " + secret), 500, "internal", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newBHRig(t)
			r.rst.stageErr = c.err
			rep := r.do(bhOwner(rbac.CoreAdmin), "POST", "/backups/"+string(bhArkham)+"/restore", "", "")
			pb := bhProblem(t, rep)
			if rep.status != c.status || pb.Code != c.code {
				t.Fatalf("= %d %s, want %d %s", rep.status, rep.body, c.status, c.code)
			}
			if strings.Contains(rep.body, secret) || strings.Contains(rep.body, "/var/lib") {
				t.Errorf("the body carries the cause: %s", rep.body)
			}
			if c.detail != "" && !strings.Contains(pb.Detail, c.detail) {
				t.Errorf("detail %q lacks %q", pb.Detail, c.detail)
			}
			if c.code == "internal" && !strings.Contains(r.logs.String(), "pebble: corrupt") {
				t.Errorf("the internal cause was not logged: %s", r.logs)
			}
		})
	}
}

// TestBackupErrorMappingOtherRoutes: the same mapping on the other routes.
func TestBackupErrorMappingOtherRoutes(t *testing.T) {
	id := string(bhArkham)
	manage := bhOwner(permSystemManage)
	restore := bhOwner(permBackupRestore)
	for _, c := range []struct {
		name   string
		p      *auth.Principal
		set    func(*bhRig)
		method string
		target string
		status int
		code   string
	}{
		{"verify mismatch", manage, func(r *bhRig) { r.cat.verifyErr = backup.ErrChecksumMismatch }, "POST", "/backups/" + id + "/verify", 409, "archive_verification_failed"},
		{"delete floor", manage, func(r *bhRig) { r.cat.deleteErr = backup.ErrProtectedByFloor }, "DELETE", "/backups/" + id, 409, "protected_by_floor"},
		{"delete missing", manage, func(r *bhRig) { r.cat.deleteErr = backup.ErrNotFound }, "DELETE", "/backups/" + id, 404, "archive_not_found"},
		{"revert none", restore, func(r *bhRig) { r.rst.revertErr = backup.ErrNoUndoPoint }, "POST", "/restore/revert", 404, "no_undo_point"},
		{"cancel swap", restore, func(r *bhRig) { r.rst.cancelErr = restorejournal.ErrSwapPending }, "DELETE", "/restore/pending", 409, "swap_pending"},
		{"rewrap busy", manage, func(r *bhRig) { r.rw.err = backup.ErrBackupInProgress }, "POST", "/backups/" + id + "/rewrap", 409, "backup_in_progress"},
		{"list fails", bhOwner(permSystemView), func(r *bhRig) { r.cat.listErr = errors.New("readdir: permission denied") }, "GET", "/backups", 500, "internal"},
		{"pending fails", restore, func(r *bhRig) { r.rst.pendErr = errors.New("journal unreadable") }, "GET", "/restore/pending", 500, "internal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newBHRig(t)
			c.set(r)
			rep := r.do(c.p, c.method, c.target, "", "")
			if rep.status != c.status || bhProblem(t, rep).Code != c.code {
				t.Errorf("= %d %s, want %d %s", rep.status, rep.body, c.status, c.code)
			}
		})
	}
}

// TestBackupUnavailableOnlyAfterAuthorisation: a collaborator that is not wired
// answers 503 to an authorised caller and 401 or 403 to anyone else.
func TestBackupUnavailableOnlyAfterAuthorisation(t *testing.T) {
	for _, c := range bhCases() {
		t.Run(c.name, func(t *testing.T) {
			r := newBHRig(t)
			r.mount(Deps{})
			if rep := r.run(bhOwner("core:users.view"), c); rep.status != 403 {
				t.Errorf("unauthorised = %d %s, want 403 (nothing about availability)", rep.status, rep.body)
			}
			if rep := r.run(nil, c); rep.status != 401 {
				t.Errorf("anonymous = %d, want 401", rep.status)
			}
			rep := r.run(c.grant(), c)
			if rep.status != 503 || bhProblem(t, rep).Code != "unavailable" {
				t.Errorf("authorised = %d %s, want 503 unavailable", rep.status, rep.body)
			}
			r.untouched("unavailable")
		})
	}
	// The audit log is a collaborator of create, import and download.
	for _, c := range bhCases() {
		if c.action == "" {
			continue
		}
		t.Run("no audit log: "+c.name, func(t *testing.T) {
			r := newBHRig(t)
			r.mount(Deps{Catalogue: r.cat, Restorer: r.rst, Rewrapper: r.rw, Enqueuer: r.enq})
			if rep := r.run(c.grant(), c); rep.status != 503 {
				t.Errorf("= %d %s, want 503", rep.status, rep.body)
			}
			r.untouched("no audit log")
		})
	}
}

// ---- import ----------------------------------------------------------------

func TestBackupImport(t *testing.T) {
	p := bhAs(auth.MethodSession, permSystemManage)
	t.Run("raw body", func(t *testing.T) {
		r := newBHRig(t)
		rep := r.do(p, "POST", "/backups/import?name=arkham-2026-11-12.tar.zst", "application/octet-stream", "Year One")
		if rep.status != 201 || r.cat.importName != "arkham-2026-11-12.tar.zst" || string(r.cat.importData) != "Year One" {
			t.Fatalf("= %d name %q data %q", rep.status, r.cat.importName, r.cat.importData)
		}
		if loc := rep.header.Get("Location"); loc != bhBase+"/backups/"+string(bhArkham) {
			t.Errorf("Location = %q", loc)
		}
		if r.rec.records[0].Details["name"] != "arkham-2026-11-12.tar.zst" {
			t.Errorf("details = %v", r.rec.records[0].Details)
		}
	})
	t.Run("multipart", func(t *testing.T) {
		r := newBHRig(t)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := mw.WriteField("note", "from the Batcave"); err != nil {
			t.Fatal(err)
		}
		fw, err := mw.CreateFormFile("file", "ignored-name.tar.zst")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte("Dark Victory"))
		_ = mw.Close()
		rep := r.do(p, "POST", "/backups/import?name=dark-victory.tar.zst", mw.FormDataContentType(), buf.String())
		if rep.status != 201 || r.cat.importName != "dark-victory.tar.zst" || string(r.cat.importData) != "Dark Victory" {
			t.Errorf("= %d name %q data %q, want the file part under the query name", rep.status, r.cat.importName, r.cat.importData)
		}
	})
	t.Run("multipart without a file part", func(t *testing.T) {
		r := newBHRig(t)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("note", "no archive")
		_ = mw.Close()
		rep := r.do(p, "POST", "/backups/import?name=x.tar.zst", mw.FormDataContentType(), buf.String())
		if rep.status != 400 || bhProblem(t, rep).Code != "file_required" || r.ev.count("import") != 0 || len(r.rec.records) != 0 {
			t.Errorf("= %d %s, want 400 file_required with nothing audited", rep.status, rep.body)
		}
	})
	t.Run("path-like names", func(t *testing.T) {
		for _, name := range []string{"", "../joker.tar", "a/b.tar", `a%5Cb.tar`, "..", "a..b", "%2Fetc%2Fpasswd", "x%00y", strings.Repeat("a", 201)} {
			r := newBHRig(t)
			rep := r.do(p, "POST", "/backups/import?name="+name, "application/octet-stream", "x")
			if rep.status != 400 || bhProblem(t, rep).Code != "invalid_name" {
				t.Errorf("name %q = %d %s, want 400 invalid_name", name, rep.status, rep.body)
			}
			r.untouched("invalid name " + name)
		}
	})
	t.Run("oversize by Content-Length", func(t *testing.T) {
		r := newBHRig(t)
		r.mount(Deps{Catalogue: r.cat, Audit: r.rec, MaxImportBytes: 16})
		rep := r.do(p, "POST", "/backups/import?name=x.tar", "application/octet-stream", strings.Repeat("A", 64))
		if rep.status != 413 || bhProblem(t, rep).Code != "body_too_large" {
			t.Errorf("= %d %s, want 413 body_too_large", rep.status, rep.body)
		}
		r.untouched("oversize")
	})
	t.Run("oversize while streaming", func(t *testing.T) {
		r := newBHRig(t)
		r.mount(Deps{Catalogue: r.cat, Audit: r.rec, MaxImportBytes: 16})
		rep := r.do(p, "POST", "/backups/import?name=x.tar", "application/octet-stream", strings.Repeat("A", 64), func(q *http.Request) { q.ContentLength = -1 })
		if rep.status != 413 || bhProblem(t, rep).Code != "body_too_large" {
			t.Errorf("= %d %s, want 413 body_too_large", rep.status, rep.body)
		}
		if len(r.cat.importData) > 16 {
			t.Errorf("Import read %d bytes past the 16 byte bound", len(r.cat.importData))
		}
	})
	t.Run("a failed audit write imports nothing", func(t *testing.T) {
		r := newBHRig(t)
		r.rec.failOn = 1
		if rep := r.do(p, "POST", "/backups/import?name=x.tar", "application/octet-stream", "x"); rep.status != 503 || r.ev.count("import") != 0 {
			t.Errorf("= %d, import calls %d, want 503 and none", rep.status, r.ev.count("import"))
		}
	})
	t.Run("service refusals", func(t *testing.T) {
		r := newBHRig(t)
		r.cat.importErr = backup.ErrWrongApp
		rep := r.do(p, "POST", "/backups/import?name=x.tar", "application/octet-stream", "x")
		if rep.status != 409 || bhProblem(t, rep).Code != "archive_incompatible" {
			t.Errorf("= %d %s", rep.status, rep.body)
		}
	})
}

// ---- download --------------------------------------------------------------

func TestBackupDownload(t *testing.T) {
	admin := bhOwner(rbac.CoreAdmin)
	target := "/backups/" + string(bhArkham) + "/download"
	t.Run("streams the archive after the audit record", func(t *testing.T) {
		r := newBHRig(t)
		r.cat.archives[0].Name = "../../etc/Joker's Ark.tar.zst"
		rep := r.do(admin, "GET", target, "", "")
		if rep.status != 200 || rep.body != "The Killing Joke" {
			t.Fatalf("= %d %q, want the archive bytes", rep.status, rep.body)
		}
		if got := rep.header.Get("Content-Disposition"); got != "attachment; filename=Joker_s_Ark.tar.zst" {
			t.Errorf("Content-Disposition = %q", got)
		}
		if got := rep.header.Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("Content-Type = %q", got)
		}
		if !r.cat.closed {
			t.Error("the archive was not closed")
		}
		if seq := r.ev.get(); len(seq) < 2 || seq[0] != "record" || seq[len(seq)-1] != "download" || len(r.rec.records) != 1 {
			t.Errorf("events = %v, want the record before the lookup and the stream", seq)
		}
		if r.rec.records[0].Details["archive_id"] != string(bhArkham) {
			t.Errorf("details = %v", r.rec.records[0].Details)
		}
	})
	t.Run("a name with nothing safe falls back to the ID", func(t *testing.T) {
		got := dispositionOf(backup.Archive{ID: bhArkham, Name: "..."})
		if got != "attachment; filename="+string(bhArkham)+".archive" {
			t.Errorf("= %q", got)
		}
	})
	t.Run("a failed audit write streams nothing", func(t *testing.T) {
		r := newBHRig(t)
		r.rec.failOn = 1
		rep := r.do(admin, "GET", target, "", "")
		if rep.status != 503 || rep.body == "The Killing Joke" || r.ev.count("download") != 0 || r.ev.count("list") != 0 {
			t.Errorf("= %d %q events %v, want 503 and no lookup or download", rep.status, rep.body, r.ev.get())
		}
	})
	t.Run("malformed ID is 404 unaudited", func(t *testing.T) {
		r := newBHRig(t)
		if rep := r.do(admin, "GET", "/backups/not-an-id/download", "", ""); rep.status != 404 {
			t.Errorf("= %d, want 404", rep.status)
		}
		r.untouched("malformed ID")
	})
	t.Run("an archive that is gone is 404 and never opened", func(t *testing.T) {
		r := newBHRig(t)
		r.cat.archives = nil
		rep := r.do(admin, "GET", target, "", "")
		if rep.status != 404 || r.ev.count("download") != 0 {
			t.Errorf("= %d, download calls %d, want 404 and none", rep.status, r.ev.count("download"))
		}
		if n := len(r.rec.records); n != 2 || r.rec.records[1].Outcome != audit.Failure {
			t.Errorf("records = %+v, want the request and a failure record", r.rec.records)
		}
	})
	t.Run("an open failure is mapped", func(t *testing.T) {
		r := newBHRig(t)
		r.cat.downloadErr = backup.ErrNotFound
		if rep := r.do(admin, "GET", target, "", ""); rep.status != 404 {
			t.Errorf("= %d, want 404", rep.status)
		}
	})
}

// ---- registration ----------------------------------------------------------

type bhWant struct {
	method, pattern, name string
	perm                  string
	interactive, owner    bool
}

func bhWants() []bhWant {
	return []bhWant{
		{"GET", bhBase + "/backups", "listBackups", "core:system.view", false, false},
		{"GET", bhBase + "/backups/{id}", "getBackup", "core:system.view", false, false},
		{"POST", bhBase + "/backups/{id}/verify", "verifyBackup", "core:system.manage", false, false},
		{"POST", bhBase + "/backups", "createBackup", "core:system.manage", false, false},
		{"DELETE", bhBase + "/backups/{id}", "deleteBackup", "core:system.manage", true, false},
		{"POST", bhBase + "/backups/import", "importBackup", "core:system.manage", true, false},
		{"GET", bhBase + "/backups/{id}/download", "downloadBackup", "core:admin", true, true},
		{"POST", bhBase + "/backups/{id}/restore", "restoreBackup", "core:backup.restore", true, true},
		{"POST", bhBase + "/restore/revert", "revertRestore", "core:backup.restore", true, true},
		{"DELETE", bhBase + "/restore/pending", "cancelRestore", "core:backup.restore", true, true},
		{"GET", bhBase + "/restore/pending", "getPendingRestore", "core:backup.restore", false, false},
		{"POST", bhBase + "/backups/{id}/rewrap", "rewrapBackup", "core:system.manage", true, false},
	}
}

func bhRegistered(t testing.TB) *httpapi.Router {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	Register(r.CoreGroup(), Deps{})
	return r
}

// TestBackupRegisterRouteTable pins 05-backup §5B.3.8's table as the router
// sees it: method, path, name, permission and guards, and that the permission
// is a scope the catalog declares.
func TestBackupRegisterRouteTable(t *testing.T) {
	var got []httpapi.RouteInfo
	for ri := range bhRegistered(t).Routes() {
		got = append(got, ri)
	}
	wants := bhWants()
	if len(got) != len(wants) {
		t.Fatalf("%d routes registered, want %d", len(got), len(wants))
	}
	names := map[string]bool{}
	for _, w := range wants {
		i := slices.IndexFunc(got, func(ri httpapi.RouteInfo) bool { return ri.Method == w.method && ri.Pattern == w.pattern })
		if i < 0 {
			t.Errorf("no route %s %s", w.method, w.pattern)
			continue
		}
		ri := got[i]
		if ri.Owner != "core" || ri.Kind != httpapi.RouteKindRaw || ri.Name != w.name {
			t.Errorf("%s %s = owner %s kind %d name %s, want core raw %s", w.method, w.pattern, ri.Owner, ri.Kind, ri.Name, w.name)
		}
		if ri.Perm.IsZero() || ri.Perm.String() != w.perm {
			t.Errorf("%s requires %s, want %s", w.name, ri.Perm, w.perm)
		}
		if want := (httpapi.Guards{Interactive: w.interactive, Owner: w.owner}); ri.Guards != want {
			t.Errorf("%s guards = %+v, want %+v", w.name, ri.Guards, want)
		}
		if names[ri.Name] {
			t.Errorf("route name %s is used twice", ri.Name)
		}
		names[ri.Name] = true
	}
	// Independent of the table above: every write but create and verify needs
	// an interactive session, and no route is open to a missing permission.
	for _, ri := range got {
		if ri.Perm.IsZero() {
			t.Errorf("%s %s has no permission", ri.Method, ri.Pattern)
		}
	}
	// The handler's table and Register's are the one table, so each route's
	// gate equals what the router was told.
	for _, def := range newAPI(Deps{}).routeTable() {
		i := slices.IndexFunc(got, func(ri httpapi.RouteInfo) bool { return ri.Name == def.name })
		if i < 0 || got[i].Guards != (httpapi.Guards{Interactive: def.gate.interactive, Owner: def.gate.owner}) || got[i].Perm.String() != string(def.gate.perm) {
			t.Errorf("%s: handler gate %+v differs from the registered route", def.name, def.gate)
		}
	}
}

// TestBackupRegisterBuildFailsOnlyForWantOfAuth: until F4-08 a router refuses
// every non-Public native route with ErrNoAuthService. Everything else Build
// checks (a pattern conflict, a body httpapi cannot describe, a non-snake_case
// JSON name, a duplicate name) would be a further error here.
func TestBackupRegisterBuildFailsOnlyForWantOfAuth(t *testing.T) {
	_, err := bhRegistered(t).Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Build error %T does not join its problems", err)
	}
	problems := joined.Unwrap()
	if len(problems) != len(bhWants()) {
		t.Errorf("Build reported %d problems, want %d (one ErrNoAuthService per route): %v", len(problems), len(bhWants()), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}
