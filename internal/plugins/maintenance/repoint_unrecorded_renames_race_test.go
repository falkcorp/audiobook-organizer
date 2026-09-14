// file: internal/plugins/maintenance/repoint_unrecorded_renames_race_test.go
// version: 1.0.0
// guid: e2b7f094-3c61-4a8d-95f2-7d0a4e1c6b38
// last-edited: 2026-09-14

package maintenance

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// A writer that commits a different path between the op's check and its write
// must not be overwritten. The store below applies that concurrent write at the
// first store access for the row: a read-then-write op reads the old row and
// then clobbers the new one; an atomic compare-and-write sees the new row.
func TestRepointUnrecordedRenames_ConcurrentChangeIsNotOverwritten(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.ITunes = config.ITunesConfig{}

	dir := t.TempDir()
	oldP, newP := filepath.Join(dir, "old", "01.mp3"), filepath.Join(dir, "new", "01.mp3")
	const concurrent = "/elsewhere/concurrent/01.mp3"
	touch(t, newP)

	var mu sync.Mutex
	stored := database.BookFile{ID: "f1", BookID: "b1", FilePath: oldP}
	changed := false
	change := func() {
		if !changed {
			stored.FilePath, changed = concurrent, true
		}
	}
	rec, _ := json.Marshal(organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f1", OldPath: oldP, NewPath: newP, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	key := organizer.RenamePathWriteFailureKey("b1", "f1")
	prefs := map[string]string{key: string(rec)}

	store := &database.MockStore{
		GetAllPreferencesForUserFunc: func(userID string) ([]database.UserPreferenceKV, error) {
			mu.Lock()
			defer mu.Unlock()
			var out []database.UserPreferenceKV
			for k, v := range prefs {
				out = append(out, database.UserPreferenceKV{UserID: userID, Key: k, Value: v})
			}
			return out, nil
		},
		SetUserPreferenceForUserFunc: func(_, k, v string) error {
			mu.Lock()
			defer mu.Unlock()
			prefs[k] = v
			return nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			mu.Lock()
			defer mu.Unlock()
			snap := stored
			change()
			return []database.BookFile{snap}, nil
		},
		UpdateBookFileFunc: func(_ string, f *database.BookFile) error {
			mu.Lock()
			defer mu.Unlock()
			stored = *f
			return nil
		},
		ModifyBookFileFunc: func(_, _ string, fn func(*database.BookFile) error) (*database.BookFile, error) {
			mu.Lock()
			defer mu.Unlock()
			change()
			row := stored
			if err := fn(&row); err != nil {
				if errors.Is(err, database.ErrSkipBookFileWrite) {
					return &stored, nil
				}
				return nil, err
			}
			stored = row
			return &row, nil
		},
	}
	runRepoint(t, &Plugin{deps: &fakeDeps{store: store}}, true)

	mu.Lock()
	defer mu.Unlock()
	if stored.FilePath != concurrent {
		t.Fatalf("concurrent path %s was overwritten with %s", concurrent, stored.FilePath)
	}
	if prefs[key] == "" {
		t.Fatal("record cleared although the row was not repointed")
	}
}
