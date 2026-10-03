// file: internal/server/metadata_state_seed_test.go
// version: 1.0.0
// guid: 36b14a56-0231-47fa-8b33-ff883f53c7fb
// last-edited: 2026-10-03

package server

import "github.com/falkcorp/audiobook-organizer/internal/metafetch"

// saveMetadataState replaces a book's whole field state, for seeding tests.
// Production code changes state through metafetch.WithStateSnapshot.
func (s *Server) saveMetadataState(bookID string, state map[string]metafetch.MetadataFieldState) error {
	return metafetch.WithStateSnapshot(s.Ops(), bookID, func(cur map[string]metafetch.MetadataFieldState) error {
		for k := range cur {
			delete(cur, k)
		}
		for k, v := range state {
			cur[k] = v
		}
		return nil
	})
}
