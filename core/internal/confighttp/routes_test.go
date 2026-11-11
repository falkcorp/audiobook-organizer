// file: core/internal/confighttp/routes_test.go
// version: 1.0.0
// guid: 6f580d63-302c-489d-b44a-681a95e044f6
// last-edited: 2026-11-10

package confighttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/secrets"
)

// secretValue is the value the cases store under the secret key; it must
// never appear in any body or log line.
const secretValue = "wayne-tech-vault-key"

// TestSettingsRoutes_StatusCodes pins PATCH /settings' answer to each way
// Apply can end (05-config §1.3.4): 400 for an unknown key, 409 locked with
// locked_by, 403 for a missing section permission and for a protected key
// with need, 422 with one error per key, 500 for a storage failure with the
// cause withheld, and 200 with restart_pending and post_commit_error.
func TestSettingsRoutes_StatusCodes(t *testing.T) {
	t.Run("400 unknown key", func(t *testing.T) {
		r := newRig(t, config.Options{})
		rep := r.patch(admin(), `{"set":{"core.batcave.robin_cape":"red"}}`)
		if rep.status != http.StatusBadRequest {
			t.Fatalf("status = %d: %s", rep.status, rep.body)
		}
		pb := problemOf(t, rep)
		if pb.Code != "unknown_key" || pb.Type != problemTypeBase+"unknown_key" || pb.Instance != "/api/core/v1/settings" {
			t.Errorf("problem = %+v", pb)
		}
		if rep.contentType != "application/problem+json" {
			t.Errorf("content type = %q", rep.contentType)
		}
		if strings.Contains(rep.body, "robin_cape") {
			t.Errorf("the key the caller typed is echoed: %s", rep.body)
		}
	})

	t.Run("409 locked carries locked_by", func(t *testing.T) {
		r := newRig(t, config.Options{Environ: []string{"GOTHAM_CORE_BATCAVE_LEVEL=debug"}})
		before := r.cave.Get()
		rep := r.patch(admin(), `{"set":{"core.batcave.level":"warn","core.batcave.signal":"owl"}}`)
		if rep.status != http.StatusConflict {
			t.Fatalf("status = %d: %s", rep.status, rep.body)
		}
		pb := problemOf(t, rep)
		if pb.Code != "locked" || pb.LockedBy[string(kLevel)] != "env GOTHAM_CORE_BATCAVE_LEVEL" || len(pb.LockedBy) != 1 {
			t.Errorf("problem = %+v, want locked_by {level: env GOTHAM_CORE_BATCAVE_LEVEL}", pb)
		}
		if r.cm.Calls() != 0 || r.cave.Get() != before {
			t.Error("a refused patch changed state")
		}
	})

	t.Run("403 section permission carries need", func(t *testing.T) {
		r := newRig(t, config.Options{})
		rep := r.patch(as(auth.MethodSession), `{"set":{"core.batcave.level":"warn"}}`)
		if rep.status != http.StatusForbidden {
			t.Fatalf("status = %d: %s", rep.status, rep.body)
		}
		pb := problemOf(t, rep)
		if pb.Code != "permission_denied" || pb.Need[string(kLevel)] != string(permManage) || pb.Required != string(permManage) {
			t.Errorf("problem = %+v, want need {level: core:settings.manage}", pb)
		}
	})

	t.Run("403 protected key carries need", func(t *testing.T) {
		r := newRig(t, config.Options{})
		rep := r.patch(manager(), `{"set":{"core.batcave.path":"/batcave/annex"}}`)
		if rep.status != http.StatusForbidden {
			t.Fatalf("status = %d: %s", rep.status, rep.body)
		}
		pb := problemOf(t, rep)
		if pb.Code != "protection_required" || pb.Need[string(kPath)] != "admin" {
			t.Errorf("problem = %+v, want need {path: admin}", pb)
		}
		if r.cm.Calls() != 0 {
			t.Error("a refused patch committed")
		}
	})

	t.Run("403 interactive key refused to an API key", func(t *testing.T) {
		r := newRig(t, config.Options{})
		rep := r.patch(as(auth.MethodAPIKey, permManage, permGotham, rbac.CoreAdmin), `{"set":{"core.batcave.signal":"owl"}}`)
		if rep.status != http.StatusForbidden || problemOf(t, rep).Need[string(kSignal)] != "interactive" {
			t.Errorf("reply = %d %s, want 403 with need {signal: interactive}", rep.status, rep.body)
		}
	})

	t.Run("422 one error per key", func(t *testing.T) {
		r := newRig(t, config.Options{})
		rep := r.patch(admin(), `{"set":{"core.batcave.level":"loud","core.batcave.workers":0}}`)
		if rep.status != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d: %s", rep.status, rep.body)
		}
		pb := problemOf(t, rep)
		var at []string
		for _, e := range pb.Errors {
			at = append(at, e.Field)
			if e.Code != "invalid" || e.Message == "" {
				t.Errorf("field error = %+v", e)
			}
		}
		slices.Sort(at)
		want := []string{"/body/set/" + string(kLevel), "/body/set/" + string(kWorkers)}
		slices.Sort(want)
		if pb.Code != "validation_failed" || !slices.Equal(at, want) {
			t.Errorf("problem = %+v, error fields %v, want %v", pb, at, want)
		}
	})

	t.Run("422 cross-field validator error is pointed at its key", func(t *testing.T) {
		r := newRig(t, config.Options{})
		// min above max is the Watch validator's refusal, attributed to min.
		rep := r.patch(admin(), `{"set":{"modules.gotham.watch.min":99}}`)
		pb := problemOf(t, rep)
		if rep.status != http.StatusUnprocessableEntity || len(pb.Errors) != 1 || pb.Errors[0].Field != "/body/set/"+string(kMin) {
			t.Errorf("reply = %d %s", rep.status, rep.body)
		}
	})

	t.Run("422 secret error carries a fixed message", func(t *testing.T) {
		r := newRig(t, config.Options{})
		rep := r.patch(admin(), `{"set":{"core.batcave.api_key":42}}`)
		pb := problemOf(t, rep)
		if rep.status != http.StatusUnprocessableEntity || len(pb.Errors) != 1 || pb.Errors[0].Message != "the value is not valid for this secret key" {
			t.Errorf("reply = %d %s", rep.status, rep.body)
		}
	})

	t.Run("500 storage failure withholds the cause", func(t *testing.T) {
		r := newRig(t, config.Options{})
		r.cm.Fail(errors.New("pebble: batcave vault disk is on fire " + secretValue))
		rep := r.patch(admin(), `{"set":{"core.batcave.level":"warn"}}`)
		if rep.status != http.StatusInternalServerError {
			t.Fatalf("status = %d: %s", rep.status, rep.body)
		}
		if strings.Contains(rep.body, "on fire") || strings.Contains(rep.body, secretValue) {
			t.Errorf("the storage error is in the body: %s", rep.body)
		}
		if r.cave.Get().Level != "info" {
			t.Error("a failed commit changed the live value")
		}
	})

	t.Run("200 carries generation, applied and restart_pending", func(t *testing.T) {
		r := newRig(t, config.Options{})
		rep := r.patch(admin(), `{"set":{"core.batcave.level":"warn","core.batcave.workers":8}}`)
		if rep.status != http.StatusOK || rep.contentType != "application/json" {
			t.Fatalf("reply = %d %s: %s", rep.status, rep.contentType, rep.body)
		}
		var got SettingsResponse
		if err := json.Unmarshal([]byte(rep.body), &got); err != nil {
			t.Fatal(err)
		}
		slices.Sort(got.Applied)
		if got.Generation != 1 || !slices.Equal(got.Applied, []string{string(kLevel), string(kWorkers)}) ||
			!slices.Equal(got.RestartPending, []string{string(kWorkers)}) || len(got.PostCommitError) != 0 {
			t.Errorf("response = %+v", got)
		}
		if strings.Contains(rep.body, "post_commit_error") {
			t.Errorf("an absent post_commit_error is in the body: %s", rep.body)
		}
		if r.cave.Get().Level != "warn" {
			t.Errorf("level = %q, want warn", r.cave.Get().Level)
		}
	})

	t.Run("200 without restart keys omits restart_pending", func(t *testing.T) {
		r := newRig(t, config.Options{})
		rep := r.patch(admin(), `{"set":{"core.batcave.level":"warn"}}`)
		if rep.status != http.StatusOK || strings.Contains(rep.body, "restart_pending") {
			t.Errorf("reply = %d %s", rep.status, rep.body)
		}
	})

	t.Run("200 carries post_commit_error without the hook's text", func(t *testing.T) {
		r := newRig(t, config.Options{})
		r.cave.AfterCommit(func(context.Context, *configtest.Batcave, *configtest.Batcave) error {
			return errors.New("joker was here " + secretValue)
		})
		rep := r.patch(admin(), `{"set":{"core.batcave.level":"warn"}}`)
		if rep.status != http.StatusOK {
			t.Fatalf("status = %d: %s", rep.status, rep.body)
		}
		var got SettingsResponse
		if err := json.Unmarshal([]byte(rep.body), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.PostCommitError) != 1 || got.PostCommitError[0].Section != string(configtest.BatcavePath) || got.PostCommitError[0].Message != postCommitMessage {
			t.Errorf("post_commit_error = %+v", got.PostCommitError)
		}
		if strings.Contains(rep.body, "joker") || strings.Contains(rep.body, secretValue) {
			t.Errorf("the hook's text is in the body: %s", rep.body)
		}
		if r.cave.Get().Level != "warn" {
			t.Error("the change is not committed")
		}
	})

	t.Run("an empty patch is a 200", func(t *testing.T) {
		r := newRig(t, config.Options{})
		rep := r.patch(admin(), `{}`)
		if rep.status != http.StatusOK {
			t.Errorf("reply = %d %s", rep.status, rep.body)
		}
	})
}

// TestSettingsRoutes_SecretsNeverLeave: a secret stored through PATCH shows
// in no answer, GET /settings shows its meta without a value, a mask, a hash
// or a fingerprint, and the logs never see it either.
func TestSettingsRoutes_SecretsNeverLeave(t *testing.T) {
	r := newRig(t, config.Options{})
	var seen []string
	rep := r.patch(admin(), fmt.Sprintf(`{"set":{"core.batcave.api_key":%q,"core.batcave.level":"warn"}}`, secretValue))
	if rep.status != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", rep.status, rep.body)
	}
	seen = append(seen, rep.body)

	view, err := r.api.getSettings(context.Background(), &httpapi.In[noReq]{Principal: admin()})
	if err != nil {
		t.Fatal(err)
	}
	ks, ok := view[string(kAPIKey)]
	if !ok {
		t.Fatalf("GET /settings has no %s: %v", kAPIKey, view)
	}
	if ks.Secret == nil || !ks.Secret.Set || len(ks.Effective) != 0 || ks.Secret.Unavailable {
		t.Errorf("secret key view = %+v, want a set secret with no effective value", ks)
	}
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, string(b))

	// A refused patch that names the secret answers without it too.
	for _, body := range []string{
		fmt.Sprintf(`{"set":{"core.batcave.api_key":%q,"core.batcave.robin_cape":1}}`, secretValue),
		fmt.Sprintf(`{"set":{"core.batcave.api_key":%q}}`, secretValue) + ` trailing`,
		fmt.Sprintf(`{"set":{"core.batcave.api_key":%q},"extra":1}`, secretValue),
		fmt.Sprintf(`{"set":[%q]}`, secretValue),
	} {
		seen = append(seen, r.patch(admin(), body).body)
	}
	r.cm.Fail(errors.New("commit failed"))
	seen = append(seen, r.patch(admin(), fmt.Sprintf(`{"set":{"core.batcave.api_key":%q}}`, secretValue)).body)
	seen = append(seen, r.logs.String())

	for i, s := range seen {
		for _, leak := range []string{secretValue, `"fingerprint"`, `"key_id"`, `"mask"`, `"value"`} {
			if strings.Contains(s, leak) {
				t.Errorf("answer %d leaks %q: %s", i, leak, s)
			}
		}
	}
}

// TestGetSettings_FiltersBySectionReadPerm: a caller sees the keys of the
// sections whose read permission it holds and nothing else, and a section
// the kernel's SectionReadPerm does not know is unreadable.
func TestGetSettings_FiltersBySectionReadPerm(t *testing.T) {
	r := newRig(t, config.Options{})
	get := func(p *auth.Principal) SettingsView {
		t.Helper()
		v, err := r.api.getSettings(context.Background(), &httpapi.In[noReq]{Principal: p})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	sections := func(v SettingsView) []string {
		seen := map[string]bool{}
		for _, ks := range v {
			seen[ks.Section] = true
		}
		var out []string
		for s := range seen {
			out = append(out, s)
		}
		slices.Sort(out)
		return out
	}
	cases := []struct {
		name string
		p    *auth.Principal
		want []string
	}{
		{"both", manager(), []string{string(configtest.BatcavePath), string(configtest.WatchPath)}},
		{"core only", as(auth.MethodSession, permManage), []string{string(configtest.BatcavePath)}},
		{"module only", as(auth.MethodSession, permGotham), []string{string(configtest.WatchPath)}},
		{"neither", as(auth.MethodSession, rbac.CoreAdmin), nil},
		{"no checker", &auth.Principal{Method: auth.MethodSession, User: &auth.User{ID: dickID}}, nil},
	}
	for _, c := range cases {
		if got := sections(get(c.p)); !slices.Equal(got, c.want) {
			t.Errorf("%s: sections = %v, want %v", c.name, got, c.want)
		}
	}

	v := get(manager())
	lvl, ok := v[string(kLevel)]
	if !ok || lvl.Path != string(kLevel) || lvl.Source != "default" || lvl.Reload != "live" || lvl.Protection != "none" || string(lvl.Effective) != `"info"` || len(lvl.Schema) == 0 {
		t.Errorf("level view = %+v", lvl)
	}
	if w := v[string(kWorkers)]; w.Reload != "restart" {
		t.Errorf("workers reload = %q", w.Reload)
	}

	r.api.read = nil
	if got := get(manager()); len(got) != 0 {
		t.Errorf("a nil ReadPerm exposes %d keys, want 0", len(got))
	}
}

// TestGetSettings_ShowsLockPendingAndSource: a key set by the operator names
// its layer, and a restart key changed since boot is pending.
func TestGetSettings_ShowsLockPendingAndSource(t *testing.T) {
	r := newRig(t, config.Options{Environ: []string{"GOTHAM_CORE_BATCAVE_LEVEL=debug"}})
	if rep := r.patch(admin(), `{"set":{"core.batcave.workers":8}}`); rep.status != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", rep.status, rep.body)
	}
	v, err := r.api.getSettings(context.Background(), &httpapi.In[noReq]{Principal: manager()})
	if err != nil {
		t.Fatal(err)
	}
	if k := v[string(kLevel)]; k.Source != "env" || k.LockedBy != "env GOTHAM_CORE_BATCAVE_LEVEL" || string(k.Effective) != `"debug"` {
		t.Errorf("level = %+v", k)
	}
	if k := v[string(kWorkers)]; !k.Pending || k.Source == "" {
		t.Errorf("workers = %+v, want pending", k)
	}
}

// TestGetSchema_FiltersBySectionReadPerm: the schema holds one property per
// section the caller may read.
func TestGetSchema_FiltersBySectionReadPerm(t *testing.T) {
	r := newRig(t, config.Options{})
	props := func(p *auth.Principal) []string {
		t.Helper()
		raw, err := r.api.getSchema(context.Background(), &httpapi.In[noReq]{Principal: p})
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("schema is not JSON: %v", err)
		}
		var out []string
		for k := range doc.Properties {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	all, core, none := props(manager()), props(as(auth.MethodSession, permManage)), props(as(auth.MethodSession))
	if len(all) == 0 || len(core) == 0 || len(core) >= len(all) || len(none) != 0 {
		t.Errorf("properties: both %v, core only %v, none %v", all, core, none)
	}
	r.api.read = nil
	if got := props(manager()); len(got) != 0 {
		t.Errorf("a nil ReadPerm exposes %v", got)
	}
}

// TestHandlers_FailClosedWithoutPrincipalOrStore: with no principal every
// handler answers 401, with no store 503 (never a panic, never an open read).
func TestHandlers_FailClosedWithoutPrincipalOrStore(t *testing.T) {
	r := newRig(t, config.Options{})
	ctx := context.Background()
	if _, err := r.api.getSettings(ctx, &httpapi.In[noReq]{}); err == nil {
		t.Error("getSettings without a principal succeeded")
	} else {
		wantStatus(t, err, http.StatusUnauthorized, "authentication_required")
	}
	if _, err := r.api.getSchema(ctx, &httpapi.In[noReq]{}); err == nil {
		t.Error("getSchema without a principal succeeded")
	} else {
		wantStatus(t, err, http.StatusUnauthorized, "authentication_required")
	}
	if rep := r.patch(nil, `{}`); rep.status != http.StatusUnauthorized || problemOf(t, rep).Code != "authentication_required" {
		t.Errorf("PATCH without a principal = %d %s", rep.status, rep.body)
	}

	r.api.store = nil
	if _, err := r.api.getSettings(ctx, &httpapi.In[noReq]{Principal: admin()}); err == nil {
		t.Error("getSettings without a store succeeded")
	} else {
		wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
	}
	if _, err := r.api.getSchema(ctx, &httpapi.In[noReq]{Principal: admin()}); err == nil {
		t.Error("getSchema without a store succeeded")
	} else {
		wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
	}
	if rep := r.patch(admin(), `{}`); rep.status != http.StatusServiceUnavailable {
		t.Errorf("PATCH without a store = %d %s", rep.status, rep.body)
	}
}

// TestPatch_ActorIsFilledFromThePrincipal: Apply gets the principal's subject,
// its credential's interactivity, the owner assurance and a Can over its
// checker; the context carries the audit actor config's committer matches
// against the subject.
func TestPatch_ActorIsFilledFromThePrincipal(t *testing.T) {
	owner := as(auth.MethodSession, permManage)
	owner.Assurance = auth.AssuranceOwner
	cases := []struct {
		name        string
		p           *auth.Principal
		subject     string
		interactive bool
		owner       bool
		can, cannot rbac.Permission
	}{
		{"session", as(auth.MethodSession, permManage), bruceID, true, false, permManage, rbac.CoreAdmin},
		{"delegated session", as(auth.MethodSessionDelegated, permManage), bruceID, true, false, permManage, rbac.CoreAdmin},
		{"api key", as(auth.MethodAPIKey, permManage), bruceID, false, false, permManage, rbac.CoreAdmin},
		{"owner", owner, bruceID, true, true, permManage, rbac.CoreAdmin},
		{"auth disabled", as(auth.MethodAuthDisabled, permManage), authDisabledSubject, false, false, permManage, rbac.CoreAdmin},
		{"no checker", &auth.Principal{Method: auth.MethodSession, User: &auth.User{ID: dickID}}, dickID, true, false, "", permManage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := &recStore{res: config.Result{Generation: 3, Applied: []config.Path{kLevel}}}
			a := &api{store: rs}
			rr := rigOver(t, a)
			rep := rr.patch(c.p, `{"set":{"core.batcave.level":"warn"},"unset":["core.batcave.signal"]}`)
			if rep.status != http.StatusOK || rs.calls != 1 {
				t.Fatalf("reply = %d %s, %d Apply calls", rep.status, rep.body, rs.calls)
			}
			if rs.actor.Subject != c.subject || rs.actor.Interactive != c.interactive || rs.actor.Owner != c.owner {
				t.Errorf("actor = {%q %v %v}, want {%q %v %v}", rs.actor.Subject, rs.actor.Interactive, rs.actor.Owner, c.subject, c.interactive, c.owner)
			}
			if c.can != "" && !rs.actor.Can(string(c.can)) {
				t.Errorf("Can(%s) = false", c.can)
			}
			if rs.actor.Can(string(c.cannot)) {
				t.Errorf("Can(%s) = true", c.cannot)
			}
			if string(rs.patch.Set[kLevel]) != `"warn"` || !slices.Equal(rs.patch.Unset, []config.Path{kSignal}) {
				t.Errorf("patch = %+v", rs.patch)
			}
			wantAddr := ""
			if c.p.ClientAddr.IsValid() {
				wantAddr = c.p.ClientAddr.String()
			}
			au, ok := audit.ActorFrom(rs.ctx)
			if !ok || au.UserID != c.subject || au.UserAgent != "batcomputer/1.0" || au.ClientAddr != wantAddr || au.CredentialID != c.p.CredentialID || au.Method != string(c.p.Method) {
				t.Errorf("audit actor = %+v (bound %v)", au, ok)
			}
		})
	}
}

// rigOver wraps a hand-built api so its patch helper can be used.
func rigOver(t *testing.T, a *api) *rig { return &rig{t: t, api: a} }

// TestPatch_KeepsTheRequestsAuditActor: a context that already carries the
// actor for this subject keeps it (it holds what stage 12 saw), and one that
// names another user is replaced, since the committer refuses a mismatch.
func TestPatch_KeepsTheRequestsAuditActor(t *testing.T) {
	rs := &recStore{}
	rr := rigOver(t, &api{store: rs})
	send := func(actorID string) audit.Actor {
		t.Helper()
		req := newPatchRequest(`{}`)
		ctx := auth.WithPrincipal(req.Context(), admin())
		ctx = audit.WithActor(ctx, audit.Actor{UserID: actorID, Username: "stage12", UserAgent: "from-stage-12"})
		rr.api.patch(newRecorder(), req.WithContext(ctx))
		got, _ := audit.ActorFrom(rs.ctx)
		return got
	}
	if got := send(bruceID); got.UserAgent != "from-stage-12" {
		t.Errorf("a matching actor was rebuilt: %+v", got)
	}
	if got := send(dickID); got.UserID != bruceID || got.Username != "bruce.wayne" {
		t.Errorf("a mismatched actor was kept: %+v", got)
	}
}

// TestPatch_RefusesWhatHasNoSubject: a principal that names nobody (no user,
// not the auth-off method) is refused 401 before Apply, since a commit with no
// actor is not audited. The auth-off principal is not: it acts as
// authDisabledSubject, and config's own protection keeps it below ProtectAdmin.
func TestPatch_RefusesWhatHasNoSubject(t *testing.T) {
	rs := &recStore{}
	rr := rigOver(t, &api{store: rs})
	for _, p := range []*auth.Principal{
		{Method: auth.MethodSession, Perms: checker{permManage}},
		{Method: auth.MethodSession, User: &auth.User{Username: "no.id"}, Perms: checker{permManage}},
		{Method: auth.MethodAPIKey, Perms: checker{permManage}},
	} {
		rep := rr.patch(p, `{}`)
		if rep.status != http.StatusUnauthorized || rs.calls != 0 {
			t.Errorf("principal %+v: reply %d, %d Apply calls, want 401 and none", p, rep.status, rs.calls)
		}
	}
}

func TestAuthDisabled_CannotReachProtectedKeys(t *testing.T) {
	r := newRig(t, config.Options{})
	off := as(auth.MethodAuthDisabled, permManage, permGotham)
	if rep := r.patch(off, `{"set":{"core.batcave.workers":9}}`); rep.status != http.StatusOK {
		t.Errorf("auth-off patch of an unprotected key = %d %s", rep.status, rep.body)
	}
	rep := r.patch(off, `{"set":{"core.batcave.path":"/batcave/annex"}}`)
	if rep.status != http.StatusForbidden || problemOf(t, rep).Code != "protection_required" {
		t.Errorf("auth-off patch of an admin key = %d %s, want 403 protection_required", rep.status, rep.body)
	}
	cms := r.cm.Commits()
	if len(cms) != 1 || cms[0].By != authDisabledSubject {
		t.Errorf("commits = %+v, want one by %s", cms, authDisabledSubject)
	}
}

// TestPatch_RequestDecodingIsStrict: the body is application/json, bounded, an
// object with only set and unset, with typed members; every refusal is a fixed
// message that quotes nothing and reaches no Apply.
func TestPatch_RequestDecodingIsStrict(t *testing.T) {
	rs := &recStore{}
	rr := rigOver(t, &api{store: rs})
	big := `{"set":{"core.batcave.level":"` + strings.Repeat("a", maxBody) + `"}}`
	cases := []struct {
		name, contentType, body string
		status                  int
		code                    string
	}{
		{"no content type", "", `{}`, 415, "unsupported_media_type"},
		{"text", "text/plain", `{}`, 415, "unsupported_media_type"},
		{"json with charset", "application/json; charset=utf-8", `{}`, 200, ""},
		{"too large", "application/json", big, 413, "body_too_large"},
		{"not an object", "application/json", `[1]`, 400, "malformed_json"},
		{"empty body", "application/json", ``, 400, "malformed_json"},
		{"trailing data", "application/json", `{} {}`, 400, "malformed_json"},
		{"unknown member", "application/json", `{"setx":{}}`, 400, "unknown_field"},
		{"set not an object", "application/json", `{"set":[1]}`, 422, "validation_failed"},
		{"unset not an array", "application/json", `{"unset":"core.batcave.level"}`, 422, "validation_failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := rs.calls
			rep := rr.patchWith(admin(), c.contentType, c.body)
			if rep.status != c.status {
				t.Fatalf("status = %d, want %d: %.200s", rep.status, c.status, rep.body)
			}
			if c.code == "" {
				return
			}
			if got := problemOf(t, rep).Code; got != c.code && c.status != 422 {
				t.Errorf("code = %q, want %q", got, c.code)
			}
			if c.status == 422 && !strings.Contains(rep.body, "invalid_type") {
				t.Errorf("422 body = %s", rep.body)
			}
			if rs.calls != before {
				t.Error("a refused body reached Apply")
			}
		})
	}
}

// TestPatch_UnmappedErrorIs500: an error Apply returns that is none of its
// documented refusals is the router's 500 with the cause withheld.
func TestPatch_UnmappedErrorIs500(t *testing.T) {
	rs := &recStore{err: errors.New("batcave sump pump failed")}
	rr := rigOver(t, &api{store: rs, log: slog.New(slog.DiscardHandler)})
	rep := rr.patch(admin(), `{}`)
	if rep.status != http.StatusInternalServerError || strings.Contains(rep.body, "sump") {
		t.Errorf("reply = %d %s", rep.status, rep.body)
	}
}

// TestResponse_ShapesTheResult: restart_pending is absent when empty, applied
// is never null, and a PostCommit error that is not a *PostCommitError is
// still reported, without its text.
func TestResponse_ShapesTheResult(t *testing.T) {
	got := response(config.Result{Generation: 2})
	b, _ := json.Marshal(got)
	if string(b) != `{"generation":2,"applied":[]}` {
		t.Errorf("empty result = %s", b)
	}
	got = response(config.Result{Applied: []config.Path{kLevel}, RestartPending: []config.Path{kWorkers}, PostCommit: errors.Join(
		&config.PostCommitError{Section: configtest.BatcavePath, Err: errors.New("joker")},
		&config.PostCommitError{Section: configtest.WatchPath, Err: errors.New("penguin")},
	)})
	if len(got.PostCommitError) != 2 || got.PostCommitError[1].Section != string(configtest.WatchPath) {
		t.Errorf("post commit = %+v", got.PostCommitError)
	}
	got = response(config.Result{PostCommit: errors.New("riddler")})
	b, _ = json.Marshal(got)
	if len(got.PostCommitError) != 1 || strings.Contains(string(b), "riddler") {
		t.Errorf("untyped post commit = %s", b)
	}
}

// TestKeyViews_NeverCarrySecretMaterial pins keyView/secretView: a secret key
// shows set, source, who and when, and its Effective, fingerprint, key ID and
// error text are dropped whatever the state holds.
func TestKeyViews_NeverCarrySecretMaterial(t *testing.T) {
	at := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		meta secrets.Meta
		want SecretView
	}{
		{"stored", secrets.Meta{Set: true, Source: secrets.SourceStored, UpdatedAt: at, UpdatedBy: "alfred", Fingerprint: "ab12", KeyID: "k1"},
			SecretView{Set: true, Source: "stored", UpdatedAt: at, UpdatedBy: "alfred"}},
		{"env", secrets.Meta{Set: true, Source: secrets.SourceEnv}, SecretView{Set: true, Source: "env"}},
		{"file", secrets.Meta{Set: true, Source: secrets.SourceFile}, SecretView{Set: true, Source: "file"}},
		{"unknown source", secrets.Meta{Set: true, Source: secrets.Source(99)}, SecretView{Set: true}},
		{"unset", secrets.Meta{Source: secrets.SourceStored}, SecretView{}},
		{"unreadable", secrets.Meta{Set: true, Source: secrets.SourceStored, Err: errors.New(secretValue)}, SecretView{Set: true, Source: "stored", Unavailable: true}},
	}
	for _, c := range cases {
		ks := config.KeyState{Path: kAPIKey, Section: configtest.BatcavePath, Effective: json.RawMessage(`"` + secretValue + `"`), Secret: &c.meta}
		v := keyView(ks)
		if v.Secret == nil || *v.Secret != c.want || len(v.Effective) != 0 {
			t.Errorf("%s: view = %+v, want %+v and no effective", c.name, v.Secret, c.want)
		}
		b, _ := json.Marshal(v)
		for _, leak := range []string{secretValue, "ab12", "k1\"", "fingerprint"} {
			if strings.Contains(string(b), leak) {
				t.Errorf("%s: %q leaks: %s", c.name, leak, b)
			}
		}
	}
	if secretView(nil) != nil {
		t.Error("secretView(nil) is not nil")
	}
	q := keyView(config.KeyState{Path: kLevel, Quarantined: errors.New("stored value " + secretValue + " is not valid")})
	if b, _ := json.Marshal(q); !q.Quarantined || strings.Contains(string(b), secretValue) {
		t.Errorf("quarantined view = %s", b)
	}
}

// TestMapApplyError_Details covers the corners the status table cannot reach
// with a real store: ErrForbidden text that does not parse, a mix of
// permissions, and an ErrInvalid that carries no FieldError.
func TestMapApplyError_Details(t *testing.T) {
	r := newRig(t, config.Options{})

	pb, ok := mapApplyError(fmt.Errorf("%w: core.batcave.level needs core:settings.manage; modules.gotham.watch.min needs gotham:settings.manage", config.ErrForbidden), r.st, config.Patch{})
	if !ok || pb.Status != 403 || len(pb.Need) != 2 || pb.Required != "" {
		t.Errorf("two different permissions: %+v", pb)
	}
	pb, _ = mapApplyError(fmt.Errorf("%w: core.batcave.level needs core:settings.manage; core.batcave.path needs core:settings.manage", config.ErrForbidden), r.st, config.Patch{})
	if pb.Required != "core:settings.manage" {
		t.Errorf("one permission: %+v", pb)
	}
	for _, text := range []string{"%w: no hint here", "%w: key needs two words"} {
		pb, ok = mapApplyError(fmt.Errorf(text, config.ErrForbidden), r.st, config.Patch{})
		if !ok || pb.Status != 403 || len(pb.Need) != 0 || pb.Required != "" {
			t.Errorf("unparsable %q: %+v", text, pb)
		}
	}
	if need := forbiddenNeeds(errors.New("something else")); need != nil {
		t.Errorf("forbiddenNeeds(foreign) = %v", need)
	}

	pb, _ = mapApplyError(fmt.Errorf("%w: invalid in some way", config.ErrInvalid), r.st, config.Patch{})
	if pb.Status != 422 || len(pb.Errors) != 1 || pb.Errors[0].Field != "/body" {
		t.Errorf("invalid with no field error: %+v", pb)
	}

	// A key the patch unset is pointed at in the unset list; a "/" in a key is
	// escaped.
	patch := config.Patch{Unset: []config.Path{kLevel, "a/b"}}
	if got := pointerFor(kLevel, patch); got != "/body/unset/0" {
		t.Errorf("unset pointer = %s", got)
	}
	if got := pointerFor("a/b", config.Patch{Set: map[config.Path]json.RawMessage{"a/b": nil}}); got != "/body/set/a~1b" {
		t.Errorf("escaped pointer = %s", got)
	}
	if got := pointerFor("zzz", patch); got != "/body" {
		t.Errorf("absent pointer = %s", got)
	}

	if _, ok := mapApplyError(errors.New("disk"), r.st, config.Patch{}); ok {
		t.Error("an unrelated error was mapped")
	}
}

// TestUserAgent_IsCutToTheAuditActorsBound: a long agent is cut to 256 bytes on
// a rune boundary.
func TestUserAgent_IsCutToTheAuditActorsBound(t *testing.T) {
	req := newPatchRequest(`{}`)
	req.Header.Set("User-Agent", strings.Repeat("é", 200))
	got := userAgent(req)
	if len(got) > 256 || len(got) < 254 || strings.ToValidUTF8(got, "?") != got {
		t.Errorf("len %d, valid %v", len(got), strings.ToValidUTF8(got, "?") == got)
	}
}

// ---- the route table -------------------------------------------------------

var wantRoutes = []struct {
	method, path, name string
	kind               httpapi.RouteKind
}{
	{http.MethodGet, "/api/core/v1/settings/schema", "getSettingsSchema", httpapi.RouteKindNative},
	{http.MethodGet, "/api/core/v1/settings", "getSettings", httpapi.RouteKindNative},
	// PATCH is raw: its 409 and 403 carry members httpapi.Error has no place for.
	{http.MethodPatch, "/api/core/v1/settings", "updateSettings", httpapi.RouteKindRaw},
}

func registered(t *testing.T) *httpapi.Router {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	Register(r.CoreGroup(), Deps{})
	return r
}

// TestRegister_RouteTable pins each route's method, path, name, permission,
// guards and rate class. PATCH carries no Guards.Interactive on purpose (see
// Register): protection is per key through config.Actor, and a route guard
// would refuse the auth-off principal that settings let reach the unprotected
// keys (05-config §1.3.7, 05-authn §4.11). A guard added to any of the three
// routes fails this test and must be argued for in Register's comment.
func TestRegister_RouteTable(t *testing.T) {
	var got []httpapi.RouteInfo
	for ri := range registered(t).Routes() {
		got = append(got, ri)
	}
	if len(got) != len(wantRoutes) {
		t.Fatalf("%d routes, want %d: %+v", len(got), len(wantRoutes), got)
	}
	for i, w := range wantRoutes {
		ri := got[i]
		if ri.Owner != "core" || ri.Kind != w.kind || ri.Method != w.method || ri.Pattern != w.path || ri.Name != w.name {
			t.Errorf("route %d = %s %s %s %s (kind %d), want core %s %s %s (kind %d)", i, ri.Owner, ri.Method, ri.Pattern, ri.Name, ri.Kind, w.method, w.path, w.name, w.kind)
		}
		if ri.Perm.IsZero() || ri.Perm.String() != rbac.AnyAuthenticated.String() {
			t.Errorf("%s requires %s, want %s", w.name, ri.Perm, rbac.AnyAuthenticated)
		}
		if ri.Guards != (httpapi.Guards{}) {
			t.Errorf("%s has guards %+v, want none (per-key protection is config's)", w.name, ri.Guards)
		}
		if ri.RateClass != httpapi.RateDefault {
			t.Errorf("%s rate class = %d, want default", w.name, ri.RateClass)
		}
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
	if len(problems) != len(wantRoutes) {
		t.Errorf("Build reported %d problems, want %d: %v", len(problems), len(wantRoutes), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}
