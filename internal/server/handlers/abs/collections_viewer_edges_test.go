// file: internal/server/handlers/abs/collections_viewer_edges_test.go
// version: 1.0.0
// guid: 9a4e2c61-7d03-4f58-b1a9-d6c80e3f2547
// last-edited: 2026-10-25

package abs_test

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	searchidx "github.com/falkcorp/audiobook-organizer/internal/search"
)

// B1: a stored per-viewer query that no longer parses serves NO books on the ABS
// surface, in the list and in the detail, with or without a per-viewer evaluation
// injected. Nobody can say it reads no per-user state, so its stored list may be a
// creator's; it is never served.
func TestCollections_ABSServesNothingForAnUnparseablePerUserQuery(t *testing.T) {
	const broken = `read_status:finished "unterminated`
	for _, injected := range []bool{true, false} {
		name := "no evaluation injected"
		if injected {
			name = "evaluation injected"
		}
		t.Run(name, func(t *testing.T) {
			f := newViewerFixture(t)
			f.store.cols = append(f.store.cols,
				database.Collection{
					ID: "bad1", Name: "No Man's Land", Type: database.CollectionTypeDynamic, Query: broken,
					CreatedByUserID: "alice", MaterializedBookIDs: []string{f.seed.singleID},
				},
				database.Collection{
					ID: "ok1", Name: "Hush", Type: database.CollectionTypeDynamic, Query: "title:single",
					CreatedByUserID: "alice", MaterializedBookIDs: []string{f.seed.singleID},
				})
			opts := []harnessOpt{withLibrary(f.seed), withUserData(fixtureUserData()), withCollections(f.store)}
			if injected {
				opts = append(opts, withViewerMembers(f.native))
			}
			h := newHarness(t, "jwt", nil, opts...)
			h.seedUser(t, "alice", "alice", "", "pw-pw-pw-pw")
			tok := str(t, userObj(t, h.login(t, "alice", "pw-pw-pw-pw")), "accessToken")

			rows := abscolResults(t, h, tok)
			if got := bookIDsOf(rowNamed(t, rows, "No Man's Land")); len(got) != 0 {
				t.Errorf("list served %v for an unparseable per-user query; it must serve none", got)
			}
			single := absplSyncIDFor(t, f.seed, f.seed.singleID)
			if got := bookIDsOf(rowNamed(t, rows, "Hush")); !equalIDs(got, []string{single}) {
				t.Errorf("control: a plain query keeps its stored list, got %v", got)
			}

			code, body := h.doAny(t, request{method: http.MethodGet, path: "/api/collections/bad1", headers: bearer(tok)})
			if code != http.StatusOK {
				t.Fatalf("GET collection = %d", code)
			}
			detail, _ := body.(map[string]any)
			if got := bookIDsOf(detail); len(got) != 0 {
				t.Errorf("detail served %v for an unparseable per-user query; it must serve none", got)
			}
		})
	}
}

// S3: an ABS PATCH of a per-viewer collection (a rename) reads the row, edits the
// name and writes the row back. Against a row that still holds a creator's list
// (a pre-change leftover) the store must drop the list on that write, or the PATCH
// writes it back with a fresh Version. The fake stores do not scrub, so this runs
// against the real one.
func TestCollections_ABSPatchOfAPerViewerRowLeavesNoStoredList(t *testing.T) {
	ps, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "c.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	// A legacy row can only be seeded with the store's classifier off.
	database.SetCollectionPerViewerClassifier(nil)
	legacy, err := ps.CreateCollection(&database.Collection{
		Name: "Hush", Type: database.CollectionTypeDynamic, Query: "read_status:finished",
		CreatedByUserID: "alice", MaterializedBookIDs: []string{"b1", "b2"},
	})
	database.SetCollectionPerViewerClassifier(searchidx.QueryMayUsePerUserField)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := ps.GetCollection(legacy.ID); len(got.MaterializedBookIDs) != 2 {
		t.Fatalf("fixture: the legacy row must hold a creator's list, has %v", got.MaterializedBookIDs)
	}

	h, _, tok := abscolHarness(t, ps)
	code, _ := h.doAny(t, request{
		method: http.MethodPatch, path: "/api/collections/" + legacy.ID, headers: bearer(tok),
		body: map[string]any{"name": "Renamed Knightfall"},
	})
	if code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200", code)
	}
	got, err := ps.GetCollection(legacy.ID)
	if err != nil || got == nil {
		t.Fatalf("get after patch: %v %v", got, err)
	}
	if got.Name != "Renamed Knightfall" {
		t.Errorf("the rename was lost: name = %q", got.Name)
	}
	if len(got.MaterializedBookIDs) != 0 {
		t.Errorf("the PATCH left a stored member list %v on a per-viewer row", got.MaterializedBookIDs)
	}
}

// S5: lastUpdate of a per-viewer collection is the time of the request, because
// its members move with the viewer's state and the library and none of that
// touches the row; a collection whose members live on the row keeps UpdatedAt.
func TestCollections_ABSLastUpdateOfAPerViewerCollectionIsNow(t *testing.T) {
	f := newViewerFixture(t)
	stamp := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, c := range []database.Collection{
		{ID: "pv", Name: "Year One", Type: database.CollectionTypeDynamic, Query: "read_status:finished"},
		{ID: "pl", Name: "Hush", Type: database.CollectionTypeDynamic, Query: "title:single",
			MaterializedBookIDs: []string{f.seed.singleID}},
		{ID: "st", Name: "The Long Halloween", Type: database.CollectionTypeStatic, BookIDs: []string{f.seed.singleID}},
	} {
		c.UpdatedAt = stamp
		f.store.cols = append(f.store.cols, c)
	}
	h := newHarness(t, "jwt", nil, withLibrary(f.seed), withUserData(fixtureUserData()),
		withCollections(f.store), withViewerMembers(f.native))
	h.seedUser(t, "alice", "alice", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "alice", "pw-pw-pw-pw")), "accessToken")

	before := time.Now().UnixMilli()
	rows := abscolResults(t, h, tok)
	after := time.Now().UnixMilli()

	last := func(name string) int64 {
		v, _ := rowNamed(t, rows, name)["lastUpdate"].(float64)
		return int64(v)
	}
	if got := last("Year One"); got < before || got > after {
		t.Errorf("per-viewer lastUpdate = %d, want the request time in [%d, %d]", got, before, after)
	}
	for _, name := range []string{"Hush", "The Long Halloween"} {
		if got := last(name); got != stamp.UnixMilli() {
			t.Errorf("%s: lastUpdate = %d, want its UpdatedAt %d", name, got, stamp.UnixMilli())
		}
	}
}
