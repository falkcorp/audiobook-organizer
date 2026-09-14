// file: internal/metafetch/rename_pipeline_stale_write_test.go
// version: 1.0.0
// guid: 4a7c2e91-6b3d-4f08-9d15-e8b0c6a3f752
// last-edited: 2026-09-14

package metafetch

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// renameFixture is a two-file book under RootDir/incoming that the rename
// pipelines move to {author}/{title}. The store is a stateful MockStore (its
// ModifyBook composes GetBookByID + UpdateBook, so a fresh read sees every
// committed write). onFileWrite runs inside each UpdateBookFile -- i.e. after
// the disk rename, before the book path write -- and may fail it.
type renameFixture struct {
	mu          sync.Mutex
	stored      database.Book
	files       []database.BookFile
	onFileWrite func(n int) error
	fileWrites  int
}

func newRenameFixture(t *testing.T) (*renameFixture, *Service) {
	t.Helper()
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	config.AppConfig.AutoRenameOnApply = true
	config.AppConfig.AutoWriteTagsOnApply = false
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"

	oldDir := filepath.Join(root, "incoming", "Some Book")
	p1, p2 := filepath.Join(oldDir, "01.mp3"), filepath.Join(oldDir, "02.mp3")
	writeFile(t, p1, "one")
	writeFile(t, p2, "two")

	fx := &renameFixture{
		stored: database.Book{
			ID: "b1", Title: "Some Book", FilePath: oldDir,
			Author: &database.Author{ID: 1, Name: "Some Author"}, Description: new("old"),
		},
		files: []database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: p1, Format: "mp3", TrackNumber: 1, FileSize: 3},
			{ID: "f2", BookID: "b1", FilePath: p2, Format: "mp3", TrackNumber: 2, FileSize: 3},
		},
	}
	svc := NewService(&database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			b := fx.stored
			return &b, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.stored = *b
			return b, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			return append([]database.BookFile(nil), fx.files...), nil
		},
		UpdateBookFileFunc: func(id string, f *database.BookFile) error {
			fx.mu.Lock()
			fx.fileWrites++
			n, hook := fx.fileWrites, fx.onFileWrite
			fx.mu.Unlock()
			if hook != nil {
				if err := hook(n); err != nil {
					return err
				}
			}
			fx.mu.Lock()
			defer fx.mu.Unlock()
			for i := range fx.files {
				if fx.files[i].ID == id {
					fx.files[i] = *f
				}
			}
			return nil
		},
	})
	return fx, svc
}

// concurrentEdit simulates an apply or edit committing while the rename runs.
func (fx *renameFixture) concurrentEdit(n int) error {
	if n == 1 {
		fx.mu.Lock()
		fx.stored.Description = new("new")
		fx.mu.Unlock()
	}
	return nil
}

func (fx *renameFixture) assertEditSurvivedAndPathMoved(t *testing.T, oldDir string) {
	t.Helper()
	fx.mu.Lock()
	defer fx.mu.Unlock()
	require.NotEqual(t, oldDir, fx.stored.FilePath, "book path not moved")
	require.NotNil(t, fx.stored.Description)
	require.Equal(t, "new", *fx.stored.Description, "edit committed during the rename was reverted")
}

var errInjectedFileWrite = errors.New("injected book_file write failure")

func TestRunApplyPipeline_RenameKeepsEditCommittedDuringRename(t *testing.T) {
	fx, svc := newRenameFixture(t)
	fx.onFileWrite = fx.concurrentEdit
	stale := fx.stored
	oldDir := stale.FilePath
	_, err := svc.runApplyPipeline("b1", &stale, "b1", nil)
	require.NoError(t, err)
	fx.assertEditSurvivedAndPathMoved(t, oldDir)
}

func TestRunApplyPipeline_ReturnsBookFilePathWriteFailure(t *testing.T) {
	fx, svc := newRenameFixture(t)
	fx.onFileWrite = func(int) error { return errInjectedFileWrite }
	stale := fx.stored
	_, err := svc.runApplyPipeline("b1", &stale, "b1", nil)
	require.ErrorIs(t, err, errInjectedFileWrite, "a moved file whose DB path was not written must fail the rename")
}

func TestRunApplyPipelineRenameOnly_KeepsEditCommittedDuringRename(t *testing.T) {
	fx, svc := newRenameFixture(t)
	fx.onFileWrite = fx.concurrentEdit
	stale := fx.stored
	oldDir := stale.FilePath
	require.NoError(t, svc.RunApplyPipelineRenameOnly("b1", &stale))
	fx.assertEditSurvivedAndPathMoved(t, oldDir)
}

func TestRunApplyPipelineRenameOnly_ReturnsBookFilePathWriteFailure(t *testing.T) {
	fx, svc := newRenameFixture(t)
	fx.onFileWrite = func(int) error { return errInjectedFileWrite }
	stale := fx.stored
	err := svc.RunApplyPipelineRenameOnly("b1", &stale)
	require.ErrorIs(t, err, errInjectedFileWrite, "a moved file whose DB path was not written must fail the rename")
}
