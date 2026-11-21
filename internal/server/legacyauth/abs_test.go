// file: internal/server/legacyauth/abs_test.go
// version: 0.1.0
// guid: 1c6e9b42-3d75-4f8a-a0b9-5e2d8c7f4a13
// last-edited: 2026-11-20

package legacyauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// fakeCompat records what the adapter asked books' compat handle and answers
// as configured.
type fakeCompat struct {
	p                 *coreauth.Principal
	authErr, checkErr error

	keys    []string
	users   []coreauth.UserID
	scopes  [][]rbac.Permission
	epochs  []int64
	checked []rbac.Requirement
}

func (f *fakeCompat) VerifyPassword(context.Context, string, string, coreauth.ClientInfo) (*coreauth.User, error) {
	return nil, errors.New("not used by the adapter")
}

func (f *fakeCompat) AuthenticateKey(_ context.Context, raw string, _ coreauth.ClientInfo) (*coreauth.Principal, error) {
	f.keys = append(f.keys, raw)
	return f.p, f.authErr
}

func (f *fakeCompat) PrincipalFor(_ context.Context, u *coreauth.User, scopes []rbac.Permission, epoch int64, _ coreauth.ClientInfo) (*coreauth.Principal, error) {
	f.users = append(f.users, u.ID)
	f.scopes = append(f.scopes, scopes)
	f.epochs = append(f.epochs, epoch)
	return f.p, f.authErr
}

func (f *fakeCompat) CheckCredentialFor(_ *coreauth.Principal, perm rbac.Requirement) error {
	f.checked = append(f.checked, perm)
	return f.checkErr
}

func credOf(cred ABSCredential, err error) ABSResolver {
	return func(*gin.Context) (ABSCredential, error) { return cred, err }
}

// serveABS runs the adapter then a handler that reports the principal and
// checker it finds bound.
func serveABS(mw gin.HandlerFunc) *httptest.ResponseRecorder {
	r := gin.New()
	r.GET("/api/libraries", mw, func(c *gin.Context) {
		p, okP := coreauth.PrincipalFrom(c.Request.Context())
		_, okC := rbac.CheckerFrom(c.Request.Context())
		if !okP || !okC || p.Method != "books.abs" {
			c.String(http.StatusTeapot, "reached without a bound principal and checker")
			return
		}
		c.String(http.StatusOK, "reached")
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/libraries", nil))
	return rec
}

var routePerm = rbac.Need("books:view")

func absPrincipal() *coreauth.Principal {
	return &coreauth.Principal{Method: "books.abs", User: &coreauth.User{ID: bruceID}, Perms: laxChecker{}}
}

// TestABSRequireAuth_Refusals (05-http-api §8.3 ABS row, SEC-1, SEC2-4):
// every refusal answers the ABS JSON body and never reaches the handler.
func TestABSRequireAuth_Refusals(t *testing.T) {
	key := ABSCredential{Key: "abk_riddler"}
	user := ABSCredential{User: &coreauth.User{ID: bruceID}, Epoch: 3}
	cases := map[string]struct {
		compat  coreauth.Compat
		resolve ABSResolver
		status  int
	}{
		"no compat handle":            {nil, credOf(key, nil), 500},
		"no resolver":                 {&fakeCompat{p: absPrincipal()}, nil, 500},
		"the resolver refuses":        {&fakeCompat{p: absPrincipal()}, credOf(key, errors.New("bad token")), 401},
		"neither key nor user":        {&fakeCompat{p: absPrincipal()}, credOf(ABSCredential{}, nil), 401},
		"both key and user":           {&fakeCompat{p: absPrincipal()}, credOf(ABSCredential{Key: "abk_riddler", User: user.User}, nil), 401},
		"a revoked key":               {&fakeCompat{authErr: coreauth.ErrRevoked}, credOf(key, nil), 401},
		"a stale user token":          {&fakeCompat{authErr: coreauth.ErrStaleEpoch}, credOf(user, nil), 401},
		"a throttled source":          {&fakeCompat{authErr: &coreauth.ThrottledError{RetryAfter: 30}}, credOf(user, nil), 429},
		"the store is unavailable":    {&fakeCompat{authErr: coreauth.ErrStoreUnavailable}, credOf(key, nil), 503},
		"no principal and no error":   {&fakeCompat{}, credOf(key, nil), 401},
		"a confined credential":       {&fakeCompat{p: absPrincipal(), checkErr: coreauth.ErrConfined}, credOf(key, nil), 403},
		"a key revoked since":         {&fakeCompat{p: absPrincipal(), checkErr: coreauth.ErrRevoked}, credOf(key, nil), 401},
		"the store fails the recheck": {&fakeCompat{p: absPrincipal(), checkErr: coreauth.ErrStoreUnavailable}, credOf(key, nil), 503},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := serveABS(ABSRequireAuth(tc.compat, tc.resolve, routePerm))
			if rec.Code != tc.status {
				t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), tc.status)
			}
			if !strings.HasPrefix(rec.Body.String(), `{"error":"`) || strings.Contains(rec.Body.String(), "reached") {
				t.Errorf("body = %q, want the ABS JSON refusal", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "riddler") {
				t.Error("the body quotes the presented key")
			}
		})
	}
}

// TestABSRequireAuth_ResolvesThroughCompat: a key goes through
// AuthenticateKey and a surface-verified user through PrincipalFor with the
// surface's scopes and epoch; both then go through CheckCredentialFor with
// the route's Perm, and the principal and its checker are bound.
func TestABSRequireAuth_ResolvesThroughCompat(t *testing.T) {
	f := &fakeCompat{p: absPrincipal()}
	if rec := serveABS(ABSRequireAuth(f, credOf(ABSCredential{Key: "abk_riddler"}, nil), routePerm)); rec.Code != 200 {
		t.Fatalf("key: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.keys) != 1 || f.keys[0] != "abk_riddler" || len(f.users) != 0 {
		t.Errorf("key path asked keys=%v users=%v", f.keys, f.users)
	}
	scopes := []rbac.Permission{"books:view"}
	if rec := serveABS(ABSRequireAuth(f, credOf(ABSCredential{User: &coreauth.User{ID: bruceID}, Epoch: 7, Scopes: scopes}, nil), routePerm)); rec.Code != 200 {
		t.Fatalf("user: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.users) != 1 || f.users[0] != bruceID || f.epochs[0] != 7 || len(f.scopes[0]) != 1 {
		t.Errorf("user path asked users=%v epochs=%v scopes=%v", f.users, f.epochs, f.scopes)
	}
	if len(f.checked) != 2 || f.checked[0].String() != routePerm.String() || f.checked[1].String() != routePerm.String() {
		t.Errorf("CheckCredentialFor saw %v, want the route Perm twice", f.checked)
	}
}
