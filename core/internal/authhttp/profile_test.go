// file: core/internal/authhttp/profile_test.go
// version: 0.1.0
// guid: 2f6a9d41-8e37-4b50-a7c2-90d3b1e5f846
// last-edited: 2026-11-20

package authhttp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// verifiedEmail stores address verified on u.
func (e *env) verifiedEmail(u *auth.User, address string) *auth.User {
	e.t.Helper()
	cur := e.reload(u.ID)
	cur.Email, cur.EmailVerified, cur.Version = address, true, cur.Version+1
	e.commit(func(b *storage.Batch) error { return e.st.PutUser(b, cur) })
	return e.reload(u.ID)
}

// FromService wires the Service's own profile writer: nothing but a test
// that removes it gets the 501.
func TestFromService_FillsProfiles(t *testing.T) {
	e := newEnv(t)
	if d := FromService(e.svc, e.rb); d.Profiles == nil {
		t.Fatal("FromService leaves PATCH /me without a writer")
	}
}

func TestUpdateMe_RealService(t *testing.T) {
	e := newEnv(t)
	alfred := e.user("alfred", "alfredalfred", rbac.RoleAdmin)
	e.verifiedEmail(alfred, "alfred@gotham.example")
	bruce := e.verifiedEmail(e.user("bruce", "brucebruce", rbac.RoleAdmin), "bruce@gotham.example")
	a := e.api()
	p := e.session(bruce)
	name := "Batman"
	got, err := call(specMeEd(), a.updateMe, p, updateMeReq{Body: ProfilePatch{DisplayName: &name}})
	if err != nil || got.DisplayName != "Batman" || got.Email != "bruce@gotham.example" || !got.EmailVerified {
		t.Fatalf("%+v %v", got, err)
	}
	// A new address is answered unverified.
	robin := "robin@gotham.example"
	got, err = call(specMeEd(), a.updateMe, p, updateMeReq{Body: ProfilePatch{Email: &robin}})
	if err != nil || got.Email != robin || got.EmailVerified {
		t.Fatalf("%+v %v", got, err)
	}
	// Another account's verified address is a 409 that names no account.
	taken := "Alfred@Gotham.Example"
	_, err = call(specMeEd(), a.updateMe, p, updateMeReq{Body: ProfilePatch{Email: &taken, DisplayName: &name}})
	wantErr(t, err, http.StatusConflict, "email_in_use")
	if he, _ := err.(*httpapi.Error); he == nil || he.Detail == "" || strings.Contains(strings.ToLower(he.Detail), "alfred") {
		t.Fatalf("detail %q names the other account", he.Detail)
	}
	// A look-alike address is a validation failure on the field.
	kelvin := "Kate@gotham.example"
	_, err = call(specMeEd(), a.updateMe, p, updateMeReq{Body: ProfilePatch{Email: &kelvin}})
	wantErr(t, err, http.StatusUnprocessableEntity, "validation_failed")
	if he, _ := err.(*httpapi.Error); he == nil || len(he.Fields) != 1 || he.Fields[0].Field != "/body/email" {
		t.Fatalf("%v", err)
	}
	if after := e.reload(bruce.ID); after.Email != robin || after.DisplayName != "Batman" {
		t.Fatalf("refused changes landed: %+v", after)
	}
}

// The route refuses the auth-disabled principal with 403 interactive_required
// (F4-08 ruling G5(a)) and a key with 403
// before the writer runs, and the real writer refuses both again if a route
// is mounted bare.
func TestUpdateMe_RefusesWhatTheWriterRefuses(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	a := e.api()
	x := "Robin"
	in := ProfilePatch{DisplayName: &x}
	_, err := call(specMeEd(), a.updateMe, e.authOff(), updateMeReq{Body: in})
	wantAuthOffRefusal(t, err)
	_, err = a.Profiles.UpdateProfile(context.Background(), e.authOff(), ProfileUpdate{DisplayName: &x})
	wantErr(t, mapFail(a, err), http.StatusForbidden, "permission_denied")
	kp := e.principal(bruce, auth.MethodAPIKey, "core:self")
	_, err = call(specMeEd(), a.updateMe, kp, updateMeReq{Body: in})
	wantErr(t, err, http.StatusForbidden, "interactive_required")
	_, err = a.Profiles.UpdateProfile(context.Background(), kp, ProfileUpdate{DisplayName: &x})
	wantErr(t, mapFail(a, err), http.StatusForbidden, "permission_denied")
	if got := e.reload(bruce.ID); got.DisplayName != "" || got.Version != bruce.Version {
		t.Fatalf("a refused caller changed the profile: %+v", got)
	}
}

func mapFail(a *api, err error) error {
	return a.fail(context.Background(), specMeEd(), nil, "", err)
}
