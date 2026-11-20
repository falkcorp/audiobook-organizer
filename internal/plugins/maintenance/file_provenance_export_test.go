// file: internal/plugins/maintenance/file_provenance_export_test.go
// version: 1.2.1
// guid: d61df6f5-c2e0-47ab-b028-44d97b64001c
// last-edited: 2026-11-19
//
// The export is what turns "we can detect that history was destroyed" into
// "history was not destroyed". The chain proves a row was rewritten; only a
// copy outside the database survives the database.
//
// Two properties carry the weight and both are pinned below: the file is never
// rewritten (re-running appends only what is new), and the cursor advances
// only AFTER the bytes are durable — so a failure duplicates lines rather than
// losing them.

package maintenance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// exportPlugin is a plugin over prov whose library root is a fresh temp dir with
// library content in it (the export refuses an empty root), and the ledger path
// the export is allowed to write there: a file directly in {root}/.ledger, the
// only place the op will write.
func exportPlugin(t *testing.T, prov database.FileProvenanceStore) (*Plugin, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "Gotham"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".ledger"), 0o755))
	return New(provDeps{fakeDeps: fakeDeps{root: root}, prov: prov}), filepath.Join(root, ".ledger", "ledger.jsonl")
}

func newExportFixture(t *testing.T) (*Plugin, *database.PebbleStore, string) {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	p, out := exportPlugin(t, store)
	return p, store, out
}

// stubProvStore serves canned sequence rows so a test can present deletion
// evidence — a vanished event row, a deleted sequence slot — without
// corrupting a real store from another package. The embedded nil interface
// makes any method this file does not implement panic loudly rather than
// return a plausible zero value.
type stubProvStore struct {
	database.FileProvenanceStore
	rows    []database.FileEventSeqRow
	history map[string][]database.FileEvent
	maxSeq  uint64
	cursor  uint64
}

func (s *stubProvStore) MaxFileEventSeq() (uint64, error) { return s.maxSeq, nil }

func (s *stubProvStore) ScanFileEventsBySeq(afterSeq uint64, limit int) ([]database.FileEventSeqRow, error) {
	var out []database.FileEventSeqRow
	for _, r := range s.rows {
		if r.Seq <= afterSeq {
			continue
		}
		out = append(out, r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *stubProvStore) GetFileHistory(bookFileID string) ([]database.FileEvent, error) {
	return s.history[bookFileID], nil
}

func (s *stubProvStore) GetFileProvExportCursor() (uint64, error) { return s.cursor, nil }

func (s *stubProvStore) SetFileProvExportCursor(seq uint64) error {
	if seq > s.cursor {
		s.cursor = seq
	}
	return nil
}

func seedEvents(t *testing.T, s *database.PebbleStore, n int) {
	t.Helper()
	for i := range n {
		require.NoError(t, s.AppendFileEvent(database.FileEvent{
			BookFileID: "bf1",
			Path:       "/lib/a.m4b",
			Kind:       database.FileEventObserved,
			Digest:     database.FileDigest{SHA256Full: string(rune('a' + i))},
		}))
	}
}

func runExport(t *testing.T, p *Plugin, params map[string]any) fileProvExportResult {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	res, err := exportFileProvenanceHEAD(p, context.Background(), raw)
	require.NoError(t, err)
	return res
}

func readLines(t *testing.T, path string) []fileProvExportLine {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var out []fileProvExportLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var line fileProvExportLine
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line))
		out = append(out, line)
	}
	require.NoError(t, sc.Err())
	return out
}

func TestFileProvenanceExportRequiresAPath(t *testing.T) {
	p, _, _ := newExportFixture(t)
	_, err := exportFileProvenanceHEAD(p, context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path")
}

// The ledger is confined to its own directory, {root_dir}/.ledger: a bare name is
// a file there; a name with a directory in it, traversal, a path anywhere else
// (the report directory included) and a symlink that leads out are refused, and
// nothing is created where they pointed. Refusal comes before the ledger is read,
// so a rejected apply moves no cursor.
func TestFileProvenanceExportConfinesThePathToTheReportDir(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)
	root := filepath.Dir(filepath.Dir(out))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, ".ledger", "linked")))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".reports"), 0o755))

	for name, bad := range map[string]string{
		"outside the root":           filepath.Join(outside, "ledger.jsonl"),
		"in the root, not .ledger":   filepath.Join(root, "ledger.jsonl"),
		"in the report directory":    filepath.Join(root, ".reports", "ledger.jsonl"),
		"traversal":                  filepath.Join("..", "ledger.jsonl"),
		"subdirectory":               filepath.Join("sub", "ledger.jsonl"),
		"through a symlinked parent": filepath.Join("linked", "ledger.jsonl"),
		"the ledger directory":       filepath.Join(root, ".ledger"),
	} {
		raw, mErr := json.Marshal(map[string]any{"path": bad, "apply": true})
		require.NoError(t, mErr)
		_, err := exportFileProvenanceHEAD(p, context.Background(), raw)
		require.Error(t, err, name)
	}
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Len(t, entries, 0, "a refused path created something where it pointed")
	cur, cErr := store.GetFileProvExportCursor()
	require.NoError(t, cErr)
	assert.Zero(t, cur, "a refused path moved the cursor")

	// The bare name lands in the ledger directory.
	res := runExport(t, p, map[string]any{"path": "ledger.jsonl", "apply": true})
	assert.Equal(t, 2, res.Written)
	_, statErr := os.Stat(out)
	require.NoError(t, statErr)
}

// The ledger is appended to, so a link planted at its name is the case that
// matters: the open refuses a symlink or a hard link rather than appending
// through it, and the cursor does not move past lines that were not written.
func TestFileProvenanceExportRefusesALinkAtTheLedgerName(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			p, store, out := newExportFixture(t)
			seedEvents(t, store, 2)
			victim := filepath.Join(t.TempDir(), "victim")
			require.NoError(t, os.WriteFile(victim, []byte("keep me"), 0o644))
			if kind == "symlink" {
				require.NoError(t, os.Symlink(victim, out))
			} else if err := os.Link(victim, out); err != nil {
				t.Skipf("hard links are not available here: %v", err)
			}

			raw, mErr := json.Marshal(map[string]any{"path": out, "apply": true})
			require.NoError(t, mErr)
			_, err := exportFileProvenanceHEAD(p, context.Background(), raw)
			require.Error(t, err)

			b, rErr := os.ReadFile(victim)
			require.NoError(t, rErr)
			assert.Equal(t, "keep me", string(b), "the export appended through a planted link")
			cur, cErr := store.GetFileProvExportCursor()
			require.NoError(t, cErr)
			assert.Zero(t, cur)
		})
	}
}

// The open itself refuses a link, independent of the resolver's earlier look at
// the name: a link planted AFTER the path was approved (the window the resolver
// cannot close) must not be appended through. This calls the default open
// directly, which is the call a late plant would reach, and checks that an
// ordinary name still appends, so a refusal is not just a broken open.
func TestFileProvenanceExportOpenRefusesALinkPlantedAfterTheResolve(t *testing.T) {
	reports := filepath.Join(t.TempDir(), ".reports")
	require.NoError(t, os.Mkdir(reports, 0o755))
	victim := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(victim, []byte("keep me"), 0o644))

	ok := filepath.Join(reports, "ledger.jsonl")
	for i := 0; i < 2; i++ {
		w, err := openExportFile(ok)
		require.NoError(t, err)
		_, err = w.Write([]byte("line\n"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
	}
	b, err := os.ReadFile(ok)
	require.NoError(t, err)
	assert.Equal(t, "line\nline\n", string(b), "the ledger is appended to, not replaced")

	sym := filepath.Join(reports, "sym.jsonl")
	require.NoError(t, os.Symlink(victim, sym))
	_, err = openExportFile(sym)
	require.Error(t, err, "a symlink at the ledger name must be refused")

	hard := filepath.Join(reports, "hard.jsonl")
	if lerr := os.Link(victim, hard); lerr == nil {
		_, err = openExportFile(hard)
		require.Error(t, err, "a hard link at the ledger name must be refused")
	}
	v, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "keep me", string(v))
}

// A report op that names the ledger file cannot touch it: the report writers
// resolve into {root}/.reports, the ledger lives in {root}/.ledger, and no spelling
// of a report path reaches across. Every report op that truncates is pointed at the
// ledger's name, by bare name and by absolute path to the ledger, and the ledger's
// bytes are the same afterwards.
func TestFileProvenanceExportLedgerIsOutOfReachOfReportOps(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)
	res := runExport(t, p, map[string]any{"path": "ledger.jsonl", "apply": true})
	require.Equal(t, 2, res.Written)
	before, err := os.ReadFile(out)
	require.NoError(t, err)
	require.NotEmpty(t, before)

	root := filepath.Dir(filepath.Dir(out))
	reports := filepath.Join(root, ".reports")
	opStore, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = opStore.Close() })
	opStore.WaitForWarmup()
	rp := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: opStore}, root: root}}
	for _, c := range reportOpCases() {
		// By bare name the op writes {root}/.reports/ledger.jsonl, a different file.
		_ = c.run(rp, context.Background(), c.raw(t, false, map[string]any{c.key(): "ledger.jsonl"}), &fakeReporter{})
		// By the ledger's own path it is refused outright, and refused for that
		// reason: an op that failed earlier for some unrelated cause would leave
		// the ledger untouched too and pass this leg without proving anything.
		err := c.run(rp, context.Background(), c.raw(t, true, map[string]any{c.key(): out}), &fakeReporter{})
		require.Error(t, err, "%s: the ledger's path must be refused as a report path", c.name)
		require.ErrorContains(t, err, "report path", "%s: refused as something other than a report path: %v", c.name, err)
		require.ErrorContains(t, err, "is not directly inside that directory", "%s: refused for another reason: %v", c.name, err)
		require.ErrorContains(t, err, reports, "%s: the refusal does not name the report directory: %v", c.name, err)

		after, rerr := os.ReadFile(out)
		require.NoError(t, rerr)
		require.Equal(t, string(before), string(after), "%s changed the ledger", c.name)
	}
	// At least one op wrote a report under the ledger's name, in the report directory.
	_, statErr := os.Stat(filepath.Join(reports, "ledger.jsonl"))
	require.NoError(t, statErr, "the bare name should have landed in .reports")
	same := os.SameFile(mustStat(t, out), mustStat(t, filepath.Join(reports, "ledger.jsonl")))
	require.False(t, same, "the report and the ledger are the same file")
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi
}

// The ledger is written to the library volume, so the volume must be there. An
// unmounted share leaves an empty (or dot-only) mountpoint; the export refuses it,
// on a preview too, and creates nothing in it.
func TestFileProvenanceExportRefusesAnUnmountedLibraryRoot(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	seedEvents(t, store, 2)
	root := t.TempDir() // empty: the share is not mounted
	p := New(provDeps{fakeDeps: fakeDeps{root: root}, prov: store})

	for _, apply := range []bool{true, false} {
		raw, mErr := json.Marshal(map[string]any{"path": "ledger.jsonl", "apply": apply})
		require.NoError(t, mErr)
		_, err = exportFileProvenanceHEAD(p, context.Background(), raw)
		require.Error(t, err, "apply=%v", apply)
		assert.Contains(t, err.Error(), "not mounted", "apply=%v", apply)
	}
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "the export created something on an unmounted root")
	cur, cErr := store.GetFileProvExportCursor()
	require.NoError(t, cErr)
	assert.Zero(t, cur)

	// A mountpoint that holds only dot-prefixed state (a .reports from an earlier
	// run) is still not mounted.
	require.NoError(t, os.Mkdir(filepath.Join(root, ".reports"), 0o755))
	raw, mErr := json.Marshal(map[string]any{"path": "ledger.jsonl", "apply": true})
	require.NoError(t, mErr)
	_, err = exportFileProvenanceHEAD(p, context.Background(), raw)
	require.Error(t, err)
}

// With no root_dir there is no ledger directory to keep the ledger inside.
func TestFileProvenanceExportNeedsARootDir(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	p := New(provDeps{fakeDeps: fakeDeps{root: "relative/library"}, prov: store})
	raw, mErr := json.Marshal(map[string]any{"path": filepath.Join(t.TempDir(), "ledger.jsonl"), "apply": true})
	require.NoError(t, mErr)
	_, err = exportFileProvenanceHEAD(p, context.Background(), raw)
	require.Error(t, err)
}

func TestFileProvenanceExportDryRunWritesNothingAndLeavesTheCursor(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 3)

	res := runExport(t, p, map[string]any{"path": out})

	assert.Equal(t, 3, res.Scanned)
	assert.Zero(t, res.Written)
	assert.EqualValues(t, 0, res.CursorAfter, "a dry run moved the cursor")
	_, err := os.Stat(out)
	assert.True(t, os.IsNotExist(err), "a dry run created the output file")
}

func TestFileProvenanceExportAppendsEveryEventAndAdvancesTheCursor(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 3)

	res := runExport(t, p, map[string]any{"path": out, "apply": true})

	assert.Equal(t, 3, res.Written)
	assert.EqualValues(t, 3, res.CursorAfter)
	assert.EqualValues(t, 3, res.MaxSeq)
	assert.Positive(t, res.BytesWritten)

	lines := readLines(t, out)
	require.Len(t, lines, 3)
	assert.EqualValues(t, []uint64{1, 2, 3},
		[]uint64{lines[0].Seq, lines[1].Seq, lines[2].Seq})
	require.NotNil(t, lines[0].Event)
	assert.Equal(t, database.FileEventObserved, lines[0].Event.Kind)
	assert.NotEmpty(t, lines[0].Event.Hash, "the exported copy must carry the chain hash")
}

// The defining property: append-only. Re-running must add only what is new and
// must never touch a byte it already wrote.
func TestFileProvenanceExportIsIncrementalAndNeverRewrites(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)
	first := runExport(t, p, map[string]any{"path": out, "apply": true})
	require.Equal(t, 2, first.Written)

	before, err := os.ReadFile(out)
	require.NoError(t, err)

	seedEvents(t, store, 2) // two more arrive
	second := runExport(t, p, map[string]any{"path": out, "apply": true})

	assert.Equal(t, 2, second.Written, "the second run re-exported events it had already written")
	assert.EqualValues(t, 2, second.CursorBefore)
	assert.EqualValues(t, 4, second.CursorAfter)

	after, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(after), string(before)),
		"the export rewrote bytes it had already committed")
	assert.Len(t, readLines(t, out), 4)
}

func TestFileProvenanceExportWithNothingNewWritesNothing(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)
	runExport(t, p, map[string]any{"path": out, "apply": true})

	again := runExport(t, p, map[string]any{"path": out, "apply": true})
	assert.Zero(t, again.Written)
	assert.Zero(t, again.Scanned)
	assert.EqualValues(t, 2, again.CursorAfter)
	assert.Len(t, readLines(t, out), 2)
}

// The cap must be visible. cursor_after < max_seq is how an operator knows to
// run it again; a silent stop reads as "everything is exported".
func TestFileProvenanceExportReportsWhatTheCapLeftBehind(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 5)

	res := runExport(t, p, map[string]any{"path": out, "apply": true, "max": 2})

	assert.Equal(t, 2, res.Written)
	assert.EqualValues(t, 2, res.CursorAfter)
	assert.EqualValues(t, 5, res.MaxSeq)
	assert.Less(t, res.CursorAfter, res.MaxSeq, "the remainder was not visible in the report")

	// And the next run continues rather than restarting.
	next := runExport(t, p, map[string]any{"path": out, "apply": true, "max": 2})
	assert.EqualValues(t, 2, next.CursorBefore)
	assert.EqualValues(t, 4, next.CursorAfter)
}

// A deleted event leaves its sequence slot behind. That slot is the evidence,
// so it is exported as a marker rather than skipped.
func TestFileProvenanceExportRecordsAMissingRowAsEvidence(t *testing.T) {
	stub := &stubProvStore{maxSeq: 3, rows: []database.FileEventSeqRow{
		{Seq: 1, Event: database.FileEvent{Kind: database.FileEventObserved, Path: "/a"}},
		{Seq: 2}, // the row this slot pointed at is gone
		{Seq: 3, Event: database.FileEvent{Kind: database.FileEventObserved, Path: "/a"}},
	}}
	p, out := exportPlugin(t, stub)

	res := runExport(t, p, map[string]any{"path": out, "apply": true})

	assert.Equal(t, 1, res.Missing, "the vanished row was not reported")
	lines := readLines(t, out)
	require.Len(t, lines, 3, "the missing row's slot was dropped from the export")
	assert.True(t, lines[1].Missing)
	assert.Nil(t, lines[1].Event)
}

// A gap means the sequence entry ITSELF was deleted — the case the per-chain
// hash link cannot see, because the evidence goes with the row.
func TestFileProvenanceExportReportsSequenceGaps(t *testing.T) {
	stub := &stubProvStore{maxSeq: 4, rows: []database.FileEventSeqRow{
		{Seq: 1, Event: database.FileEvent{Kind: database.FileEventObserved, Path: "/a"}},
		// seq 2 and 3 have no index entry at all
		{Seq: 4, Event: database.FileEvent{Kind: database.FileEventObserved, Path: "/a"}},
	}}
	p, out := exportPlugin(t, stub)

	res := runExport(t, p, map[string]any{"path": out, "apply": true})

	require.NotEmpty(t, res.Gaps, "deleted sequence slots went unreported")
	assert.Equal(t, []string{"2-3"}, res.Gaps)
	assert.Equal(t, 2, res.Written)
}

func TestFileProvenanceExportFailsWithoutAProvenanceStore(t *testing.T) {
	p, _ := exportPlugin(t, nil)
	_, err := exportFileProvenanceHEAD(p, context.Background(),
		json.RawMessage(`{"path":"x.jsonl"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

// If the bytes cannot be made durable the cursor must NOT move. Duplicated
// lines on a retry are recoverable; a cursor past events that were never
// written loses them silently and forever.
//
// The three ways this can fail are tested separately because they fail at
// different points and an early return can mask a later bug: the file never
// opens, the write errors, or the sync errors. Only the last two reach the
// cursor-advance code at all — a test that only covers the open path proves
// nothing about the ordering.
func TestFileProvenanceExportLeavesTheCursorWhenTheFileCannotBeOpened(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)

	// A directory at the ledger's name cannot be opened for appending.
	require.NoError(t, os.Mkdir(out, 0o755))
	raw, mErr := json.Marshal(map[string]any{"path": out, "apply": true})
	require.NoError(t, mErr)
	_, err := exportFileProvenanceHEAD(p, context.Background(), raw)
	require.Error(t, err)

	cur, cErr := store.GetFileProvExportCursor()
	require.NoError(t, cErr)
	assert.Zero(t, cur, "the cursor advanced past events that were never written")
}

// failingWriter opens fine and then refuses, which is the case a real
// filesystem will not produce on demand and the one that actually exercises
// the write-then-advance ordering.
type failingWriter struct {
	failWrite bool
	failSync  bool
	written   int
}

func (f *failingWriter) Write(b []byte) (int, error) {
	if f.failWrite {
		return 0, errors.New("disk went away mid-write")
	}
	f.written += len(b)
	return len(b), nil
}
func (f *failingWriter) Sync() error {
	if f.failSync {
		return errors.New("fsync failed")
	}
	return nil
}
func (f *failingWriter) Close() error { return nil }

// The rest is a ledger that is empty and stays where it is: the failing writer is
// used with a cursor of 0, where nothing is read back.
func (f *failingWriter) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }
func (f *failingWriter) Stat() (fs.FileInfo, error)        { return nil, errors.New("not a file") }

func withOpenExportFile(t *testing.T, w exportLedger) {
	t.Helper()
	prev := openExportFile
	openExportFile = func(string) (exportLedger, error) { return w, nil }
	t.Cleanup(func() { openExportFile = prev })
}

func TestFileProvenanceExportLeavesTheCursorWhenTheWriteFails(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)
	withOpenExportFile(t, &failingWriter{failWrite: true})

	raw, mErr := json.Marshal(map[string]any{"path": out, "apply": true})
	require.NoError(t, mErr)
	_, err := exportFileProvenanceHEAD(p, context.Background(), raw)
	require.Error(t, err)

	cur, cErr := store.GetFileProvExportCursor()
	require.NoError(t, cErr)
	assert.Zero(t, cur, "the cursor advanced although the write failed")
}

// The subtle one. Every byte was accepted, so the export "succeeded" as far as
// the write calls know — but nothing was flushed. Advancing here loses events
// on the next power cut with no record that anything went missing.
func TestFileProvenanceExportLeavesTheCursorWhenTheSyncFails(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)
	w := &failingWriter{failSync: true}
	withOpenExportFile(t, w)

	raw, mErr := json.Marshal(map[string]any{"path": out, "apply": true})
	require.NoError(t, mErr)
	_, err := exportFileProvenanceHEAD(p, context.Background(), raw)
	require.Error(t, err)
	assert.Positive(t, w.written, "the test did not reach the sync step")

	cur, cErr := store.GetFileProvExportCursor()
	require.NoError(t, cErr)
	assert.Zero(t, cur, "the cursor advanced although the bytes were never durable")
}

// The export is the sweep that already walks the ledger, so it is where
// verification runs. Without this the chain would be a property nothing ever
// checked — built, correct, and never once executed.
func TestFileProvenanceExportVerifiesTheChainsItTouches(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 3)

	res := runExport(t, p, map[string]any{"path": out, "apply": true})

	assert.Equal(t, 1, res.ChainsVerified, "the chain behind the exported events was never checked")
	assert.Zero(t, res.ChainsBroken)
	assert.Empty(t, res.BrokenChains)
}

// Verification is read-only, so the DEFAULT invocation — no apply — is a
// ledger health check that writes nothing.
func TestFileProvenanceExportVerifiesOnADryRunToo(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)

	res := runExport(t, p, map[string]any{"path": out})

	assert.Equal(t, 1, res.ChainsVerified)
	assert.Zero(t, res.Written)
	_, err := os.Stat(out)
	assert.True(t, os.IsNotExist(err))
}

func TestFileProvenanceExportReportsABrokenChain(t *testing.T) {
	// A chain whose second event claims a hash that does not match its
	// content — what an in-place edit leaves behind.
	good := database.FileEvent{BookFileID: "bf1", Path: "/a", Kind: database.FileEventObserved, Seq: 1}
	good.Hash = good.ComputeHash()
	tampered := database.FileEvent{BookFileID: "bf1", Path: "/edited", Kind: database.FileEventObserved, Seq: 2, PrevHash: good.Hash}
	tampered.Hash = "0000000000000000000000000000000000000000000000000000000000000000"

	stub := &stubProvStore{
		maxSeq:  2,
		rows:    []database.FileEventSeqRow{{Seq: 1, Event: good}, {Seq: 2, Event: tampered}},
		history: map[string][]database.FileEvent{"bf1": {good, tampered}},
	}
	p, out := exportPlugin(t, stub)

	res := runExport(t, p, map[string]any{"path": out, "apply": true})

	assert.Equal(t, 1, res.ChainsBroken)
	assert.Equal(t, []string{"bf1"}, res.BrokenChains)
}

// A chain written before chaining existed is legitimate. Counting it as broken
// would make the first run after deploy report the whole library as tampered,
// and the report would be ignored from then on.
func TestFileProvenanceExportDoesNotCallPreChainEventsBroken(t *testing.T) {
	legacy := database.FileEvent{BookFileID: "bf1", Path: "/a", Kind: database.FileEventObserved, Seq: 1}
	stub := &stubProvStore{
		maxSeq:  1,
		rows:    []database.FileEventSeqRow{{Seq: 1, Event: legacy}},
		history: map[string][]database.FileEvent{"bf1": {legacy}},
	}
	p, out := exportPlugin(t, stub)

	res := runExport(t, p, map[string]any{"path": out, "apply": true})

	assert.Equal(t, 1, res.ChainsVerified)
	assert.Zero(t, res.ChainsBroken, "pre-chain events were reported as tampering")
}

// exportFileProvenanceHEAD runs HEAD's export body the way HEAD's shell does (the
// shared params decode, the head env over the deps' provenance store, the report
// lines) and returns the tally the shell discards, so a test asserts the numbers
// as values rather than scraping a log line.
func exportFileProvenanceHEAD(p *Plugin, ctx context.Context, rawParams json.RawMessage) (fileProvExportResult, error) {
	params, err := decodeFileProvExportParams(rawParams)
	if err != nil {
		return fileProvExportResult{}, err
	}
	return p.runFileProvenanceExportCore(ctx, params, p.fileProvenanceExportHeadEnv(&fakeReporter{}, params.Apply))
}

// ledgerRoot is the library root and ledger directory of an export fixture.
func ledgerRoot(out string) (root, ledger string) {
	ledger = filepath.Dir(out)
	return filepath.Dir(ledger), ledger
}

func exportErr(p *Plugin, params map[string]any) (fileProvExportResult, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return fileProvExportResult{}, err
	}
	return exportFileProvenanceHEAD(p, context.Background(), raw)
}

// Whoever can write the library root can rename the ledger directory aside; the
// next run would make a fresh one and append from the cursor, leaving the earlier
// events in no file at the ledger's path. That is refused, nothing is appended and
// the cursor stays, and the file that was set aside is untouched.
func TestFileProvenanceExportRefusesALedgerThatWasRenamedAside(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)
	runExport(t, p, map[string]any{"path": "ledger.jsonl", "apply": true})
	root, ledger := ledgerRoot(out)
	aside := filepath.Join(root, ".ledger.aside")
	require.NoError(t, os.Rename(ledger, aside))
	before, err := os.ReadFile(filepath.Join(aside, "ledger.jsonl"))
	require.NoError(t, err)
	seedEvents(t, store, 2)

	_, err = exportErr(p, map[string]any{"path": "ledger.jsonl", "apply": true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start_new")
	assert.Contains(t, err.Error(), "seq 2")

	cur, cErr := store.GetFileProvExportCursor()
	require.NoError(t, cErr)
	assert.EqualValues(t, 2, cur, "the cursor moved although nothing was appended")
	fresh, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Empty(t, fresh, "the export appended to a ledger that did not continue from the cursor")
	after, err := os.ReadFile(filepath.Join(aside, "ledger.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// The other ways the file at the ledger's name stops continuing from the cursor:
// it is deleted (the open then makes an empty one), cut back to an earlier line, or
// replaced with an older copy. Each is refused; a file whose lines run to the
// cursor is accepted.
func TestFileProvenanceExportRefusesALedgerThatDoesNotReachTheCursor(t *testing.T) {
	for name, damage := range map[string]func(t *testing.T, out string){
		"deleted": func(t *testing.T, out string) { require.NoError(t, os.Remove(out)) },
		"truncated to the first line": func(t *testing.T, out string) {
			b, err := os.ReadFile(out)
			require.NoError(t, err)
			first := bytes.SplitAfterN(b, []byte("\n"), 2)[0]
			require.NoError(t, os.WriteFile(out, first, 0o644))
		},
		"emptied": func(t *testing.T, out string) { require.NoError(t, os.WriteFile(out, nil, 0o644)) },
		"only marker lines": func(t *testing.T, out string) {
			require.NoError(t, os.WriteFile(out, []byte(`{"seq":0,"exported_at":"2026-01-01T00:00:00Z","gap":"earlier"}`+"\n"), 0o644))
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, store, out := newExportFixture(t)
			seedEvents(t, store, 3)
			runExport(t, p, map[string]any{"path": "ledger.jsonl", "apply": true})
			damage(t, out)
			seedEvents(t, store, 1)
			_, err := exportErr(p, map[string]any{"path": "ledger.jsonl", "apply": true})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "start_new")
			cur, cErr := store.GetFileProvExportCursor()
			require.NoError(t, cErr)
			assert.EqualValues(t, 3, cur)
		})
	}
}

// start_new carries on past the gap: the run appends from the cursor, the result
// says what the gap was, and the file gets a marker line ahead of what this run
// writes. Afterwards the file is continuous again, so the next run needs no flag.
func TestFileProvenanceExportStartNewAcceptsTheGapAndRecordsIt(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)
	runExport(t, p, map[string]any{"path": "ledger.jsonl", "apply": true})
	root, ledger := ledgerRoot(out)
	require.NoError(t, os.Rename(ledger, filepath.Join(root, ".ledger.aside")))
	seedEvents(t, store, 2)

	res, err := exportErr(p, map[string]any{"path": "ledger.jsonl", "apply": true, "start_new": true})
	require.NoError(t, err)
	assert.Contains(t, res.LedgerGap, "empty")
	assert.Contains(t, res.LedgerGap, "seq 2")
	assert.Equal(t, 2, res.Written, "the marker line is not an event")
	assert.EqualValues(t, 4, res.CursorAfter)

	lines := readLines(t, out)
	require.Len(t, lines, 3)
	assert.Zero(t, lines[0].Seq)
	assert.Equal(t, res.LedgerGap, lines[0].Gap)
	assert.Nil(t, lines[0].Event)
	assert.EqualValues(t, 3, lines[1].Seq)
	assert.EqualValues(t, 4, lines[2].Seq)

	seedEvents(t, store, 1)
	res, err = exportErr(p, map[string]any{"path": "ledger.jsonl", "apply": true})
	require.NoError(t, err, "the file continues from the cursor again, so no flag is needed")
	assert.Empty(t, res.LedgerGap)
	assert.Equal(t, 1, res.Written)
}

// A ledger that is continuous with the cursor is not affected, with or without the
// flag: it is appended to in order, in chunks, and never records a gap.
func TestFileProvenanceExportAppendsToAContinuousLedgerWithoutAGap(t *testing.T) {
	for _, startNew := range []bool{false, true} {
		t.Run(fmt.Sprintf("start_new=%v", startNew), func(t *testing.T) {
			p, store, out := newExportFixture(t)
			seedEvents(t, store, 5)
			for _, want := range []uint64{2, 4, 5} {
				res, err := exportErr(p, map[string]any{"path": "ledger.jsonl", "apply": true, "max": 2, "start_new": startNew})
				require.NoError(t, err)
				assert.Empty(t, res.LedgerGap)
				assert.Equal(t, want, res.CursorAfter)
			}
			lines := readLines(t, out)
			require.Len(t, lines, 5)
			for i, l := range lines {
				assert.EqualValues(t, i+1, l.Seq)
				assert.Empty(t, l.Gap)
			}
		})
	}
}

// memLedger is a ledger file held in memory, for the checks that read it.
type memLedger struct {
	*bytes.Reader
	size int64
}

func newMemLedger(b []byte) *memLedger {
	return &memLedger{Reader: bytes.NewReader(b), size: int64(len(b))}
}
func (m *memLedger) Write(b []byte) (int, error) { return len(b), nil }
func (m *memLedger) Close() error                { return nil }
func (m *memLedger) Sync() error                 { return nil }
func (m *memLedger) Stat() (fs.FileInfo, error)  { return memInfo{m.size}, nil }

type memInfo struct{ size int64 }

func (memInfo) Name() string           { return "ledger" }
func (m memInfo) Size() int64          { return m.size }
func (memInfo) Mode() fs.FileMode      { return 0o644 }
func (memInfo) ModTime() (t time.Time) { return t }
func (memInfo) IsDir() bool            { return false }
func (memInfo) Sys() any               { return nil }

func ledgerText(seqs ...uint64) []byte {
	var b bytes.Buffer
	for _, s := range seqs {
		fmt.Fprintf(&b, `{"seq":%d,"exported_at":"2026-01-01T00:00:00Z"}`+"\n", s)
	}
	return b.Bytes()
}

// What the continuity check accepts and what it refuses, against the bytes of the
// file: a file at or past the cursor passes (a run that wrote and synced and died
// before advancing the cursor leaves it ahead, and the next run re-appends), a
// write cut off at the end is not a line, and a marker line never counts as the
// ledger's end.
func TestCheckLedgerContinuity(t *testing.T) {
	cut := append(ledgerText(1, 2, 3), []byte(`{"seq":4,"exported_at":"2026`)...)
	marked := append(ledgerText(1, 2), []byte(`{"seq":0,"exported_at":"2026-01-01T00:00:00Z","gap":"x"}`+"\n")...)
	for _, tc := range []struct {
		name    string
		file    []byte
		cursor  uint64
		wantGap bool
	}{
		{"nothing exported yet", nil, 0, false},
		{"nothing exported yet, file has lines", ledgerText(1, 2), 0, false},
		{"ends at the cursor", ledgerText(1, 2, 3), 3, false},
		{"ahead of the cursor", ledgerText(1, 2, 3, 4), 3, false},
		{"behind the cursor", ledgerText(1, 2), 3, true},
		{"empty", nil, 3, true},
		{"a cut-off last write is not a line", cut, 4, true},
		{"a cut-off last write after the cursor is fine", cut, 3, false},
		{"a marker line is not the end", marked, 3, true},
		{"a marker line after the end", marked, 2, false},
		{"not json", []byte("hello\n"), 3, true},
		{"a complete last line with no newline is a cut-off write", append(ledgerText(1, 2, 3), []byte(`{"seq":4}`)...), 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gap, err := checkLedgerContinuity(newMemLedger(tc.file), tc.cursor, true)
			require.NoError(t, err)
			assert.Equal(t, tc.wantGap, gap != "", gap)
			_, err = checkLedgerContinuity(newMemLedger(tc.file), tc.cursor, false)
			assert.Equal(t, tc.wantGap, err != nil, "without start_new a gap must be an error and nothing else may be")
		})
	}
}

// The end of a ledger is found past the size of one read: lines are read back from
// the end across several chunks, including a line that straddles a chunk boundary.
func TestLastLedgerSeqReadsAcrossChunks(t *testing.T) {
	var b bytes.Buffer
	pad := strings.Repeat("x", 900)
	for i := 1; i <= 400; i++ { // ~370 KB, several chunks
		fmt.Fprintf(&b, `{"seq":%d,"exported_at":"2026-01-01T00:00:00Z","pad":%q}`+"\n", i, pad)
	}
	require.Greater(t, b.Len(), 3*ledgerTailChunk)
	last, found, err := lastLedgerSeq(bytes.NewReader(b.Bytes()), int64(b.Len()))
	require.NoError(t, err)
	assert.True(t, found)
	assert.EqualValues(t, 400, last)

	// Marker lines, a long run of them, between the last event and the end.
	markers := strings.Repeat(`{"seq":0,"gap":"`+pad+`"}`+"\n", 200) // ~180 KB of markers
	all := append(append([]byte(nil), b.Bytes()...), markers...)
	last, found, err = lastLedgerSeq(bytes.NewReader(all), int64(len(all)))
	require.NoError(t, err)
	assert.True(t, found)
	assert.EqualValues(t, 400, last, "marker lines must be skipped however many chunks they fill")

	// The last event line is itself longer than a chunk, so its start is read in an
	// earlier chunk than its end.
	huge := append(append([]byte(nil), ledgerText(1, 2)...),
		[]byte(fmt.Sprintf(`{"seq":3,"pad":%q}`+"\n", strings.Repeat("y", 2*ledgerTailChunk+100)))...)
	last, found, err = lastLedgerSeq(bytes.NewReader(huge), int64(len(huge)))
	require.NoError(t, err)
	assert.True(t, found)
	assert.EqualValues(t, 3, last, "a line spanning chunks must be put back together")

	_, found, err = lastLedgerSeq(bytes.NewReader([]byte(markers)), int64(len(markers)))
	require.NoError(t, err)
	assert.False(t, found)
}

// The refusal says which way the file fails to continue, so the operator can tell
// a recreated empty file from one that was cut short.
func TestCheckLedgerContinuityNamesWhatIsWrong(t *testing.T) {
	for name, tc := range map[string]struct {
		file []byte
		want string
	}{
		"empty":                  {nil, "ledger file is empty"},
		"no exported event":      {[]byte(`{"seq":0,"gap":"x"}` + "\n"), "holds no exported event"},
		"ends before the cursor": {ledgerText(1, 2), "ends at seq 2"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := checkLedgerContinuity(newMemLedger(tc.file), 3, false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "seq 3")
			assert.Contains(t, err.Error(), "start_new")
		})
	}
}
