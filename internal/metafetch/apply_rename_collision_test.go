// file: internal/metafetch/apply_rename_collision_test.go
// version: 1.3.1
// guid: 9b61d0e4-2f7a-4c38-a5e9-4d1c8f0b7e26
// last-edited: 2026-09-14

// End-to-end reproduction of the 2026-09-13 batch-apply failure through
// runApplyPipeline: a book whose two files carry the same track number (disc 1
// track 1 and disc 2 track 1) planned both onto "<title> - 01 - 01.mp3"; the
// first file was published, the second failed "link <tmp> <dest>: file already
// exists", and the book was left half renamed with its DB metadata already
// applied. The planner-level and RenameFiles-level cases are pinned in
// internal/organizer/plan_unique_targets_test.go.
package metafetch

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

func TestRunApplyPipeline_RepeatedTrackNumbersRenameToDistinctTargets(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	config.AppConfig.AutoRenameOnApply = true
	config.AppConfig.AutoWriteTagsOnApply = false
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"

	oldDir := filepath.Join(root, "incoming", "Mynoghra 3")
	disc1 := filepath.Join(oldDir, "CD1", "01.mp3")
	disc2 := filepath.Join(oldDir, "CD2", "01.mp3")
	writeFile(t, disc1, "disc one")
	writeFile(t, disc2, "disc two, longer")

	author := &database.Author{ID: 1, Name: "Fehu Kazuno"}
	book := &database.Book{ID: "b1", Title: "Apocalypse Bringer Mynoghra, Volume 3", FilePath: oldDir, Author: author}
	var mu sync.Mutex
	files := []database.BookFile{
		{ID: "f1", BookID: "b1", FilePath: disc1, Format: "mp3", TrackNumber: 1, DiscNumber: 1, FileSize: int64(len("disc one"))},
		{ID: "f2", BookID: "b1", FilePath: disc2, Format: "mp3", TrackNumber: 1, DiscNumber: 2, FileSize: int64(len("disc two, longer"))},
	}
	svc := NewService(&database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) {
			mu.Lock()
			defer mu.Unlock()
			b := *book
			return &b, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			mu.Lock()
			defer mu.Unlock()
			*book = *b
			return b, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]database.BookFile(nil), files...), nil
		},
		UpdateBookFileFunc: func(id string, f *database.BookFile) error {
			mu.Lock()
			defer mu.Unlock()
			for i := range files {
				if files[i].ID == id {
					files[i] = *f
				}
			}
			return nil
		},
	})

	_, err := svc.runApplyPipeline("b1", book, "b1", nil)
	require.NoError(t, err, "a two-disc book must rename cleanly")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, files, 2)
	assert.NotEqual(t, files[0].FilePath, files[1].FilePath, "the two rows must end on distinct paths")
	for _, f := range files {
		assert.NotContains(t, filepath.Base(f.FilePath), " - 01 - 01", "the doubled suffix is the bug's signature")
		_, statErr := os.Stat(f.FilePath)
		assert.NoError(t, statErr, "row %s points at a file that is not there", f.ID)
	}
	got1, _ := os.ReadFile(files[0].FilePath)
	got2, _ := os.ReadFile(files[1].FilePath)
	assert.Equal(t, "disc one", string(got1), "disc 1 keeps its bytes under its row")
	assert.Equal(t, "disc two, longer", string(got2), "disc 2 keeps its bytes under its row")
	assert.Equal(t, "Apocalypse Bringer Mynoghra, Volume 3 - 01.mp3", filepath.Base(files[0].FilePath))
	assert.Equal(t, "Apocalypse Bringer Mynoghra, Volume 3 - 02.mp3", filepath.Base(files[1].FilePath))
}

// RenamePreflight refuses, before any write, a book whose post-apply rename
// plan cannot be computed -- runApplyPipeline would return the same error only
// AFTER ApplyMetadataCandidate had written the database. A malformed format
// spec is the plan error reachable with a mock store; ErrDuplicateRenameTarget
// takes the same branch.
func TestRenamePreflight_RefusesWhenThePlanCannotBeComputed(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	config.AppConfig.AutoRenameOnApply = true
	config.AppConfig.FolderNamingPattern = "{author}/{title}"

	dir := filepath.Join(root, "Someone", "Old")
	a, b := filepath.Join(dir, "1.mp3"), filepath.Join(dir, "2.mp3")
	writeFile(t, a, "one")
	writeFile(t, b, "two")
	book := &database.Book{ID: "b1", Title: "Old", FilePath: dir, Author: &database.Author{ID: 1, Name: "Someone"}}
	svc := NewService(&database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { c := *book; return &c, nil },
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "f1", BookID: "b1", FilePath: a, Format: "mp3", TrackNumber: 1},
				{ID: "f2", BookID: "b1", FilePath: b, Format: "mp3", TrackNumber: 2},
			}, nil
		},
	})
	cand := MetadataCandidate{Title: "New Title", Author: "Someone"}

	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"
	require.NoError(t, svc.RenamePreflight("b1", cand, nil), "a plannable rename must not be refused")

	config.AppConfig.FileNamingPattern = "{title} - {track:x}"
	err := svc.RenamePreflight("b1", cand, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrApplyFileWorkWouldFail)

	// Read-only: nothing moved.
	for _, p := range []string{a, b} {
		_, statErr := os.Stat(p)
		assert.NoError(t, statErr, "preflight moved %s", p)
	}
}

// The single-book apply sends a field subset, and the preflight must plan the
// book the subset leaves, not the whole candidate: a subset without the title
// keeps the old title, so the rename is planned under the old name.
func TestRenamePreflight_PlansTheAppliedFieldSubset(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	config.AppConfig.AutoRenameOnApply = true
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"

	dir := filepath.Join(root, "Someone", "Old")
	a := filepath.Join(dir, "1.mp3")
	writeFile(t, a, "one")
	book := &database.Book{ID: "b1", Title: "Old", FilePath: dir, Author: &database.Author{ID: 1, Name: "Someone"}}
	svc := NewService(&database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { c := *book; return &c, nil },
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{{ID: "f1", BookID: "b1", FilePath: a, Format: "mp3", TrackNumber: 1}}, nil
		},
	})
	cand := MetadataCandidate{Title: "New Title", Author: "Someone", Description: "A description"}

	whole, err := svc.previewMetadataCandidate("b1", cand, nil, false, true)
	require.NoError(t, err)
	subset, err := svc.previewMetadataCandidate("b1", cand, []string{"description"}, false, true)
	require.NoError(t, err)

	fieldsOf := func(pv *ApplyPreview) []string {
		var out []string
		for _, c := range pv.Changes {
			out = append(out, c.Field)
		}
		return out
	}
	assert.Contains(t, fieldsOf(whole), "title", "the whole candidate changes the title")
	assert.NotContains(t, fieldsOf(subset), "title", "a description-only apply must not plan the new title")
	for _, m := range subset.Rename.Moves {
		assert.NotContains(t, m.To, "New Title", "the subset's rename plan must use the title the apply leaves")
	}
	require.NoError(t, svc.RenamePreflight("b1", cand, []string{"description"}))
}

// RenamePreflight must not write, even when it meets a recorded rename failure
// that has gone stale: clearing it is the real rename's job, and the apply this
// check guards may yet be refused.
func TestRenamePreflight_DoesNotClearAStaleFailureRecord(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	config.AppConfig.AutoRenameOnApply = true
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"

	dir := filepath.Join(root, "Someone", "Old")
	a := filepath.Join(dir, "1.mp3")
	writeFile(t, a, "one")
	book := &database.Book{ID: "b1", Title: "Old", FilePath: dir, Author: &database.Author{ID: 1, Name: "Someone"}}
	recKey := organizer.ApplyRenameFailurePrefix + "b1"
	gone := filepath.Join(root, "Someone", "New Title", "gone.mp3")
	prefs := map[string]string{recKey: `{"book_id":"b1","target_path":"` + gone + `","occupant_path":"` + gone + `"}`}
	writes := 0
	svc := NewService(&database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { c := *book; return &c, nil },
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{{ID: "f1", BookID: "b1", FilePath: a, Format: "mp3", TrackNumber: 1}}, nil
		},
		GetUserPreferenceForUserFunc: func(_, key string) (*database.UserPreferenceKV, error) {
			if v, ok := prefs[key]; ok {
				return &database.UserPreferenceKV{Key: key, Value: v}, nil
			}
			return nil, nil
		},
		SetUserPreferenceForUserFunc: func(_, key, value string) error {
			writes++
			prefs[key] = value
			return nil
		},
	})

	require.NoError(t, svc.RenamePreflight("b1", MetadataCandidate{Title: "New Title", Author: "Someone"}, nil))
	assert.Zero(t, writes, "preflight wrote a preference")
	assert.NotEmpty(t, prefs[recKey], "preflight cleared the failure record")
}
