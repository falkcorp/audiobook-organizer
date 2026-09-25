// file: internal/plugins/maintenance/repoint_missing_to_folder_audio_test.go
// version: 1.1.0
// guid: 7aa3a17c-fb70-48c4-ad3c-0911029bae0b
// last-edited: 2026-09-25

package maintenance

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/stretchr/testify/require"
)

// rfFakeStore holds books and rows in memory. BookFilesAtPath and
// UpdateBookFiles read and write the same rows, so an apply is visible to the
// owner checks that follow it.
type rfFakeStore struct {
	mu         sync.Mutex
	books      []database.BookCore
	rows       map[string][]database.BookFile
	liveAtPath map[string][]string
	// indexNotBuilt reports the book_atpath index as unbuilt.
	indexNotBuilt bool
	updates       []database.BookFile
	// failOnWrite makes UpdateBookFiles fail the test (dry-run guard).
	failOnWrite *testing.T
}

func (s *rfFakeStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []database.BookFileCore
	for _, rows := range s.rows {
		for i := range rows {
			out = append(out, rows[i].Core())
		}
	}
	return out, nil
}

func (s *rfFakeStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	if offset >= len(s.books) {
		return nil, nil
	}
	end := min(offset+limit, len(s.books))
	return s.books[offset:end], nil
}

func (s *rfFakeStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.BookFile(nil), s.rows[bookID]...), nil
}

func (s *rfFakeStore) BookFilesAtPath(path string) ([]database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []database.BookFile
	for _, rows := range s.rows {
		for i := range rows {
			if rows[i].FilePath == path {
				out = append(out, rows[i])
			}
		}
	}
	return out, nil
}

func (s *rfFakeStore) LiveBookIDsAtPath(path string) ([]string, error) {
	return s.liveAtPath[path], nil
}

func (s *rfFakeStore) BookAtPathIndexBuilt() (bool, error) { return !s.indexNotBuilt, nil }

func (s *rfFakeStore) UpdateBookFiles(_ context.Context, files []*database.BookFile, afterRow func(int, bool)) (int, error) {
	if s.failOnWrite != nil {
		s.failOnWrite.Errorf("UpdateBookFiles called during a dry run (%d rows)", len(files))
	}
	s.mu.Lock()
	for _, f := range files {
		s.updates = append(s.updates, *f)
		rows := s.rows[f.BookID]
		for i := range rows {
			if rows[i].ID == f.ID {
				rows[i] = *f
			}
		}
	}
	s.mu.Unlock()
	for i := range files {
		if afterRow != nil {
			afterRow(i, true)
		}
	}
	return len(files), nil
}

func (s *rfFakeStore) updateByID(id string) (database.BookFile, bool) {
	for _, u := range s.updates {
		if u.ID == id {
			return u, true
		}
	}
	return database.BookFile{}, false
}

func rfBookCore(id, path string) database.BookCore {
	yes, organized := true, "organized"
	return database.BookCore{ID: id, Title: "T " + id, FilePath: path, IsPrimaryVersion: &yes, LibraryState: &organized}
}

func rfStubProbe(t *testing.T, dur int) {
	t.Helper()
	restore := bookfileaudio.SetProbeForTesting(func(string) (*mediainfo.MediaInfo, error) {
		return &mediainfo.MediaInfo{Duration: dur, Codec: "aac"}, nil
	})
	t.Cleanup(restore)
}

func rfRun(t *testing.T, store *rfFakeStore, root string, dryRun bool) *rfReport {
	t.Helper()
	env := rfEnv{store: store, rootDir: root, queue: fsQueue{}}
	report, err := repointMissingToFolderAudio(context.Background(), env, rfParams{DryRun: &dryRun}, &fakeReporter{})
	require.NoError(t, err)
	return report
}

func rfOnly(t *testing.T, r *rfReport) rfBookResult {
	t.Helper()
	require.Len(t, r.Books, 1)
	return r.Books[0]
}

// seedConsolidated: three chapter rows whose files are gone, and one
// consolidated file in the same folder.
func seedConsolidated(t *testing.T, root string, consolidatedSize int) (*rfFakeStore, string) {
	t.Helper()
	folder := filepath.Join(root, "Author", "Book")
	target := filepath.Join(folder, "Book.m4b")
	writeFile(t, target, consolidatedSize)
	var rows []database.BookFile
	for i, name := range []string{"Book - 02.mp3", "Book - 01.mp3", "Book - 03.mp3"} {
		rows = append(rows, database.BookFile{
			ID: "f" + string(rune('a'+i)), BookID: "b1", FilePath: filepath.Join(folder, name),
			FileSize: 100, TrackNumber: map[string]int{"Book - 01.mp3": 1, "Book - 02.mp3": 2, "Book - 03.mp3": 3}[name],
			FileHash: "chapter-hash-" + name, AcoustIDFingerprint: []byte("chapter-fp"),
		})
	}
	return &rfFakeStore{
		books: []database.BookCore{rfBookCore("b1", folder)},
		rows:  map[string][]database.BookFile{"b1": rows},
	}, target
}

func TestRepointFolderAudio_ConsolidatedRepointsOneRowAndMarksRestMissing(t *testing.T) {
	root := t.TempDir()
	rfStubProbe(t, 3600)
	store, target := seedConsolidated(t, root, 250)

	got := rfOnly(t, rfRun(t, store, root, false))
	require.Equal(t, rfDecisionConsolidated, got.Decision, got.Reason+" "+got.Detail)
	require.Equal(t, rfOutcomeApplied, got.Outcome, got.Error)
	require.Equal(t, 3600, got.DurationSec)
	require.Len(t, store.updates, 3)

	// The kept row is the first chapter (track 1 = file "fb").
	kept, ok := store.updateByID("fb")
	require.True(t, ok)
	require.Equal(t, target, kept.FilePath)
	require.False(t, kept.Missing)
	require.Equal(t, int64(250), kept.FileSize, "a consolidated row must carry the new file's size")
	require.Equal(t, 3600, kept.Duration, "duration comes from the header probe")
	require.Equal(t, "m4b", kept.Format)
	require.Empty(t, kept.FileHash, "the chapter's hash does not describe the new file")
	require.Nil(t, kept.AcoustIDFingerprint, "the chapter's fingerprint does not describe the new file")

	for _, id := range []string{"fa", "fc"} {
		u, ok := store.updateByID(id)
		require.True(t, ok)
		require.True(t, u.Missing, "row %s must be marked missing, not deleted", id)
		require.NotEqual(t, target, u.FilePath, "a superseded row keeps its old path")
		require.Equal(t, []byte("chapter-fp"), u.AcoustIDFingerprint, "a superseded row keeps its data")
	}
	require.Len(t, store.rows["b1"], 3, "no row is deleted")
}

func TestRepointFolderAudio_OneToOneSizeMatchRepoints(t *testing.T) {
	root := t.TempDir()
	rfStubProbe(t, 1200)
	folder := filepath.Join(root, "Author", "Book")
	present := filepath.Join(folder, "Part 1.mp3")
	renamed := filepath.Join(folder, "Part 2 (renamed).mp3")
	writeFile(t, present, 500)
	writeFile(t, renamed, 777)
	writeFile(t, filepath.Join(folder, "Other.mp3"), 999)
	store := &rfFakeStore{
		books: []database.BookCore{rfBookCore("b1", folder)},
		rows: map[string][]database.BookFile{"b1": {
			{ID: "p1", BookID: "b1", FilePath: present, FileSize: 500, Duration: 600},
			{ID: "p2", BookID: "b1", FilePath: filepath.Join(folder, "Part 2.mp3"), FileSize: 777,
				AcoustIDFingerprint: []byte("same-bytes-fp")},
		}},
	}

	got := rfOnly(t, rfRun(t, store, root, false))
	require.Equal(t, rfDecisionSizeMatch, got.Decision, got.Reason+" "+got.Detail)
	require.Equal(t, rfOutcomeApplied, got.Outcome, got.Error)
	require.Len(t, store.updates, 1, "only the missing row is written")
	u := store.updates[0]
	require.Equal(t, "p2", u.ID)
	require.Equal(t, renamed, u.FilePath)
	require.False(t, u.Missing)
	require.Equal(t, []byte("same-bytes-fp"), u.AcoustIDFingerprint, "same bytes: the fingerprint is kept")
	require.Equal(t, 1200, u.Duration)
}

func TestRepointFolderAudio_AmbiguousCasesAreUntouched(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]int // name -> size in the folder
		sizes  []int64        // missing rows' recorded sizes
		reason string
	}{
		{"two candidates, no size match", map[string]int{"A.m4b": 300, "B.m4b": 310}, []int64{100, 100}, "multiple-candidates"},
		{"no candidate", map[string]int{"cover.jpg": 10}, []int64{100}, "no-candidate"},
		{"two files of the row's size", map[string]int{"A.mp3": 100, "B.mp3": 100}, []int64{100}, "multiple-size-matches"},
		{"stray chapter", map[string]int{"53.mp3": 3}, []int64{1000, 1000}, "size-implausible"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rfStubProbe(t, 60)
			folder := filepath.Join(root, "Author", "Book")
			for n, sz := range tc.files {
				writeFile(t, filepath.Join(folder, n), sz)
			}
			var rows []database.BookFile
			for i, sz := range tc.sizes {
				rows = append(rows, database.BookFile{ID: "r" + string(rune('0'+i)), BookID: "b1",
					FilePath: filepath.Join(folder, "gone", "ch"+string(rune('0'+i))+".mp3"), FileSize: sz})
			}
			store := &rfFakeStore{books: []database.BookCore{rfBookCore("b1", folder)},
				rows: map[string][]database.BookFile{"b1": rows}}
			got := rfOnly(t, rfRun(t, store, root, false))
			require.Equal(t, rfDecisionAmbiguous, got.Decision)
			require.Equal(t, tc.reason, got.Reason, got.Detail)
			require.Empty(t, store.updates, "an ambiguous book must not be written")
		})
	}
}

func TestRepointFolderAudio_FileOwnedByAnotherBookIsUntouched(t *testing.T) {
	t.Run("book_file row of another book", func(t *testing.T) {
		root := t.TempDir()
		rfStubProbe(t, 60)
		store, target := seedConsolidated(t, root, 250)
		yes, imported := true, "imported"
		store.books = append(store.books, database.BookCore{ID: "b2", FilePath: target, IsPrimaryVersion: &yes, LibraryState: &imported})
		store.rows["b2"] = []database.BookFile{{ID: "o1", BookID: "b2", FilePath: target, FileSize: 250}}

		got := rfOnly(t, rfRun(t, store, root, false))
		require.Equal(t, rfDecisionAmbiguous, got.Decision)
		require.Equal(t, "folder-shared-with-other-book", got.Reason)
		require.Empty(t, store.updates)
	})
	t.Run("book whose file_path is the file", func(t *testing.T) {
		root := t.TempDir()
		rfStubProbe(t, 60)
		store, target := seedConsolidated(t, root, 250)
		store.liveAtPath = map[string][]string{target: {"rowless-book"}}

		got := rfOnly(t, rfRun(t, store, root, false))
		require.Equal(t, "folder-shared-with-other-book", got.Reason)
		require.Empty(t, store.updates)
	})
	t.Run("lone unowned file beside another book's audio", func(t *testing.T) {
		root := t.TempDir()
		rfStubProbe(t, 60)
		store, _ := seedConsolidated(t, root, 250)
		other := filepath.Join(root, "Author", "Book", "Other Book - 01.mp3")
		writeFile(t, other, 5000)
		store.books = append(store.books, rfBookCore("b2", other))
		store.rows["b2"] = []database.BookFile{{ID: "o1", BookID: "b2", FilePath: other, FileSize: 5000}}

		got := rfOnly(t, rfRun(t, store, root, false))
		require.Equal(t, "folder-shared-with-other-book", got.Reason,
			"a single unowned file is not this book's consolidation when another book's audio shares the folder")
		require.Empty(t, store.updates)
	})
}

func TestRepointFolderAudio_DryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	rfStubProbe(t, 3600)
	store, target := seedConsolidated(t, root, 250)
	store.failOnWrite = t

	r := rfRun(t, store, root, true)
	got := rfOnly(t, r)
	require.True(t, r.DryRun)
	require.Equal(t, rfDecisionConsolidated, got.Decision)
	require.Equal(t, []string{target}, got.NewPaths)
	require.Equal(t, 3600, got.DurationSec, "the dry run reports the duration the apply would write")
	require.Empty(t, got.Outcome)
	require.Equal(t, 1, r.RowsRepointed)
	require.Equal(t, 2, r.RowsMarkedMissing)
	require.Empty(t, store.updates)
}

func TestRepointFolderAudio_ITunesNeverWritten(t *testing.T) {
	t.Run("rows under books/itunes", func(t *testing.T) {
		root := t.TempDir()
		rfStubProbe(t, 60)
		folder := filepath.Join(root, "books", "itunes", "Author", "Book")
		writeFile(t, filepath.Join(folder, "Book.m4b"), 250)
		store := &rfFakeStore{books: []database.BookCore{rfBookCore("b1", folder)},
			rows: map[string][]database.BookFile{"b1": {{ID: "r1", BookID: "b1",
				FilePath: filepath.Join(folder, "Book - 01.mp3"), FileSize: 200}}}}
		got := rfOnly(t, rfRun(t, store, root, false))
		require.Equal(t, "itunes-path", got.Reason)
		require.Empty(t, store.updates)
	})
	t.Run("row carrying an iTunes persistent id", func(t *testing.T) {
		root := t.TempDir()
		rfStubProbe(t, 60)
		store, _ := seedConsolidated(t, root, 250)
		store.rows["b1"][0].ITunesPersistentID = "ABCDEF0123456789"
		got := rfOnly(t, rfRun(t, store, root, false))
		require.Equal(t, "itunes-linked", got.Reason)
		require.Empty(t, store.updates)
	})
}

func TestRepointFolderAudio_SupersededRowWithDurationIsAmbiguous(t *testing.T) {
	root := t.TempDir()
	rfStubProbe(t, 3600)
	store, _ := seedConsolidated(t, root, 250)
	store.rows["b1"][2].Duration = 900 // "Book - 03.mp3": would be marked missing
	got := rfOnly(t, rfRun(t, store, root, false))
	require.Equal(t, "superseded-rows-carry-duration", got.Reason)
	require.Empty(t, store.updates)
}

func TestRepointFolderAudio_TwoBooksOnOneTargetCollide(t *testing.T) {
	root := t.TempDir()
	rfStubProbe(t, 3600)
	store, _ := seedConsolidated(t, root, 250)
	folder := filepath.Join(root, "Author", "Book")
	store.books = append(store.books, rfBookCore("b2", folder))
	store.rows["b2"] = []database.BookFile{{ID: "x1", BookID: "b2", FilePath: filepath.Join(folder, "dup - 01.mp3"), FileSize: 240}}

	r := rfRun(t, store, root, false)
	require.Len(t, r.Books, 2)
	reasons := []string{r.Books[0].Reason, r.Books[1].Reason}
	sort.Strings(reasons)
	require.Equal(t, []string{"target-collision", "target-collision"}, reasons)
	require.Empty(t, store.updates)
}

func TestRepointFolderAudio_RowlessBookAtTheFolderOwnsIt(t *testing.T) {
	root := t.TempDir()
	rfStubProbe(t, 60)
	store, _ := seedConsolidated(t, root, 250)
	folder := filepath.Join(root, "Author", "Book")
	store.books = append(store.books, rfBookCore("rowless", folder))
	store.liveAtPath = map[string][]string{folder: {"rowless"}}

	got := rfOnly(t, rfRun(t, store, root, false))
	require.Equal(t, "folder-shared-with-other-book", got.Reason, got.Detail)
	require.Empty(t, store.updates, "a book whose file_path is the folder owns its audio")
}

func TestRepointFolderAudio_RefusesWithoutPathIndex(t *testing.T) {
	root := t.TempDir()
	rfStubProbe(t, 60)
	store, _ := seedConsolidated(t, root, 250)
	store.indexNotBuilt = true
	dry := true
	report, err := repointMissingToFolderAudio(context.Background(),
		rfEnv{store: store, rootDir: root, queue: fsQueue{}}, rfParams{DryRun: &dry}, &fakeReporter{})
	require.Error(t, err)
	require.Contains(t, report.Aborted, "book_atpath index not built")
	require.Empty(t, store.updates)
}

// A consolidation resets the kept row even when its recorded size happens to
// equal the new file's: it described one chapter, and a chapter duration left
// on it would survive EnsureDuration and become the whole book's length.
func TestRepointFolderAudio_ConsolidationResetsKeptRowEvenOnEqualSize(t *testing.T) {
	root := t.TempDir()
	rfStubProbe(t, 3600)
	store, target := seedConsolidated(t, root, 250)
	for i := range store.rows["b1"] {
		if store.rows["b1"][i].ID == "fb" { // track 1: the kept row
			store.rows["b1"][i].FileSize = 250
			store.rows["b1"][i].Duration = 120
		}
	}
	got := rfOnly(t, rfRun(t, store, root, false))
	require.Equal(t, rfOutcomeApplied, got.Outcome, got.Detail+" "+got.Error)
	kept, ok := store.updateByID("fb")
	require.True(t, ok)
	require.Equal(t, target, kept.FilePath)
	require.Equal(t, 3600, kept.Duration, "the chapter's 120 s must not survive as the whole file's duration")
}

func TestRepointFolderAudio_ReportsRequestedIDsOutOfScope(t *testing.T) {
	root := t.TempDir()
	rfStubProbe(t, 60)
	store, _ := seedConsolidated(t, root, 250)
	dry := true
	report, err := repointMissingToFolderAudio(context.Background(),
		rfEnv{store: store, rootDir: root, queue: fsQueue{}},
		rfParams{DryRun: &dry, BookIDs: []string{"b1", "nope"}}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, []string{"nope"}, report.OutOfScope)
	require.Equal(t, 1, report.Planned)
}
