// file: core/internal/confighttp/env_test.go
// version: 1.0.1
// guid: aee43407-160f-4883-ad9b-997aadb09143
// last-edited: 2026-11-11

package confighttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Fixture keys.
const (
	kLevel   config.Path = configtest.BatcavePath + ".level"
	kWorkers config.Path = configtest.BatcavePath + ".workers"
	kPath    config.Path = configtest.BatcavePath + ".path"
	kSignal  config.Path = configtest.BatcavePath + ".signal"
	kAPIKey  config.Path = configtest.BatcavePath + ".api_key"
	kMin     config.Path = configtest.WatchPath + ".min"
)

// The permissions the cases hold. permManage is config's default section
// permission for a core section, permGotham the one for modules.gotham.*.
const (
	permManage rbac.Permission = "core:settings.manage"
	permGotham rbac.Permission = "gotham:settings.manage"
)

// checker is an rbac.Checker holding exactly its permissions.
type checker []rbac.Permission

func (c checker) Has(p rbac.Permission) bool { return slices.Contains(c, p) }
func (c checker) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindPublic:
		return true
	case rbac.KindAuthenticated:
		return len(c) > 0 // core:self or a non-Confining scope (05-rbac 4.10)
	case rbac.KindAllOf:
		return len(ps) > 0 && !slices.ContainsFunc(ps, func(p rbac.Permission) bool { return !c.Has(p) })
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, c.Has)
	}
	return false
}
func (checker) Confined() bool                { return false }
func (c checker) Concrete() []rbac.Permission { return slices.Clone(c) }

// confinedChecker is a Confined key's checker: it names scopes, never
// satisfies AnyAuthenticated, and holds none of the settings scopes.
type confinedChecker struct{ checker }

func (confinedChecker) Satisfies(r rbac.Requirement) bool { return r.Kind() == rbac.KindPublic }
func (confinedChecker) Confined() bool                    { return true }

// The principals the cases act as.
const (
	bruceID = "01GOTHAMBRUCEWAYNE0000000"
	dickID  = "01GOTHAMDICKGRAYSON0000000"
)

// as is a principal authenticated by m holding perms. Every method but
// MethodAuthDisabled gets a user.
func as(m auth.Method, perms ...rbac.Permission) *auth.Principal {
	p := &auth.Principal{Method: m, Perms: checker(perms), CredentialID: "cred-gotham", ClientAddr: addr("192.0.2.7")}
	if m != auth.MethodAuthDisabled {
		p.User = &auth.User{ID: bruceID, Username: "bruce.wayne"}
	}
	return p
}

// admin is an interactive session of an administrator: every section's manage
// permission and core:admin.
func admin() *auth.Principal {
	return as(auth.MethodSession, permManage, permGotham, rbac.CoreAdmin)
}

// manager holds the section permissions but not core:admin.
func manager() *auth.Principal { return as(auth.MethodSession, permManage, permGotham) }

// readPerm is the kernel's SectionReadPerm over the fixture sections;
// core.modules is deliberately unknown to it.
func readPerm(section config.Path) (rbac.Permission, bool) {
	switch section {
	case configtest.BatcavePath:
		return permManage, true
	case configtest.WatchPath:
		return permGotham, true
	}
	return "", false
}

// rig is a real config.Store over the fixture sections and the in-memory
// doubles, with the routes over it.
type rig struct {
	t    *testing.T
	st   *config.Store
	cm   *configtest.Committer
	cave *config.Handle[configtest.Batcave]
	api  *api
	logs *bytes.Buffer
}

// newRig builds, boots and attaches the store. o carries the operator layers
// a case needs (a lock).
func newRig(t *testing.T, o config.Options) *rig {
	t.Helper()
	dir := t.TempDir()
	if o.AppID == "" {
		o.AppID = "gotham"
	}
	o.Logger = slog.New(slog.DiscardHandler)
	o.BootRecordRoot = func(string) (*os.Root, error) { return os.OpenRoot(dir) }
	st, err := config.NewStore(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	r := &rig{t: t, st: st, logs: &bytes.Buffer{}}
	if r.cave, err = config.Register(st.Registry(), configtest.BatcaveSection()); err != nil {
		t.Fatal(err)
	}
	if _, err = config.Register(st.Registry(), configtest.WatchSection()); err != nil {
		t.Fatal(err)
	}
	if _, err = config.Register(st.Registry(), configtest.ModulesSection(map[string]configtest.Module{"batman": {Enabled: true}})); err != nil {
		t.Fatal(err)
	}
	mem := configtest.NewMemory()
	r.cm = configtest.NewCommitter(mem)
	ctx := context.Background()
	if _, err := st.Boot(ctx); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if err := st.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := st.Attach(ctx, mem, r.cm); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	r.api = &api{store: st, read: readPerm, log: slog.New(slog.NewTextHandler(r.logs, nil))}
	return r
}

// reply is a recorded response.
type reply struct {
	status      int
	contentType string
	body        string
}

// patch sends body as PATCH /settings as p (nil: no principal bound).
func (r *rig) patch(p *auth.Principal, body string) reply {
	r.t.Helper()
	return r.patchWith(p, "application/json", body)
}

func (r *rig) patchWith(p *auth.Principal, contentType, body string) reply {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/core/v1/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "batcomputer/1.0")
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	rec := httptest.NewRecorder()
	r.api.patch(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return reply{status: res.StatusCode, contentType: res.Header.Get("Content-Type"), body: string(b)}
}

// problemOf decodes a problem body, extension members included.
func problemOf(t *testing.T, rep reply) problemBody {
	t.Helper()
	var pb problemBody
	if err := json.Unmarshal([]byte(rep.body), &pb); err != nil {
		t.Fatalf("body is not a problem: %v: %s", err, rep.body)
	}
	return pb
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

// wantStatus fails t unless err is an *httpapi.Error with status and code.
func wantStatus(t testing.TB, err error, status int, code string) {
	t.Helper()
	he, ok := errors.AsType[*httpapi.Error](err)
	if !ok {
		t.Errorf("err = %v (%T), want an *httpapi.Error %d %s", err, err, status, code)
		return
	}
	if he.Status != status || he.Code != code {
		t.Errorf("err = %d %s, want %d %s", he.Status, he.Code, status, code)
	}
}

// recStore is a Store double that records what the routes pass Apply and
// answers it with a canned result.
type recStore struct {
	Store
	mu    sync.Mutex
	calls int
	patch config.Patch
	actor config.Actor
	ctx   context.Context
	res   config.Result
	err   error
}

func (s *recStore) Apply(ctx context.Context, p config.Patch, a config.Actor) (config.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.patch, s.actor, s.ctx = p, a, ctx
	return s.res, s.err
}

// newPatchRequest is a PATCH /settings request carrying body.
func newPatchRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, "/api/core/v1/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }
