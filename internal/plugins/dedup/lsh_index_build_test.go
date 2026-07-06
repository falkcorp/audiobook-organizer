// file: internal/plugins/dedup/lsh_index_build_test.go
// version: 1.3.0
// guid: c1cf5590-1bc1-4f88-9031-62333bcb593f
// last-edited: 2026-07-06

package dedup

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// synthRawLSH generates a synthetic raw chromaprint for testing.
// Uses a deterministic RNG to produce repeatable results.
func synthRawLSH(seed int64, frames int) []byte {
	rng := rand.New(rand.NewSource(seed))
	raw := make([]byte, frames*4)
	for i := 0; i < frames; i++ {
		binary.LittleEndian.PutUint32(raw[i*4:], rng.Uint32())
	}
	return raw
}

// mockLSHStore is a test double for LSHIndexStore. It tracks calls to
// PutLSHEntries and HasLSHIndex so the op-level test can assert correct
// behavior without a real PebbleDB instance.
//
// Note: mockLSHStore only needs to satisfy the LSHIndexStore interface
// (used via type assertion inside runLSHIndexBuild), not database.Store.
type mockLSHStore struct {
	files        []database.BookFile
	indexedFiles map[string]bool // HasLSHIndex returns true for these IDs
	mu           sync.Mutex      // guards putCalls (written concurrently by RunItems workers)
	putCalls     []string        // fileIDs passed to PutLSHEntries
	flagSet      bool

	// hydrateFiles simulates the raw-Pebble GetBookFiles(bookID) read that
	// the hydrate path falls back to when a memdb-slim BookFile row (from
	// GetAllBookFiles) has AcoustIDFingerprintDurationSec > 0 but a nil
	// AcoustIDFingerprint blob. Keyed by BookID.
	hydrateFiles map[string][]database.BookFile
	// hydrateErr, when non-nil, makes GetBookFiles fail for every book —
	// simulates a Pebble read error during hydrate.
	hydrateErr error
}

// GetBookFiles simulates the raw-Pebble read used to hydrate a memdb-slim
// BookFile's stripped AcoustIDFingerprint blob. Unlike GetAllBookFiles, a
// real PebbleStore.GetBookFiles never goes through memdb, so it always
// returns full rows — the mock mirrors that by returning hydrateFiles as-is.
func (m *mockLSHStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	if m.hydrateErr != nil {
		return nil, m.hydrateErr
	}
	return m.hydrateFiles[bookID], nil
}

// database.Store compliance: use nil for the plugin.store field — the
// type assertion path in runLSHIndexBuild casts to LSHIndexStore directly.

func (m *mockLSHStore) GetAllBookFiles() ([]database.BookFile, error) {
	return m.files, nil
}
func (m *mockLSHStore) HasLSHIndex(id string) bool {
	return m.indexedFiles[id]
}
func (m *mockLSHStore) PutLSHEntries(fileID, _ string, _ []fingerprint.Subprint, _ []byte) error {
	m.mu.Lock()
	m.putCalls = append(m.putCalls, fileID)
	m.mu.Unlock()
	return nil
}
func (m *mockLSHStore) IsLSHIndexBuilt() bool {
	return m.flagSet
}
func (m *mockLSHStore) SetLSHIndexBuilt() error {
	m.flagSet = true
	return nil
}
func (m *mockLSHStore) GetSetting(_ string) (*database.Setting, error) {
	return nil, nil
}
func (m *mockLSHStore) SetSetting(_, _, _ string, _ bool) error {
	return nil
}

// fakeReporter is a minimal sdk.Reporter that satisfies the interface.
type fakeReporter struct{}

func (f *fakeReporter) UpdateProgress(_, _ int, _ string) error { return nil }
func (f *fakeReporter) Log(_ slog.Level, _ string, _ ...slog.Attr) error {
	return nil
}
func (f *fakeReporter) Logger() *slog.Logger { return slog.Default() }
func (f *fakeReporter) Checkpoint(_ any) error {
	return nil
}
func (f *fakeReporter) IsCanceled() bool { return false }
func (f *fakeReporter) RunPhase(_ context.Context, _ string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(context.Background(), f)
}
func (f *fakeReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }
func (f *fakeReporter) SetCurrentItem(_ string)                           {}

// pluginWithMockStore creates a Plugin whose store satisfies LSHIndexStore
// via type assertion without needing to implement database.Store. We rely on
// the fact that p.store is a database.Store interface; for tests we can cast
// to any and let the type assertion in runLSHIndexBuild handle it.
//
// The trick: we wrap the mock in a helper type that embeds *database.MockStore
// from the mocks package and overrides only the LSHIndexStore methods — but
// that's heavyweight. Instead, because runLSHIndexBuild does:
//
//	lshStore, ok := p.store.(LSHIndexStore)
//
// …and p.store is just a database.Store interface, we can set p.store to a
// value that satisfies database.Store AND LSHIndexStore. Using mockLSHStoreAdapter
// which embeds a stub database.Store and adds the LSH methods.
type mockLSHStoreAdapter struct {
	database.Store // embed to satisfy the interface; methods we need are on the mock
	inner          *mockLSHStore
}

// Override only the LSHIndexStore methods (plus GetBookFiles, used by the
// hydrate path — it is part of database.BookFileStore/database.Store, not
// LSHIndexStore, but runLSHIndexBuild calls it via p.store directly).
func (a *mockLSHStoreAdapter) GetAllBookFiles() ([]database.BookFile, error) {
	return a.inner.GetAllBookFiles()
}
func (a *mockLSHStoreAdapter) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return a.inner.GetBookFiles(bookID)
}
func (a *mockLSHStoreAdapter) HasLSHIndex(id string) bool {
	return a.inner.HasLSHIndex(id)
}
func (a *mockLSHStoreAdapter) PutLSHEntries(fileID, bookID string, subs []fingerprint.Subprint, bands []byte) error {
	return a.inner.PutLSHEntries(fileID, bookID, subs, bands)
}
func (a *mockLSHStoreAdapter) IsLSHIndexBuilt() bool {
	return a.inner.IsLSHIndexBuilt()
}
func (a *mockLSHStoreAdapter) SetLSHIndexBuilt() error {
	return a.inner.SetLSHIndexBuilt()
}
func (a *mockLSHStoreAdapter) GetSetting(key string) (*database.Setting, error) {
	return a.inner.GetSetting(key)
}
func (a *mockLSHStoreAdapter) SetSetting(k, v, dt string, internal bool) error {
	return a.inner.SetSetting(k, v, dt, internal)
}

// TestLSHIndexBuild_OpIndexesAllWithFingerprints verifies that the op:
//   - calls PutLSHEntries for files with fingerprints
//   - skips files without a fingerprint
//   - skips files that already have an LSH index entry (resumable)
//   - sets the completion flag on success
func TestLSHIndexBuild_OpIndexesAllWithFingerprints(t *testing.T) {
	fp := synthRawLSH(42, 57600)

	ms := &mockLSHStore{
		files: []database.BookFile{
			{ID: "file-1", BookID: "book-1", AcoustIDFingerprint: fp},
			{ID: "file-2", BookID: "book-2", AcoustIDFingerprint: fp},
			{ID: "file-3", BookID: "book-3", AcoustIDFingerprint: nil}, // no fp
			{ID: "file-4", BookID: "book-4", AcoustIDFingerprint: fp},
		},
		indexedFiles: map[string]bool{
			"file-4": true, // already indexed — should be skipped
		},
	}

	// Build a Plugin using the adapter as its store.
	// engine is nil — runLSHIndexBuild doesn't use it (no engine guard in this op).
	p := &Plugin{
		store: &mockLSHStoreAdapter{inner: ms},
	}

	err := p.runLSHIndexBuild(context.Background(), json.RawMessage("{}"), &fakeReporter{})
	if err != nil {
		t.Fatalf("runLSHIndexBuild: %v", err)
	}

	// file-1 and file-2 should be indexed; file-3 (no fp) and file-4 (already indexed) skipped.
	indexed := make(map[string]bool)
	for _, id := range ms.putCalls {
		indexed[id] = true
	}
	if !indexed["file-1"] {
		t.Errorf("file-1 not indexed")
	}
	if !indexed["file-2"] {
		t.Errorf("file-2 not indexed")
	}
	if indexed["file-3"] {
		t.Errorf("file-3 (no fingerprint) should NOT be indexed")
	}
	if indexed["file-4"] {
		t.Errorf("file-4 (already indexed) should NOT be re-indexed (skipped by HasLSHIndex)")
	}

	if !ms.flagSet {
		t.Errorf("completion flag not set after successful op run")
	}
}

// TestLSHIndexBuild_OpEmptyLibrary verifies the op exits cleanly on an
// empty store (no panic, correct "nothing to index" path).
func TestLSHIndexBuild_OpEmptyLibrary(t *testing.T) {
	ms := &mockLSHStore{
		files:        nil,
		indexedFiles: map[string]bool{},
	}
	p := &Plugin{store: &mockLSHStoreAdapter{inner: ms}}

	err := p.runLSHIndexBuild(context.Background(), json.RawMessage("{}"), &fakeReporter{})
	if err != nil {
		t.Fatalf("runLSHIndexBuild on empty store: %v", err)
	}
	if len(ms.putCalls) != 0 {
		t.Errorf("expected 0 PutLSHEntries calls, got %d", len(ms.putCalls))
	}
}

// TestLSHIndexBuild_OpNonLSHStore verifies a helpful error is returned
// when the plugin's store doesn't implement LSHIndexStore.
func TestLSHIndexBuild_OpNonLSHStore(t *testing.T) {
	// Use a nil store — p.store.(LSHIndexStore) will fail.
	p := &Plugin{store: nil}
	err := p.runLSHIndexBuild(context.Background(), json.RawMessage("{}"), &fakeReporter{})
	if err == nil {
		t.Fatal("expected error when store doesn't implement LSHIndexStore, got nil")
	}
}

// TestLSHIndexBuild_EnqueuesFingerRescanForNoFPBooks verifies that books
// whose files lack a fingerprint trigger an acoustid.fingerprint-rescan
// enqueue, and that book IDs are deduplicated (multiple files per book
// produce exactly one entry).
func TestLSHIndexBuild_EnqueuesFingerRescanForNoFPBooks(t *testing.T) {
	fp := synthRawLSH(42, 57600)

	ms := &mockLSHStore{
		files: []database.BookFile{
			{ID: "file-1", BookID: "book-has-fp", AcoustIDFingerprint: fp},
			{ID: "file-2", BookID: "book-nofp-a", AcoustIDFingerprint: nil},
			{ID: "file-3", BookID: "book-nofp-a", AcoustIDFingerprint: nil}, // same book → deduplicated
			{ID: "file-4", BookID: "book-nofp-b", AcoustIDFingerprint: nil},
		},
		indexedFiles: map[string]bool{},
	}

	reg := &mockRegistry{}
	p := &Plugin{
		store:    &mockLSHStoreAdapter{inner: ms},
		registry: reg,
	}

	if err := p.runLSHIndexBuild(context.Background(), json.RawMessage("{}"), &fakeReporter{}); err != nil {
		t.Fatalf("runLSHIndexBuild: %v", err)
	}

	// Exactly one EnqueueOp call should have been made.
	if len(reg.enqueuedDefs) != 1 {
		t.Fatalf("expected 1 EnqueueOp call, got %d", len(reg.enqueuedDefs))
	}
	if reg.enqueuedDefs[0] != "acoustid.fingerprint-rescan" {
		t.Errorf("expected acoustid.fingerprint-rescan, got %s", reg.enqueuedDefs[0])
	}

	// Params must contain 2 unique book IDs (book-nofp-a deduped).
	params, ok := reg.enqueuedParams[0].(map[string]any)
	if !ok {
		t.Fatalf("params not map[string]any")
	}
	bookIDs, ok := params["book_ids"].([]string)
	if !ok {
		t.Fatalf("book_ids not []string")
	}
	if len(bookIDs) != 2 {
		t.Errorf("expected 2 unique book IDs, got %d: %v", len(bookIDs), bookIDs)
	}
	// file-1 (book-has-fp) must NOT be in the list.
	for _, id := range bookIDs {
		if id == "book-has-fp" {
			t.Errorf("book-has-fp (has fingerprint) should not appear in fingerprint-rescan book_ids")
		}
	}
}

// TestLSHIndexBuild_NoEnqueueWhenAllHaveFingerprints verifies that no
// fingerprint-rescan is enqueued when every file already has a fingerprint.
func TestLSHIndexBuild_NoEnqueueWhenAllHaveFingerprints(t *testing.T) {
	fp := synthRawLSH(7, 57600)

	ms := &mockLSHStore{
		files: []database.BookFile{
			{ID: "f1", BookID: "b1", AcoustIDFingerprint: fp},
			{ID: "f2", BookID: "b2", AcoustIDFingerprint: fp},
		},
		indexedFiles: map[string]bool{},
	}

	reg := &mockRegistry{}
	p := &Plugin{
		store:    &mockLSHStoreAdapter{inner: ms},
		registry: reg,
	}

	if err := p.runLSHIndexBuild(context.Background(), json.RawMessage("{}"), &fakeReporter{}); err != nil {
		t.Fatalf("runLSHIndexBuild: %v", err)
	}

	if len(reg.enqueuedDefs) != 0 {
		t.Errorf("expected no EnqueueOp calls when all files have fingerprints, got %d", len(reg.enqueuedDefs))
	}
}

// TestLSHIndexBuild_SkipsPermanentlyFailedBooksFromEnqueue verifies that books
// whose noFP files are ALL permanently failed (FingerprintFailedAt != nil) are
// NOT enqueued for fingerprint-rescan. This prevents an infinite retry loop for
// structurally impossible files (too short, corrupt, DRM-protected).
//
// A book is only enqueued if at least one of its noFP files has
// FingerprintFailedAt == nil (i.e., was never tried).
func TestLSHIndexBuild_SkipsPermanentlyFailedBooksFromEnqueue(t *testing.T) {
	fp := synthRawLSH(42, 57600)
	now := time.Now()

	ms := &mockLSHStore{
		files: []database.BookFile{
			// book-has-fp: has fingerprint → indexed
			{ID: "file-1", BookID: "book-has-fp", AcoustIDFingerprint: fp},
			// book-perm-fail: noFP but ALL files permanently failed → no enqueue
			{ID: "file-2", BookID: "book-perm-fail", FingerprintFailedAt: &now},
			{ID: "file-3", BookID: "book-perm-fail", FingerprintFailedAt: &now},
			// book-never-tried: noFP, never attempted → should be enqueued
			{ID: "file-4", BookID: "book-never-tried"},
		},
		indexedFiles: map[string]bool{},
	}

	reg := &mockRegistry{}
	p := &Plugin{
		store:    &mockLSHStoreAdapter{inner: ms},
		registry: reg,
	}

	if err := p.runLSHIndexBuild(context.Background(), json.RawMessage("{}"), &fakeReporter{}); err != nil {
		t.Fatalf("runLSHIndexBuild: %v", err)
	}

	// Exactly one EnqueueOp for the one book with never-tried files.
	if len(reg.enqueuedDefs) != 1 {
		t.Fatalf("expected 1 EnqueueOp call, got %d", len(reg.enqueuedDefs))
	}

	params, ok := reg.enqueuedParams[0].(map[string]any)
	if !ok {
		t.Fatalf("params not map[string]any")
	}
	bookIDs, ok := params["book_ids"].([]string)
	if !ok {
		t.Fatalf("book_ids not []string")
	}
	if len(bookIDs) != 1 {
		t.Errorf("expected 1 book_id (book-never-tried only), got %d: %v", len(bookIDs), bookIDs)
	}
	if len(bookIDs) > 0 && bookIDs[0] != "book-never-tried" {
		t.Errorf("expected book-never-tried, got %s", bookIDs[0])
	}
	// book-perm-fail must not appear in the enqueue list.
	for _, id := range bookIDs {
		if id == "book-perm-fail" {
			t.Errorf("book-perm-fail (all files permanently failed) should not be enqueued")
		}
	}
}

// TestLSHIndexBuild_HydratesMemdbStrippedFingerprint is the regression test
// for the memdb-slim no-op bug: GetAllBookFiles (the memdb-slim projection
// under prod's UseMemDB=true) nils AcoustIDFingerprint while preserving
// AcoustIDFingerprintDurationSec (>0 ⇒ a whole-file fingerprint exists on
// the raw row). Before the fix, the gate was len(AcoustIDFingerprint)==0,
// which is always true for a memdb-slim row, so every such file was silently
// skipped and PutLSHEntries was never called — the op reported success but
// indexed nothing.
//
// This test simulates that shape: file-1 arrives from GetAllBookFiles with a
// nil blob + DurationSec>0, and GetBookFiles(bookID) — standing in for the
// real PebbleStore.GetBookFiles, which always reads raw Pebble and is never
// memdb-stripped — returns the same file WITH the real fingerprint bytes.
// Asserts the hydrate path recovers the blob and the file gets indexed.
func TestLSHIndexBuild_HydratesMemdbStrippedFingerprint(t *testing.T) {
	fp := synthRawLSH(99, 57600)

	ms := &mockLSHStore{
		files: []database.BookFile{
			// Memdb-slim projection: DurationSec survives, blob is nil'd.
			{ID: "file-1", BookID: "book-1", AcoustIDFingerprint: nil, AcoustIDFingerprintDurationSec: 1800},
		},
		indexedFiles: map[string]bool{},
		hydrateFiles: map[string][]database.BookFile{
			"book-1": {
				{ID: "file-1", BookID: "book-1", AcoustIDFingerprint: fp, AcoustIDFingerprintDurationSec: 1800},
			},
		},
	}

	p := &Plugin{store: &mockLSHStoreAdapter{inner: ms}}

	if err := p.runLSHIndexBuild(context.Background(), json.RawMessage("{}"), &fakeReporter{}); err != nil {
		t.Fatalf("runLSHIndexBuild: %v", err)
	}

	found := false
	for _, id := range ms.putCalls {
		if id == "file-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("file-1 (memdb-stripped fingerprint) should have been hydrated via GetBookFiles and indexed via PutLSHEntries; got calls=%v", ms.putCalls)
	}
	if !ms.flagSet {
		t.Errorf("completion flag not set after successful op run")
	}
}

// TestLSHIndexBuild_HydrateDataDriftCountsAsErrorNotNoFP verifies that when
// the proxy (AcoustIDFingerprintDurationSec>0) claims a fingerprint exists
// but the hydrate read (GetBookFiles) comes back empty — either because it
// errored or because the raw row genuinely has no blob (data drift) — the
// file is NOT misclassified as "no fingerprint" (which would enqueue a
// pointless acoustid.fingerprint-rescan for a book that actually IS
// fingerprinted). It should be skipped cleanly with no PutLSHEntries call
// and no rescan enqueue.
func TestLSHIndexBuild_HydrateDataDriftCountsAsErrorNotNoFP(t *testing.T) {
	ms := &mockLSHStore{
		files: []database.BookFile{
			{ID: "file-1", BookID: "book-1", AcoustIDFingerprint: nil, AcoustIDFingerprintDurationSec: 1800},
		},
		indexedFiles: map[string]bool{},
		hydrateFiles: map[string][]database.BookFile{
			// GetBookFiles "succeeds" but returns no matching file / empty blob.
			"book-1": {},
		},
	}

	reg := &mockRegistry{}
	p := &Plugin{store: &mockLSHStoreAdapter{inner: ms}, registry: reg}

	if err := p.runLSHIndexBuild(context.Background(), json.RawMessage("{}"), &fakeReporter{}); err != nil {
		t.Fatalf("runLSHIndexBuild: %v", err)
	}

	if len(ms.putCalls) != 0 {
		t.Errorf("expected no PutLSHEntries calls for a data-drift hydrate miss, got %v", ms.putCalls)
	}
	if len(reg.enqueuedDefs) != 0 {
		t.Errorf("book-1 has a fingerprint per the DurationSec proxy — it must NOT be enqueued for fingerprint-rescan, got %d enqueue(s)", len(reg.enqueuedDefs))
	}
}
