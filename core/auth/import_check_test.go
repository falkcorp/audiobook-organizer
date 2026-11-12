// file: core/auth/import_check_test.go
// version: 1.0.0
// guid: de299bdf-a199-4b5b-ac6e-82dde03f52b2
// last-edited: 2026-11-12

package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

const argonHash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo"

// TestCheckImportUser: the rule of Importer.ImportUser for a record's own
// fields, every refusal wrapping ErrImportRejected and none carrying the hash.
func TestCheckImportUser(t *testing.T) {
	ok := func() *auth.User {
		return &auth.User{ID: "01BRUCE", Username: "bruce.wayne", Status: auth.StatusActive}
	}
	for _, c := range []struct {
		name string
		mut  func(*auth.User)
		hash string
		want string // "" = accepted
	}{
		{"active with argon2id", func(*auth.User) {}, argonHash, ""},
		{"active with bcrypt", func(*auth.User) {}, "$2b$10$abcdefghijklmnopqrstuuJ8x5B1yZk7eD2Gm3e1t0Qq0a8b9c0dK", ""},
		{"no password", func(*auth.User) {}, "", ""},
		{"pending", func(u *auth.User) { u.Status = auth.StatusPending }, "", ""},
		{"system", func(u *auth.User) { u.Status = auth.StatusSystem }, "", ""},
		{"no ID", func(u *auth.User) { u.ID = "" }, "", "an ID and a username"},
		{"blank username", func(u *auth.User) { u.Username = " \t " }, "", "an ID and a username"},
		{"unknown status", func(u *auth.User) { u.Status = "suspended" }, "", "status"},
		{"plain-text hash", func(*auth.User) {}, "hunter2", "argon2id or bcrypt"},
		{"oauth marker as hash", func(*auth.User) {}, "oauth", "argon2id or bcrypt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := ok()
			c.mut(u)
			err := auth.CheckImportUser(u, c.hash)
			if c.want == "" {
				if err != nil {
					t.Fatalf("CheckImportUser = %v, want accepted", err)
				}
				return
			}
			if !errors.Is(err, auth.ErrImportRejected) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("CheckImportUser = %v, want ErrImportRejected naming %q", err, c.want)
			}
			if c.hash != "" && strings.Contains(err.Error(), c.hash) {
				t.Fatalf("the refusal carries the hash: %v", err)
			}
		})
	}
	if err := auth.CheckImportUser(nil, ""); !errors.Is(err, auth.ErrImportRejected) {
		t.Fatalf("nil user = %v, want ErrImportRejected", err)
	}
}

// TestCheckImportAPIKey: scopes, status, hash and the expiry rule, with a
// revoked_at counted as revoked.
func TestCheckImportAPIKey(t *testing.T) {
	hash := [32]byte{1}
	later := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	ok := func() *auth.APIKey {
		return &auth.APIKey{ID: "01KEY", Owner: "01BRUCE", Scopes: []rbac.Permission{"books:view"}, Status: auth.KeyActive, ExpiresAt: later}
	}
	for _, c := range []struct {
		name string
		mut  func(*auth.APIKey)
		hash [32]byte
		want string
	}{
		{"active with expiry", func(*auth.APIKey) {}, hash, ""},
		{"disabled with expiry", func(k *auth.APIKey) { k.Status = auth.KeyDisabled }, hash, ""},
		{"revoked with no expiry", func(k *auth.APIKey) { k.Status = auth.KeyRevoked; k.ExpiresAt = time.Time{} }, hash, ""},
		{"revoked_at set with no expiry", func(k *auth.APIKey) { k.RevokedAt = later; k.ExpiresAt = time.Time{} }, hash, ""},
		{"no ID", func(k *auth.APIKey) { k.ID = "" }, hash, "needs an ID"},
		{"no scopes", func(k *auth.APIKey) { k.Scopes = nil }, hash, "at least one scope"},
		{"wildcard scope", func(k *auth.APIKey) { k.Scopes = []rbac.Permission{"books:*"} }, hash, "scope"},
		{"malformed scope", func(k *auth.APIKey) { k.Scopes = []rbac.Permission{"library.view"} }, hash, "scope"},
		{"unknown status", func(k *auth.APIKey) { k.Status = "paused" }, hash, "status"},
		{"no token hash", func(*auth.APIKey) {}, [32]byte{}, "token hash"},
		{"live key with no expiry", func(k *auth.APIKey) { k.ExpiresAt = time.Time{} }, hash, "no expiry"},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := ok()
			c.mut(k)
			err := auth.CheckImportAPIKey(k, c.hash)
			if c.want == "" {
				if err != nil {
					t.Fatalf("CheckImportAPIKey = %v, want accepted", err)
				}
				return
			}
			if !errors.Is(err, auth.ErrImportRejected) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("CheckImportAPIKey = %v, want ErrImportRejected naming %q", err, c.want)
			}
		})
	}
	if err := auth.CheckImportAPIKey(nil, hash); !errors.Is(err, auth.ErrImportRejected) {
		t.Fatalf("nil key = %v, want ErrImportRejected", err)
	}
}

// TestCheckImportIdentity: provider, subject and user, no slash in the provider.
func TestCheckImportIdentity(t *testing.T) {
	ok := func() *auth.ExternalIdentity {
		return &auth.ExternalIdentity{Provider: "github", Subject: "oracle-1989", UserID: "01BARBARA"}
	}
	for _, c := range []struct {
		name string
		mut  func(*auth.ExternalIdentity)
		want string
	}{
		{"linked", func(*auth.ExternalIdentity) {}, ""},
		{"no provider", func(i *auth.ExternalIdentity) { i.Provider = "" }, "provider"},
		{"no subject", func(i *auth.ExternalIdentity) { i.Subject = "" }, "subject"},
		{"no user", func(i *auth.ExternalIdentity) { i.UserID = "" }, "user"},
		{"slash in provider", func(i *auth.ExternalIdentity) { i.Provider = "git/hub" }, "slash"},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := ok()
			c.mut(id)
			err := auth.CheckImportIdentity(id)
			if c.want == "" {
				if err != nil {
					t.Fatalf("CheckImportIdentity = %v, want accepted", err)
				}
				return
			}
			if !errors.Is(err, auth.ErrImportRejected) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("CheckImportIdentity = %v, want ErrImportRejected naming %q", err, c.want)
			}
		})
	}
	if err := auth.CheckImportIdentity(nil); !errors.Is(err, auth.ErrImportRejected) {
		t.Fatalf("nil identity = %v, want ErrImportRejected", err)
	}
}
