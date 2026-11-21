// file: internal/server/legacy_password_count.go
// version: 0.1.0
// guid: 1e3ead22-61cd-499e-9b19-a9358116a06e
// last-edited: 2026-11-21

package server

import (
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	authpw "github.com/falkcorp/mediacore/auth/password"
)

// legacyPasswordLog carries this file's one boot line through the
// log-injection barrier.
var legacyPasswordLog = logger.New("server.legacy-passwords")

// legacyPasswordUserLister is what logLegacyBcryptPasswordCount needs.
type legacyPasswordUserLister interface {
	ListUsers() ([]database.User, error)
}

// countLegacyBcryptPasswords counts the users whose password is still a
// legacy bcrypt hash: the rows the login verifier compares with bcrypt
// (authpw.HasCredential with algorithm "bcrypt", or the empty algorithm that
// predates the column).
func countLegacyBcryptPasswords(users []database.User) int {
	n := 0
	for _, u := range users {
		if !authpw.HasCredential(u.PasswordHashAlgo, u.PasswordHash) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(u.PasswordHashAlgo)) {
		case authpw.AlgoBcrypt, "":
			n++
		}
	}
	return n
}

// logLegacyBcryptPasswordCount logs, once per boot, how many users still have
// a bcrypt password, with no names. Every login pays a bcrypt comparison
// (against a dummy for everyone else) so a bcrypt account's timing matches
// an argon2id account's and an unknown name's; each bcrypt user's next
// successful sign-in re-stores the row as argon2id. When this count reaches
// 0, bcrypt verification and its dummy can be dropped (todo.d fragment
// "drop bcrypt verify once the count is 0").
func logLegacyBcryptPasswordCount(store legacyPasswordUserLister) {
	if store == nil {
		return
	}
	users, err := store.ListUsers()
	if err != nil {
		legacyPasswordLog.Warn("legacy bcrypt password count unavailable: %s", logger.SanitizeLogValue(err.Error()))
		return
	}
	legacyPasswordLog.Info("legacy bcrypt password rows: %d of %d users (bcrypt verify can be dropped at 0)",
		countLegacyBcryptPasswords(users), len(users))
}
