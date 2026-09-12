// file: internal/itunes/backfill_test.go
// version: 1.4.0
// guid: c9d0e1f2-a3b4-c5d6-e7f8-a9b0c1d2e3f4
// last-edited: 2026-09-12

package itunes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// errMockFailure is the sentinel returned by MockBackfillStore methods when
// hasError is set. Replaces a stale reference to a non-existent
// database.ErrNotFound sentinel.
var errMockFailure = errors.New("mock store failure")

// MockBackfillStore provides a minimal mock for testing backfill operations.
type MockBackfillStore struct {
	books          map[string]database.Book
	bookFiles      []database.BookFileCore
	settings       map[string]string
	externalIDMaps []database.ExternalIDMapping
	hasError       bool
	// failBulkCreate fails only BulkCreateExternalIDMappings, leaving reads
	// (GetAllBooksCore/GetAllBookFilesCore) working so the H7
	// bulk-write-error propagation test can exercise the write failure
	// without the pagination loop breaking out before it ever reaches a write.
	failBulkCreate bool
	// Call counters — the SQ-04 tests assert the boot path does NOT rescan
	// once the done flag is set, and that the file read happens once per run.
	getAllBooksCalls     int
	getAllBookFilesCalls int
	// swapAfterFirstPage simulates the production hazard offset pagination
	// is exposed to (PERF-5): the memdb reconciler swaps the whole snapshot
	// asynchronously, so an offset page read after the swap indexes into a
	// shifted list. When set, a paged read (limit > 0) serves page 1 from the
	// full ID-sorted list and every later page from that list minus its first
	// entry — so the row at index `limit` is silently skipped. A single-call
	// read (limit <= 0) is one snapshot and sees every row. paged records
	// whether any offset-paged read happened at all.
	swapAfterFirstPage bool
	paged              bool
}

func NewMockBackfillStore() *MockBackfillStore {
	return &MockBackfillStore{
		books:    make(map[string]database.Book),
		settings: make(map[string]string),
	}
}

func (m *MockBackfillStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	m.getAllBooksCalls++
	if m.hasError {
		return nil, errMockFailure
	}
	// Sort by ID so pages are deterministic — offset slicing over a map's
	// random iteration order would hand out duplicate/missing books across
	// pages.
	ids := make([]string, 0, len(m.books))
	for id := range m.books {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	all := make([]database.BookCore, 0, len(ids))
	for _, id := range ids {
		b := m.books[id]
		all = append(all, b.Core())
	}
	// limit <= 0 is unbounded on both real store paths (memdb and the Pebble
	// scan); the mock used to slice [offset:offset+0] and return nothing,
	// which would let a single-call caller pass a test for the wrong reason.
	if limit <= 0 {
		if offset >= len(all) {
			return []database.BookCore{}, nil
		}
		return all[offset:], nil
	}
	m.paged = true
	if m.swapAfterFirstPage && m.getAllBooksCalls > 1 && len(all) > 0 {
		all = all[1:] // the snapshot swap: every later position shifts by one
	}
	// Simulate pagination via limit/offset to allow backfill loop to terminate.
	if offset >= len(all) {
		return []database.BookCore{}, nil
	}
	end := min(offset+limit, len(all))
	return all[offset:end], nil
}

func (m *MockBackfillStore) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	m.getAllBookFilesCalls++
	if m.hasError {
		return nil, errMockFailure
	}
	out := make([]database.BookFileCore, len(m.bookFiles))
	copy(out, m.bookFiles)
	return out, nil
}

func (m *MockBackfillStore) CreateExternalIDMapping(mapping *database.ExternalIDMapping) error {
	if m.hasError {
		return errMockFailure
	}
	m.externalIDMaps = append(m.externalIDMaps, *mapping)
	return nil
}

func (m *MockBackfillStore) BulkCreateExternalIDMappings(mappings []database.ExternalIDMapping) error {
	if m.hasError || m.failBulkCreate {
		return errMockFailure
	}
	m.externalIDMaps = append(m.externalIDMaps, mappings...)
	return nil
}

func (m *MockBackfillStore) GetSetting(key string) (*database.Setting, error) {
	if m.hasError {
		return nil, errMockFailure
	}
	v, ok := m.settings[key]
	if !ok {
		return nil, fmt.Errorf("setting not found: %s: %w", key, database.ErrSettingNotFound)
	}
	return &database.Setting{Key: key, Value: v, Type: "string"}, nil
}

func (m *MockBackfillStore) SetSetting(key, value, dataType string, internal bool) error {
	if m.hasError {
		return errMockFailure
	}
	m.settings[key] = value
	return nil
}

// setITunesXMLPath points the global iTunes XML config at path for the
// duration of the test and restores the previous value afterwards.
func setITunesXMLPath(t *testing.T, path string) {
	t.Helper()
	prev := config.AppConfig.ITunes.LibraryReadPath
	config.AppConfig.ITunes.LibraryReadPath = path
	t.Cleanup(func() { config.AppConfig.ITunes.LibraryReadPath = prev })
}

func TestBackfillExternalIDsWithNilStore(t *testing.T) {
	err := BackfillExternalIDs(context.Background(), nil, nil)
	if err != nil {
		t.Errorf("expected nil error for nil store, got %v", err)
	}
	if err := BackfillExternalIDsOnce(context.Background(), nil, nil); err != nil {
		t.Errorf("expected nil error for nil store (Once), got %v", err)
	}
}

func TestBackfillExternalIDsCollectsBookPIDs(t *testing.T) {
	mockStore := NewMockBackfillStore()
	pidValue := "test-pid-123"
	mockStore.books["book1"] = database.Book{
		ID:                 "book1",
		Title:              "Test Book",
		ITunesPersistentID: &pidValue,
	}

	var progressCalls int
	err := BackfillExternalIDs(context.Background(), mockStore, func(_, _ int, _ string) {
		progressCalls++
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Should have created one mapping from the book PID
	if len(mockStore.externalIDMaps) < 1 {
		t.Errorf("expected at least 1 mapping, got %d", len(mockStore.externalIDMaps))
	}

	// H7: pagination must report progress at least once per page scanned.
	if progressCalls == 0 {
		t.Error("expected progress callback to be invoked at least once, got 0 calls")
	}
}

// TestBackfillExternalIDsPropagatesBulkWriteError proves the H7 fix: a
// persistence failure during the book-pagination pass is returned to the
// caller instead of being silently discarded (previously `_ =
// store.BulkCreateExternalIDMappings(batch)`), so the op can actually fail.
func TestBackfillExternalIDsPropagatesBulkWriteError(t *testing.T) {
	mockStore := NewMockBackfillStore()
	pidValue := "test-pid-456"
	mockStore.books["book1"] = database.Book{
		ID:                 "book1",
		Title:              "Test Book",
		ITunesPersistentID: &pidValue,
	}
	mockStore.failBulkCreate = true

	err := BackfillExternalIDs(context.Background(), mockStore, nil)
	if err == nil {
		t.Fatal("expected error when BulkCreateExternalIDMappings fails, got nil")
	}
}

// TestBackfillExternalIDsPropagatesReadError proves the H7 fix: a
// GetAllBooksCore read failure during pagination is returned instead of
// silently `break`-ing out of the loop and falling through to mark the
// whole backfill "done" despite skipping the rest of the library.
func TestBackfillExternalIDsPropagatesReadError(t *testing.T) {
	mockStore := NewMockBackfillStore()
	mockStore.hasError = true

	err := BackfillExternalIDs(context.Background(), mockStore, nil)
	if err == nil {
		t.Fatal("expected error when GetAllBooksCore fails, got nil")
	}
}

func TestBackfillITunesTrackPIDsWithNoConfiguredPath(t *testing.T) {
	mockStore := NewMockBackfillStore()

	// With no configured path, should return 0 gracefully
	count, err := BackfillITunesTrackPIDs(context.Background(), mockStore, nil)
	if count != 0 {
		t.Errorf("expected 0 registered PIDs with no configured path, got %d", count)
	}
	if err != nil {
		t.Errorf("unexpected error with no path: %v", err)
	}
}

// TestBackfillExternalIDsOnceSkipsAfterCompletedRun is the SQ-04 regression
// test: the first boot runs the full-library scan and records completion;
// a second boot against the same store must NOT rescan. On the pre-fix code
// the done setting was written but never read, so both calls scanned.
func TestBackfillExternalIDsOnceSkipsAfterCompletedRun(t *testing.T) {
	setITunesXMLPath(t, "")
	mockStore := NewMockBackfillStore()
	pidValue := "test-pid-once"
	mockStore.books["book1"] = database.Book{ID: "book1", Title: "Once", ITunesPersistentID: &pidValue}

	if err := BackfillExternalIDsOnce(context.Background(), mockStore, nil); err != nil {
		t.Fatalf("first run: unexpected error: %v", err)
	}
	if mockStore.getAllBooksCalls == 0 {
		t.Fatal("first run must scan the library")
	}
	if got := mockStore.settings[ExternalIDBackfillDoneKey]; got != externalIDBackfillDoneBooksOnly {
		t.Fatalf("done flag after a run with no XML path configured: got %q, want %q", got, externalIDBackfillDoneBooksOnly)
	}
	booksCalls, filesCalls, mappings := mockStore.getAllBooksCalls, mockStore.getAllBookFilesCalls, len(mockStore.externalIDMaps)

	var lastMsg string
	if err := BackfillExternalIDsOnce(context.Background(), mockStore, func(_, _ int, msg string) { lastMsg = msg }); err != nil {
		t.Fatalf("second run: unexpected error: %v", err)
	}
	if mockStore.getAllBooksCalls != booksCalls {
		t.Errorf("second boot rescanned books: GetAllBooksCore calls went %d -> %d", booksCalls, mockStore.getAllBooksCalls)
	}
	if mockStore.getAllBookFilesCalls != filesCalls {
		t.Errorf("second boot re-read files: GetAllBookFilesCore calls went %d -> %d", filesCalls, mockStore.getAllBookFilesCalls)
	}
	if len(mockStore.externalIDMaps) != mappings {
		t.Errorf("second boot wrote mappings: %d -> %d", mappings, len(mockStore.externalIDMaps))
	}
	if !strings.Contains(lastMsg, "skipped") {
		t.Errorf("skip must be reported through progress, got %q", lastMsg)
	}
}

// TestBackfillExternalIDsErrorDoesNotSetDoneFlag: a run that fails midway
// must leave the flag unset so the next boot retries — a flag written after
// a partial run is a false "done" that hides the gap forever.
func TestBackfillExternalIDsErrorDoesNotSetDoneFlag(t *testing.T) {
	setITunesXMLPath(t, "")
	mockStore := NewMockBackfillStore()
	pidValue := "test-pid-err"
	mockStore.books["book1"] = database.Book{ID: "book1", Title: "Err", ITunesPersistentID: &pidValue}
	mockStore.failBulkCreate = true

	if err := BackfillExternalIDsOnce(context.Background(), mockStore, nil); err == nil {
		t.Fatal("expected the bulk-write error to propagate")
	}
	if v, ok := mockStore.settings[ExternalIDBackfillDoneKey]; ok {
		t.Fatalf("done flag must not be set after an errored run, got %q", v)
	}

	// And the next boot runs again.
	mockStore.failBulkCreate = false
	before := mockStore.getAllBooksCalls
	if err := BackfillExternalIDsOnce(context.Background(), mockStore, nil); err != nil {
		t.Fatalf("retry: unexpected error: %v", err)
	}
	if mockStore.getAllBooksCalls == before {
		t.Error("the boot after an errored run must rescan")
	}
	if _, ok := mockStore.settings[ExternalIDBackfillDoneKey]; !ok {
		t.Error("done flag must be set after the successful retry")
	}
}

// TestBackfillExternalIDsCanceledDoesNotSetDoneFlag: cancellation returns
// nil (not an error) but is still not completion.
func TestBackfillExternalIDsCanceledDoesNotSetDoneFlag(t *testing.T) {
	setITunesXMLPath(t, "")
	mockStore := NewMockBackfillStore()
	pidValue := "test-pid-cancel"
	mockStore.books["book1"] = database.Book{ID: "book1", Title: "Cancel", ITunesPersistentID: &pidValue}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := BackfillExternalIDsOnce(ctx, mockStore, nil); err != nil {
		t.Fatalf("cancellation must return nil, got %v", err)
	}
	if v, ok := mockStore.settings[ExternalIDBackfillDoneKey]; ok {
		t.Fatalf("done flag must not be set after a cancelled run, got %q", v)
	}
}

// TestBackfillExternalIDsForcedRunIgnoresDoneFlag: the manual maintenance op
// calls BackfillExternalIDs directly and must run even when the boot flag is
// set — an operator's explicit request is never silently skipped.
func TestBackfillExternalIDsForcedRunIgnoresDoneFlag(t *testing.T) {
	setITunesXMLPath(t, "")
	mockStore := NewMockBackfillStore()
	mockStore.settings[ExternalIDBackfillDoneKey] = externalIDBackfillDoneFull
	pidValue := "test-pid-force"
	mockStore.books["book1"] = database.Book{ID: "book1", Title: "Force", ITunesPersistentID: &pidValue}

	if err := BackfillExternalIDs(context.Background(), mockStore, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockStore.getAllBooksCalls == 0 {
		t.Error("a forced run must scan even with the done flag set")
	}
	if len(mockStore.externalIDMaps) != 1 {
		t.Errorf("expected 1 mapping from the forced run, got %d", len(mockStore.externalIDMaps))
	}
}

// TestExternalIDBackfillDoneValues pins the flag's meaning: "true" is done
// unconditionally; "books_only" is done only while no iTunes XML path is
// configured (configuring one re-arms the boot-time run so the track-PID
// pass finally happens); anything else, or a missing flag, means run.
func TestExternalIDBackfillDoneValues(t *testing.T) {
	xmlPath := filepath.Join(t.TempDir(), "iTunes Library.xml")
	cases := []struct {
		name    string
		value   string // "" = flag absent
		xmlPath string
		want    bool
	}{
		{"absent", "", "", false},
		{"full", externalIDBackfillDoneFull, "", true},
		{"full with xml configured", externalIDBackfillDoneFull, xmlPath, true},
		{"books_only, xml still unconfigured", externalIDBackfillDoneBooksOnly, "", true},
		{"books_only, xml now configured", externalIDBackfillDoneBooksOnly, xmlPath, false},
		{"unrecognised value", "maybe", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setITunesXMLPath(t, tc.xmlPath)
			mockStore := NewMockBackfillStore()
			if tc.value != "" {
				mockStore.settings[ExternalIDBackfillDoneKey] = tc.value
			}
			got, _ := externalIDBackfillDone(mockStore)
			if got != tc.want {
				t.Errorf("done=%v, want %v", got, tc.want)
			}
		})
	}
}

// TestBackfillExternalIDsUsesBatchFileRead proves the PERF-5 fix: file-level
// PIDs come from ONE GetAllBookFilesCore read per run regardless of how many
// books or pages there are (previously one GetBookFiles point read per book),
// and the mappings it yields are the same ones the N+1 produced.
func TestBackfillExternalIDsUsesBatchFileRead(t *testing.T) {
	setITunesXMLPath(t, "")
	mockStore := NewMockBackfillStore()
	pid1 := "book-pid-1"
	mockStore.books["b1"] = database.Book{ID: "b1", Title: "One", ITunesPersistentID: &pid1}
	mockStore.books["b2"] = database.Book{ID: "b2", Title: "Two"}
	mockStore.books["b3"] = database.Book{ID: "b3", Title: "Three"}
	mockStore.bookFiles = []database.BookFileCore{
		{ID: "f1", BookID: "b2", FilePath: "/lib/two/01.m4b", ITunesPersistentID: "file-pid-2a"},
		{ID: "f2", BookID: "b2", FilePath: "/lib/two/02.m4b", ITunesPersistentID: "file-pid-2b"},
		{ID: "f3", BookID: "b3", FilePath: "/lib/three/01.m4b"}, // no PID → no mapping
		{ID: "f4", BookID: "orphan", FilePath: "/lib/orphan.m4b", ITunesPersistentID: "file-pid-orphan"},
	}

	if err := BackfillExternalIDs(context.Background(), mockStore, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockStore.getAllBookFilesCalls != 1 {
		t.Errorf("GetAllBookFilesCore calls = %d, want exactly 1", mockStore.getAllBookFilesCalls)
	}
	// The book list is one single-snapshot read per run (PERF-5), not offset
	// pages — see TestBackfillExternalIDsSnapshotSwapCannotSkipBooks.
	if mockStore.getAllBooksCalls != 1 {
		t.Errorf("GetAllBooksCore calls = %d, want exactly 1 (one snapshot read)", mockStore.getAllBooksCalls)
	}

	got := map[string]database.ExternalIDMapping{}
	for _, m := range mockStore.externalIDMaps {
		got[m.ExternalID] = m
	}
	if len(got) != 3 {
		t.Fatalf("mappings = %v, want book-pid-1 + file-pid-2a + file-pid-2b", got)
	}
	if m := got["book-pid-1"]; m.BookID != "b1" || m.FilePath != "" {
		t.Errorf("book-level mapping wrong: %+v", m)
	}
	for _, id := range []string{"file-pid-2a", "file-pid-2b"} {
		m, ok := got[id]
		if !ok {
			t.Errorf("missing file-level mapping %s", id)
			continue
		}
		if m.BookID != "b2" || m.FilePath == "" || m.Provenance != "backfill_v4" {
			t.Errorf("file-level mapping %s wrong: %+v", id, m)
		}
	}
	if _, ok := got["file-pid-orphan"]; ok {
		t.Error("a file whose book is not in the library must not yield a mapping")
	}
}

// snapshotSwapBookCount is one more book than the backfill's old 10,000-row
// offset page, so the pre-fix loops needed two pages and the swap between
// them skipped exactly one book.
const snapshotSwapBookCount = 10001

// TestBackfillExternalIDsSnapshotSwapCannotSkipBooks pins the PERF-5 fix for
// BackfillExternalIDs's book pass: with more books than one old offset page
// and the snapshot shifting between pages, the offset loop silently skipped a
// book's mapping and still reported success (and would then set the done
// flag). The single-snapshot read writes a mapping for every book.
func TestBackfillExternalIDsSnapshotSwapCannotSkipBooks(t *testing.T) {
	setITunesXMLPath(t, "")
	mockStore := NewMockBackfillStore()
	mockStore.swapAfterFirstPage = true
	for i := range snapshotSwapBookCount {
		id := fmt.Sprintf("b%05d", i)
		pid := fmt.Sprintf("pid-%05d", i)
		mockStore.books[id] = database.Book{ID: id, Title: id, ITunesPersistentID: &pid}
	}

	if err := BackfillExternalIDs(context.Background(), mockStore, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockStore.paged {
		t.Error("book pass used offset pages — the snapshot-swap skip window is back")
	}
	got := map[string]bool{}
	for _, m := range mockStore.externalIDMaps {
		got[m.ExternalID] = true
	}
	if len(got) != snapshotSwapBookCount {
		t.Fatalf("mappings written for %d books, want %d: a book was never enumerated (silent skip, success reported)", len(got), snapshotSwapBookCount)
	}
}

// TestBackfillITunesTrackPIDsSnapshotSwapCannotSkipBooks pins the same fix
// for BackfillITunesTrackPIDs's PID/title index build (TASK-063). The one
// book whose PID matches the synthetic XML track sits at the position the old
// offset loop skipped after a snapshot swap, so the pre-fix code built an
// index without it and registered zero track PIDs, returning no error.
func TestBackfillITunesTrackPIDsSnapshotSwapCannotSkipBooks(t *testing.T) {
	const targetPID = "0123456789ABCDEF"
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
		<key>1</key>
		<dict>
			<key>Track ID</key><integer>1</integer>
			<key>Persistent ID</key><string>` + targetPID + `</string>
			<key>Name</key><string>Chapter 1</string>
			<key>Album</key><string>Synthetic Album With No Title Match</string>
			<key>Track Number</key><integer>1</integer>
		</dict>
	</dict>
</dict>
</plist>
`
	xmlPath := filepath.Join(t.TempDir(), "synthetic-library.xml")
	if err := os.WriteFile(xmlPath, []byte(xml), 0o600); err != nil {
		t.Fatalf("write synthetic XML: %v", err)
	}
	setITunesXMLPath(t, xmlPath)

	mockStore := NewMockBackfillStore()
	mockStore.swapAfterFirstPage = true
	for i := range snapshotSwapBookCount {
		id := fmt.Sprintf("b%05d", i)
		book := database.Book{ID: id, Title: id}
		if i == snapshotSwapBookCount-1 {
			// The last ID-sorted book: the row the post-swap offset page
			// never reaches.
			pid := targetPID
			book.ITunesPersistentID = &pid
		}
		mockStore.books[id] = book
	}
	targetID := fmt.Sprintf("b%05d", snapshotSwapBookCount-1)

	registered, err := BackfillITunesTrackPIDs(context.Background(), mockStore, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockStore.paged {
		t.Error("index build used offset pages — the snapshot-swap skip window is back")
	}
	if registered != 1 {
		t.Fatalf("registered = %d, want 1: the PID-bearing book was missing from the index (silent skip, no error)", registered)
	}
	if len(mockStore.externalIDMaps) != 1 || mockStore.externalIDMaps[0].BookID != targetID || mockStore.externalIDMaps[0].ExternalID != targetPID {
		t.Fatalf("mappings = %+v, want one %s -> %s", mockStore.externalIDMaps, targetPID, targetID)
	}
}
