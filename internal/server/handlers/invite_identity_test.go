// file: internal/server/handlers/invite_identity_test.go
// version: 0.1.0
// guid: 1d7f3b58-6a24-4c91-b0e5-9a8c2f4d7e13
// last-edited: 2026-11-22

package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	coreauth "github.com/falkcorp/mediacore/auth"
	imail "github.com/falkcorp/mediacore/integrations/notify/mail"
	"github.com/falkcorp/mediacore/rbac"
)

// F4-08 B10t (books half): the invite and admin set-email routes under core
// identity are wrappers over core's own calls, made with the request's
// principal. These tests run them against fakeAccounts, which enforces what core
// enforces and what the order of the calls depends on, so a wrong order fails
// here and not only at full boot:
//
//   - one open invite per username (core's usernameFree), so a resend must
//     delete the old invite before it creates the new one;
//   - MarkInviteEmailDelivered needs core:admin, a live invite and an e-mail,
//     and is idempotent;
//   - every call records the principal it was made with.

// fakeAccounts is core's auth.Admin over maps. The merge half (PlanUserMerge,
// StageUserMerge) is the embedded nil interface: these routes never call it.
type fakeAccounts struct {
	coreauth.Admin
	mu      sync.Mutex
	now     func() time.Time
	seq     int
	invites map[string]*fakeInvite
	users   map[coreauth.UserID]*coreauth.User
	calls   []string // "<call> <arg> as <actor>", in order
	// markErr, when set, is what MarkInviteEmailDelivered returns.
	markErr error
	// createErr, when set, is what CreateInvite returns.
	createErr error
	// setEmailErr, when set, is what SetUserEmail returns.
	setEmailErr error
}

type fakeInvite struct {
	rec   coreauth.InviteView
	token string
}

func newFakeAccounts(now func() time.Time) *fakeAccounts {
	return &fakeAccounts{now: now, invites: map[string]*fakeInvite{}, users: map[coreauth.UserID]*coreauth.User{}}
}

func actorName(p *coreauth.Principal) string {
	if p == nil || p.User == nil {
		return "nobody"
	}
	return string(p.User.ID)
}

func ctxActor(ctx context.Context) string {
	p, _ := coreauth.PrincipalFrom(ctx)
	return actorName(p)
}

func (f *fakeAccounts) log(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakeAccounts) CreateInvite(ctx context.Context, actor *coreauth.Principal, in coreauth.InviteSpec) (string, coreauth.InviteView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("create %s as %s", in.Username, actorName(actor))
	if p, _ := coreauth.PrincipalFrom(ctx); actorName(p) != actorName(actor) {
		return "", coreauth.InviteView{}, fmt.Errorf("%w: the actor is not the principal of the request", coreauth.ErrDenied)
	}
	if f.createErr != nil {
		return "", coreauth.InviteView{}, f.createErr
	}
	for _, v := range f.invites {
		if strings.EqualFold(v.rec.Username, in.Username) {
			return "", coreauth.InviteView{}, fmt.Errorf("%w: an open invite names %q", coreauth.ErrUserExists, in.Username)
		}
	}
	f.seq++
	ttl := in.TTL
	if ttl == 0 {
		ttl = 72 * time.Hour
	}
	now := f.now()
	id := fmt.Sprintf("inv-%d", f.seq)
	rec := coreauth.InviteView{
		ID: id, Username: in.Username, Email: in.Email, Role: in.Role, CreatedBy: actor.User.ID,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	tok := fmt.Sprintf("tok-%d-0123456789abcdef", f.seq)
	f.invites[id] = &fakeInvite{rec: rec, token: tok}
	return "/invite#token=" + tok, rec, nil
}

func (f *fakeAccounts) DeleteInvite(ctx context.Context, actor *coreauth.Principal, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("delete %s as %s", id, actorName(actor))
	if p, _ := coreauth.PrincipalFrom(ctx); actorName(p) != actorName(actor) {
		return fmt.Errorf("%w: the actor is not the principal of the request", coreauth.ErrDenied)
	}
	if _, ok := f.invites[id]; !ok {
		return coreauth.ErrNotFound
	}
	delete(f.invites, id)
	return nil
}

func (f *fakeAccounts) Invite(ctx context.Context, id string) (coreauth.InviteView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("invite %s as %s", id, ctxActor(ctx))
	v, ok := f.invites[id]
	if !ok || !f.now().Before(v.rec.ExpiresAt) {
		// An expired invite is not open: core answers ErrNotFound for it too.
		return coreauth.InviteView{}, coreauth.ErrNotFound
	}
	return v.rec, nil
}

func (f *fakeAccounts) AdminInvites(ctx context.Context) iter.Seq2[coreauth.InviteView, error] {
	return func(yield func(coreauth.InviteView, error) bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.log("invites as %s", ctxActor(ctx))
		for _, v := range f.invites {
			if !yield(v.rec, nil) {
				return
			}
		}
	}
}

// RotateInvite is core's: one step that replaces the open invite by a new one for
// the same username, address and role with EXACTLY the old expiry, born
// unflagged, reporting whether the old one was flagged. An expired invite is
// refused and nothing changes.
func (f *fakeAccounts) RotateInvite(ctx context.Context, id string) (string, coreauth.InviteRotation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("rotate %s as %s", id, ctxActor(ctx))
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok || p.User == nil {
		return "", coreauth.InviteRotation{}, coreauth.ErrDenied
	}
	old, ok := f.invites[id]
	if !ok {
		return "", coreauth.InviteRotation{}, coreauth.ErrNotFound
	}
	if !f.now().Before(old.rec.ExpiresAt) {
		return "", coreauth.InviteRotation{}, fmt.Errorf("%w: the invite expired", coreauth.ErrInviteInvalid)
	}
	delete(f.invites, id)
	f.seq++
	nid := fmt.Sprintf("inv-%d", f.seq)
	rec := old.rec
	rec.ID, rec.CreatedBy, rec.CreatedAt, rec.BindEmailOnAccept = nid, p.User.ID, f.now(), false
	tok := fmt.Sprintf("tok-%d-0123456789abcdef", f.seq)
	f.invites[nid] = &fakeInvite{rec: rec, token: tok}
	return "/invite#token=" + tok, coreauth.InviteRotation{Invite: rec, WasEmailBound: old.rec.BindEmailOnAccept}, nil
}

// MarkInviteEmailDelivered refuses as core does: no principal holding core:admin,
// no such invite (deleted or never made), no e-mail, or past the expiry. A second
// call on a flagged live invite is a no-op.
func (f *fakeAccounts) MarkInviteEmailDelivered(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("mark %s as %s", id, ctxActor(ctx))
	if f.markErr != nil {
		return f.markErr
	}
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok || p.Perms == nil || !p.Perms.Has(rbac.CoreAdmin) {
		return coreauth.ErrDenied
	}
	v, ok := f.invites[id]
	switch {
	case !ok:
		return coreauth.ErrNotFound
	case v.rec.Email == "":
		return fmt.Errorf("%w: the invite has no e-mail", coreauth.ErrBadInput)
	case !f.now().Before(v.rec.ExpiresAt):
		return coreauth.ErrInviteInvalid
	}
	v.rec.BindEmailOnAccept = true
	return nil
}

func (f *fakeAccounts) SetUserEmail(ctx context.Context, id coreauth.UserID, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("setemail %s %s as %s", id, email, ctxActor(ctx))
	if f.setEmailErr != nil {
		return f.setEmailErr
	}
	u, ok := f.users[id]
	if !ok {
		return coreauth.ErrNotFound
	}
	for _, o := range f.users {
		if o.ID != id && email != "" && o.EmailVerified && strings.EqualFold(o.Email, email) {
			return coreauth.ErrEmailInUse
		}
	}
	u.Email, u.EmailVerified = email, email != ""
	return nil
}

func (f *fakeAccounts) AdminUser(_ context.Context, id coreauth.UserID) (*coreauth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return nil, coreauth.ErrNotFound
	}
	c := *u
	return &c, nil
}

func (f *fakeAccounts) AdminUsers(context.Context, coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	return func(func(*coreauth.User, error) bool) {}
}

func (f *fakeAccounts) invite(id string) (fakeInvite, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.invites[id]
	if !ok {
		return fakeInvite{}, false
	}
	return *v, true
}

// flag stands for an earlier MarkInviteEmailDelivered by an admin.
func (f *fakeAccounts) flag(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invites[id].rec.BindEmailOnAccept = true
}

func (f *fakeAccounts) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// identityInviteFixture serves the invite routes as a request under core
// identity: an Identity carrying fakeAccounts, and a principal that holds
// `scopes` (core:admin for an admin).
type identityInviteFixture struct {
	*inviteFixture
	acc *fakeAccounts
	clk *fakeClock
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newIdentityInviteFixture builds the routes with actor "bruce" holding scopes.
func newIdentityInviteFixture(t *testing.T, scopes ...rbac.Permission) *identityInviteFixture {
	t.Helper()
	f := newInviteFixture(t)
	clk := &fakeClock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	acc := newFakeAccounts(clk.now)
	f.h.SetInviteDelivery(&handlers.InviteDelivery{
		Sender: func() imail.Sender {
			if !f.mailOn {
				return nil
			}
			return f.rec
		},
		BaseURL: func() string { return f.base },
		Now:     clk.now,
		// Both promises depend on the creator, which is what these tests vary.
		GuestPolicySyncConfigured: func() bool { return f.syncOn },
		GuestServiceTokensEnabled: func() bool { return f.tokens },
	})
	id := bridgeIdentity(t)
	id.Admin = acc
	f.useIdentity(id, "bruce", scopes...)
	return &identityInviteFixture{inviteFixture: f, acc: acc, clk: clk}
}

// useIdentity re-serves the routes under id with a principal for user holding
// scopes, and HEAD's admin as the books-side user so SetUserEmail's own gate
// passes (it keeps HEAD's role test and adds core:admin).
func (f *inviteFixture) useIdentity(id *legacyauth.Identity, user string, scopes ...rbac.Permission) {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if f.actor != nil {
			c.Set("auth_user", f.actor)
		}
		chk := bridgeChecker(scopes)
		p := &coreauth.Principal{Method: coreauth.MethodSession, Perms: chk, User: &coreauth.User{ID: coreauth.UserID(user)}}
		ctx := coreauth.WithPrincipal(c.Request.Context(), p)
		ctx = rbac.WithChecker(ctx, chk)
		ctx = legacyauth.WithIdentity(ctx, id)
		c.Request = c.Request.WithContext(ctx)
	})
	r.POST("/users/invite", f.h.CreateInvite)
	r.GET("/users/invites", f.h.ListInvites)
	r.DELETE("/users/invites/:id", f.h.DeleteInvite)
	r.POST("/users/invites/:id/resend", f.h.ResendInvite)
	r.PUT("/users/:id/email", f.h.SetUserEmail)
	f.router = r
}

func createBody(username, email string, send bool) map[string]any {
	return map[string]any{"username": username, "role_id": "viewer", "email": email, "send_email": send}
}

// TestIdentityCreateInvite_SendFlagsAfterTheMailGoes: a create with send mails
// the core link, then flags the invite; the link is the absolute core fragment
// link and nothing of it is stored by books.
func TestIdentityCreateInvite_SendFlagsAfterTheMailGoes(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	w, data := f.do(t, http.MethodPost, "/users/invite", createBody("Selina Kyle", " Selina@Example.com ", true))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if data["email_sent"] != true {
		t.Fatalf("email_sent = %v, error = %v", data["email_sent"], data["email_error"])
	}
	inv, ok := f.acc.invite("inv-1")
	if !ok || !inv.rec.BindEmailOnAccept {
		t.Fatalf("the invite must be flagged once the mail went out: %+v", inv)
	}
	tok, _ := data["token"].(string)
	if tok != inv.token || data["invite_url"] != testBase+"/invite#token="+inv.token {
		t.Fatalf("token %q, invite_url %v: want core's absolute fragment link", tok, data["invite_url"])
	}
	sent := f.rec.Sent()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, testBase+"/invite#token="+inv.token) {
		t.Fatalf("the mail must carry core's link: %+v", sent)
	}
	if sent[0].To != "selina@example.com" {
		t.Fatalf("mail to %q", sent[0].To)
	}
	// Books' delivery record: sent-at and recipient, keyed by the core id.
	rec, err := f.store.GetInviteMail("inv-1")
	if err != nil || rec == nil || rec.To != "selina@example.com" || rec.SentAt.IsZero() {
		t.Fatalf("delivery record = %+v, %v", rec, err)
	}
	// The books invite store is not touched: the invite is core's.
	if invs, _ := f.store.ListActiveInvites(); len(invs) != 0 {
		t.Fatalf("books' invite store holds %d invites under identity", len(invs))
	}
	// Call order: create, then mark, both as the request's principal.
	want := []string{"create Selina Kyle as bruce", "mark inv-1 as bruce"}
	if got := f.acc.callLog(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// TestIdentityCreateInvite_SendFailureSetsNoFlag: a send failure never sets the
// flag (so accepting leaves the address unverified) and never calls core's mark.
func TestIdentityCreateInvite_SendFailureSetsNoFlag(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	f.rec.SetErr(errors.New("mail: RCPT TO: 550 no such user"))
	w, data := f.do(t, http.MethodPost, "/users/invite", createBody("selina", "selina@example.com", true))
	if w.Code != http.StatusCreated || data["email_sent"] != false || !strings.Contains(fmt.Sprint(data["email_error"]), "550") {
		t.Fatalf("send failure not reported: %d %v", w.Code, data)
	}
	inv, _ := f.acc.invite("inv-1")
	if inv.rec.BindEmailOnAccept {
		t.Fatal("a failed send must not flag the invite")
	}
	for _, c := range f.acc.callLog() {
		if strings.HasPrefix(c, "mark ") {
			t.Fatalf("mark called after a failed send: %v", f.acc.callLog())
		}
	}
	if rec, _ := f.store.GetInviteMail("inv-1"); rec != nil {
		t.Fatalf("a delivery record exists for a mail that never went out: %+v", rec)
	}
}

// TestIdentityCreateInvite_NonAdminSenderIsMailedAndUnflagged: HEAD let a
// users.manage non-admin send. Core's mark needs core:admin, so the invite goes
// out unflagged, with email_sent true and no 500 and no call to mark.
func TestIdentityCreateInvite_NonAdminSenderIsMailedAndUnflagged(t *testing.T) {
	f := newIdentityInviteFixture(t, "core:users.manage")
	f.asUser(t, "bruce", "admin")
	w, data := f.do(t, http.MethodPost, "/users/invite", createBody("selina", "selina@example.com", true))
	if w.Code != http.StatusCreated || data["email_sent"] != true {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if inv, _ := f.acc.invite("inv-1"); inv.rec.BindEmailOnAccept {
		t.Fatal("a non-admin sender must not flag the invite")
	}
	for _, c := range f.acc.callLog() {
		if strings.HasPrefix(c, "mark ") {
			t.Fatalf("a non-admin sender must not even ask core to flag: %v", f.acc.callLog())
		}
	}
	if len(f.rec.Sent()) != 1 {
		t.Fatalf("sent %d", len(f.rec.Sent()))
	}
}

// TestIdentityCreateInvite_CoreRefusalIsMapped: core's refusals take HEAD's
// statuses, and a mark refusal after a good send is reported, not fatal.
func TestIdentityCreateInvite_CoreRefusalIsMapped(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	if w, _ := f.do(t, http.MethodPost, "/users/invite", createBody("selina", "", false)); w.Code != http.StatusCreated {
		t.Fatalf("first create: %d", w.Code)
	}
	if w, _ := f.do(t, http.MethodPost, "/users/invite", createBody("Selina", "", false)); w.Code != http.StatusConflict {
		t.Fatalf("a username an open invite names: %d, want 409", w.Code)
	}
	f.acc.createErr = coreauth.ErrEmailInUse
	if w, _ := f.do(t, http.MethodPost, "/users/invite", createBody("harvey", "robin@example.com", false)); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "another account already has that email address verified") {
		t.Fatalf("an address another account holds verified: %d %s, want 409 with HEAD's text", w.Code, w.Body.String())
	}
	f.acc.createErr = coreauth.ErrScopeEscalation
	if w, _ := f.do(t, http.MethodPost, "/users/invite", createBody("harvey", "", false)); w.Code != http.StatusForbidden {
		t.Fatalf("a role the caller may not assign: %d, want 403", w.Code)
	}
	f.acc.createErr = nil
	f.acc.markErr = errors.New("storage down")
	w, data := f.do(t, http.MethodPost, "/users/invite", createBody("harvey", "harvey@example.com", true))
	if w.Code != http.StatusCreated || data["email_sent"] != true || data["email_binding_error"] == nil {
		t.Fatalf("a failed mark after a good send: %d %v", w.Code, data)
	}
}

// TestIdentityInvite_NilAdminAnswers503: an Identity with no Admin handle
// refuses at every route and never falls through to books' own store.
func TestIdentityInvite_NilAdminAnswers503(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	id := bridgeIdentity(t) // Admin nil
	f.useIdentity(id, "bruce", rbac.CoreAdmin)
	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/users/invite"},
		{http.MethodGet, "/users/invites"},
		{http.MethodDelete, "/users/invites/abc"},
		{http.MethodPost, "/users/invites/abc/resend"},
		{http.MethodPut, "/users/someone/email"},
	} {
		var body any
		switch rt.path {
		case "/users/invite":
			body = createBody("selina", "", false)
		case "/users/someone/email":
			body = map[string]any{"email": "x@example.com"}
		}
		if w, _ := f.do(t, rt.method, rt.path, body); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", rt.method, rt.path, w.Code)
		}
	}
	if invs, _ := f.store.ListActiveInvites(); len(invs) != 0 {
		t.Fatalf("a route fell through to books' invite store: %d invites", len(invs))
	}
}

// openInvite creates an invite WITHOUT sending, so the resend limits are unspent
// (they run on the wall clock), and returns its id. flagged stands for an
// earlier delivery by an admin: core's flag set and books' record written.
func (f *identityInviteFixture) openInvite(t *testing.T, username, email string, flagged bool) string {
	t.Helper()
	w, data := f.do(t, http.MethodPost, "/users/invite", createBody(username, email, false))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	inv, _ := data["invite"].(map[string]any)
	id, _ := inv["id"].(string)
	if flagged {
		f.acc.flag(id)
		if err := f.store.PutInviteMail(store.InviteMailRecord{InviteID: id, To: email, SentAt: f.clk.now(), ExpiresAt: f.clk.now().Add(72 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// TestIdentityResend_RotatesTheInvite: the old link dies, the new one is mailed
// and flagged, and a refused resend leaves the old link alive.
func TestIdentityResend_RotatesTheInvite(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	old := f.openInvite(t, "selina", "selina@example.com", true)
	oldTok := mustInvite(t, f.acc, old).token
	w, data := f.do(t, http.MethodPost, "/users/invites/"+old+"/resend", nil)
	if w.Code != http.StatusOK || data["email_sent"] != true {
		t.Fatalf("resend: %d %s", w.Code, w.Body.String())
	}
	if _, alive := f.acc.invite(old); alive {
		t.Fatal("the old invite must be deleted: its link must die")
	}
	invv, _ := data["invite"].(map[string]any)
	newID, _ := invv["id"].(string)
	if newID == "" || newID == old {
		t.Fatalf("rotated invite id = %q", newID)
	}
	nv, ok := f.acc.invite(newID)
	if !ok || !nv.rec.BindEmailOnAccept || nv.token == oldTok {
		t.Fatalf("the new invite must exist, be flagged and carry a new token: %+v", nv)
	}
	sent := f.rec.Sent()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, nv.token) || strings.Contains(sent[0].Text, oldTok) {
		t.Fatalf("the resend must mail only the new link: %d messages", len(sent))
	}
	// The delivery record moved to the new id.
	if rec, _ := f.store.GetInviteMail(old); rec != nil {
		t.Fatalf("the old invite's delivery record must go: %+v", rec)
	}
	if rec, _ := f.store.GetInviteMail(newID); rec == nil || rec.To != "selina@example.com" {
		t.Fatalf("the new invite's delivery record = %+v", rec)
	}
}

func mustInvite(t *testing.T, acc *fakeAccounts, id string) fakeInvite {
	t.Helper()
	v, ok := acc.invite(id)
	if !ok {
		t.Fatalf("no invite %q", id)
	}
	return v
}

// TestIdentityResend_IsOneCoreRotation: a resend is core's RotateInvite and
// nothing else, so core's own batch is what keeps the old link alive when the
// rotation fails. Books makes no separate delete or create.
func TestIdentityResend_IsOneCoreRotation(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	old := f.openInvite(t, "selina", "selina@example.com", true)
	before := len(f.acc.callLog())
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+old+"/resend", nil); w.Code != http.StatusOK {
		t.Fatalf("resend: %d", w.Code)
	}
	for _, c := range f.acc.callLog()[before:] {
		if strings.HasPrefix(c, "delete ") || strings.HasPrefix(c, "create ") {
			t.Fatalf("a resend must be one RotateInvite, got %v", f.acc.callLog()[before:])
		}
	}
	if !slices.Contains(f.acc.callLog(), "rotate "+old+" as bruce") {
		t.Fatalf("no rotation as the request's principal: %v", f.acc.callLog())
	}
}

// TestIdentityResend_NonAdminResendOfAFlaggedInvite: the rotated invite is born
// unflagged and only an admin can flag it, so a resend by a users.manage holder
// who is not core:admin is "sent, but not marked as e-mailed": email_sent is true,
// email_binding_error says so, core is never asked to flag, and it is no error.
// HEAD kept such an invite emailed (its creator, an admin, vouched at accept);
// this is stricter, never wider.
func TestIdentityResend_NonAdminResendOfAFlaggedInvite(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	old := f.openInvite(t, "selina", "selina@example.com", true)
	id := bridgeIdentity(t)
	id.Admin = f.acc
	f.useIdentity(id, "alfred", "core:users.manage")
	w, data := f.do(t, http.MethodPost, "/users/invites/"+old+"/resend", nil)
	if w.Code != http.StatusOK || data["email_sent"] != true {
		t.Fatalf("resend: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(fmt.Sprint(data["email_binding_error"]), "not marked as e-mailed") {
		t.Fatalf("the admin must be told it was not marked: %v", data)
	}
	invv, _ := data["invite"].(map[string]any)
	if mustInvite(t, f.acc, fmt.Sprint(invv["id"])).rec.BindEmailOnAccept {
		t.Fatal("a non-admin's resend must not flag the invite")
	}
	for _, c := range f.acc.callLog() {
		if strings.HasPrefix(c, "mark ") && strings.HasSuffix(c, "as alfred") {
			t.Fatalf("core was asked to flag as a non-admin: %v", f.acc.callLog())
		}
	}
}

// TestIdentityResend_RateLimitedAcrossRotation: the per-invite limit survives
// the id change, so rotating cannot reset it; and a refused resend keeps the old
// link alive.
func TestIdentityResend_RateLimitedAcrossRotation(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	old := f.openInvite(t, "selina", "selina@example.com", true)
	w, data := f.do(t, http.MethodPost, "/users/invites/"+old+"/resend", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("first resend: %d", w.Code)
	}
	invv, _ := data["invite"].(map[string]any)
	rotated, _ := invv["id"].(string)
	// Immediately again, now by the rotated id: still inside the minute.
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+rotated+"/resend", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second resend within a minute of the first: %d, want 429 (rotation must not reset the limit)", w.Code)
	}
	if _, alive := f.acc.invite(rotated); !alive {
		t.Fatal("a rate-limited resend deleted the live link")
	}
	if n := len(f.rec.Sent()); n != 1 {
		t.Fatalf("sent %d, want 1", n)
	}
}

// TestIdentityResend_PerAdminCapSurvivesRotation: eleven resends by one admin
// across eleven invites: the eleventh is refused.
func TestIdentityResend_PerAdminCap(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	var last int
	// Create without sending, so only resends spend the admin's bucket.
	for i := range 11 {
		_, data := f.do(t, http.MethodPost, "/users/invite", createBody(fmt.Sprintf("g%d", i), fmt.Sprintf("g%d@example.com", i), false))
		invv, _ := data["invite"].(map[string]any)
		id, _ := invv["id"].(string)
		w, _ := f.do(t, http.MethodPost, "/users/invites/"+id+"/resend", nil)
		last = w.Code
		if i < 10 && w.Code != http.StatusOK {
			t.Fatalf("resend %d: %d", i, w.Code)
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("11th resend by one admin: %d, want 429", last)
	}
}

// TestIdentityResend_Refusals: missing, expired (HEAD parity: 404), no e-mail,
// mail off. None of them deletes anything.
func TestIdentityResend_Refusals(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	if w, _ := f.do(t, http.MethodPost, "/users/invites/nope/resend", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing invite: %d", w.Code)
	}
	_, data := f.do(t, http.MethodPost, "/users/invite", createBody("noemail", "", false))
	noEmail, _ := data["invite"].(map[string]any)
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+fmt.Sprint(noEmail["id"])+"/resend", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("no-email invite: %d", w.Code)
	}
	_, data = f.do(t, http.MethodPost, "/users/invite", createBody("expiring", "e@example.com", false))
	exp, _ := data["invite"].(map[string]any)
	f.clk.advance(100 * time.Hour)
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+fmt.Sprint(exp["id"])+"/resend", nil); w.Code != http.StatusNotFound {
		t.Fatalf("expired invite: %d, want 404", w.Code)
	}
	if _, alive := f.acc.invite(fmt.Sprint(exp["id"])); !alive {
		t.Fatal("a refused resend of an expired invite deleted it")
	}
	_, data = f.do(t, http.MethodPost, "/users/invite", createBody("mailoff", "m@example.com", false))
	mo, _ := data["invite"].(map[string]any)
	f.mailOn = false
	if w, _ := f.do(t, http.MethodPost, "/users/invites/"+fmt.Sprint(mo["id"])+"/resend", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("mail off: %d", w.Code)
	}
	if _, alive := f.acc.invite(fmt.Sprint(mo["id"])); !alive {
		t.Fatal("a resend refused for want of mail deleted the invite")
	}
}

// TestIdentityResend_NeverExtendsTheLifetime: the rotated invite expires when the
// old one would have.
func TestIdentityResend_NeverExtendsTheLifetime(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	old := f.openInvite(t, "selina", "selina@example.com", true)
	oldExp := mustInvite(t, f.acc, old).rec.ExpiresAt
	f.clk.advance(10 * time.Hour)
	_, data := f.do(t, http.MethodPost, "/users/invites/"+old+"/resend", nil)
	invv, _ := data["invite"].(map[string]any)
	got := mustInvite(t, f.acc, fmt.Sprint(invv["id"])).rec.ExpiresAt
	if !got.Equal(oldExp) {
		t.Fatalf("expiry %v, want the old invite's %v", got, oldExp)
	}
}

// TestIdentityResend_SendFailureAfterRotation: the rotated invite exists, is not
// newly flagged, and the admin gets the new link to hand over.
func TestIdentityResend_SendFailureAfterRotation(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	old := f.openInvite(t, "selina", "selina@example.com", true)
	f.rec.SetErr(errors.New("mail: 451 try later"))
	w, _ := f.do(t, http.MethodPost, "/users/invites/"+old+"/resend", nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "MAIL_SEND_FAILED") || !strings.Contains(w.Body.String(), "invite_url") {
		t.Fatalf("send failure after rotation: %d %s", w.Code, w.Body.String())
	}
	for id, v := range f.acc.invites {
		if id != old && v.rec.BindEmailOnAccept {
			t.Fatal("a failed send flagged the rotated invite")
		}
	}
	for _, c := range f.acc.callLog() {
		if strings.HasPrefix(c, "mark ") {
			t.Fatalf("mark called after a failed resend send: %v", f.acc.callLog())
		}
	}
}

// TestIdentityResend_FlagFollowsTheOldInvite: an admin resending a non-admin's
// emailed invite (never flagged) does not bind where HEAD's would not; an admin
// resending an admin's flagged invite keeps the flag.
func TestIdentityResend_FlagFollowsTheOldInvite(t *testing.T) {
	// The non-admin creates and emails: no flag.
	f := newIdentityInviteFixture(t, "core:users.manage")
	f.asUser(t, "bruce", "admin")
	old := f.openInvite(t, "selina", "selina@example.com", false)
	if mustInvite(t, f.acc, old).rec.BindEmailOnAccept {
		t.Fatal("setup: a non-admin's invite must not be flagged")
	}
	// An admin now resends it.
	id := bridgeIdentity(t)
	id.Admin = f.acc
	f.useIdentity(id, "alfred", rbac.CoreAdmin, "core:users.manage")
	w, data := f.do(t, http.MethodPost, "/users/invites/"+old+"/resend", nil)
	if w.Code != http.StatusOK || data["email_sent"] != true {
		t.Fatalf("resend: %d %s", w.Code, w.Body.String())
	}
	invv, _ := data["invite"].(map[string]any)
	nv := mustInvite(t, f.acc, fmt.Sprint(invv["id"]))
	if nv.rec.BindEmailOnAccept {
		t.Fatal("an admin's resend of a never-flagged invite must not flag it (it would verify an address HEAD's would not)")
	}
	for _, c := range f.acc.callLog() {
		if strings.HasPrefix(c, "mark ") && strings.HasSuffix(c, "as alfred") {
			t.Fatalf("mark called for an unflagged old invite: %v", f.acc.callLog())
		}
	}
	// The creator-admin's flagged invite, resent by an admin, stays flagged.
	g := newIdentityInviteFixture(t, rbac.CoreAdmin)
	g.asUser(t, "bruce", "admin")
	flagged := g.openInvite(t, "harvey", "harvey@example.com", true)
	if !mustInvite(t, g.acc, flagged).rec.BindEmailOnAccept {
		t.Fatal("setup: an admin's emailed invite must be flagged")
	}
	_, data = g.do(t, http.MethodPost, "/users/invites/"+flagged+"/resend", nil)
	invv, _ = data["invite"].(map[string]any)
	if !mustInvite(t, g.acc, fmt.Sprint(invv["id"])).rec.BindEmailOnAccept {
		t.Fatal("an admin's resend of a flagged invite must keep it flagged")
	}
}

// TestIdentityListAndDeleteInvites: the list is core's (never a token, even with
// authentication off), carries the delivery record's sent-at, and delete goes to
// core and takes the delivery record with it.
func TestIdentityListAndDeleteInvites(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	id := f.openInvite(t, "selina", "selina@example.com", true)
	w, _ := f.do(t, http.MethodGet, "/users/invites", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, id) || !strings.Contains(body, "email_sent_at") || strings.Contains(body, "tok-1") || strings.Contains(body, `"token"`) {
		t.Fatalf("list must name the invite and its send and carry no token: %s", body)
	}
	if w, _ := f.do(t, http.MethodDelete, "/users/invites/"+id, nil); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	if _, alive := f.acc.invite(id); alive {
		t.Fatal("delete must go to core")
	}
	if rec, _ := f.store.GetInviteMail(id); rec != nil {
		t.Fatal("the delivery record must go with the invite")
	}
	if w, _ := f.do(t, http.MethodDelete, "/users/invites/"+id, nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete again: %d, want 404", w.Code)
	}
}

// TestIdentityListInvites_SweepsOnlyExpiredDeliveryRecords: a record is dropped
// only a day after its invite expired, never because it is missing from a list.
func TestIdentityListInvites_SweepsOnlyExpiredDeliveryRecords(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	f.asUser(t, "bruce", "admin")
	now := f.clk.now()
	_ = f.store.PutInviteMail(store.InviteMailRecord{InviteID: "gone-long-ago", To: "a@example.com", SentAt: now.Add(-100 * time.Hour), ExpiresAt: now.Add(-48 * time.Hour)})
	_ = f.store.PutInviteMail(store.InviteMailRecord{InviteID: "just-made", To: "b@example.com", SentAt: now, ExpiresAt: now.Add(time.Hour)})
	if w, _ := f.do(t, http.MethodGet, "/users/invites", nil); w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	if r, _ := f.store.GetInviteMail("gone-long-ago"); r != nil {
		t.Fatal("a record of an invite expired two days ago must be swept")
	}
	if r, _ := f.store.GetInviteMail("just-made"); r == nil {
		t.Fatal("a record of a live invite absent from core's list snapshot must not be swept")
	}
}

// TestIdentitySetUserEmail: the write goes to core with the request's principal,
// the response is the account as core holds it, an address another account holds
// verified is 409, and books' own store is not written.
func TestIdentitySetUserEmail(t *testing.T) {
	f := newIdentityInviteFixture(t, rbac.CoreAdmin)
	admin := f.asUser(t, "bruce", "admin")
	_ = admin
	f.acc.users["u-target"] = &coreauth.User{ID: "u-target", Username: "dick", Email: "dick@example.com", Status: coreauth.StatusActive}
	f.acc.users["u-holder"] = &coreauth.User{ID: "u-holder", Username: "jason", Email: "robin@example.com", EmailVerified: true, Status: coreauth.StatusActive}
	w, data := f.do(t, http.MethodPut, "/users/u-target/email", map[string]any{"email": " Nightwing@Example.com "})
	if w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	user, _ := data["user"].(map[string]any)
	if user["email"] != "nightwing@example.com" {
		t.Fatalf("response user = %v", user)
	}
	if !f.acc.users["u-target"].EmailVerified {
		t.Fatal("core must have bound the address verified")
	}
	if got := f.acc.callLog(); len(got) == 0 || got[0] != "setemail u-target nightwing@example.com as bruce" {
		t.Fatalf("calls = %v", got)
	}
	if w, _ := f.do(t, http.MethodPut, "/users/u-target/email", map[string]any{"email": "robin@example.com"}); w.Code != http.StatusConflict {
		t.Fatalf("an address another account holds verified: %d, want 409", w.Code)
	}
	if w, _ := f.do(t, http.MethodPut, "/users/u-missing/email", map[string]any{"email": "x@example.com"}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d, want 404", w.Code)
	}
	if w, _ := f.do(t, http.MethodPut, "/users/u-target/email", map[string]any{"email": "a\r\nb@example.com"}); w.Code != http.StatusBadRequest {
		t.Fatalf("a malformed address: %d, want 400", w.Code)
	}
	f.acc.users["u-target"].EmailVerified = true
	if w, _ := f.do(t, http.MethodPut, "/users/u-target/email", map[string]any{"email": ""}); w.Code != http.StatusOK || f.acc.users["u-target"].Email != "" {
		t.Fatalf("clear: %d, email %q", w.Code, f.acc.users["u-target"].Email)
	}
}

// TestIdentitySetUserEmail_KeepsHeadsGates: the books-side admin gates run before
// the core call, so a caller without core:admin never reaches core.
func TestIdentitySetUserEmail_KeepsHeadsGates(t *testing.T) {
	f := newIdentityInviteFixture(t) // principal holds nothing
	f.asUser(t, "bruce", "admin")
	f.acc.users["u-target"] = &coreauth.User{ID: "u-target", Username: "dick", Status: coreauth.StatusActive}
	if w, _ := f.do(t, http.MethodPut, "/users/u-target/email", map[string]any{"email": "x@example.com"}); w.Code != http.StatusForbidden {
		t.Fatalf("no core:admin: %d, want 403", w.Code)
	}
	for _, c := range f.acc.callLog() {
		if strings.HasPrefix(c, "setemail") {
			t.Fatalf("core was called past the gate: %v", f.acc.callLog())
		}
	}
	// A refusal from core itself (dominance) is a 403 as well.
	g := newIdentityInviteFixture(t, rbac.CoreAdmin)
	g.asUser(t, "bruce", "admin")
	g.acc.users["u-target"] = &coreauth.User{ID: "u-target", Username: "dick", Status: coreauth.StatusActive}
	g.acc.setEmailErr = coreauth.ErrDominance
	if w, _ := g.do(t, http.MethodPut, "/users/u-target/email", map[string]any{"email": "x@example.com"}); w.Code != http.StatusForbidden {
		t.Fatalf("core's dominance refusal: %d, want 403", w.Code)
	}
}
