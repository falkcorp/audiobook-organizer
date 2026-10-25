// file: internal/database/collection_perviewer_test.go
// version: 1.0.0
// guid: 9d4a7e31-5f82-4c06-b3a9-1e6c8d0f2a75
// last-edited: 2026-10-25

package database

import (
	"strings"
	"testing"
)

// useTestClassifier installs a stand-in for search.QueryMayUsePerUserField (this
// package cannot import search) and removes it afterwards.
func useTestClassifier(t *testing.T) {
	t.Helper()
	SetCollectionPerViewerClassifier(func(q string) bool { return strings.Contains(q, "read_status") })
	t.Cleanup(func() { SetCollectionPerViewerClassifier(nil) })
}

func TestUpdateCollection_ClearsAPerViewerMemberListNoMatterWhoWrites(t *testing.T) {
	useTestClassifier(t)
	store := newTestCollectionsStore(t)
	created, err := store.CreateCollection(&Collection{
		Name: "finished", Type: CollectionTypeDynamic, Query: "read_status:finished",
		MaterializedBookIDs: []string{"b1", "b2"}, // a stale list from before the change
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetCollection(created.ID); len(got.MaterializedBookIDs) != 0 {
		t.Fatalf("CreateCollection stored a per-viewer member list: %v", got.MaterializedBookIDs)
	}

	// The writer that read the row, edited the name and wrote it back with the
	// list it loaded: the ABS PATCH path.
	row, _ := store.GetCollection(created.ID)
	row.Name = "done"
	row.MaterializedBookIDs = []string{"b1", "b2", "b3"}
	if err := store.UpdateCollection(row); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCollection(created.ID)
	if len(got.MaterializedBookIDs) != 0 || got.Name != "done" {
		t.Fatalf("after UpdateCollection: name=%q members=%v; want the rename kept and no stored members", got.Name, got.MaterializedBookIDs)
	}
}

func TestUpdateCollection_KeepsTheListOfACollectionThatIsNotPerViewer(t *testing.T) {
	useTestClassifier(t)
	store := newTestCollectionsStore(t)
	created, err := store.CreateCollection(&Collection{
		Name: "Hush", Type: CollectionTypeDynamic, Query: "author:kyle",
		MaterializedBookIDs: []string{"b1", "b2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	created.Description = "d"
	if err := store.UpdateCollection(created); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetCollection(created.ID)
	if len(got.MaterializedBookIDs) != 2 {
		t.Fatalf("a collection with no per-user field lost its list: %v", got.MaterializedBookIDs)
	}
}

func TestUpdateCollection_WithoutAClassifierStoresTheListAsGiven(t *testing.T) {
	SetCollectionPerViewerClassifier(nil)
	store := newTestCollectionsStore(t)
	created, _ := store.CreateCollection(&Collection{
		Name: "finished", Type: CollectionTypeDynamic, Query: "read_status:finished",
		MaterializedBookIDs: []string{"b1"},
	})
	if got, _ := store.GetCollection(created.ID); len(got.MaterializedBookIDs) != 1 {
		t.Fatalf("with no classifier the store must not guess: %v", got.MaterializedBookIDs)
	}
}
