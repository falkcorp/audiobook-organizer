// file: internal/database/member_list_merge.go
// version: 1.0.0
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
