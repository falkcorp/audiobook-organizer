// file: internal/plugins/maintenance/repoint_unrecorded_renames_book_test.go
// version: 1.0.0
// guid: de8d720f-bb4b-4093-9619-009ad0feca1e
// last-edited: 2026-09-14

package maintenance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// bookRowFixture is a store of book rows only, with ModifyBook under one lock.
type bookRowFixture struct {
	mu     sync.Mutex
	prefs  map[string]string
	books  map[string]database.Book
	writes int
	// liveAt answers LiveBookIDsAtPath; nil means "scan books".
	liveAt func(path string) ([]string, error)
}

func (fx *bookRowFixture) record(t *testing.T, rec organizer.RenamePathWriteFailure) string {
	t.Helper()
	rec.RecordedAt = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	key := organizer.RenamePathWriteFailureKey(rec.BookID, rec.BookFileID)
	fx.prefs[key] = string(b)
	return key
}

func (fx *bookRowFixture) plugin() *Plugin {
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
		SetUserPreferenceForUserFunc: func(_, k, v string) error {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.prefs[k] = v
			return nil
		},
		LiveBookIDsAtPathFunc: func(path string) ([]string, error) {
			if fx.liveAt != nil {
				return fx.liveAt(path)
			}
			fx.mu.Lock()
			defer fx.mu.Unlock()
			var ids []string
			for id, b := range fx.books {
				if b.FilePath == path {
					ids = append(ids, id)
				}
			}
			return ids, nil
		},
		ModifyBookFunc: func(id string, fn func(*database.Book) error) (*database.Book, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			b, ok := fx.books[id]
			if !ok {
				return nil, nil
			}
			if err := fn(&b); err != nil {
				if errors.Is(err, database.ErrSkipBookWrite) {
					return &b, nil
				}
				return nil, err
			}
			fx.writes++
			fx.books[id] = b
			return &b, nil
		},
	}
	return &Plugin{deps: &fakeDeps{store: store}}
}

// F2: a row already at new_path clears its record even when a precondition
// read fails (here the live-books lookup) and new_path is not on disk.
func TestRepointUnrecordedRenames_AlreadyAtNewClearsDespiteBlockerError(t *testing.T) {
	clearITunes(t)
	dir := t.TempDir()
	newDir := filepath.Join(dir, "new") // not on disk
	fx := &bookRowFixture{
		prefs:  map[string]string{},
		books:  map[string]database.Book{"b1": {ID: "b1", FilePath: newDir}},
		liveAt: func(string) ([]string, error) { return nil, errors.New("index unavailable") },
	}
	k := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", OldPath: filepath.Join(dir, "old"), NewPath: newDir})
	runRepoint(t, fx.plugin(), true)
	if fx.prefs[k] != "" {
		t.Fatal("record of a row already at new_path was kept")
	}
	if fx.writes != 0 {
		t.Fatalf("wrote %d rows", fx.writes)
	}
}

// F3: a multi-file book's old directory is left behind by the rename. Holding
// only non-audio leftovers it counts as gone; holding an audio file it does
// not. Nothing on disk is deleted either way.
func TestRepointUnrecordedRenames_LeftoverOldDirectory(t *testing.T) {
	clearITunes(t)
	dir := t.TempDir()
	leftover, withAudio := filepath.Join(dir, "old1"), filepath.Join(dir, "old2")
	touch(t, filepath.Join(leftover, "cover.jpg"))
	touch(t, filepath.Join(leftover, "sub", "book.nfo"))
	touch(t, filepath.Join(withAudio, "cover.jpg"))
	touch(t, filepath.Join(withAudio, "CD2", "07.mp3"))
	new1, new2 := filepath.Join(dir, "new1"), filepath.Join(dir, "new2")
	touch(t, filepath.Join(new1, "01.mp3"))
	touch(t, filepath.Join(new2, "01.mp3"))

	fx := &bookRowFixture{
		prefs: map[string]string{},
		books: map[string]database.Book{
			"b1": {ID: "b1", FilePath: leftover},
			"b2": {ID: "b2", FilePath: withAudio},
		},
	}
	k1 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", OldPath: leftover, NewPath: new1})
	k2 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b2", OldPath: withAudio, NewPath: new2})
	runRepoint(t, fx.plugin(), true)

	if fx.books["b1"].FilePath != new1 || fx.prefs[k1] != "" {
		t.Errorf("leftover-only old dir: path=%s record=%q, want repointed and cleared", fx.books["b1"].FilePath, fx.prefs[k1])
	}
	if fx.books["b2"].FilePath != withAudio || fx.prefs[k2] == "" {
		t.Errorf("old dir with audio: path=%s, want kept at old path with record", fx.books["b2"].FilePath)
	}
	for _, p := range []string{filepath.Join(leftover, "cover.jpg"), filepath.Join(withAudio, "CD2", "07.mp3")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
}

// F4: two books aimed at one new directory. Even when the live-books read is a
// stale snapshot taken before either write (so it names neither), only the
// first is repointed; the second is skipped as claimed and keeps its record.
func TestRepointUnrecordedRenames_SameNewPathOnlyOneWins(t *testing.T) {
	clearITunes(t)
	dir := t.TempDir()
	newDir := filepath.Join(dir, "new")
	touch(t, filepath.Join(newDir, "01.mp3"))
	oldA, oldB := filepath.Join(dir, "oldA"), filepath.Join(dir, "oldB") // gone

	for _, stale := range []bool{false, true} {
		fx := &bookRowFixture{
			prefs: map[string]string{},
			books: map[string]database.Book{
				"b1": {ID: "b1", FilePath: oldA},
				"b2": {ID: "b2", FilePath: oldB},
			},
		}
		if stale {
			fx.liveAt = func(string) ([]string, error) { return nil, nil }
		}
		k1 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", OldPath: oldA, NewPath: newDir})
		k2 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b2", OldPath: oldB, NewPath: newDir + "/"})
		runRepoint(t, fx.plugin(), true)

		if fx.writes != 1 {
			t.Fatalf("stale=%t: %d book rows written, want 1", stale, fx.writes)
		}
		if fx.books["b1"].FilePath != newDir || fx.prefs[k1] != "" {
			t.Errorf("stale=%t: first record not repointed: %+v", stale, fx.books["b1"])
		}
		if fx.books["b2"].FilePath != oldB || fx.prefs[k2] == "" {
			t.Errorf("stale=%t: second record must be skipped and kept: %+v", stale, fx.books["b2"])
		}
	}
}
