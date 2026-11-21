// file: core/httpapi/stage13_realauth_test.go
// version: 0.1.0
// guid: 8d26f8e3-8ed8-4be8-9207-21b00c2c225e
// last-edited: 2026-11-20

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// s13RealAuth is a real auth.Service over an in-memory store with one user,
// so stage 12's own middleware records the failures Stage13 reads.
func s13RealAuth(t *testing.T) *auth.Service {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: authstore.Owner, Spec: authstore.StorageSpec(), Enabled: true},
			{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true},
		},
		Build: "httpapi+stage13", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		if cerr := o.Close(); cerr != nil {
			t.Logf("close after a failed boot: %v", cerr)
		}
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	now := time.Date(2026, 11, 20, 19, 30, 0, 0, time.UTC)
	aks, _ := db.Keyspace(authstore.Owner)
	rks, _ := db.Keyspace(rbacstore.Owner)
	st, err := authstore.New(aks, authstore.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	rst, err := rbacstore.New(rks, rbacstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := rbac.New(ctx, rbac.Options{Source: "httpapi-stage13-test"}, rbac.Deps{
		Store: rst, Compile: authz.Compile, Audit: s13Audit{}, Revocations: s13Journal{},
		NewBatch: rst.NewBatch, Holders: s13Holders{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rb.Freeze(ctx); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err := rb.CommitSeeds(ctx); err != nil {
		t.Fatalf("CommitSeeds: %v", err)
	}
	u := &auth.User{ID: "u-alfred", Username: "alfred", Roles: []rbac.RoleName{rbac.RoleViewer}, Status: auth.StatusActive,
		CreatedAt: now, UpdatedAt: now, Version: 1}
	b := st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := st.PutUser(b, u); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(ctx, auth.Options{}, auth.Deps{
		Store: st, NewBatch: st.NewBatch, RBAC: rb, Audit: s13Audit{}, Revocations: s13Journal{},
		Clock: func() time.Time { return now }, Hasher: password.New(password.Options{Time: 1, MemoryKiB: 64, BcryptCost: bcrypt.MinCost}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !svc.HasUsers() {
		t.Fatal("the fixture user is not visible: stage 12 would bind nothing and record no failure")
	}
	return svc
}

type s13Holders struct{}

func (s13Holders) HoldersOf(context.Context, *storage.Batch, []rbac.RoleName) ([]rbac.Holder, error) {
	return nil, nil
}

type s13Audit struct{}

func (s13Audit) Record(context.Context, audit.Event) error {
	return errors.New("audited with RecordIn")
}
func (s13Audit) RecordIn(_ context.Context, b *storage.Batch, _ audit.Event) error {
	if b == nil {
		return errors.New("RecordIn without the change's batch")
	}
	return nil
}
func (s13Audit) Emit(context.Context, audit.Event) {}

type s13Journal struct{}

func (s13Journal) Append(_ context.Context, b *storage.Batch, _ journal.Revocation) error {
	if b == nil {
		return errors.New("Append without the revoking batch")
	}
	return nil
}
func (s13Journal) Gap(b *storage.Batch, _ time.Time) error {
	if b == nil {
		return errors.New("Gap without the revoking batch")
	}
	return nil
}

// TestStage13_BehindRealStage12 composes auth's own stage 12 with Stage13, as
// B4 will: a stale session cookie on a Public route proceeds anonymous and is
// cleared (05-authn §4.3), on a non-Public route it is 401 with the failure
// code and cleared, and an invalid Bearer is 401 on a Public route too.
func TestStage13_BehindRealStage12(t *testing.T) {
	svc := s13RealAuth(t)
	clear := func(r *http.Request) *http.Cookie {
		return svc.SessionCookie(auth.ClientInfo{HTTPS: r.TLS != nil}, "", time.Time{})
	}
	cookieName := svc.SessionCookie(auth.ClientInfo{}, "", time.Time{}).Name
	for _, c := range []struct {
		name    string
		perm    rbac.Requirement
		bearer  bool
		status  int
		code    string
		cleared bool
	}{
		{"stale cookie on Public", rbac.Public, false, http.StatusOK, "", true},
		{"stale cookie on a native route", rbac.Need("core:self"), false, http.StatusUnauthorized, auth.CodeInvalidCredential, true},
		{"invalid Bearer on Public", rbac.Public, true, http.StatusUnauthorized, auth.CodeInvalidCredential, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			mw, err := Stage13(c.perm, Guards{}, Stage13Options{ClearCookie: clear})
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			h := svc.Middleware()(mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})))
			r := httptest.NewRequest(http.MethodGet, "/api/core/v1/gotham", nil)
			r.RemoteAddr = "192.0.2.10:4000"
			if c.bearer {
				r.Header.Set("Authorization", "Bearer "+string(auth.KindSession)+"joker-card")
			} else {
				r.AddCookie(&http.Cookie{Name: cookieName, Value: "penguin-umbrella"})
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			cleared := false
			for _, ck := range rec.Result().Cookies() {
				if ck.Name == cookieName && ck.MaxAge < 0 {
					cleared = true
				}
			}
			if rec.Code != c.status || reached != (c.status == http.StatusOK) || cleared != c.cleared {
				t.Fatalf("status=%d reached=%v cleared=%v, want %d %v %v (body %s)", rec.Code, reached, cleared, c.status, c.status == http.StatusOK, c.cleared, rec.Body)
			}
			if c.code != "" {
				if got := s13Problem(t, rec).Code; got != c.code {
					t.Fatalf("code %q, want %q", got, c.code)
				}
			}
		})
	}
}
