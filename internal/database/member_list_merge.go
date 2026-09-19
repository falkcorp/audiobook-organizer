// file: internal/database/member_list_merge.go
// version: 1.1.0
// guid: bf80550f-b696-40ac-a320-8549eb1bf408
// last-edited: 2026-09-19

package database

// MergeMemberListNoLoss is the ONLY safe way to apply a client-supplied WHOLE
// membership list (playlist book_ids / items, collection books) to a stored
// list: the client's members first, in the client's order, then every stored
// member the client did not mention, in stored order. Duplicates collapse.
//
// 🔴 WHY NOT A REPLACE (review of #3470, 2026-09-19). A whole-list replace
// cannot tell "the user removed C" from "the user never saw C": the app reads
// {A,B}, the web UI adds C, the app sends a reorder [B,A], and a replace writes
// [B,A] — C is gone and every party got a 200. A version check does not help,
// because the retry re-applies the same stale list to the fresh row. So a list
// is applied as a reorder of the members it names plus an add of any it names
// that are new; REMOVAL only happens through an explicit remove request (batch
// remove / item delete / DELETE book), which names what to drop. When the
// incoming set equals the stored set this is exactly the reorder it always was.
func MergeMemberListNoLoss(stored, incoming []string) []string {
	out := make([]string, 0, len(stored)+len(incoming))
	seen := make(map[string]struct{}, len(stored)+len(incoming))
	for _, list := range [][]string{incoming, stored} {
		for _, id := range list {
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// MemberListOmitted returns the stored members incoming does not name (after
// mapping each stored id through canonical, when non-nil, so a merge loser the
// client knows only by its survivor counts as named).
//
// A whole-list update that omits a stored member is ambiguous: the user may
// have removed it, or the client may never have seen it (added concurrently).
// MergeMemberListNoLoss would keep it — a silent no-op for a client that meant
// "remove". Callers therefore REFUSE such an update with 409 and a message
// pointing at the explicit remove routes; only a pure reorder or an addition
// (incoming names every stored member) is applied.
func MemberListOmitted(stored, incoming []string, canonical func(string) string) []string {
	named := make(map[string]struct{}, len(incoming))
	for _, id := range incoming {
		named[id] = struct{}{}
	}
	var omitted []string
	for _, id := range stored {
		if _, ok := named[id]; ok {
			continue
		}
		if canonical != nil {
			if _, ok := named[canonical(id)]; ok {
				continue
			}
		}
		omitted = append(omitted, id)
	}
	return omitted
}
