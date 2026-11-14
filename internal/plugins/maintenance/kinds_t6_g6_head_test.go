// file: internal/plugins/maintenance/kinds_t6_g6_head_test.go
// version: 1.0.1
// guid: 8c1d4e72-3b95-4a60-b2f8-6e0a9d5c1347
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The HEAD shells (def.Run, what the registry calls) write through the plugin's
// own store: each test drives the shell over a real in-memory store and reads
// the write back from it (coordinator ruling 2a).

func TestT6G6Head_RelinkApplyWritesBookFile(t *testing.T) {
	s := newRepairPebble(t)
	path := filepath.Join(t.TempDir(), "The Long Halloween.mp3")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	b, err := s.CreateBook(&database.Book{ID: "01BATBOOK00000000000000B1", Title: "The Long Halloween", FilePath: path})
	require.NoError(t, err)
	p := &Plugin{deps: &fakeDeps{store: s}}

	// A detect-only run (no apply) writes nothing.
	require.NoError(t, p.relinkUnlinkedBooksDef().Run(context.Background(), json.RawMessage(`{}`), &fakeReporter{}))
	files, err := s.GetBookFiles(b.ID)
	require.NoError(t, err)
	require.Empty(t, files, "a detect-only run wrote a row")

	require.NoError(t, p.relinkUnlinkedBooksDef().Run(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}))
	files, err = s.GetBookFiles(b.ID)
	require.NoError(t, err)
	require.Len(t, files, 1, "apply links the file")
	require.Equal(t, path, files[0].FilePath)
}

func TestT6G6Head_TranscribeRepairApplyClearsStatus(t *testing.T) {
	s := newRepairPebble(t)
	status := statusWhisperError
	errText := `transcribe remote: transcribe-batch chunk 0-32: Post "http://<host>/transcribe-batch": dial tcp: connect: connection refused`
	b, err := s.CreateBook(&database.Book{ID: "01BATBOOK00000000000000D1", Title: "Arkham Asylum",
		TranscribeStatus: &status, TranscribeError: &errText})
	require.NoError(t, err)
	p := &Plugin{deps: &fakeDeps{store: s}}

	require.NoError(t, p.repairTranscribeStatusDef().Run(context.Background(), json.RawMessage(`{}`), &fakeReporter{}))
	got, err := s.GetBookByID(b.ID)
	require.NoError(t, err)
	require.NotNil(t, got.TranscribeStatus, "a dry run cleared the status")

	require.NoError(t, p.repairTranscribeStatusDef().Run(context.Background(), json.RawMessage(`{"dry_run":false}`), &fakeReporter{}))
	got, err = s.GetBookByID(b.ID)
	require.NoError(t, err)
	require.Nil(t, got.TranscribeStatus, "apply clears the transport-error status")
}

func TestT6G6Head_RecoverApplyRepointsRow(t *testing.T) {
	s := newRepairPebble(t)
	root := t.TempDir()
	gone := filepath.Join(root, "Gotham", "Dark Victory", "gone.mp3")
	target := filepath.Join(root, "Gotham", "Dark Victory", "renamed.mp3")
	writeFile(t, target, 4242)
	_, err := s.CreateBook(&database.Book{ID: "01BATBOOK00000000000000E1", Title: "Dark Victory", FilePath: filepath.Dir(gone)})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "01BATFILE00000000000000E1",
		BookID: "01BATBOOK00000000000000E1", FilePath: gone, FileSize: 4242}))
	p := &Plugin{deps: &fakeDeps{store: s, root: root}}
	report := filepath.Join(t.TempDir(), "recover.tsv")
	params := func(apply bool) json.RawMessage {
		b, mErr := json.Marshal(map[string]any{"apply": apply, "report_path": report})
		require.NoError(t, mErr)
		return b
	}

	require.NoError(t, p.recoverMissingFilesDef().Run(context.Background(), params(false), &fakeReporter{}))
	row, err := s.GetBookFileByID("01BATBOOK00000000000000E1", "01BATFILE00000000000000E1")
	require.NoError(t, err)
	require.Equal(t, gone, row.FilePath, "a dry run repointed the row")

	require.NoError(t, p.recoverMissingFilesDef().Run(context.Background(), params(true), &fakeReporter{}))
	row, err = s.GetBookFileByID("01BATBOOK00000000000000E1", "01BATFILE00000000000000E1")
	require.NoError(t, err)
	require.Equal(t, target, row.FilePath, "apply repoints the row onto the renamed file")
}
