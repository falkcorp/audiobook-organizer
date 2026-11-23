// file: internal/books/identity_flip.go
// version: 0.1.0
// guid: 3f7a5c19-2d84-4e6b-a1c0-9b5e8d27f406
// last-edited: 2026-11-21

package books

// ABO's identity flip (F4-08 B10) is ONE decision, identityOn, and everything
// the flip changes in this package reads it: the manifest (the books.level-2
// step, M3 in the cutover list, the retired identity claims), books.Options
// (the identity boot and its inputs), Register (the server built under core
// identity). They are tied to one predicate on purpose. Three of them can come
// apart, and each of the ways is a way to serve with the wrong authority:
//
//   - the identity boot without M3: core identity over an empty user table,
//     every HEAD user locked out;
//   - core identity without the server built under it (server.WithIdentity):
//     HEAD's own middleware keeps judging requests against HEAD's tables, which
//     M3 drained;
//   - the server built under core identity while the mount is not
//     LegacyAuthCore, or on a store that is not at BooksIdentityLevel (the
//     merge permanence rule reads s.identity != nil and must agree with the
//     store's level).
//
// B10g, the switch, turned the predicate on for good: there is no selector and
// no state behind it, so every consumer sees the flip on in production and in
// every test alike. The predicate stays a function, and its consumers keep
// their branches, until B10h deletes the plumbing; identityTies still refuses
// a boot whose three parts disagree.

// identityOn reports whether this build runs ABO under core identity: always,
// since B10g.
func identityOn() bool { return true }
