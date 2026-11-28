// file: internal/books/store/user_directory.go
// version: 1.0.0
// guid: 7091ed32-f617-48f0-811f-8b0953730d36
// last-edited: 2026-11-28

package store

import (
	"errors"
	"sync/atomic"
)

// The account list under core identity (F4-08 fix-forward, data-loss
// prevention).
//
// Under core identity the accounts are core's. M3 (internal/books/cutover/m3)
// imports every u: row into core and deletes it, so after the flip this store's
// u: family is empty for good. ListUsers is not an access read. It is how every
// per-user fan-out finds the users: a merge carrying each user's listening
// progress to the survivor, and every "keep the book if anyone has state on it"
// gate before a hard delete (merge.UserStateProbe and BookHasUserProgress, the
// purge, the fixers, reconcile). Read from the drained family, it answers [] on
// every migrated install. A merge then carries nobody's progress, and every gate
// answers "nobody has state", so the book is deleted with the progress on it.
//
// So, under core identity, the two list methods answer from core's directory
// (UserDirectory, installed by books' Register over core's Identities). There
// is no fallback to the drained family. A store marked as under core identity
// (RequireUserDirectory) with no directory installed answers
// ErrNoUserDirectory, and a directory error is returned as it is: an error is
// never an empty list, because every gate above treats an error as "do not
// delete" and an empty list as "delete".
//
// The point reads (GetUserByID, GetUserByUsername, GetUserByVerifiedEmail) and
// CountUsers are deliberately NOT redirected. They guard access, and each of
// their callers decides under identity on its own (an IdentityFrom or
// s.identity branch, or a refusal).

// UserDirectory lists every account core holds, in HEAD's user record
// (legacyauth.LegacyUserFromCore): every status, the system user included, as
// HEAD's ListUsers listed every u: row. An error must be returned as an error,
// never as an empty or partial list.
type UserDirectory func() ([]User, error)

// ErrNoUserDirectory is ListUsers' answer on a store under core identity whose
// directory has not been installed.
var ErrNoUserDirectory = errors.New("database: the accounts are core's (core identity) and no user directory is installed; refusing to answer an empty user list")

// userDirectorySeam is the PebbleStore's half: whether the store runs under core
// identity, and the directory once installed. Both are set once at boot and read
// lock-free.
type userDirectorySeam struct {
	required atomic.Bool
	dir      atomic.Pointer[UserDirectory]
}

// RequireUserDirectory marks the store as running under core identity: from now
// on ListUsers and ListUsersStrict answer from the installed directory, or
// ErrNoUserDirectory while none is. books' Register calls it as soon as the
// store is open, before anything else can read through it, so no caller ever
// reads the drained u: family as "no users". It cannot be undone.
func (p *PebbleStore) RequireUserDirectory() { p.users.required.Store(true) }

// SetUserDirectory installs the account list of a store under core identity, and
// marks it as under core identity (RequireUserDirectory). A nil dir leaves the
// store refusing (ErrNoUserDirectory).
func (p *PebbleStore) SetUserDirectory(dir UserDirectory) {
	p.users.required.Store(true)
	if dir == nil {
		p.users.dir.Store(nil)
		return
	}
	p.users.dir.Store(&dir)
}

// directoryUsers is the answer of ListUsers and ListUsersStrict under core
// identity; ok is false when the store is not under core identity and the u:
// family is the account list, as at HEAD.
func (p *PebbleStore) directoryUsers() (users []User, ok bool, err error) {
	if !p.users.required.Load() {
		return nil, false, nil
	}
	dir := p.users.dir.Load()
	if dir == nil || *dir == nil {
		return nil, true, ErrNoUserDirectory
	}
	users, err = (*dir)()
	if err != nil {
		return nil, true, err
	}
	return users, true, nil
}
