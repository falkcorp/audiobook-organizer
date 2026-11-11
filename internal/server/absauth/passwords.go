// file: internal/server/absauth/passwords.go
// version: 1.0.0
// guid: f51a51bd-89f0-464f-8eb4-d8a7cf9581fb
// last-edited: 2026-11-10

package absauth

import (
	coreaudit "github.com/falkcorp/mediacore/audit"
	authpw "github.com/falkcorp/mediacore/auth/password"
)

// Passwords is ABO's one password hasher until F4-08 hands the web and ABS
// sign-ins to core's auth.Service: every web login, ABS login, user write,
// bootstrap and invite accept hashes and verifies through it, so they share
// one semaphore, one dummy hash and one derivation count, as HEAD's
// package-level hashing in this package did (core/auth/password is instance
// state, R5). Production cost; a test binary's TestMain replaces it with a
// cheaper one before any test runs.
var Passwords = authpw.New(authpw.Options{})

// AttemptLog is the ABS surface's attempt log: HEAD's "abs.auth" records,
// written to the default logger at the time of each record.
var AttemptLog = coreaudit.AuthAttemptLog{Event: "abs.auth", Message: "abs auth attempt"}
