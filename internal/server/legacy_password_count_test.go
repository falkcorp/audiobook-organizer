// file: internal/server/legacy_password_count_test.go
// version: 0.1.0
// guid: 8132c4e1-c20a-4264-a4a3-feb0a7d823f2
// last-edited: 2026-11-21

package server

import (
	"errors"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	authpw "github.com/falkcorp/mediacore/auth/password"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// The boot count is the rows the login verifier compares with bcrypt: the
// "bcrypt" algorithm and the empty one that predates the column, with a hash.
// argon2id rows, OAuth users and rows with no hash are not counted.
func TestCountLegacyBcryptPasswords(t *testing.T) {
	const bc = "$2a$10$abcdefghijklmnopqrstuuJokerJokerJokerJokerJokerJokerJo"
	users := []database.User{
		{Username: "bruce", PasswordHashAlgo: "bcrypt", PasswordHash: bc},
		{Username: "alfred", PasswordHashAlgo: "", PasswordHash: bc},
		{Username: "dick", PasswordHashAlgo: " BCRYPT ", PasswordHash: bc},
		{Username: "barbara", PasswordHashAlgo: "argon2id", PasswordHash: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$a2V5"},
		{Username: "selina", PasswordHashAlgo: "oauth"},
		{Username: "jason", PasswordHashAlgo: "bcrypt", PasswordHash: "  "},
		{Username: "tim", PasswordHashAlgo: "scrypt", PasswordHash: bc},
	}
	if got := countLegacyBcryptPasswords(users); got != 3 {
		t.Fatalf("countLegacyBcryptPasswords = %d, want 3 (bruce, alfred, dick)", got)
	}
	if got := countLegacyBcryptPasswords(nil); got != 0 {
		t.Fatalf("no users: %d", got)
	}
}

type fakeUserLister struct {
	users []database.User
	err   error
	calls int
}

func (f *fakeUserLister) ListUsers() ([]database.User, error) {
	f.calls++
	return f.users, f.err
}

// The boot line reads the user list once and survives a listing error and a
// missing store: it is a diagnostic and never stops a start.
func TestLogLegacyBcryptPasswordCount_ReadsOnceAndNeverFails(t *testing.T) {
	ok := &fakeUserLister{users: []database.User{{PasswordHashAlgo: "bcrypt", PasswordHash: "$2a$10$x"}}}
	logLegacyBcryptPasswordCount(ok)
	if ok.calls != 1 {
		t.Fatalf("ListUsers called %d times, want 1", ok.calls)
	}
	bad := &fakeUserLister{err: errors.New("pebble: closed")}
	logLegacyBcryptPasswordCount(bad)
	if bad.calls != 1 {
		t.Fatalf("ListUsers called %d times on the failing store, want 1", bad.calls)
	}
	var none database.Store
	logLegacyBcryptPasswordCount(none)
}

// The wiring, end to end: on the real server and store, a web sign-in with a
// legacy bcrypt password re-stores the row as argon2id (the capability
// resolves through the store's decorators; were it nil the rehash would be a
// silent no-op), the new hash still signs the user in, and the boot count
// drops by one.
func TestWebLogin_RehashesALegacyBcryptRowThroughTheRealStore(t *testing.T) {
	fx := budgetFixture(t)
	legacy, err := bcrypt.GenerateFromPassword([]byte("utility-belt-9"), bcrypt.MinCost)
	require.NoError(t, err)
	_, err = fx.store.CreateUser("oracle", "oracle@example.test", authpw.AlgoBcrypt, string(legacy), []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	users, err := fx.store.ListUsers()
	require.NoError(t, err)
	before := countLegacyBcryptPasswords(users)
	require.Positive(t, before)

	require.Equal(t, http.StatusOK, budgetLogin(fx, "/api/v1/auth/login", "192.0.2.93:4000", "oracle", "utility-belt-9"))
	u, err := fx.store.GetUserByUsername("oracle")
	require.NoError(t, err)
	require.Equal(t, authpw.AlgoArgon2id, u.PasswordHashAlgo, "the verified bcrypt password is re-stored as argon2id")
	require.NotEqual(t, string(legacy), u.PasswordHash)

	users, err = fx.store.ListUsers()
	require.NoError(t, err)
	require.Equal(t, before-1, countLegacyBcryptPasswords(users))
	require.Equal(t, http.StatusOK, budgetLogin(fx, "/api/v1/auth/login", "192.0.2.94:4000", "oracle", "utility-belt-9"),
		"the argon2id row signs the same password in")
}
