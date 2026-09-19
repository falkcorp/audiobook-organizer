// file: internal/organizer/collision_durability_unknown_test.go
// version: 1.0.0
// guid: b26f1a67-9376-4be9-b012-28b6d7b9e636
// last-edited: 2026-09-19

package organizer

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// collisionDurUnknownStore restores the row (records it) and reports that
// the write's fsync failed.
type collisionDurUnknownStore struct {
	CollisionStore
	restored map[string]string
}

func (s *collisionDurUnknownStore) UpdateBookFile(id string, f *database.BookFile) error {
	s.restored[id] = f.FilePath
	return fmt.Errorf("%w: injected fsync failure", database.ErrBookFileDurabilityUnknown)
}

// A restore that was applied (only its fsync failed) is not a failed
// rollback and must not be reported as one.
func TestCollisionRollback_DurabilityUnknownRestoreIsNotAFailure(t *testing.T) {
	store := &collisionDurUnknownStore{restored: map[string]string{}}
	j := &collisionJournal{
		store: store,
		rows:  []rowRestore{{fileID: "f1", prior: &database.BookFile{ID: "f1", FilePath: "/lib/orig.mp3"}}},
	}
	var res RenameFilesResult
	j.rollback(&res)
	if store.restored["f1"] != "/lib/orig.mp3" {
		t.Fatalf("restore not attempted: %v", store.restored)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("an applied restore was recorded as a failure: %v", res.Errors)
	}
}
