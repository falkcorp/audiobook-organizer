// file: internal/maintenance/jobs/user_lock_fakes_test.go
// version: 1.0.0
// guid: 7277579a-80b5-43fe-b808-4c485421f309
// last-edited: 2026-11-18

package jobs

import database "github.com/falkcorp/audiobook-organizer/internal/books/store"

// noUserLocks is the field-lock read of a store on which the user has locked
// nothing. A hand-written fake of a narrow store slice embeds it to satisfy the
// slice's database.MetadataFieldStateReader without modelling locks it does not
// test; a fake that tests locks defines the two methods itself.
type noUserLocks struct{}

func (noUserLocks) GetMetadataFieldStates(string) ([]database.MetadataFieldState, error) {
	return nil, nil
}

func (noUserLocks) GetUserPreference(string) (*database.UserPreference, error) { return nil, nil }
