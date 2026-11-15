// file: internal/plugins/maintenance/kinds_t6_g6_head_test.go
// version: 1.0.4
// guid: 8c1d4e72-3b95-4a60-b2f8-6e0a9d5c1347
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
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

// The native nightly run takes its cutoff from the configured keep-days dep, the
// local midnight keepDays days back, and hands it to the one compaction write.
func TestT6G6Native_NightlyCutoffFollowsKeepDays(t *testing.T) {
	loc := time.FixedZone("gotham", -5*3600)
	orig := nightlyNow
	nightlyNow = func() time.Time { return time.Date(2026, 9, 14, 0, 10, 0, 0, loc) }
	t.Cleanup(func() { nightlyNow = orig })

	p := newT6G6Probe(t) // ActivityLogFullDetailDays is 30
	defs := (&Plugin{}).t6g6Kinds(p.deps(), nil)
	_, err := runT6G6Kind(t, t6g6ByKind(t, defs, nightlyCompactActivityLogKind), tasks.ModeLive, &t6g6GateWriter{name: "gotham"}, "")
	require.NoError(t, err)
	require.Len(t, p.rec.cutoffs, 1)
	require.True(t, time.Date(2026, 8, 15, 0, 0, 0, 0, loc).Equal(p.rec.cutoffs[0]), "cutoff %s", p.rec.cutoffs[0])
}

// Without resolvable iTunes roots the repoint cannot prove a path is outside
// them, so the native shell refuses before it builds a writer.
func TestT6G6Native_RepointRefusesWithoutITunesRoots(t *testing.T) {
	f := newRBFFixture(t, -1, "-")
	p := newT6G6Probe(t)
	p.store = t6g6RBFStore{s: f.s}
	deps := p.deps()
	deps.ITunesRoots = nil
	defs := (&Plugin{}).t6g6Kinds(deps, nil)
	b, err := json.Marshal(map[string]any{"repoints": []rbfRepoint{f.item()}})
	require.NoError(t, err)
	_, err = runT6G6Kind(t, t6g6ByKind(t, defs, rbfKind), tasks.ModeLive, &t6g6GateWriter{name: "gotham"}, string(b))
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve iTunes roots")
	require.Zero(t, p.writerCalls(), "a refused run built a writer")
	require.Equal(t, f.oldPath, f.storedRow(t).FilePath)
}

// t6g6OpsFetchSpy counts the OpsStore fetches a shell makes.
type t6g6OpsFetchSpy struct {
	fakeDeps
	fetches int
}

func (d *t6g6OpsFetchSpy) OpsStore() OpsStore {
	d.fetches++
	return d.fakeDeps.OpsStore()
}

// The orphan-cleanup HEAD shell refuses the removed delete mode before it
// fetches the store, as HEAD did; the core refuses too, so only the fetch count
// tells the shell's own guard from the core's.
func TestT6G6Head_OrphanCleanupRefusesDeleteBeforeFetchingStore(t *testing.T) {
	spy := &t6g6OpsFetchSpy{fakeDeps: fakeDeps{store: newRepairPebble(t)}}
	p := &Plugin{deps: spy}
	err := p.orphanBookFilesCleanupDef().Run(context.Background(), json.RawMessage(`{"delete":true}`), &fakeReporter{})
	require.ErrorIs(t, err, errOrphanDeleteRemoved)
	require.Zero(t, spy.fetches, "the shell fetched the store before refusing delete")
}

// The native orphan-cleanup kind refuses the removed delete mode through its
// own core (a ReadOnly kind never reaches the shell's guard).
func TestT6G6Native_OrphanCleanupRefusesDelete(t *testing.T) {
	p := newT6G6Probe(t)
	defs := (&Plugin{}).t6g6Kinds(p.deps(), nil)
	_, err := runT6G6Kind(t, t6g6ByKind(t, defs, orphanBookFilesCleanupKind), tasks.ModeLive, &t6g6GateWriter{name: "gotham"}, `{"delete":true}`)
	require.ErrorIs(t, err, errOrphanDeleteRemoved)
}
