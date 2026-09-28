// file: internal/scanner/file_ownership_test.go
// version: 1.1.0
// guid: 6c2a43e1-d6ec-4eae-8570-41c987062ce6
// last-edited: 2026-09-28

package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/require"
)

// ownershipErrStore fails the path lookup, to prove the check fails closed.
type ownershipErrStore struct{ *database.PebbleStore }

func (ownershipErrStore) BookFilesAtPath(string) ([]database.BookFile, error) {
	return nil, errors.New("injected: path index unavailable")
}

// ownershipDanglingStore reports one book as gone while its book_file rows
// remain, the dangling-row shape.
type ownershipDanglingStore struct {
	*database.PebbleStore
	goneID string
}

func (s ownershipDanglingStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.goneID {
		return nil, nil
	}
	return s.PebbleStore.GetBookByID(id)
}

// ownershipFixture is a multi-file parent book normalized to its directory,
// owning three chapter files, plus two files nobody owns.
type ownershipFixture struct {
	store    *database.PebbleStore
	parent   *database.Book
	dir      string
	chapters []string
	loose    []string
}

func newOwnershipFixture(t *testing.T) ownershipFixture {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)

	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	dir := filepath.Join(root, "Christopher Paolini", "Eldest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	chapters := []string{write("97.mp3", "a"), write("98.mp3", "b"), write("99.mp3", "c")}
	loose := []string{write("new-1.mp3", "d"), write("new-2.mp3", "e")}

	parent, err := store.CreateBook(&database.Book{Title: "Eldest", FilePath: dir})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	for i, p := range chapters {
		if err := store.CreateBookFile(&database.BookFile{BookID: parent.ID, FilePath: p, TrackNumber: i + 1}); err != nil {
			t.Fatalf("create book_file %s: %v", p, err)
		}
	}
	return ownershipFixture{store: store, parent: parent, dir: dir, chapters: chapters, loose: loose}
}

func useScannerStore(t *testing.T, s scannerStore) {
	t.Helper()
	SetStore(s)
	t.Cleanup(func() { SetStore(nil) })
}

func TestCheckFileOwnership(t *testing.T) {
	cases := []struct {
		name     string
		book     func(f ownershipFixture) *Book
		wrap     func(f ownershipFixture) scannerStore
		wantSkip bool
		wantErr  bool
		// wantAppend: a staged arrival, appended to the parent.
		wantAppend []string
		// otherOwner: the skip names a book minted inside wrap, not the parent.
		otherOwner bool
	}{
		{
			name: "unowned single file is a new book",
			book: func(f ownershipFixture) *Book { return &Book{FilePath: f.loose[0]} },
		},
		{
			name:     "chapter owned by a multi-file parent is a fragment",
			book:     func(f ownershipFixture) *Book { return &Book{FilePath: f.chapters[1]} },
			wantSkip: true,
		},
		{
			name: "rescan of the normalized multi-file book is the same book",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.chapters[0], SegmentFiles: f.chapters}
			},
		},
		{
			name: "group mixing an owned chapter with unowned files is a fragment",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.loose[0], SegmentFiles: []string{f.loose[0], f.chapters[2], f.loose[1]}}
			},
			wantSkip: true,
		},
		{
			name: "two of three chapters is a piece of the parent",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.chapters[0], SegmentFiles: f.chapters[:2]}
			},
			wantSkip: true,
		},
		{
			name: "a book already at the path is an update",
			book: func(f ownershipFixture) *Book { return &Book{FilePath: f.dir} },
		},
		{
			// A directory-shaped book with no row at the folder: its files are
			// the folder's audio files, and the parent owns three of them while
			// two are loose. A new directory book there would duplicate the
			// parent's rows; the parent is whole inside the folder, so the two
			// loose files are its staged arrivals.
			name: "new directory book over owned files appends to the owner",
			book: func(f ownershipFixture) *Book { return &Book{FilePath: f.dir} },
			wrap: func(f ownershipFixture) scannerStore {
				_, err := f.store.ModifyBook(f.parent.ID, func(b *database.Book) error {
					b.FilePath = f.chapters[0] // not normalized: no row at the folder
					return nil
				})
				if err != nil {
					panic(err)
				}
				return f.store
			},
			wantSkip:   true,
			wantAppend: []string{"new-1.mp3", "new-2.mp3"},
		},
		{
			name: "dangling row whose book is gone owns nothing",
			book: func(f ownershipFixture) *Book { return &Book{FilePath: f.chapters[1]} },
			wrap: func(f ownershipFixture) scannerStore {
				return ownershipDanglingStore{PebbleStore: f.store, goneID: f.parent.ID}
			},
		},
		{
			// B2: the parent is whole inside the scanned set and the other two
			// files have no rows: a download scanned half-written, now complete.
			name: "staged arrival appends to the single owner",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.chapters[0], SegmentFiles: append(slices.Clone(f.chapters), f.loose...)}
			},
			wantSkip:   true,
			wantAppend: []string{"new-1.mp3", "new-2.mp3"},
		},
		{
			// S5: a soft-deleted book still holding a row at one chapter does
			// not turn the rescan of the live parent into "several owners".
			name: "soft-deleted co-owner is ignored when a live owner exists",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.chapters[0], SegmentFiles: f.chapters}
			},
			wrap: func(f ownershipFixture) scannerStore {
				softDeletedOwnerOf(f, f.chapters[1])
				return f.store
			},
		},
		{
			// S5, the other half: with no live owner the soft-deleted book's
			// claim still counts, so a group mixing its file with unowned
			// ones is a fragment, not a new book over a deleted book's file.
			name: "soft-deleted sole owner still counts",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.loose[0], SegmentFiles: f.loose}
			},
			wrap: func(f ownershipFixture) scannerStore {
				softDeletedOwnerOf(f, f.loose[0])
				return f.store
			},
			wantSkip:   true,
			otherOwner: true,
		},
		{
			// S6: fragments that never got rows leave the group looking
			// unowned, but a book already sits at its first file. Saving would
			// overwrite that book with the group's values.
			name: "unowned group over a rowless book at its first file is not overlaid",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.loose[0], SegmentFiles: f.loose}
			},
			wrap: func(f ownershipFixture) scannerStore {
				if _, err := f.store.CreateBook(&database.Book{Title: "01", FilePath: f.loose[0]}); err != nil {
					panic(err)
				}
				return f.store
			},
			wantSkip:   true,
			otherOwner: true,
		},
		{
			// S6 with an owner: the parent owns every scanned file, but a
			// different book sits at the group's first file.
			name: "owned group over a different book at its first file is not overlaid",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.chapters[0], SegmentFiles: f.chapters}
			},
			wrap: func(f ownershipFixture) scannerStore {
				if _, err := f.store.CreateBook(&database.Book{Title: "97", FilePath: f.chapters[0]}); err != nil {
					panic(err)
				}
				return f.store
			},
			wantSkip:   true,
			otherOwner: true,
		},
		{
			name:    "store error fails closed",
			book:    func(f ownershipFixture) *Book { return &Book{FilePath: f.loose[0]} },
			wrap:    func(f ownershipFixture) scannerStore { return ownershipErrStore{f.store} },
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwnershipFixture(t)
			var s scannerStore = f.store
			if tc.wrap != nil {
				s = tc.wrap(f)
			}
			useScannerStore(t, s)

			v, err := checkFileOwnership(tc.book(f))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if v.skip != tc.wantSkip {
				t.Fatalf("skip = %v (%s), want %v", v.skip, v.reason, tc.wantSkip)
			}
			if tc.wantSkip && !tc.otherOwner && (len(v.owners) != 1 || v.owners[0] != f.parent.ID) {
				t.Fatalf("owners = %v, want [%s]", v.owners, f.parent.ID)
			}
			if tc.otherOwner && (len(v.owners) == 0 || slices.Equal(v.owners, []string{f.parent.ID})) {
				t.Fatalf("owners = %v, want the book minted by the case", v.owners)
			}
			var gotAppend []string
			for _, p := range v.unowned {
				gotAppend = append(gotAppend, filepath.Base(p))
			}
			if !slices.Equal(gotAppend, tc.wantAppend) {
				t.Fatalf("append = %v, want %v", gotAppend, tc.wantAppend)
			}
			if (v.appendTo != "") != (tc.wantAppend != nil) || (v.appendTo != "" && v.appendTo != f.parent.ID) {
				t.Fatalf("appendTo = %q, want parent %q only for a staged arrival", v.appendTo, f.parent.ID)
			}
		})
	}
}

// TestSaveBookToDatabase_ChapterOfOwnedBookIsNotImported is the end-to-end
// shape of the Eldest incident: a chapter file already owned by its parent's
// book_file row is scanned on its own, carrying the parent's organizer-ID tag.
// No book may be created for it, and the parent's FilePath must not be
// repointed at the chapter by the organizer-ID relink.
func TestSaveBookToDatabase_ChapterOfOwnedBookIsNotImported(t *testing.T) {
	f := newOwnershipFixture(t)
	useScannerStore(t, f.store)

	chapter := &Book{
		FilePath:        f.chapters[1],
		Title:           "98",
		Author:          "Eldest",
		Format:          ".mp3",
		BookOrganizerID: f.parent.ID,
	}
	if err := saveBookToDatabase(context.Background(), chapter); !errors.Is(err, errFileOwnedByOtherBook) {
		t.Fatalf("saveBookToDatabase = %v, want errFileOwnedByOtherBook", err)
	}
	if got, _ := f.store.GetBookByFilePath(f.chapters[1]); got != nil {
		t.Fatalf("a book %s was created at the chapter path", got.ID)
	}
	parent, err := f.store.GetBookByID(f.parent.ID)
	if err != nil || parent == nil {
		t.Fatalf("parent lookup: %v", err)
	}
	if parent.FilePath != f.dir {
		t.Fatalf("parent FilePath = %q, want %q (relinked to a chapter)", parent.FilePath, f.dir)
	}
	if a, _ := f.store.GetAuthorByName("Eldest"); a != nil {
		t.Fatalf("author %q was created for a skipped fragment", a.Name)
	}

	// And an unowned file in the same folder still imports.
	loose := &Book{FilePath: f.loose[0], Title: "New", Author: "Someone", Format: ".mp3"}
	if err := saveBookToDatabase(context.Background(), loose); err != nil {
		t.Fatalf("save unowned: %v", err)
	}
	if got, _ := f.store.GetBookByFilePath(f.loose[0]); got == nil {
		t.Fatalf("unowned file was not imported")
	}
}

// TestSaveBookToDatabase_RegroupedFragmentFolderIsNotOverlaid is the first
// rescan after the wider chapter grouping deploys: a folder whose chapters were
// ALREADY imported as one single-file book each is now emitted as one
// multi-file group whose FilePath is fragment #1's path. The path hit must not
// be treated as "update fragment #1 with the group's values" -- the files
// belong to several books, so the group is a fragment set and is skipped.
func TestSaveBookToDatabase_RegroupedFragmentFolderIsNotOverlaid(t *testing.T) {
	f := newOwnershipFixture(t)
	useScannerStore(t, f.store)

	var frags []*database.Book
	for i, p := range f.loose {
		b, err := f.store.CreateBook(&database.Book{Title: fmt.Sprintf("%02d", i+1), FilePath: p})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: p}); err != nil {
			t.Fatal(err)
		}
		frags = append(frags, b)
	}

	group := &Book{FilePath: f.loose[0], SegmentFiles: f.loose, Title: "The Whole Book", Author: "Someone", Duration: 36000}
	v, err := checkFileOwnership(group)
	if err != nil || !v.skip || len(v.owners) != len(frags) {
		t.Fatalf("verdict = %+v, err %v; want a skip naming %d owners", v, err, len(frags))
	}
	if err := saveBookToDatabase(context.Background(), group); !errors.Is(err, errFileOwnedByOtherBook) {
		t.Fatalf("saveBookToDatabase = %v, want errFileOwnedByOtherBook", err)
	}
	got, err := f.store.GetBookByID(frags[0].ID)
	if err != nil || got == nil {
		t.Fatalf("fragment #1 lookup: %v", err)
	}
	if got.Title != "01" {
		t.Fatalf("fragment #1 was overlaid with the group's values: title %q", got.Title)
	}
}

// softDeletedOwnerOf creates a soft-deleted book holding a book_file row at path.
func softDeletedOwnerOf(f ownershipFixture, path string) *database.Book {
	marked := true
	b, err := f.store.CreateBook(&database.Book{Title: "deleted", FilePath: path + ".deleted", MarkedForDeletion: &marked})
	if err != nil {
		panic(err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: path}); err != nil {
		panic(err)
	}
	return b
}

// TestProcessBooksParallel_OwnershipSkipTouchesNothingOfTheOwner is B1 of the
// 2026-09-28 review. saveBookToDatabase used to return nil on an ownership
// skip, so the worker ran its post-save steps; createBookFilesForBook's
// recoverNormalizedBookPath then redirected the scanned book's FilePath to the
// OWNER's directory, and chapter persistence and the scan-cache stamp ran on
// the owner -- clearing its NeedsRescan -- while the AI nomination made before
// the save queued a parse for the owner on every scan.
//
// Two skipped pieces of the owner are scanned: one nominated for AI (no series)
// and one not (full metadata), so both the withheld-stamp and the stamp path
// are exercised.
func TestProcessBooksParallel_OwnershipSkipTouchesNothingOfTheOwner(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })
	f := newOwnershipFixture(t)
	origGlobal := database.GetGlobalStore()
	database.SetGlobalStore(f.store)
	t.Cleanup(func() { database.SetGlobalStore(origGlobal) })
	useScannerStore(t, f.store)

	config.AppConfig.EnableAIParsing = true
	config.AppConfig.AIBackend.LLMMode = config.AIBackendModeLocal
	config.AppConfig.AIBackend.LocalBaseURL = "http://192.0.2.1:1"
	config.AppConfig.AIBackend.LocalLLMModel = "test-model"
	config.AppConfig.MinBookSizeBytes = 0

	// Durations make chapter synthesis productive: had it run on the owner,
	// the owner would now have chapters.
	rows, err := f.store.GetBookFiles(f.parent.ID)
	require.NoError(t, err)
	for _, r := range rows {
		r.Duration = 600
		require.NoError(t, f.store.UpdateBookFile(r.ID, &r))
	}
	require.NoError(t, f.store.MarkNeedsRescan(f.parent.ID))

	var mu sync.Mutex
	var queued []AIParseCandidate
	withEnqueueHook(t, func(_ context.Context, batch []AIParseCandidate) error {
		mu.Lock()
		defer mu.Unlock()
		queued = append(queued, batch...)
		return nil
	})

	books := []Book{
		{FilePath: f.chapters[0], SegmentFiles: f.chapters[:2], Format: ".mp3"},
		{FilePath: f.chapters[1], SegmentFiles: f.chapters[1:], Format: ".mp3",
			Title: "Eldest", Author: "Christopher Paolini", Series: "Inheritance"},
	}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 2, nil, logger.New("test")))

	owner, err := f.store.GetBookByID(f.parent.ID)
	require.NoError(t, err)
	require.NotNil(t, owner.NeedsRescan)
	require.True(t, *owner.NeedsRescan, "the owner's NeedsRescan was cleared by a skipped book's scan-cache stamp")
	require.Nil(t, owner.LastScanMtime, "the owner was stamped by a skipped book")
	chapters, err := f.store.GetChaptersForBook(f.parent.ID)
	require.NoError(t, err)
	require.Empty(t, chapters, "chapters were persisted onto the owner by a skipped book")
	after, err := f.store.GetBookFiles(f.parent.ID)
	require.NoError(t, err)
	require.Len(t, after, len(f.chapters), "the owner's rows changed")
	mu.Lock()
	require.Empty(t, queued, "an AI parse was queued for a skipped book (it resolves to the owner)")
	mu.Unlock()
	all, err := f.store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	require.Len(t, all, 1, "a skipped piece was imported as a book")
}

// TestProcessBooksParallel_StagedArrivalAppendsToTheOwner is B2: a download
// scanned while half-written was imported with the files present then; when the
// rest arrive, the next scan must grow that book rather than skip the new
// files as fragments forever.
func TestProcessBooksParallel_StagedArrivalAppendsToTheOwner(t *testing.T) {
	SetScanner(nil)
	t.Cleanup(func() { SetScanner(nil) })
	f := newOwnershipFixture(t)
	useScannerStore(t, f.store)
	config.AppConfig.EnableAIParsing = false
	config.AppConfig.MinBookSizeBytes = 0

	all := append(slices.Clone(f.chapters), f.loose...)
	books := []Book{{FilePath: f.chapters[0], SegmentFiles: all, Format: ".mp3",
		Title: "Eldest", Author: "Christopher Paolini", Series: "Inheritance"}}
	require.NoError(t, ProcessBooksParallel(t.Context(), books, 1, nil, logger.New("test")))

	rows, err := f.store.GetBookFiles(f.parent.ID)
	require.NoError(t, err)
	require.Len(t, rows, len(all), "the new files were not appended to the owner")
	tracks := map[string]int{}
	for _, r := range rows {
		tracks[filepath.Base(r.FilePath)] = r.TrackNumber
	}
	require.Equal(t, map[string]int{"97.mp3": 1, "98.mp3": 2, "99.mp3": 3, "new-1.mp3": 4, "new-2.mp3": 5}, tracks)
	books2, err := f.store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	require.Len(t, books2, 1, "a new book was minted for the late files")
	owner, err := f.store.GetBookByID(f.parent.ID)
	require.NoError(t, err)
	require.NotNil(t, owner.NeedsRescan)
	require.True(t, *owner.NeedsRescan, "the grown owner was not re-armed for a full re-read")

	// And the next scan of the whole folder is an ordinary rescan of the owner.
	v, err := checkFileOwnership(&Book{FilePath: f.chapters[0], SegmentFiles: all})
	require.NoError(t, err)
	require.False(t, v.skip, "after the append the owner is the whole set: %s", v.reason)
}
