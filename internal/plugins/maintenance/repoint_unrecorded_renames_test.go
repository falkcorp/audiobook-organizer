// file: internal/plugins/maintenance/repoint_unrecorded_renames_test.go
// version: 1.1.0
// guid: 9f41c2d7-6e08-4a53-b19c-2d7e5a0f8c36
// last-edited: 2026-09-14

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

type repointFixture struct {
	mu        sync.Mutex
	prefs     map[string]string
	files     map[string]database.BookFile // by ID, all book b1
	book      database.Book
	fileWrite int
	bookWrite int
}

func (fx *repointFixture) plugin() *Plugin {
	store := &database.MockStore{
		GetAllPreferencesForUserFunc: func(userID string) ([]database.UserPreferenceKV, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			var out []database.UserPreferenceKV
			for k, v := range fx.prefs {
				out = append(out, database.UserPreferenceKV{UserID: userID, Key: k, Value: v})
			}
			return out, nil
		},
		SetUserPreferenceForUserFunc: func(_, key, value string) error {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.prefs[key] = value
			return nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			var out []database.BookFile
			for _, f := range fx.files {
				out = append(out, f)
			}
			return out, nil
		},
		UpdateBookFileFunc: func(id string, f *database.BookFile) error {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.fileWrite++
			fx.files[id] = *f
			return nil
		},
		GetBookByIDFunc: func(string) (*database.Book, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			b := fx.book
			return &b, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.bookWrite++
			fx.book = *b
			return b, nil
		},
	}
	return &Plugin{deps: &fakeDeps{store: store}}
}

func (fx *repointFixture) record(t *testing.T, rec organizer.RenamePathWriteFailure) string {
	t.Helper()
	if rec.RecordedAt == "" {
		rec.RecordedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	key := organizer.RenamePathWriteFailureKey(rec.BookID, rec.BookFileID)
	fx.prefs[key] = string(b)
	return key
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runRepoint(t *testing.T, p *Plugin, apply bool) {
	t.Helper()
	raw, _ := json.Marshal(repointUnrecordedRenamesParams{Apply: apply})
	runRepointParams(t, p, string(raw))
}

func runRepointParams(t *testing.T, p *Plugin, raw string) {
	t.Helper()
	if err := p.runRepointUnrecordedRenames(context.Background(), json.RawMessage(raw), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRepointUnrecordedRenames(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.ITunes = config.ITunesConfig{}

	dir := t.TempDir()
	oldF1, newF1 := filepath.Join(dir, "old", "01.mp3"), filepath.Join(dir, "new", "01.mp3")
	oldF2, newF2 := filepath.Join(dir, "old", "02.mp3"), filepath.Join(dir, "new", "02.mp3") // new missing
	oldF3, newF3 := filepath.Join(dir, "old", "03.mp3"), filepath.Join(dir, "new", "03.mp3") // row changed
	itOld, itNew := filepath.Join(dir, "books", "itunes", "a.m4b"), filepath.Join(dir, "books", "itunes", "b.m4b")
	oldDir, newDir := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	touch(t, newF1)
	touch(t, newF3)
	touch(t, itNew)

	setup := func(t *testing.T) (*repointFixture, map[string]string) {
		fx := &repointFixture{
			prefs: map[string]string{"pipeline_checkpoint:b9:rename": "keep"},
			files: map[string]database.BookFile{
				"f1": {ID: "f1", BookID: "b1", FilePath: oldF1, Format: "mp3", FileHash: "h1"},
				"f2": {ID: "f2", BookID: "b1", FilePath: oldF2},
				"f3": {ID: "f3", BookID: "b1", FilePath: "/elsewhere/03.mp3"},
				"f4": {ID: "f4", BookID: "b1", FilePath: itOld},
			},
			book: database.Book{ID: "b1", Title: "T", FilePath: oldDir},
		}
		keys := map[string]string{
			"f1":   fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f1", OldPath: oldF1, NewPath: newF1, NewITunesPath: "file:///n/01.mp3"}),
			"f2":   fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f2", OldPath: oldF2, NewPath: newF2}),
			"f3":   fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f3", OldPath: oldF3, NewPath: newF3}),
			"f4":   fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f4", OldPath: itOld, NewPath: itNew}),
			"book": fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", OldPath: oldDir, NewPath: newDir}),
		}
		return fx, keys
	}

	t.Run("dry run changes nothing", func(t *testing.T) {
		fx, keys := setup(t)
		runRepoint(t, fx.plugin(), false)
		if fx.fileWrite != 0 || fx.bookWrite != 0 {
			t.Fatalf("dry run wrote rows: files=%d books=%d", fx.fileWrite, fx.bookWrite)
		}
		for name, k := range keys {
			if fx.prefs[k] == "" {
				t.Errorf("dry run cleared record %s", name)
			}
		}
		if fx.files["f1"].FilePath != oldF1 || fx.book.FilePath != oldDir {
			t.Fatal("dry run moved a path")
		}
	})

	t.Run("apply repoints only eligible rows", func(t *testing.T) {
		fx, keys := setup(t)
		runRepoint(t, fx.plugin(), true)

		f1 := fx.files["f1"]
		if f1.FilePath != newF1 || f1.ITunesPath != "file:///n/01.mp3" || f1.FileHash != "h1" || f1.Format != "mp3" {
			t.Fatalf("f1 not repointed as a full row: %+v", f1)
		}
		if fx.prefs[keys["f1"]] != "" {
			t.Error("f1 record not cleared after repoint")
		}
		if fx.book.FilePath != newDir || fx.prefs[keys["book"]] != "" {
			t.Errorf("book row: path=%s record=%q", fx.book.FilePath, fx.prefs[keys["book"]])
		}
		// new_path missing, row changed, iTunes: untouched, record kept.
		for _, id := range []string{"f2", "f3", "f4"} {
			if fx.prefs[keys[id]] == "" {
				t.Errorf("%s record cleared; it must be kept", id)
			}
		}
		if fx.files["f2"].FilePath != oldF2 || fx.files["f3"].FilePath != "/elsewhere/03.mp3" || fx.files["f4"].FilePath != itOld {
			t.Fatalf("skipped row changed: %+v", fx.files)
		}
		if fx.fileWrite != 1 {
			t.Fatalf("file writes = %d, want 1", fx.fileWrite)
		}
		if fx.prefs["pipeline_checkpoint:b9:rename"] != "keep" {
			t.Fatal("unrelated preference touched")
		}
	})

	t.Run("book row that moved on is kept", func(t *testing.T) {
		fx, keys := setup(t)
		fx.book.FilePath = "/somewhere/else"
		runRepoint(t, fx.plugin(), true)
		if fx.bookWrite != 0 || fx.prefs[keys["book"]] == "" {
			t.Fatalf("changed book row: writes=%d record=%q", fx.bookWrite, fx.prefs[keys["book"]])
		}
	})
}
