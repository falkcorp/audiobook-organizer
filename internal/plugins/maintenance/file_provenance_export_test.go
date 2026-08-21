// file: internal/plugins/maintenance/file_provenance_export_test.go
// version: 1.0.0
// guid: d61df6f5-c2e0-47ab-b028-44d97b64001c
// last-edited: 2026-08-21
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
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newExportFixture(t *testing.T) (*Plugin, *database.PebbleStore, string) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return New(provDeps{prov: store}), store, filepath.Join(t.TempDir(), "ledger.jsonl")
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
	res, err := p.exportFileProvenance(context.Background(), raw)
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
	_, err := p.exportFileProvenance(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path")
}

// A relative path or one with traversal is refused rather than resolved
// against whatever the process happens to consider its working directory.
func TestFileProvenanceExportRejectsANonAbsolutePath(t *testing.T) {
	p, _, _ := newExportFixture(t)
	_, err := p.exportFileProvenance(context.Background(),
		json.RawMessage(`{"path":"ledger.jsonl"}`))
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
	p := New(provDeps{prov: stub})
	out := filepath.Join(t.TempDir(), "ledger.jsonl")

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
	p := New(provDeps{prov: stub})
	out := filepath.Join(t.TempDir(), "ledger.jsonl")

	res := runExport(t, p, map[string]any{"path": out, "apply": true})

	require.NotEmpty(t, res.Gaps, "deleted sequence slots went unreported")
	assert.Equal(t, []string{"2-3"}, res.Gaps)
	assert.Equal(t, 2, res.Written)
}

func TestFileProvenanceExportFailsWithoutAProvenanceStore(t *testing.T) {
	p := New(provDeps{prov: nil})
	_, err := p.exportFileProvenance(context.Background(),
		json.RawMessage(`{"path":"/tmp/x.jsonl"}`))
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
	p, store, _ := newExportFixture(t)
	seedEvents(t, store, 2)

	// A directory cannot be opened for appending.
	raw, mErr := json.Marshal(map[string]any{"path": t.TempDir(), "apply": true})
	require.NoError(t, mErr)
	_, err := p.exportFileProvenance(context.Background(), raw)
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

func withOpenExportFile(t *testing.T, w syncWriteCloser) {
	t.Helper()
	prev := openExportFile
	openExportFile = func(string) (syncWriteCloser, error) { return w, nil }
	t.Cleanup(func() { openExportFile = prev })
}

func TestFileProvenanceExportLeavesTheCursorWhenTheWriteFails(t *testing.T) {
	p, store, out := newExportFixture(t)
	seedEvents(t, store, 2)
	withOpenExportFile(t, &failingWriter{failWrite: true})

	raw, mErr := json.Marshal(map[string]any{"path": out, "apply": true})
	require.NoError(t, mErr)
	_, err := p.exportFileProvenance(context.Background(), raw)
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
	_, err := p.exportFileProvenance(context.Background(), raw)
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
	p := New(provDeps{prov: stub})

	res := runExport(t, p, map[string]any{"path": filepath.Join(t.TempDir(), "l.jsonl"), "apply": true})

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
	p := New(provDeps{prov: stub})

	res := runExport(t, p, map[string]any{"path": filepath.Join(t.TempDir(), "l.jsonl"), "apply": true})

	assert.Equal(t, 1, res.ChainsVerified)
	assert.Zero(t, res.ChainsBroken, "pre-chain events were reported as tampering")
}
