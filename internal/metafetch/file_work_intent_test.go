// file: internal/metafetch/file_work_intent_test.go
// version: 1.0.1
// guid: 6c1e4b3a-8d27-4f5e-9a0c-3b7d5e2f1a48
// last-edited: 2026-11-18

// M1. A retired book's files are written by an attended job (the owner's own
// action on that one book) and by no unattended one. The refusal lives in
// fileWorkTarget, the one resolution the rename, the cover embed and the tag
// write all go through, so the three stop together; before it, only the tag
// write refused, after the rename and the embed had already run.
package metafetch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// intentFixture is a book under root_dir with two audio files whose naming
// pattern puts them somewhere else, so a rename that runs is visible on disk.
type intentFixture struct {
	svc       *Service
	root      string
	oldDir    string
	oldFiles  []string
	tags      atomic.Int64 // tag writes, counted through the writeTags seam
	fileReads atomic.Int64 // GetBookFiles calls: a step that reached the files
	locks     *testPathLocks

	mu    sync.Mutex
	book  *database.Book
	files []database.BookFile
}

func newIntentFixture(t *testing.T, retire func(*database.Book)) *intentFixture {
	t.Helper()
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	f := &intentFixture{root: t.TempDir(), locks: &testPathLocks{}}
	config.AppConfig.RootDir = f.root
	config.AppConfig.AutoRenameOnApply = true
	config.AppConfig.AutoWriteTagsOnApply = true
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"

	f.oldDir = filepath.Join(f.root, "incoming", "Year One")
	a, b := filepath.Join(f.oldDir, "01.mp3"), filepath.Join(f.oldDir, "02.mp3")
	writeFile(t, a, "part one")
	writeFile(t, b, "part two, longer")
	f.oldFiles = []string{a, b}
	f.book = &database.Book{ID: "b1", Title: "Year One", FilePath: f.oldDir, Author: &database.Author{ID: 1, Name: "Harmon Example"}}
	if retire != nil {
		retire(f.book)
	}
	f.files = []database.BookFile{
		{ID: "f1", BookID: "b1", FilePath: a, Format: "mp3", TrackNumber: 1, FileSize: int64(len("part one"))},
		{ID: "f2", BookID: "b1", FilePath: b, Format: "mp3", TrackNumber: 2, FileSize: int64(len("part two, longer"))},
	}
	f.svc = NewService(&database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			c := *f.book
			return &c, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			*f.book = *b
			return b, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			f.fileReads.Add(1)
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]database.BookFile(nil), f.files...), nil
		},
		UpdateBookFileFunc: func(id string, bf *database.BookFile) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			for i := range f.files {
				if f.files[i].ID == id {
					f.files[i] = *bf
				}
			}
			return nil
		},
	})
	f.svc.SetPathLocker(f.locks.lock)
	f.svc.tagWriter = func(context.Context, string) (int, error) { f.tags.Add(1); return 1, nil }
	return f
}

// moved reports whether the book's files left their original paths.
func (f *intentFixture) moved(t *testing.T) bool {
	t.Helper()
	gone := 0
	for _, p := range f.oldFiles {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			gone++
		}
	}
	return gone > 0
}

func (f *intentFixture) untouched(t *testing.T) {
	t.Helper()
	assert.False(t, f.moved(t), "no file of a retired book may be renamed by an unattended job")
	assert.Zero(t, f.tags.Load(), "no tag may be written")
	assert.Zero(t, f.fileReads.Load(), "no step may reach the book's files")
}

// The fileWorkTarget decision, for every shape of retired and every intent.
func TestFileWorkTarget_RetiredSelfByIntent(t *testing.T) {
	withRootDir(t, "/lib")
	live := &database.Book{ID: "self", Title: "Year One", FilePath: "/lib/Year One"}
	cases := append([]retiredCopyShape{
		// A merged-into id of only whitespace is retired, as IsRetired has always said.
		{"merged_into_whitespace", func(b *database.Book) { ws := "  "; b.MergedIntoBookID = &ws }},
	}, retiredCopyShapes...)
	for _, shape := range cases {
		t.Run(shape.name, func(t *testing.T) {
			book := *live
			shape.retire(&book)
			svc := NewService(&database.MockStore{GetBookByIDFunc: func(string) (*database.Book, error) { b := book; return &b, nil }})

			got, err := svc.fileWorkTarget(&book, "self", Unattended)
			require.ErrorIs(t, err, ErrBookRetired, "an unattended job writes none of a retired book's files")
			require.Nil(t, got)

			got, err = svc.fileWorkTarget(&book, "self", Interactive)
			require.NoError(t, err, "the owner's own action on this one book writes it")
			require.NotNil(t, got)
			require.Equal(t, "self", got.ID)
		})
	}
	t.Run("live_control", func(t *testing.T) {
		svc := NewService(&database.MockStore{})
		for _, intent := range []FetchIntent{Unattended, Interactive} {
			got, err := svc.fileWorkTarget(live, "self", intent)
			require.NoError(t, err, intent.String())
			require.NotNil(t, got)
		}
	})
	t.Run("no_intent_is_an_error", func(t *testing.T) {
		svc := NewService(&database.MockStore{})
		for _, intent := range []FetchIntent{0, 99} {
			got, err := svc.fileWorkTarget(live, "self", intent)
			require.Error(t, err, "a caller that did not state its intent must get neither behaviour")
			require.NotErrorIs(t, err, ErrBookRetired)
			require.Nil(t, got)
		}
	})
	// The intent is checked before any branch resolves a target: a job that
	// locked a library copy (or none) and forgot its intent fails too, and
	// the store is not even asked for the copy.
	t.Run("no_intent_is_an_error_on_every_branch", func(t *testing.T) {
		svc := NewService(&database.MockStore{GetBookByIDFunc: func(id string) (*database.Book, error) {
			t.Errorf("GetBookByID(%q): an invalid intent must be refused before any target is resolved", id)
			return nil, nil
		}})
		for _, targetID := range []string{"copy-1", ""} {
			for _, intent := range []FetchIntent{0, 99} {
				got, err := svc.fileWorkTarget(live, targetID, intent)
				require.Error(t, err, "target %q intent %d", targetID, intent)
				require.NotErrorIs(t, err, ErrBookRetired)
				require.Nil(t, got)
			}
		}
	})
}

// The whole file sequel, unattended, on a retired book: the rename, the cover
// embed and the tag write all stop, observed on disk and through the seams,
// not in a returned error (the embed reports none by design).
func TestFinishApplyFileWork_UnattendedWritesNothingOfARetiredBook(t *testing.T) {
	for _, shape := range retiredCopyShapes {
		t.Run(shape.name, func(t *testing.T) {
			f := newIntentFixture(t, shape.retire)

			err := f.svc.FinishApplyFileWork(context.Background(), "b1", "", true, true, Unattended, nil)

			require.ErrorIs(t, err, ErrBookRetired)
			f.untouched(t)
		})
	}
}

// The attended twin: the same retired book, the same call, and all three
// writes happen. This is what the single-book dialog and the Fetch button keep.
func TestFinishApplyFileWork_AttendedWritesARetiredBook(t *testing.T) {
	for _, shape := range retiredCopyShapes {
		t.Run(shape.name, func(t *testing.T) {
			f := newIntentFixture(t, shape.retire)

			err := f.svc.FinishApplyFileWork(context.Background(), "b1", "", true, true, Interactive, nil)

			require.NoError(t, err)
			assert.True(t, f.moved(t), "the rename ran")
			assert.Equal(t, int64(1), f.tags.Load(), "the tags were written once")
		})
	}
}

// Each leg on its own, because the three stop in one place only if each asks it.
func TestFileWorkLegs_RetiredBookByIntent(t *testing.T) {
	for _, shape := range retiredCopyShapes {
		t.Run(shape.name, func(t *testing.T) {
			t.Run("embed_cover", func(t *testing.T) {
				f := newIntentFixture(t, shape.retire)
				book, _ := f.svc.db.GetBookByID("b1")

				f.svc.embedCover(context.Background(), "b1", book, "b1", Unattended)
				assert.Empty(t, f.locks.taken(), "an unattended embed must not even lock the files it would write")

				f.svc.embedCover(context.Background(), "b1", book, "b1", Interactive)
				assert.Contains(t, f.locks.taken(), f.oldDir, "an attended embed reaches the files")
			})
			t.Run("rename_only", func(t *testing.T) {
				f := newIntentFixture(t, shape.retire)
				err := f.svc.RunApplyPipelineRenameOnly(context.Background(), "b1", nil, Unattended)
				require.ErrorIs(t, err, ErrBookRetired)
				f.untouched(t)

				f = newIntentFixture(t, shape.retire)
				require.NoError(t, f.svc.RunApplyPipelineRenameOnly(context.Background(), "b1", nil, Interactive))
				assert.True(t, f.moved(t), "an attended rename-only runs")
			})
			t.Run("write_back", func(t *testing.T) {
				f := newIntentFixture(t, shape.retire)
				_, err := f.svc.WriteBackMetadataForBook("b1", Unattended)
				require.ErrorIs(t, err, ErrBookRetired)
				f.untouched(t)

				f = newIntentFixture(t, shape.retire)
				_, err = f.svc.WriteBackMetadataForBook("b1", Interactive)
				require.NotErrorIs(t, err, ErrBookRetired, "an attended write-back reaches the files (a fixture's fake audio fails later, not here)")
				assert.Positive(t, f.fileReads.Load(), "an attended write-back reads the files it tags")
			})
			t.Run("write_back_context", func(t *testing.T) {
				f := newIntentFixture(t, shape.retire)
				_, err := f.svc.WriteBackMetadataForBookContext(context.Background(), "b1", Unattended)
				require.ErrorIs(t, err, ErrBookRetired)
				f.untouched(t)
			})
			t.Run("apply_file_io", func(t *testing.T) {
				f := newIntentFixture(t, shape.retire)
				err := f.svc.ApplyMetadataFileIO(context.Background(), "b1", Unattended)
				require.ErrorIs(t, err, ErrBookRetired)
				f.untouched(t)

				f = newIntentFixture(t, shape.retire)
				require.NoError(t, f.svc.ApplyMetadataFileIO(context.Background(), "b1", Interactive))
				assert.True(t, f.moved(t))
			})
			t.Run("auto_fetch", func(t *testing.T) {
				f := newIntentFixture(t, shape.retire)
				err := f.svc.FinishAutoFetchFileWork(context.Background(), "b1", "", true, Unattended)
				require.ErrorIs(t, err, ErrBookRetired)
				f.untouched(t)

				f = newIntentFixture(t, shape.retire)
				require.NoError(t, f.svc.FinishAutoFetchFileWork(context.Background(), "b1", "", true, Interactive))
				assert.Equal(t, int64(1), f.tags.Load(), "the owner's Fetch button on a retired book still tags it")
			})
		})
	}
}

// A live book is unaffected by the intent: both write.
func TestFinishApplyFileWork_LiveBookIgnoresIntent(t *testing.T) {
	for _, intent := range []FetchIntent{Unattended, Interactive} {
		t.Run(intent.String(), func(t *testing.T) {
			f := newIntentFixture(t, nil)
			require.NoError(t, f.svc.FinishApplyFileWork(context.Background(), "b1", "", true, true, intent, nil))
			assert.True(t, f.moved(t))
			assert.Equal(t, int64(1), f.tags.Load())
		})
	}
}

// The fetch's file-work job carries the fetch's own intent to the scheduler
// (which persists it for a restart's replay) and to the file work. An
// Interactive fetch of a retired book (the Fetch button) therefore tags it; an
// Unattended fetch of one never gets as far as scheduling (checkFetchIntent).
func TestFetchMetadataForBook_FileWorkJobCarriesTheFetchsIntent(t *testing.T) {
	newFetch := func(t *testing.T, retire func(*database.Book)) (*Service, *[]string, *[]FetchIntent, *database.Book) {
		svc, calls, book := fileWorkHarness(t, "/lib", false, false, nil)
		config.AppConfig.WriteBackMetadata = true
		if retire != nil {
			retire(book)
		}
		var scheduled []FetchIntent
		svc.fileWorkScheduler = func(_ string, intent FetchIntent, work func()) {
			scheduled = append(scheduled, intent)
			work()
		}
		svc.overrideSources = []metadata.MetadataSource{fakeSource{
			name:    "Audible",
			results: []metadata.BookMetadata{{Title: "A Book", Author: "Some Author", Genre: "Fantasy"}},
		}}
		return svc, calls, &scheduled, book
	}

	for _, intent := range []FetchIntent{Unattended, Interactive} {
		t.Run("live_"+intent.String(), func(t *testing.T) {
			svc, calls, scheduled, _ := newFetch(t, nil)
			_, err := svc.FetchMetadataForBook(context.Background(), "b1", intent)
			require.NoError(t, err)
			assert.Equal(t, []FetchIntent{intent}, *scheduled, "the scheduler is told the fetch's intent")
			assert.Equal(t, 1, countPrefix(*calls, "tags:"))
		})
	}
	for _, shape := range retiredCopyShapes {
		t.Run("retired_interactive_"+shape.name, func(t *testing.T) {
			svc, calls, scheduled, _ := newFetch(t, shape.retire)
			_, err := svc.FetchMetadataForBook(context.Background(), "b1", Interactive)
			require.NoError(t, err)
			assert.Equal(t, []FetchIntent{Interactive}, *scheduled)
			assert.Equal(t, 1, countPrefix(*calls, "tags:"), "the owner's Fetch button on a retired book still tags it")
		})
		t.Run("retired_unattended_"+shape.name, func(t *testing.T) {
			svc, calls, scheduled, _ := newFetch(t, shape.retire)
			_, err := svc.FetchMetadataForBook(context.Background(), "b1", Unattended)
			require.ErrorIs(t, err, ErrBookRetired)
			assert.Empty(t, *scheduled, "no file work is scheduled for a refused fetch")
			assert.Zero(t, countPrefix(*calls, "tags:"))
		})
	}
}
