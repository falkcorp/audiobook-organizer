// file: internal/plugins/maintenance/regroup_apply_warnings_test.go
// version: 1.0.0
// guid: c9891cd1-04b0-4b90-aab1-2d7d410fce64
// last-edited: 2026-10-25

package maintenance

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// warningCombiner is a bookCombiner whose combine "commits" and reports warnings,
// the way merge.Service does for a file it could not attach to the survivor.
type warningCombiner struct {
	warnings []string
}

func (c warningCombiner) CombineBooks(ids []string, primaryID string, _ *merge.CombineOverride) (*merge.CombineResult, error) {
	return &merge.CombineResult{PrimaryID: primaryID, JournalID: "journal-1", OperationID: "op-1", Warnings: c.warnings}, nil
}

// A combine that committed but left a file unattached used to be visible only in the
// journal: the regroup apply logged the counts and returned nil, so the review item
// read as cleanly applied. The warnings now reach the notices the review handler
// puts in the approve response.
func TestApplyMultidisc_CombineWarningsReachTheApplyNotices(t *testing.T) {
	store := newApplyTestStore(t)
	ids := []string{ulid.Make().String(), ulid.Make().String()}
	for i, id := range ids {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "Chapter", Format: "mp3", FilePath: "/lib/folder/ch0" + string(rune('1'+i)) + ".mp3"})
		require.NoError(t, err)
	}

	ctx, notices := database.WithApplyNotices(context.Background())
	apply := ApplyMultidisc(store, warningCombiner{warnings: []string{"path held by 2 live book(s) (2 row(s)) (book b2, /lib/folder/ch02.mp3): not reattached"}})
	require.NoError(t, apply(ctx, multidiscItem(t, "/lib/folder", ids)), "the combine committed, so the item is applied")

	lines := notices.Lines()
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "path held by 2 live book(s)")
	assert.Contains(t, lines[0], "/lib/folder", "it says which folder's combine")
	assert.Contains(t, lines[0], "journal-1", "and where to undo it")
}

func TestApplyMultidisc_CleanCombineAddsNoNotice(t *testing.T) {
	store := newApplyTestStore(t)
	ids := []string{ulid.Make().String(), ulid.Make().String()}
	for i, id := range ids {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "Chapter", Format: "mp3", FilePath: "/lib/folder/ch0" + string(rune('1'+i)) + ".mp3"})
		require.NoError(t, err)
	}
	ctx, notices := database.WithApplyNotices(context.Background())
	require.NoError(t, ApplyMultidisc(store, warningCombiner{})(ctx, multidiscItem(t, "/lib/folder", ids)))
	assert.Empty(t, notices.Lines())
}

func TestApplyDuplicateOf_CombineWarningsReachTheApplyNotices(t *testing.T) {
	store := newApplyTestStore(t)
	canonical := "zzz" + ulid.Make().String()
	debris := "aaa" + ulid.Make().String()
	mkDupBook(t, store, canonical, "The Real Book", "/lib/real/book.mp3")
	mkDupBook(t, store, debris, "Debris", "/lib/junk/d1.mp3")
	cands := &fakeCandidates{byEntity: map[string][]database.DedupCandidate{
		debris: {pendingCand(debris, canonical)},
	}}

	ctx, notices := database.WithApplyNotices(context.Background())
	apply := ApplyDuplicateOf(store, warningCombiner{warnings: []string{"path held by 1 live book(s) (1 row(s)): not reattached"}}, cands)
	require.NoError(t, apply(ctx, duplicateOfItem(t, "/lib/junk", []string{debris})))

	lines := notices.Lines()
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "path held by 1 live book(s)")
	assert.Contains(t, lines[0], canonical, "it names the canonical book the debris went into")
}

// End to end through the real merge service: a rowless member whose file two other
// live books hold is retired without a row on the survivor, and the apply reports it.
func TestApplyMultidisc_RealCombineOfARowlessMemberAtAHeldPathReportsIt(t *testing.T) {
	store := newApplyTestStore(t)
	path := "/lib/folder/held.mp3"
	for _, title := range []string{"Holder one", "Holder two"} {
		hid := ulid.Make().String()
		_, err := store.CreateBook(&database.Book{ID: hid, Title: title, Format: "mp3", FilePath: "/lib/elsewhere/" + hid + ".mp3"})
		require.NoError(t, err)
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: hid, FilePath: path, Format: "mp3"}))
	}
	ids := []string{"aaa" + ulid.Make().String(), "bbb" + ulid.Make().String()}
	_, err := store.CreateBook(&database.Book{ID: ids[0], Title: "Survivor", Format: "mp3", FilePath: "/lib/folder/own.mp3"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: ids[1], Title: "Rowless member", Format: "mp3", FilePath: path})
	require.NoError(t, err)

	ctx, notices := database.WithApplyNotices(context.Background())
	apply := ApplyMultidisc(store, merge.NewService(store))
	require.NoError(t, apply(ctx, multidiscItem(t, "/lib/folder", ids)))

	lines := notices.Lines()
	require.Len(t, lines, 1, "the held path is reported")
	assert.True(t, strings.Contains(lines[0], "path held by 2 live book(s)"), lines[0])
	assert.Contains(t, lines[0], ids[1])
}
