// file: internal/plugins/maintenance/merge_same_path_dupes_test.go
// version: 1.0.0
// guid: 5d2e8a41-9c3b-4f7e-8a10-2b6c4d9e1f30
// last-edited: 2026-09-05

package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

type mergeFakeStore struct {
	books []database.BookCore
	rows  map[string][]database.BookFile // bookID → rows
}

func (f *mergeFakeStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	if offset >= len(f.books) {
		return nil, nil
	}
	end := offset + limit
	if end > len(f.books) {
		end = len(f.books)
	}
	return f.books[offset:end], nil
}
func (f *mergeFakeStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return f.rows[bookID], nil
}

// recordingMerge captures every merge call so a test can assert what was merged
// and, just as importantly, that nothing else was.
type recordingMerge struct {
	calls [][]string // each call's bookIDs, primary first
}

func (m *recordingMerge) fn(bookIDs []string, primaryID string) (int, error) {
	m.calls = append(m.calls, append([]string{primaryID}, diff(bookIDs, primaryID)...))
	return len(bookIDs) - 1, nil
}

func diff(ids []string, primary string) []string {
	var out []string
	for _, id := range ids {
		if id != primary {
			out = append(out, id)
		}
	}
	return out
}

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool     { return &b }

// Two single-file records on the same file with matching hashes merge, keeping the
// metadata-bearing record as primary.
func TestMergeSamePath_MergesHashConfirmedDuplicate(t *testing.T) {
	path := "/lib/Author/Book.m4b"
	store := &mergeFakeStore{
		books: []database.BookCore{
			{ID: "shell", FilePath: path},                                            // bare rescanned shell
			{ID: "meta", FilePath: path, MetadataReviewStatus: strptr("matched")},    // the applied record
		},
		rows: map[string][]database.BookFile{
			"shell": {{ID: "r1", BookID: "shell", FilePath: path, FileHash: "HASH-A"}},
			"meta":  {{ID: "r2", BookID: "meta", FilePath: path, FileHash: "HASH-A"}},
		},
	}
	m := &recordingMerge{}
	plan, err := planMergeSamePathDupes(context.Background(), store, m.fn,
		mergeSamePathParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.SharedPaths)
	require.Equal(t, 1, plan.Mergeable)
	require.Equal(t, 1, plan.Merged)
	require.Equal(t, 1, plan.RecordsMerged)
	require.Len(t, m.calls, 1)
	require.Equal(t, "meta", m.calls[0][0], "the metadata-bearing record must be primary")
	require.ElementsMatch(t, []string{"meta", "shell"}, m.calls[0])
}

// Dry run reports the mergeable group but merges nothing.
func TestMergeSamePath_DryRunMergesNothing(t *testing.T) {
	path := "/lib/Author/Book.m4b"
	store := &mergeFakeStore{
		books: []database.BookCore{
			{ID: "a", FilePath: path}, {ID: "b", FilePath: path},
		},
		rows: map[string][]database.BookFile{
			"a": {{ID: "r1", BookID: "a", FilePath: path, FileHash: "H"}},
			"b": {{ID: "r2", BookID: "b", FilePath: path, FileHash: "H"}},
		},
	}
	m := &recordingMerge{}
	plan, err := planMergeSamePathDupes(context.Background(), store, m.fn,
		mergeSamePathParams{}, &fakeReporter{}) // Apply defaults false
	require.NoError(t, err)
	require.Equal(t, 1, plan.Mergeable)
	require.Equal(t, 0, plan.Merged)
	require.Empty(t, m.calls, "DRY RUN MUST NOT MERGE")
}

// A stored-hash disagreement across records on the same path is refused, not merged.
func TestMergeSamePath_RefusesHashMismatch(t *testing.T) {
	path := "/lib/Author/Book.m4b"
	store := &mergeFakeStore{
		books: []database.BookCore{{ID: "a", FilePath: path}, {ID: "b", FilePath: path}},
		rows: map[string][]database.BookFile{
			"a": {{ID: "r1", BookID: "a", FilePath: path, FileHash: "H1"}},
			"b": {{ID: "r2", BookID: "b", FilePath: path, FileHash: "H2"}},
		},
	}
	m := &recordingMerge{}
	plan, err := planMergeSamePathDupes(context.Background(), store, m.fn,
		mergeSamePathParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.HashMismatch)
	require.Equal(t, 0, plan.Mergeable)
	require.Empty(t, m.calls)
}

// A record with no stored hash for the shared file cannot be vouched for; the group
// is left for review rather than merged blind.
func TestMergeSamePath_RefusesUnverifiedHash(t *testing.T) {
	path := "/lib/Author/Book.m4b"
	store := &mergeFakeStore{
		books: []database.BookCore{{ID: "a", FilePath: path}, {ID: "b", FilePath: path}},
		rows: map[string][]database.BookFile{
			"a": {{ID: "r1", BookID: "a", FilePath: path, FileHash: "H"}},
			"b": {{ID: "r2", BookID: "b", FilePath: path}}, // no hash
		},
	}
	m := &recordingMerge{}
	plan, err := planMergeSamePathDupes(context.Background(), store, m.fn,
		mergeSamePathParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.UnverifiedHash)
	require.Equal(t, 0, plan.Mergeable)
	require.Empty(t, m.calls)
}

// A directory book (FilePath is a directory, not an audio file) is never grouped,
// so same-directory records are out of scope — this op is same-file only.
func TestMergeSamePath_IgnoresDirectoryPaths(t *testing.T) {
	dirPath := "/lib/Author/Book" // no audio extension
	store := &mergeFakeStore{
		books: []database.BookCore{{ID: "a", FilePath: dirPath}, {ID: "b", FilePath: dirPath}},
		rows:  map[string][]database.BookFile{},
	}
	m := &recordingMerge{}
	plan, err := planMergeSamePathDupes(context.Background(), store, m.fn,
		mergeSamePathParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 0, plan.SharedPaths, "directory paths must not be grouped")
	require.Empty(t, m.calls)
}

// A soft-deleted record must not pull a live book into a merge, nor count as a
// duplicate: only one live record remains at the path.
func TestMergeSamePath_ExcludesSoftDeleted(t *testing.T) {
	path := "/lib/Author/Book.m4b"
	store := &mergeFakeStore{
		books: []database.BookCore{
			{ID: "live", FilePath: path},
			{ID: "trashed", FilePath: path, MarkedForDeletion: boolptr(true)},
		},
		rows: map[string][]database.BookFile{
			"live":    {{ID: "r1", BookID: "live", FilePath: path, FileHash: "H"}},
			"trashed": {{ID: "r2", BookID: "trashed", FilePath: path, FileHash: "H"}},
		},
	}
	m := &recordingMerge{}
	plan, err := planMergeSamePathDupes(context.Background(), store, m.fn,
		mergeSamePathParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 0, plan.SharedPaths, "a soft-deleted record leaves only one live book at the path")
	require.Empty(t, m.calls)
}

// Primary election prefers the organized record when neither is a metadata match,
// then falls back to the oldest by CreatedAt.
func TestElectPrimary_PrefersOrganizedThenOldest(t *testing.T) {
	older := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	group := []database.BookCore{
		{ID: "z-new-plain", CreatedAt: &newer},
		{ID: "a-organized", LibraryState: strptr("organized"), CreatedAt: &newer},
		{ID: "m-old-plain", CreatedAt: &older},
	}
	require.Equal(t, "a-organized", electPrimary(group).ID)

	// With no organized record, the oldest wins.
	group2 := []database.BookCore{
		{ID: "z", CreatedAt: &newer},
		{ID: "a", CreatedAt: &older},
	}
	require.Equal(t, "a", electPrimary(group2).ID)

	// A metadata match beats an organized record.
	group3 := []database.BookCore{
		{ID: "organized", LibraryState: strptr("organized"), CreatedAt: &older},
		{ID: "matched", MetadataReviewStatus: strptr("matched"), CreatedAt: &newer},
	}
	require.Equal(t, "matched", electPrimary(group3).ID)
}
