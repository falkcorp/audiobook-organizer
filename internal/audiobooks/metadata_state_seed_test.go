// file: internal/audiobooks/metadata_state_seed_test.go
// version: 1.0.0
// guid: 1c754dd6-cc3a-4e71-9bd5-e66bb37bd2f4
// last-edited: 2026-10-03

package audiobooks

// saveMetadataState replaces a book's whole field state, for tests.
// Production code changes state through modifyMetadataState.
func (svc *AudiobookService) saveMetadataState(bookID string, state map[string]metadataFieldState) error {
	return svc.modifyMetadataState(bookID, func(cur map[string]metadataFieldState) error {
		for k := range cur {
			delete(cur, k)
		}
		for k, v := range state {
			cur[k] = v
		}
		return nil
	})
}
