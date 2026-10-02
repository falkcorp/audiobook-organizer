// file: internal/reconcile/membership_lock_writers_test.go
// version: 1.0.0
// guid: 7e2a6c91-4f3b-4d58-b0c7-1a9e5d8f2b36
// last-edited: 2026-10-02

package reconcile

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// AssignOrphanVGs moves an ungrouped book into a new group, so it waits for a
// holder of the no-group sentinel (a regroup apply relying on its target
// staying ungrouped).
func TestAssignOrphanVGs_WaitsForNoGroupSentinel(t *testing.T) {
	prevRoot := config.AppConfig.RootDir
	defer func() { config.AppConfig.RootDir = prevRoot }()
	const libRoot = "/lib"
	config.AppConfig.RootDir = libRoot

	store := newFakeStore()
	const id = "orphan"
	path := libRoot + "/orphan.m4b"
	store.books = append(store.books, database.BookCore{ID: id, Title: id, FilePath: path})
	store.byID[id] = &database.Book{ID: id, Title: id, FilePath: path}

	vptest.RequireWaitsForHolder(t,
		func() func() { return versionprimary.LockGroup("") },
		func() error {
			res, err := AssignOrphanVGs(store, libRoot)
			if err == nil && res.Assigned != 1 {
				t.Errorf("Assigned=%d, want 1", res.Assigned)
			}
			return err
		},
		func() bool {
			b, err := store.GetBookByID(id)
			return err == nil && b != nil && (b.VersionGroupID == nil || *b.VersionGroupID == "")
		})
}
