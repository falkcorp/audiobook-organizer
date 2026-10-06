// file: internal/plugins/maintenance/reparse_folder_names_fixer_test.go
// version: 1.1.0
// guid: f823acd0-485e-4ec1-87a1-49cfedc17554
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// A field is a row only when the stored value is what the OLD folder parse
// wrote and the new parse reads something else.
func TestReparseChanges_OnlyFieldsTheOldParseWrote(t *testing.T) {
	legacy := &metadata.FolderMetadata{Title: "2018", Authors: []string{"Mara Quill"}, SeriesName: "Old Series"}
	fresh := &metadata.FolderMetadata{Title: "Glasswake", Authors: []string{"Dorian Vex"}, SeriesName: "Driftworld", SeriesPosition: 24}

	got := reparseChanges(reparseStored{Title: "2018", Author: "Mara Quill", Series: "Old Series"}, legacy, fresh)
	require.Len(t, got, 3)
	assert.Equal(t, reparseChange{Class: reparseClassTitle, From: "2018", To: "Glasswake"}, got[0])
	assert.Equal(t, reparseChange{Class: reparseClassAuthor, From: "Mara Quill", To: "Dorian Vex"}, got[1])
	assert.Equal(t, reparseChange{Class: reparseClassSeries, From: "Old Series", To: "Driftworld", ToPosition: 24}, got[2])

	// A title from a tag or an edit (not what the old parse wrote) is never a row.
	assert.Empty(t, reparseChanges(reparseStored{Title: "Glass Wake (Tagged)"}, legacy, &metadata.FolderMetadata{Title: "Glasswake"}))
	// A proposed title that is the book's own author is no title.
	assert.Empty(t, reparseChanges(reparseStored{Title: "1966", Author: "Gene Holt"}, &metadata.FolderMetadata{Title: "1966"},
		&metadata.FolderMetadata{Title: "Gene Holt"}))
	// The new parse reading nothing never clears a field.
	assert.Empty(t, reparseChanges(reparseStored{Title: "2018", Author: "Mara Quill"}, legacy, &metadata.FolderMetadata{}))
}

// Plan lists the title rows the new parse changes, holds a locked title,
// leaves alone a book whose title is not the old parse's, and the framework
// skips an iTunes-owned path. Apply writes ONLY the approved row ids.
func TestReparseFolderNamesFixer_PlansAndAppliesByIDsOnly(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	add := func(title, path string) string {
		b, err := st.CreateBook(&database.Book{Title: title, FilePath: path, Format: "m4b"})
		require.NoError(t, err)
		return b.ID
	}
	approved := add("2018", "/srv/library/Mara Quill/2018/2018 - Glasswake")
	notApproved := add("2019", "/srv/library/Mara Quill/2019/2019 - Copper Rain")
	locked := add("2020", "/srv/library/Mara Quill/2020/2020 - Ember Road")
	tagged := add("Glass Orchard", "/srv/library/Mara Quill/2021/2021 - Glass Orchard Deluxe")
	itunes := add("2017", "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Mara Quill/2017 - Paper Moon")
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: locked, Field: "title",
		OverrideLocked: true, UpdatedAt: time.Now()}))

	f := newReparseFolderNamesFixer(&Plugin{deps: fakeDeps{store: st}})
	res, err := repairs.RunPlan(context.Background(), f, nil, repairs.PlanDeps{Guard: st}, &fakeReporter{})
	require.NoError(t, err)
	byID := map[string]repairs.Row{}
	for _, r := range res.Rows {
		byID[r.RowID] = r
	}
	require.Contains(t, byID, approved+"|title")
	assert.Equal(t, "Glasswake", byID[approved+"|title"].Proposed["title"])
	assert.Equal(t, "2018", byID[approved+"|title"].Current["title"])
	assert.Empty(t, byID[notApproved+"|title"].Skipped)
	assert.Equal(t, junkSkipUserLocked, byID[locked+"|title"].Skipped)
	assert.NotContains(t, byID, tagged+"|title", "a title the old parse did not write is never a row")
	if r, ok := byID[itunes+"|title"]; ok {
		assert.NotEmpty(t, r.Skipped, "an iTunes-owned book is never applicable")
	}

	const opID = "op-reparse"
	w := repairs.NewWriter(st, st, f.ID(), "bulk_update", "repairs-").WithJournal(st, st, opID).WithFieldStates(st)
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{approved + "|title", locked + "|title"}, false,
		repairs.ApplyDeps{Guard: st, Writer: w, OpID: opID}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied, "%v", out.ByOutcome)
	for id, want := range map[string]string{approved: "Glasswake", notApproved: "2019", locked: "2020", tagged: "Glass Orchard"} {
		b, err := st.GetBookByID(id)
		require.NoError(t, err)
		assert.Equal(t, want, b.Title)
	}
}

// The fixer reads folders with the scanner's evidence (foldernames): a
// series row named after its own author is author junk and no series
// evidence, while a real series beside a junk author row of its name stays a
// series.
func TestReparseEvidence_FiltersAuthorJunkSeries(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	sanderson, err := st.CreateAuthor("Brandon Sanderson")
	require.NoError(t, err)
	junk, err := st.CreateSeries("Brandon Sanderson", &sanderson.ID)
	require.NoError(t, err)
	_, err = st.CreateBook(&database.Book{Title: "Elantris", AuthorID: &sanderson.ID, SeriesID: &junk.ID, FilePath: "/srv/a.m4b"})
	require.NoError(t, err)
	_, err = st.CreateAuthor("Star Wars")
	require.NoError(t, err)
	zahn, err := st.CreateAuthor("Timothy Zahn")
	require.NoError(t, err)
	sw, err := st.CreateSeries("Star Wars", nil)
	require.NoError(t, err)
	_, err = st.CreateBook(&database.Book{Title: "Thrawn", AuthorID: &zahn.ID, SeriesID: &sw.ID, FilePath: "/srv/b.m4b"})
	require.NoError(t, err)

	ev, err := reparseEvidence(st)
	require.NoError(t, err)
	fm, err := metadata.ExtractMetadataFromFolderWith("/srv/library/Brandon Sanderson/Brandon Sanderson - Elantris", ev)
	require.NoError(t, err)
	assert.Equal(t, []string{"Brandon Sanderson"}, fm.Authors)
	assert.Equal(t, "", fm.SeriesName)
	fm, err = metadata.ExtractMetadataFromFolderWith("/srv/library/Star Wars/Star Wars - Thrawn", ev)
	require.NoError(t, err)
	assert.Empty(t, fm.Authors)
	assert.Equal(t, "Star Wars", fm.SeriesName)
}
