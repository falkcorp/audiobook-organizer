// file: internal/usermerge/unavailable.go
// version: 0.1.0
// guid: 3a7c1e52-9d40-4b8f-a6e3-5f20c8d1b974
// last-edited: 2026-11-21

package usermerge

import "errors"

// ProblemUnavailableUnderIdentity is the problem code of the 409 a merge preview,
// apply or resume answers once core identity is wired and the merge has not yet
// been ported onto core's auth.Admin.StageUserMerge (F4-08 B10l).
const ProblemUnavailableUnderIdentity = "merge_unavailable_under_identity"

// ErrUnavailableUnderIdentity: with core identity wired, the accounts, keys and
// sign-ins a merge plans from and writes live in core's store, not in the store
// this service holds (M3 retires u:, apikey: and oauthid: there). A plan built
// from it would read nothing and an apply would write rows nothing reads, so the
// merge refuses before it builds a plan or reads a journal.
var ErrUnavailableUnderIdentity = errors.New("usermerge: " + ProblemUnavailableUnderIdentity + ": account merges are not available once the identity store is core's")

// CheckAvailable refuses with ErrUnavailableUnderIdentity when the service's
// wiring says the identity flip has happened (WithIdentityFlipped), and answers
// nil otherwise (a service built without the option answers as HEAD does). The
// handler asks it before it resolves an account or builds a plan; BuildPlan,
// Apply and Resume ask it again, so no entry point reaches the books store's
// user rows on the strength of the handler alone (the operation runs later,
// from a grant minted before a restart that may have flipped).
func (s *Service) CheckAvailable() error {
	if s.flipped() && !s.identityReady() {
		return ErrUnavailableUnderIdentity
	}
	return nil
}
