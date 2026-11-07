// file: core/log/otheruser.go
// version: 1.0.0
// guid: 6ab16966-3ac0-453d-9880-dcebd76d0903
// last-edited: 2026-11-07

package log

// OtherUserPredicate reports whether a journal record (from the native store
// or from an app's own JournalStore) names another user's data; viewer is the
// principal's user ID. A module registers at most one through
// module.Registrar.LogOtherUserData; the journal query and the live tail
// return a flagged record only to a principal that also holds core:users.view
// and blank its user_id otherwise (05-logging §5). It must be pure and fast
// (called per record) and fail closed: true when unsure.
type OtherUserPredicate func(rec Record, viewer string) bool
