// file: core/internal/authhttp/secrets_test.go
// version: 1.0.1
// guid: a5d3e816-4c27-4b90-b8e2-7f61c09d4a35
// last-edited: 2026-11-11

package authhttp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// No secret a caller submits comes back in a response or reaches a log: not a
// password, not a one-time token, not a bootstrap token, whatever the route
// answered.
func TestSubmittedSecretsAreNeverEchoedOrLogged(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	var logs bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a := e.api(func(d *Deps) { d.Log = lg })
	h := publicHandler(t, a, httpapi.LimitOptions{})
	const pw, tok = "Zq9-batcave-passphrase-77", "mct_zq9_marker_token_77"
	var bodies []string
	for _, c := range []struct {
		path string
		body any
	}{
		{"/auth/login", LoginRequest{Username: "bruce", Password: pw}},
		{"/auth/login", LoginRequest{Username: "nobody", Password: pw}},
		{"/auth/setup", SetupRequest{Username: "x", Password: pw, BootstrapToken: tok}},
		{"/auth/bootstrap", BootstrapRequest{Token: tok}},
		{"/auth/invites/lookup", InviteLookup{Token: tok}},
		{"/auth/invites/accept", InviteAccept{Token: tok, Password: pw}},
		{"/auth/temp-login/consume", TempLoginConsume{Token: tok, NewPassword: pw}},
	} {
		w := serve(h, "POST", core+c.path, c.body)
		if w.Code < 400 {
			t.Fatalf("%s: %d, want a refusal", c.path, w.Code)
		}
		bodies = append(bodies, w.Body.String())
		for k, v := range w.Result().Header {
			bodies = append(bodies, k+": "+strings.Join(v, ","))
		}
	}
	// Changing one's own password with the wrong current one.
	_, err := call(specMePassword(), a.changePassword, e.session(bruce), passwordReq{Body: PasswordChange{Current: pw, New: pw + "x"}})
	if err == nil {
		t.Fatal("a wrong current password changed the password")
	}
	bodies = append(bodies, err.Error())
	if he, ok := errors.AsType[*httpapi.Error](err); ok {
		bodies = append(bodies, he.Detail, he.Code)
		for _, f := range he.Fields {
			bodies = append(bodies, f.Field, f.Message, f.Code)
		}
	}
	for _, s := range append(bodies, logs.String()) {
		if strings.Contains(s, pw) || strings.Contains(s, tok) || strings.Contains(s, "marker_token") {
			t.Fatalf("a submitted secret came back or was logged: %q", s)
		}
	}
}

// An error that is not one of the Service's sentinels is a 500 that carries no
// cause: the cause is logged, the response says nothing of it.
func TestUnmappedErrorIs500AndTheCauseIsWithheld(t *testing.T) {
	e := newEnv(t)
	var logs bytes.Buffer
	cause := errors.New("pebble: disk on fire at the batcave")
	a := e.api(func(d *Deps) {
		d.Log = slog.New(slog.NewTextHandler(&logs, nil))
		d.Entry = refusing{Entry: d.Entry, err: cause}
	})
	h := publicHandler(t, a, httpapi.LimitOptions{})
	w := serve(h, "POST", core+"/auth/login", LoginRequest{Username: "bruce", Password: "x"})
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "pebble") || strings.Contains(w.Body.String(), "batcave") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "disk on fire") || !strings.Contains(logs.String(), "POST /auth/login") {
		t.Fatalf("the cause is not logged with its route: %s", logs.String())
	}
}

// A store that is down is auth_unavailable, a 503, on a sign-in: not a 401 that
// would read as a wrong password.
func TestStoreUnavailableIs503OnSignIn(t *testing.T) {
	e := newEnv(t)
	a := e.api(func(d *Deps) { d.Entry = refusing{Entry: d.Entry, err: auth.ErrStoreUnavailable} })
	w := serve(publicHandler(t, a, httpapi.LimitOptions{}), "POST", core+"/auth/login", LoginRequest{Username: "bruce", Password: "x"})
	wantProblem(t, w, http.StatusServiceUnavailable, "auth_unavailable")
}

// The sign-in route refuses another content type and an oversize body before
// the Service is called.
func TestLogin_BodyRules(t *testing.T) {
	e := newEnv(t)
	a := e.api(func(d *Deps) { d.Entry = tripwireEntry{t} })
	h := publicHandler(t, a, httpapi.LimitOptions{})
	w := serve(h, "POST", core+"/auth/login", nil, func(r *http.Request) {
		r.Body = http.NoBody
		r.Header.Set("Content-Type", "text/plain")
	})
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d %s", w.Code, w.Body.String())
	}
	big := `{"username":"` + strings.Repeat("a", maxBody) + `"}`
	w = serve(h, "POST", core+"/auth/login", nil, func(r *http.Request) {
		r.Body = readerBody(big)
		r.ContentLength = int64(len(big))
		r.Header.Set("Content-Type", "application/json")
	})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d %s", w.Code, w.Body.String())
	}
}

// tripwireEntry fails the test when a sign-in call reaches it.
type tripwireEntry struct{ t testing.TB }

func (w tripwireEntry) Login(context.Context, auth.LoginInput) (*auth.IssuedSession, error) {
	w.t.Helper()
	w.t.Fatal("Login ran for a request the route should have refused")
	return nil, nil
}
func (w tripwireEntry) ConsumeTempLogin(context.Context, string, string, auth.ClientInfo) (*auth.IssuedSession, error) {
	w.t.Fatal("ConsumeTempLogin ran")
	return nil, nil
}
func (w tripwireEntry) LookupInvite(context.Context, string, auth.ClientInfo) (auth.InviteView, error) {
	w.t.Fatal("LookupInvite ran")
	return auth.InviteView{}, nil
}
func (w tripwireEntry) AcceptInvite(context.Context, auth.AcceptInviteInput) (*auth.User, *auth.IssuedSession, error) {
	w.t.Fatal("AcceptInvite ran")
	return nil, nil, nil
}
func (w tripwireEntry) Setup(context.Context, auth.SetupInput) (*auth.User, error) {
	w.t.Fatal("Setup ran")
	return nil, nil
}
func (w tripwireEntry) RedeemBootstrap(context.Context, string, auth.ClientInfo) (*auth.BootstrapResult, error) {
	w.t.Fatal("RedeemBootstrap ran")
	return nil, nil
}
func (w tripwireEntry) Posture() auth.Posture { return auth.Posture{} }
func (w tripwireEntry) HasUsers() bool        { return true }
