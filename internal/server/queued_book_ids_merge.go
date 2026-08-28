// file: internal/server/queued_book_ids_merge.go
// version: 1.0.0
// guid: f48d0f0d-f4dd-44ed-b70c-f3c21c2ce63e
// last-edited: 2026-08-28

package server

// mergeUniqueBookIDs preserves first-seen order while producing the union of
// two book selections. Queue mergers use it only after confirming that all
// behavior-affecting flags are identical.
func mergeUniqueBookIDs(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, id := range append(existing, incoming...) {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		merged = append(merged, id)
	}
	return merged
}
