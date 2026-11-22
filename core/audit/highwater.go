// file: core/audit/highwater.go
// version: 0.1.0
// guid: 6e1a9c20-47d3-4f58-8b02-d9a5c3e7f041
// last-edited: 2026-11-22

package audit

import "context"

// HighWater is the audit sequence's boundary for a backup (05-backup §5B.3.5
// step 4, 05-rbac §7.1): it ends the sequence's live lease and returns B such
// that every record allocated after it has a seq above B and every seq
// allocated before it is at most B. A restore copies the pre-swap records above
// the manifest's B (RestoreHook), so a record written between the read and the
// checkpoint, which the checkpoint may lack, is above it.
//
// It is the store's own sequence that is cut, never a second handle on the
// same key: the live lease lives in this Store's Sequence, and a mark stored
// beneath it by another handle would not stop it handing out IDs below. The
// cost is the unissued remainder of the ended lease, skipped and never
// reissued (a record's seq is not gap-free, §7.2), and one Sync commit.
func (s *Store) HighWater(ctx context.Context) (uint64, error) {
	return s.seq.Cut(ctx)
}
