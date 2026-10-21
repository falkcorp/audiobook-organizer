// file: internal/dedup/split_book_recheck_test.go
// version: 1.0.1
// guid: 8d3f5b21-6a94-4c70-b1e8-2f7a4c9d0e35
// last-edited: 2026-10-21

package dedup

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recheckReader is the book and file rows a recheck reads, keyed by book id.
type recheckReader struct {
	books map[string]*database.Book
	files map[string][]database.BookFile
	err   error
}

func (r *recheckReader) GetBookByID(id string) (*database.Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.books[id], nil
}

func (r *recheckReader) GetBookFiles(id string) ([]database.BookFile, error) {
	return r.files[id], nil
}

// add stores a live book at path with one file row of the given duration (0
// stores a book with no file row at all).
func (r *recheckReader) add(id, title, path string, fileSecs int) {
	if r.books == nil {
		r.books, r.files = map[string]*database.Book{}, map[string][]database.BookFile{}
	}
	r.books[id] = &database.Book{ID: id, Title: title, FilePath: path}
	if fileSecs != 0 {
		r.files[id] = []database.BookFile{{ID: "f-" + id, BookID: id, Duration: fileSecs}}
	}
}

// chapters is a synthetic three-chapter flat cluster that passes every test.
func chapters() *recheckReader {
	r := &recheckReader{}
	for i := 1; i <= 3; i++ {
		r.add(fmt.Sprintf("b%d", i), fmt.Sprintf("Sample Work - Chapter %d", i), fmt.Sprintf("/lib/Sample Author/Sample Work/%02d.mp3", i), 600)
	}
	return r
}

var chapterIDs = []string{"b1", "b2", "b3"}

func reasonsOf(t *testing.T, r SplitBookRecheckReader, ids []string) string {
	t.Helper()
	got, err := RecheckSplitBookMembers(r, ids, testChapterMaxSec)
	require.NoError(t, err)
	return strings.Join(got, " | ")
}

func TestRecheckSplitBookMembers_ChaptersPass(t *testing.T) {
	assert.Empty(t, reasonsOf(t, chapters(), chapterIDs))
}

func TestRecheckSplitBookMembers_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *recheckReader)
		ids    []string
		want   string
	}{
		{"a member is gone", func(r *recheckReader) { delete(r.books, "b2") }, chapterIDs, "gone or already retired"},
		{"a member is soft-deleted", func(r *recheckReader) {
			yes := true
			r.books["b2"].MarkedForDeletion = &yes
		}, chapterIDs, "gone or already retired"},
		{"a member has no file rows", func(r *recheckReader) { delete(r.files, "b3") }, chapterIDs, "no file rows"},
		{"a member has no stored path", func(r *recheckReader) { r.books["b3"].FilePath = "" }, chapterIDs, "no stored path"},
		{"a file has no recorded duration", func(r *recheckReader) { r.files["b1"][0].Duration = 0 }, chapterIDs, "no recorded duration"},
		{"a file is at the duration limit", func(r *recheckReader) { r.files["b1"][0].Duration = testChapterMaxSec }, chapterIDs, "run 120 min or longer"},
		{"V3: a book's own duration is over the limit", func(r *recheckReader) {
			d := testChapterMaxSec + 60
			r.books["b2"].Duration = &d
		}, chapterIDs, "V3"},
		{"V2: titles carry different volume numbers", func(r *recheckReader) {
			r.books["b1"].Title, r.books["b2"].Title, r.books["b3"].Title = "Sample Saga Book 1", "Sample Saga Book 2", "Sample Saga Book 3"
		}, chapterIDs, "V2"},
		{"V4: the folder names several works", func(r *recheckReader) {
			for i := 1; i <= 3; i++ {
				r.books[fmt.Sprintf("b%d", i)].FilePath = fmt.Sprintf("/lib/Sample Author/Sample Complete Collection/%02d.mp3", i)
			}
		}, chapterIDs, "V4"},
		{"V5: the numbers are years", func(r *recheckReader) {
			for i := 1; i <= 3; i++ {
				r.books[fmt.Sprintf("b%d", i)].FilePath = fmt.Sprintf("/lib/Sample Author/Sample Almanac/%d.mp3", 2009+i)
			}
		}, chapterIDs, "V5"},
		{"V9: grandparent subfolders name different things", func(r *recheckReader) {
			for i, name := range []string{"Alpha", "Beta", "Gamma"} {
				r.books[fmt.Sprintf("b%d", i+1)].FilePath = fmt.Sprintf("/lib/Sample Author/Sample Work/%s/01.mp3", name)
			}
		}, chapterIDs, "V9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := chapters()
			tc.mutate(r)
			assert.Contains(t, reasonsOf(t, r, tc.ids), tc.want)
		})
	}
}

// The shape is read off the members' CURRENT paths, never off the stored
// candidate: the same members under per-part subfolders ("Part 1".."Part 3")
// are still one work, so the grandparent shape passes V9 for them.
func TestRecheckSplitBookMembers_GrandparentPartsPass(t *testing.T) {
	r := &recheckReader{}
	for i := 1; i <= 3; i++ {
		r.add(fmt.Sprintf("b%d", i), fmt.Sprintf("Sample Work (%d of 3)", i), fmt.Sprintf("/lib/Sample Author/Sample Work/Part %d/%02d.mp3", i, i), 600)
	}
	assert.Empty(t, reasonsOf(t, r, chapterIDs))
}

// A member that cannot be read is an error, not a pass and not a refusal.
func TestRecheckSplitBookMembers_ReadErrorFailsClosed(t *testing.T) {
	r := chapters()
	r.err = errors.New("db down")
	got, err := RecheckSplitBookMembers(r, chapterIDs, testChapterMaxSec)
	require.Error(t, err)
	assert.Empty(t, got)
	assert.False(t, errors.Is(err, ErrSplitBookRefused))
}

// recheckMergeMock models a keep ("K") and two sources over the given reader's
// rows, counting the writes the merge makes.
func recheckMergeMock(r *recheckReader, writes *int) *database.MockStore {
	m := &database.MockStore{}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) { return r.GetBookByID(id) }
	m.GetBookFilesFunc = func(id string) ([]database.BookFile, error) { return r.GetBookFiles(id) }
	m.MoveBookFilesToBookFunc = func([]string, string, string) error { *writes++; return nil }
	m.UpdateBookFunc = func(_ string, b *database.Book) (*database.Book, error) { *writes++; return b, nil }
	m.DeleteBookFunc = func(string) error { *writes++; return nil }
	return m
}

// A stored candidate that is now vetoed is refused before any write.
func TestMergeSplitBookClusterRechecked_RefusesVetoedWithoutWriting(t *testing.T) {
	r := chapters()
	r.books["b1"].Title, r.books["b2"].Title, r.books["b3"].Title = "Sample Saga Book 1", "Sample Saga Book 2", "Sample Saga Book 3"
	writes := 0
	res, err := MergeSplitBookClusterRechecked(recheckMergeMock(r, &writes), chapterIDs, "b1", "Sample Saga", SplitMergeOptions{})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.True(t, errors.Is(err, ErrSplitBookRefused))
	var refusal *SplitBookRefusal
	require.True(t, errors.As(err, &refusal))
	assert.Contains(t, strings.Join(refusal.Reasons, " | "), "V2")
	assert.Zero(t, writes, "a refused cluster writes nothing")
}

func TestMergeSplitBookClusterRechecked_RefusesUnknownDurationWithoutWriting(t *testing.T) {
	r := chapters()
	r.files["b2"][0].Duration = 0
	writes := 0
	_, err := MergeSplitBookClusterRechecked(recheckMergeMock(r, &writes), chapterIDs, "b1", "", SplitMergeOptions{})
	assert.True(t, errors.Is(err, ErrSplitBookRefused))
	assert.Zero(t, writes)
}

func TestMergeSplitBookClusterRechecked_MergesAClusterThatStillPasses(t *testing.T) {
	r := chapters()
	writes := 0
	res, err := MergeSplitBookClusterRechecked(recheckMergeMock(r, &writes), chapterIDs, "b1", "Sample Work", SplitMergeOptions{})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 2, res.MergedSrcCount)
	assert.NotZero(t, writes)
}

func TestMergeSplitBookClusterRechecked_KeepMustBeInTheCluster(t *testing.T) {
	writes := 0
	_, err := MergeSplitBookClusterRechecked(recheckMergeMock(chapters(), &writes), chapterIDs, "nope", "", SplitMergeOptions{})
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrSplitBookRefused))
	assert.Zero(t, writes)
}

// The recheck is the merge's Precheck, so it runs under the merge lock: a row
// that changes between the call and the lock being taken is still caught.
func TestMergeSplitBookClusterRechecked_RecheckRunsUnderTheLock(t *testing.T) {
	r := chapters() // passes when the merge is called
	writes := 0
	locked := false
	opts := SplitMergeOptions{AcquireLock: func(lock, unlock func()) error {
		lock()
		locked = true
		// A concurrent edit lands after the caller's call but before the
		// first write: the members now read as separate volumes.
		r.books["b1"].Title, r.books["b2"].Title, r.books["b3"].Title = "Sample Saga Book 1", "Sample Saga Book 2", "Sample Saga Book 3"
		return nil
	}}
	res, err := MergeSplitBookClusterRechecked(recheckMergeMock(r, &writes), chapterIDs, "b1", "Sample Saga", opts)
	require.True(t, locked)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.True(t, errors.Is(err, ErrSplitBookRefused), "the Precheck wrap keeps errors.Is working: %v", err)
	assert.Zero(t, writes, "a refusal under the lock writes nothing")
}

// A caller's own Precheck still runs, after the recheck and under the lock; a
// refused recheck short-circuits it.
func TestMergeSplitBookClusterRechecked_CallerPrecheckStillRuns(t *testing.T) {
	boom := errors.New("caller precheck says no")
	called := 0
	opts := SplitMergeOptions{Precheck: func() error { called++; return boom }}
	writes := 0
	_, err := MergeSplitBookClusterRechecked(recheckMergeMock(chapters(), &writes), chapterIDs, "b1", "", opts)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, called)
	assert.Zero(t, writes)

	r := chapters()
	r.files["b2"][0].Duration = 0
	called = 0
	_, err = MergeSplitBookClusterRechecked(recheckMergeMock(r, &writes), chapterIDs, "b1", "", opts)
	assert.True(t, errors.Is(err, ErrSplitBookRefused))
	assert.Zero(t, called, "the recheck refused first")
}
