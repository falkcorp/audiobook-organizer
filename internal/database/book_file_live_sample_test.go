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
	root := "/lib/Gotham/Arkham"
	want := map[string]bool{}
	for i, name := range []string{"hush", "knightfall", "year-one", "long-halloween", "no-mans-land", "the-killing-joke", "dark-victory"} {
		p := filepath.Join(root, []string{"Bruce Wayne", "Alfred Pennyworth", "Dick Grayson", "Barbara Gordon", "Jim Gordon", "Selina Kyle", "Harvey Dent"}[i], name+".m4b")
		liveFile(t, store, name, p)
		want[p] = true
	}
	// Not evidence: a sibling folder sharing the prefix, a file outside, a trashed book.
	liveFile(t, store, "sibling", "/lib/Gotham/Arkham2/asylum.m4b")
	liveFile(t, store, "outside", "/lib/Gotham/Wayne/year-two.m4b")
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
		liveFile(t, store, fmt.Sprintf("out-%03d", i), fmt.Sprintf("/lib/Gotham/Arkham/%03d.m4b", i))
	}
	liveFile(t, store, "in-1", "/lib/Gotham/Wayne/hush.m4b")
	liveFile(t, store, "in-2", "/lib/Gotham/Wayne/year-one.m4b")
	for i := 0; i < 20; i++ {
		got, err := store.SampleLiveBookFilePathsUnder("/lib/Gotham/Wayne", 50)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"/lib/Gotham/Wayne/hush.m4b", "/lib/Gotham/Wayne/year-one.m4b"}, got)
	}
}

// With more files than k the sample stops at k, one path per book, and spreads
// over the library rather than being one run of neighbours.
func TestSampleLiveBookFilePathsUnder_StopsAtKAndSpreads(t *testing.T) {
	store := samplerStore(t)
	for i := 0; i < 120; i++ {
		liveFile(t, store, fmt.Sprintf("b-%03d", i), fmt.Sprintf("/lib/Gotham/%c-Batcave/hush-%03d.m4b", 'A'+rune(i%26), i))
	}
	// A second file of an already-listed book is not a second sample.
	require.NoError(t, store.CreateBookFile(&BookFile{ID: "b-000-g", BookID: "b-000", FilePath: "/lib/Gotham/A-Batcave/hush-000-extra.m4b", Format: "m4b"}))
	got, err := store.SampleLiveBookFilePathsUnder("/lib/Gotham", 50)
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
	got, err := store.SampleLiveBookFilePathsUnder("/lib/Gotham", 50)
	require.NoError(t, err)
	assert.Empty(t, got)
	got, err = store.SampleLiveBookFilePathsUnder("", 50)
	require.NoError(t, err)
	assert.Empty(t, got)
	got, err = store.SampleLiveBookFilePathsUnder("/lib/Gotham", 0)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// firstBytesOf is firstBytesUnder over the store's memdb, for the tests.
func firstBytesOf(t *testing.T, store *PebbleStore, prefix string) []byte {
	t.Helper()
	m := store.memOrRefuse("test")
	require.NotNil(t, m, "memdb must be serving")
	txn := m.db.Txn(false)
	defer txn.Abort()
	s := &liveSample{txn: txn}
	got, err := s.firstBytesUnder(prefix)
	require.NoError(t, err)
	return got
}

// n-1. The places a sample starts from are the folders that exist under the root,
// found from the index: a library of three folders offers exactly their three
// first characters, in order, and nothing outside the root or beyond a sibling
// that shares the prefix.
func TestFirstBytesUnder_AreTheFoldersThatExist(t *testing.T) {
	store := samplerStore(t)
	for i, folder := range []string{"Arkham", "Gotham", "Gotham-2", "Wayne", "Wayne-2"} {
		liveFile(t, store, fmt.Sprintf("f-%d", i), "/lib/Gotham/"+folder+"/hush.m4b")
	}
	liveFile(t, store, "sibling", "/lib/Gotham2/Zed/hush.m4b")
	liveFile(t, store, "outside", "/lib/Batcave/Zed/hush.m4b")
	got := firstBytesOf(t, store, "/lib/Gotham/")
	assert.Equal(t, []byte{'A', 'G', 'W'}, got)
	assert.Empty(t, firstBytesOf(t, store, "/lib/Nowhere/"))
}

// n-1. A byte of 0xff is the last one the hop can seek past: it ends the hop
// instead of wrapping to 0x00 and looping.
func TestFirstBytesUnder_StopsAtTheLastByte(t *testing.T) {
	store := samplerStore(t)
	liveFile(t, store, "low", "/lib/Gotham/Arkham/hush.m4b")
	liveFile(t, store, "high", "/lib/Gotham/\xffbatcave/hush.m4b")
	assert.Equal(t, []byte{'A', 0xff}, firstBytesOf(t, store, "/lib/Gotham/"))
}

// n-1. Every chunk of the sample starts at a folder that exists, in a random
// order, so with as many folders as chunks every sample takes from every folder.
// Before, starts came from a fixed alphabet and a library of folders that begin
// with "A", "G" and "W" began most chunks at its first folder: the others were
// reached by luck.
func TestSampleLiveBookFilePathsUnder_EveryFolderIsSampledNotJustTheFirst(t *testing.T) {
	store := samplerStore(t)
	folders := []string{"Arkham", "Gotham", "Wayne"}
	for _, folder := range folders {
		for i := 0; i < 20; i++ {
			liveFile(t, store, fmt.Sprintf("%s-%02d", folder, i), fmt.Sprintf("/lib/Gotham/%s/hush-%02d.m4b", folder, i))
		}
	}
	firstFolders := map[string]bool{}
	for run := 0; run < 40; run++ {
		got, err := store.SampleLiveBookFilePathsUnder("/lib/Gotham", 5) // five chunks of one path
		require.NoError(t, err)
		require.Len(t, got, 5, "%v", got)
		firstFolders[filepath.Base(filepath.Dir(got[0]))] = true
		seen := map[string]bool{}
		for _, p := range got {
			seen[filepath.Base(filepath.Dir(p))] = true
		}
		for _, folder := range folders {
			assert.True(t, seen[folder], "run %d: nothing sampled from %s: %v", run, folder, got)
		}
	}
	// The order of the starts is random: the first chunk does not always begin
	// at the same folder (three folders, 40 runs: all-equal by chance is 3/3^40).
	assert.Greater(t, len(firstFolders), 1, "every run began at the same folder: %v", firstFolders)
}

// n-8. A Pebble store with the in-memory layer off cannot sample (a scan of every
// book_file is an outage, not a fallback): it says so with the sentinel the
// library-root check turns into "not ready".
func TestSampleLiveBookFilePathsUnder_MemdbOffIsUnavailable(t *testing.T) {
	store := samplerStore(t)
	liveFile(t, store, "a", "/lib/Gotham/Arkham/hush.m4b")
	store.UseMemDB = false
	got, err := store.SampleLiveBookFilePathsUnder("/lib/Gotham", 50)
	require.ErrorIs(t, err, ErrLiveSampleUnavailable)
	assert.Empty(t, got)
}

// Round 13 SH-1. A book_file row whose path is exactly the root with its
// trailing separator (a directory path stored as a file) sorts first under the
// root. The first-byte hop used to seek back onto that same row, spent all its
// hops, and reported "nothing under the root", which the root check reads as a
// ready library.
func TestSampleLiveBookFilePathsUnder_ARowThatIsTheRootDoesNotHideTheLibrary(t *testing.T) {
	store := samplerStore(t)
	for i := 0; i < 30; i++ {
		liveFile(t, store, fmt.Sprintf("b%02d", i), fmt.Sprintf("/lib/Gotham/Bruce Wayne/hush-%02d.m4b", i))
	}
	liveFile(t, store, "root-row", "/lib/Gotham/")

	got, err := store.SampleLiveBookFilePathsUnder("/lib/Gotham", 50)
	require.NoError(t, err)
	assert.Len(t, got, 30, "the library is still seen: %v", got)
	assert.NotContains(t, got, "/lib/Gotham/", "a directory path is not evidence of a file")
}

// The same row must not be the only thing the sample finds: a root that holds
// nothing but that row has no live file, and says so.
func TestSampleLiveBookFilePathsUnder_AloneARootRowIsNoEvidence(t *testing.T) {
	store := samplerStore(t)
	liveFile(t, store, "root-row", "/lib/Gotham/")
	got, err := store.SampleLiveBookFilePathsUnder("/lib/Gotham", 50)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFirstBytesUnder_SkipsARowThatIsTheRoot(t *testing.T) {
	store := samplerStore(t)
	liveFile(t, store, "root-row", "/lib/Gotham/")
	liveFile(t, store, "a", "/lib/Gotham/Arkham/hush.m4b")
	liveFile(t, store, "w", "/lib/Gotham/Wayne/year-one.m4b")
	assert.Equal(t, []byte{'A', 'W'}, firstBytesOf(t, store, "/lib/Gotham/"))
}
