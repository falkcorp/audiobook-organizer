// file: core/auth/external.go
// version: 1.0.0
// guid: 5c8d1e72-9b34-4a06-8e1f-2d7a4b9c0e53
// last-edited: 2026-11-11

package auth

// The account-write seam auth/oidc shares with Service (F4-03, 05-authn §4.7).
//
// An OIDC sign-in creates, links and un-mails accounts without an actor, so it
// cannot go through the actor-checked user methods; it writes the identity
// Store itself. Two things it must not do alone: the store indexes usernames
// and not e-mails, so "no other account holds this address verified" holds
// only under the lock every other creator takes (CreateUser, AcceptInvite);
// and HasUsers is a flag Service owns. Both are handed out here, and nothing
// else of Service is.

// LockEmails takes the e-mail lock every account creator holds around "no
// other account holds this address verified" and the write that depends on
// it, and returns the function that releases it. A caller holds it across the
// check and the commit, never across a network call.
func (s *Service) LockEmails() (unlock func()) {
	s.emailMu.Lock()
	return s.emailMu.Unlock
}

// NoteUserCreated records that a user now exists, so stage 12 stops treating
// the app as first-run (HasUsers). Safe to call for every creation.
func (s *Service) NoteUserCreated() { s.hasUsers.Store(true) }
