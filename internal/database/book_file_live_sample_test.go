// file: internal/database/book_file_live_sample_test.go
// version: 1.0.0
// guid: d41a7c2e-9b6f-4e03-8a57-61f0c93e2b8d
// last-edited: 2026-10-26

package database

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// samplerStore opens a Pebble store with memdb serving.
func samplerStore(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()
	return store
}

// sampleTitles are Batman comic storylines, the only titles the fixtures use.
var sampleTitles = []string{"The Long Halloween", "Year One", "Hush", "Knightfall", "No Man's Land", "The Killing Joke", "Dark Victory", "Court of Owls"}

// liveFile adds a live book with one file at path.
func liveFile(t *testing.T, store *PebbleStore, id, path string) {
	t.Helper()
	_, err := store.CreateBook(&Book{ID: id, Title: sampleTitles[len(id)%len(sampleTitles)], FilePath: path})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&BookFile{ID: id + "-f", BookID: id, FilePath: path, Format: "m4b"}))
}

// SH-3 / SH-4. The sample is every live in-root file when there are fewer than k,
// whatever the random start, and never one of a trashed book, of a file outside
// the root, or of a folder that only shares the root's name as a prefix.
func TestSampleLiveBookFilePathsUnder_ShortAnswerIsTheWholePopulation(t *testing.T) {
	store := samplerStore(t)
	root := "/lib/Gotham"
	want := map[string]bool{}
	for i, name := range []string{"hush", "knightfall", "year-one", "long-halloween", "no-mans-land", "the-killing-joke", "dark-victory"} {
		p := filepath.Join(root, fmt.Sprintf("Author %d", i), name+".m4b")
		liveFile(t, store, name, p)
		want[p] = true
	}
	// Not evidence: a sibling folder sharing the prefix, a file outside, a trashed book.
	liveFile(t, store, "sibling", "/lib/Gotham2/arkham/asylum.m4b")
	liveFile(t, store, "outside", "/elsewhere/batcave/year-two.m4b")
	liveFile(t, store, "trashed", filepath.Join(root, "Zed", "hush-2.m4b"))
	yes := true
	_, err := store.ModifyBook("trashed", func(b *Book) error { b.MarkedForDeletion = &yes; return nil })
	require.NoError(t, err)

	for i := 0; i < 20; i++ { // the start is random: every start must give the whole set
		got, err := store.SampleLiveBookFilePathsUnder(root, 50)
		require.NoError(t, err)
		require.Len(t, got, len(want), "%v", got)
		for _, p := range got {
			assert.True(t, want[p], "%s is not live evidence under the root", p)
		}
	}
}

// SH-3. A library whose first thousands of rows are outside the root, and whose
// in-root files come later in path order, still yields them: the empty window
// that read as "ready" cannot happen.
func TestSampleLiveBookFilePathsUnder_FindsInRootFilesBehindManyOutsideOnes(t *testing.T) {
	store := samplerStore(t)
	for i := 0; i < 450; i++ {
		liveFile(t, store, fmt.Sprintf("out-%03d", i), fmt.Sprintf("/aaa/itunes/%03d.m4b", i))
	}
	liveFile(t, store, "in-1", "/zzz/lib/Gotham/hush.m4b")
	liveFile(t, store, "in-2", "/zzz/lib/Gotham/year-one.m4b")
	for i := 0; i < 20; i++ {
		got, err := store.SampleLiveBookFilePathsUnder("/zzz/lib", 50)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"/zzz/lib/Gotham/hush.m4b", "/zzz/lib/Gotham/year-one.m4b"}, got)
	}
}

// With more files than k the sample stops at k, one path per book, and spreads
// over the library rather than being one run of neighbours.
func TestSampleLiveBookFilePathsUnder_StopsAtKAndSpreads(t *testing.T) {
	store := samplerStore(t)
	for i := 0; i < 120; i++ {
		liveFile(t, store, fmt.Sprintf("b-%03d", i), fmt.Sprintf("/lib/%c-author/title-%03d.m4b", 'A'+rune(i%26), i))
	}
	// A second file of an already-listed book is not a second sample.
	require.NoError(t, store.CreateBookFile(&BookFile{ID: "b-000-g", BookID: "b-000", FilePath: "/lib/A-author/title-000-extra.m4b", Format: "m4b"}))
	got, err := store.SampleLiveBookFilePathsUnder("/lib", 50)
	require.NoError(t, err)
	assert.Len(t, got, 50)
	seen := map[string]bool{}
	folders := map[string]bool{}
	for _, p := range got {
		assert.False(t, seen[p], "duplicate %s", p)
		seen[p] = true
		folders[filepath.Dir(p)] = true
	}
	assert.Greater(t, len(folders), 5, "the sample comes from several places in the library")
}

func TestSampleLiveBookFilePathsUnder_EmptyAndDegenerateArguments(t *testing.T) {
	store := samplerStore(t)
	got, err := store.SampleLiveBookFilePathsUnder("/lib", 50)
	require.NoError(t, err)
	assert.Empty(t, got)
	got, err = store.SampleLiveBookFilePathsUnder("", 50)
	require.NoError(t, err)
	assert.Empty(t, got)
	got, err = store.SampleLiveBookFilePathsUnder("/lib", 0)
	require.NoError(t, err)
	assert.Empty(t, got)
}
