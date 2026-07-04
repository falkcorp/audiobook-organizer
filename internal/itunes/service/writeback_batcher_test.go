// file: internal/itunes/service/writeback_batcher_test.go
// version: 1.3.0
// guid: d4e5f6a7-b8c9-0123-def4-56789abcdef0
// last-edited: 2026-07-03

package itunesservice

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// withFakeITLHooks replaces the package-level itunes hooks with
// test doubles and restores them on cleanup. Lets every SafeWriteITL
// test drive the validate + apply paths without needing a real ITL
// fixture on disk — the fixture is fragile and format changes have
// broken it before.
func withFakeITLHooks(t *testing.T, validate func(string) error, apply func(in, out string, ops itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error)) {
	t.Helper()
	prevValidate := itlValidateFn
	prevApply := itlApplyOperationsFn
	itlValidateFn = validate
	// itlApplyOperationsFn is now variadic (TASK-004 added an optional
	// ContractConfig for Force); the batcher never passes one, so the test
	// double ignores the variadic tail and delegates to the simple closure.
	itlApplyOperationsFn = func(in, out string, ops itunes.ITLOperationSet, _ ...itunes.ContractConfig) (*itunes.ITLWriteBackResult, error) {
		return apply(in, out, ops)
	}
	// The full-contract audit hook (step 4b) is neutralized by default —
	// these tests drive validate/apply flow control with placeholder files
	// that no real audit could pass. TestSafeWriteITL_AuditRejection covers
	// the audit path explicitly.
	prevAudit := itlAuditFileFn
	itlAuditFileFn = func(string) error { return nil }
	t.Cleanup(func() {
		itlValidateFn = prevValidate
		itlApplyOperationsFn = prevApply
		itlAuditFileFn = prevAudit
	})
}

// makeITL writes a placeholder "ITL" file for tests. Since our
// ValidateITL hook is mocked, the contents don't need to parse —
// they just need to exist so os.ReadFile succeeds during backup.
func makeITL(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("makeITL: %v", err)
	}
	return p
}

// TestSafeWriteITL_HappyPath covers the full success cycle:
// source validates, backup is written, apply produces a temp,
// temp validates, rename lands, final validates. End state: the
// original is replaced, one backup exists, no temp remains.
func TestSafeWriteITL_HappyPath(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "original-content")

	applyCalls := 0
	withFakeITLHooks(t,
		func(path string) error {
			// Every validate call passes.
			return nil
		},
		func(in, out string, ops itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			applyCalls++
			// Simulate the real function: read in, transform, write out.
			data, err := os.ReadFile(in)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(out, append(data, '!'), 0o644); err != nil {
				return nil, err
			}
			return &itunes.ITLWriteBackResult{UpdatedCount: 1, OutputPath: out}, nil
		},
	)

	err := SafeWriteITL(itlPath, itunes.ITLOperationSet{
		LocationUpdates: []itunes.ITLLocationUpdate{{PersistentID: "aa", NewLocation: "x"}},
	})
	if err != nil {
		t.Fatalf("SafeWriteITL happy path failed: %v", err)
	}
	if applyCalls != 1 {
		t.Errorf("expected 1 apply call, got %d", applyCalls)
	}

	// Original file now has the transform applied.
	out, _ := os.ReadFile(itlPath)
	if string(out) != "original-content!" {
		t.Errorf("final file content = %q, want %q", string(out), "original-content!")
	}

	// Exactly one backup exists.
	backups, _ := filepath.Glob(itlPath + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(backups))
	}
	// Backup contains the pre-write bytes.
	backup, _ := os.ReadFile(backups[0])
	if string(backup) != "original-content" {
		t.Errorf("backup content = %q, want %q", string(backup), "original-content")
	}

	// No .tmp left over.
	if _, err := os.Stat(itlPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("expected .tmp to be cleaned up")
	}
}

// TestSafeWriteITL_AuditRejection: the step-4b full-contract audit of the
// temp file rejects the write and preserves the original — the service
// wrapper's equivalent of the hardened path's re-read validation.
func TestSafeWriteITL_AuditRejection(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "original-content")

	withFakeITLHooks(t,
		func(string) error { return nil },
		func(in, out string, _ itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			data, err := os.ReadFile(in)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(out, append(data, '!'), 0o644); err != nil {
				return nil, err
			}
			return &itunes.ITLWriteBackResult{UpdatedCount: 1, OutputPath: out}, nil
		},
	)
	itlAuditFileFn = func(string) error { return fmt.Errorf("mhoh-format: fake violation") }

	err := SafeWriteITL(itlPath, itunes.ITLOperationSet{
		LocationUpdates: []itunes.ITLLocationUpdate{{PersistentID: "aa", NewLocation: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "contract audit") {
		t.Fatalf("expected contract-audit rejection, got: %v", err)
	}
	// Original untouched, no temp left behind.
	out, _ := os.ReadFile(itlPath)
	if string(out) != "original-content" {
		t.Errorf("original modified after audit rejection: %q", string(out))
	}
	if _, err := os.Stat(itlPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("expected .tmp to be cleaned up after audit rejection")
	}
}

// TestSafeWriteITL_RefusesBrokenSource is the regression test for
// rule #1: never write to an ITL that's already corrupted. The
// validate call for the source path fails, and SafeWriteITL must
// abort without creating a backup or touching the file.
func TestSafeWriteITL_RefusesBrokenSource(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "corrupted")

	withFakeITLHooks(t,
		func(path string) error {
			return errors.New("bad magic bytes")
		},
		func(in, out string, ops itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			t.Fatal("apply must not be called when source validation fails")
			return nil, nil
		},
	)

	err := SafeWriteITL(itlPath, itunes.ITLOperationSet{
		LocationUpdates: []itunes.ITLLocationUpdate{{PersistentID: "aa", NewLocation: "x"}},
	})
	if err == nil {
		t.Fatal("expected error on broken source")
	}
	if !strings.Contains(err.Error(), "source ITL validation failed") {
		t.Errorf("expected source-validation error, got: %v", err)
	}

	// No backup created for a broken source.
	backups, _ := filepath.Glob(itlPath + ".bak-*")
	if len(backups) != 0 {
		t.Errorf("expected 0 backups for broken source, got %d", len(backups))
	}

	// Source is untouched.
	if data, _ := os.ReadFile(itlPath); string(data) != "corrupted" {
		t.Errorf("source modified despite validation failure: %q", string(data))
	}
}

// TestSafeWriteITL_TempValidationFailure verifies that when the
// post-write temp file doesn't validate, we delete the temp and
// abort cleanly with the original intact. This catches the case
// where ApplyITLOperations succeeds byte-wise but produces
// something iTunes can't read.
func TestSafeWriteITL_TempValidationFailure(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "original")

	// Validate passes for the source, fails for the temp.
	validateCalls := 0
	withFakeITLHooks(t,
		func(path string) error {
			validateCalls++
			if strings.HasSuffix(path, ".tmp") {
				return errors.New("temp file malformed")
			}
			return nil
		},
		func(in, out string, ops itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			return &itunes.ITLWriteBackResult{UpdatedCount: 1}, os.WriteFile(out, []byte("garbage"), 0o644)
		},
	)

	err := SafeWriteITL(itlPath, itunes.ITLOperationSet{
		LocationUpdates: []itunes.ITLLocationUpdate{{PersistentID: "aa", NewLocation: "x"}},
	})
	if err == nil {
		t.Fatal("expected error on temp validation failure")
	}
	if !strings.Contains(err.Error(), "validation of temp ITL failed") {
		t.Errorf("expected temp-validation error, got: %v", err)
	}

	// Temp file was cleaned up.
	if _, err := os.Stat(itlPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("expected .tmp to be cleaned up after validation failure")
	}

	// Original still has pre-write content.
	if data, _ := os.ReadFile(itlPath); string(data) != "original" {
		t.Errorf("original modified despite temp validation failure: %q", string(data))
	}
}

// TestSafeWriteITL_PostRenameRestore verifies the restore path:
// temp validates, rename lands, then post-rename validation fails
// (e.g., the filesystem corrupted it during the rename). We must
// restore from the backup we took before the write.
func TestSafeWriteITL_PostRenameRestore(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "original-bytes")

	// Validate passes for source, passes for temp, fails for the
	// final file after rename.
	validateCount := 0
	withFakeITLHooks(t,
		func(path string) error {
			validateCount++
			// Source (call 1) + temp (call 2) pass. The third call is
			// the post-rename validation on itlPath — fail that.
			if validateCount == 3 {
				return errors.New("corrupted after rename")
			}
			return nil
		},
		func(in, out string, ops itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			// Write the new content.
			return &itunes.ITLWriteBackResult{UpdatedCount: 1}, os.WriteFile(out, []byte("new-bytes"), 0o644)
		},
	)

	err := SafeWriteITL(itlPath, itunes.ITLOperationSet{
		LocationUpdates: []itunes.ITLLocationUpdate{{PersistentID: "aa", NewLocation: "x"}},
	})
	if err == nil {
		t.Fatal("expected error on post-rename validation failure")
	}
	if !strings.Contains(err.Error(), "restored from backup") {
		t.Errorf("expected restore message in error, got: %v", err)
	}

	// The final file should contain the ORIGINAL bytes (restored
	// from backup), not the new ones (written and then rolled back).
	data, _ := os.ReadFile(itlPath)
	if string(data) != "original-bytes" {
		t.Errorf("file not restored: got %q, want %q", string(data), "original-bytes")
	}
}

// TestPruneITLBackups verifies backup rotation: given N+1 backup
// files, pruneITLBackups keeps the N newest (by lex sort on the
// timestamp suffix) and removes the oldest.
func TestPruneITLBackups(t *testing.T) {
	dir := t.TempDir()
	itlPath := filepath.Join(dir, "test.itl")
	if err := os.WriteFile(itlPath, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}

	stamps := []string{
		"20260101-000000", "20260102-000000", "20260103-000000",
		"20260104-000000", "20260105-000000", "20260106-000000",
		"20260107-000000", "20260108-000000",
	}
	for _, s := range stamps {
		if err := os.WriteFile(itlPath+".bak-"+s, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := pruneITLBackups(itlPath, 5); err != nil {
		t.Fatalf("pruneITLBackups: %v", err)
	}

	remaining, _ := filepath.Glob(itlPath + ".bak-*")
	if len(remaining) != 5 {
		t.Fatalf("expected 5 remaining, got %d: %v", len(remaining), remaining)
	}

	survivors := map[string]bool{}
	for _, p := range remaining {
		survivors[filepath.Base(p)] = true
	}
	for _, want := range []string{
		"test.itl.bak-20260104-000000",
		"test.itl.bak-20260105-000000",
		"test.itl.bak-20260106-000000",
		"test.itl.bak-20260107-000000",
		"test.itl.bak-20260108-000000",
	} {
		if !survivors[want] {
			t.Errorf("expected survivor %s, not found", want)
		}
	}
	for _, gone := range []string{
		"test.itl.bak-20260101-000000",
		"test.itl.bak-20260102-000000",
		"test.itl.bak-20260103-000000",
	} {
		if survivors[gone] {
			t.Errorf("expected %s to be pruned, still present", gone)
		}
	}
}

// TestPruneITLBackups_KeepZero is a no-op by contract: passing
// keep=0 means "don't prune". Callers that want to delete
// everything pass a very large keep and use Remove directly.
func TestPruneITLBackups_KeepZero(t *testing.T) {
	dir := t.TempDir()
	itlPath := filepath.Join(dir, "test.itl")
	_ = os.WriteFile(itlPath+".bak-20260101-000000", []byte("x"), 0o644)
	if err := pruneITLBackups(itlPath, 0); err != nil {
		t.Fatalf("pruneITLBackups: %v", err)
	}
	if _, err := os.Stat(itlPath + ".bak-20260101-000000"); err != nil {
		t.Error("backup was removed despite keep=0")
	}
}

// TestFlush_RefusesOverCap proves the safety circuit-breaker fires
// when pendingRemoves exceeds MaxRemovesPerFlush. The flush MUST
// drop the entire batch (no partial writes, no SafeWriteITL call)
// and log loudly. Regression guard for the May-2026 incident where
// a buggy cleanup-orphans call enqueued ~90 K removes and wiped the
// user's iTunes library.
func TestFlush_RefusesOverCap(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "x")

	applyCalls := 0
	withFakeITLHooks(t,
		func(string) error { return nil },
		func(in, out string, _ itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			applyCalls++
			data, _ := os.ReadFile(in)
			_ = os.WriteFile(out, data, 0o644)
			return &itunes.ITLWriteBackResult{UpdatedCount: 1, OutputPath: out}, nil
		},
	)

	b := &WriteBackBatcher{
		pendingBooks:        map[string]bool{},
		pendingRemoves:      map[string]bool{},
		autoWriteBack:       true,
		itlWriteBackEnabled: true,
		libraryWritePath:    itlPath,
	}
	for i := 0; i < MaxRemovesPerFlush+1; i++ {
		b.pendingRemoves[strings.Repeat("a", 16-len(itoaSafe(i)))+itoaSafe(i)] = true
	}

	b.flush()

	if applyCalls != 0 {
		t.Errorf("flush over-cap: expected 0 SafeWriteITL apply calls, got %d", applyCalls)
	}
}

// itoaSafe is a tiny helper to build distinct PIDs without pulling
// strconv into the test file's import set; keeps the regression
// test free of unrelated dependencies.
func itoaSafe(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

// TestFlush_DryRunSkipsWrite verifies ITUNES_WRITEBACK_DRYRUN=true
// short-circuits before SafeWriteITL. Critical: in dry-run mode no
// file on disk may change.
func TestFlush_DryRunSkipsWrite(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "untouched")

	applyCalls := 0
	withFakeITLHooks(t,
		func(string) error { return nil },
		func(in, out string, _ itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			applyCalls++
			data, _ := os.ReadFile(in)
			_ = os.WriteFile(out, append(data, '!'), 0o644)
			return &itunes.ITLWriteBackResult{UpdatedCount: 1, OutputPath: out}, nil
		},
	)

	t.Setenv("ITUNES_WRITEBACK_DRYRUN", "true")

	b := &WriteBackBatcher{
		pendingBooks:        map[string]bool{},
		pendingRemoves:      map[string]bool{"abcdef0123456789": true},
		autoWriteBack:       true,
		itlWriteBackEnabled: true,
		libraryWritePath:    itlPath,
	}
	b.flush()

	if applyCalls != 0 {
		t.Errorf("dry-run: expected 0 SafeWriteITL apply calls, got %d", applyCalls)
	}
	got, _ := os.ReadFile(itlPath)
	if string(got) != "untouched" {
		t.Errorf("dry-run modified the ITL file: got %q want %q", string(got), "untouched")
	}
}

// ---------------------------------------------------------------------------
// Diff-before-write tests (T008 / HIGH-3)
// ---------------------------------------------------------------------------

// withFakeParseITLHook replaces the parseITLFn package-level hook with a
// test double that returns the provided library and restores the original on
// cleanup. Analogous to withFakeITLHooks.
func withFakeParseITLHook(t *testing.T, lib *itunes.ITLLibrary, err error) {
	t.Helper()
	prev := parseITLFn
	parseITLFn = func(_ string) (*itunes.ITLLibrary, error) { return lib, err }
	t.Cleanup(func() { parseITLFn = prev })
}

// makePIDBytes builds an [8]byte PID from a 16-char hex string, stored in
// the BE (MSB-first) order that parseMithLE writes into ITLTrack.PersistentID.
func makePIDBytes(hexStr string) [8]byte {
	var pid [8]byte
	b, err := hex.DecodeString(hexStr)
	if err != nil || len(b) != 8 {
		panic("makePIDBytes: invalid hex " + hexStr)
	}
	copy(pid[:], b)
	return pid
}

// TestFlush_NoChangeProducesZeroMetadataUpdates is the primary T008 regression
// guard: when the ITL library already contains identical Name/Album/Artist/
// Genre/Location values, flush must NOT emit any ITLMetadataUpdate entries and
// SafeWriteITL must NOT be called. Without the diff-before-write fix this test
// would have witnessed ~90K unnecessary mhoh writes every sync.
func TestFlush_NoChangeProducesZeroMetadataUpdates(t *testing.T) {
	const pid = "aabbccdd11223344"
	const bookID = "book-no-change"

	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "x")

	// Build a fake ITL library whose single track already matches
	// what the DB wants to write. The diff should see no change and
	// produce zero updates.
	fakeLib := &itunes.ITLLibrary{
		Tracks: []itunes.ITLTrack{
			{
				PersistentID: makePIDBytes(pid),
				Name:         "Chapter 1",
				Album:        "Great Book",
				Artist:       "Jane Author",
				Genre:        "Audiobook",
				Location:     `C:\Books\Great Book\Chapter 1.m4b`,
				DateAdded:    time.Now(),
			},
		},
	}
	withFakeParseITLHook(t, fakeLib, nil)

	applyCalls := 0
	withFakeITLHooks(t,
		func(string) error { return nil },
		func(in, out string, _ itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			applyCalls++
			data, _ := os.ReadFile(in)
			_ = os.WriteFile(out, data, 0o644)
			return &itunes.ITLWriteBackResult{UpdatedCount: 0, OutputPath: out}, nil
		},
	)

	isPrimary := true
	title := "Great Book"
	authorID := 42
	authorName := "Jane Author"
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{
				ID:               bookID,
				Title:            title,
				AuthorID:         &authorID,
				IsPrimaryVersion: &isPrimary,
			}, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{Name: authorName}, nil
		},
		GetBookFilesFunc: func(_ string) ([]database.BookFile, error) {
			return []database.BookFile{
				{
					ITunesPersistentID: pid,
					Title:              "Chapter 1",
					ITunesPath:         `C:\Books\Great Book\Chapter 1.m4b`,
				},
			}, nil
		},
	}

	b := &WriteBackBatcher{
		pendingBooks:        map[string]bool{bookID: true},
		pendingRemoves:      map[string]bool{},
		autoWriteBack:       true,
		itlWriteBackEnabled: true,
		libraryWritePath:    itlPath,
		store:               store,
	}
	b.flush()

	if applyCalls != 0 {
		t.Errorf("no-change: expected 0 apply calls (no metadata changes), got %d", applyCalls)
	}
}

// TestFlush_SingleFieldChangeProducesOneUpdate verifies that when exactly one
// field (Album) differs between the DB and the parsed library, flush emits
// exactly one ITLMetadataUpdate and SafeWriteITL is called once.
func TestFlush_SingleFieldChangeProducesOneUpdate(t *testing.T) {
	const pid = "aabbccdd11223344"
	const bookID = "book-single-change"

	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "x")

	// Library has stale album name; DB wants "Great Book (New Edition)".
	fakeLib := &itunes.ITLLibrary{
		Tracks: []itunes.ITLTrack{
			{
				PersistentID: makePIDBytes(pid),
				Name:         "Chapter 1",
				Album:        "Great Book", // stale
				Artist:       "Jane Author",
				Genre:        "Audiobook",
				Location:     `C:\Books\Great Book\Chapter 1.m4b`,
				DateAdded:    time.Now(),
			},
		},
	}
	withFakeParseITLHook(t, fakeLib, nil)

	var capturedOps itunes.ITLOperationSet
	applyCalls := 0
	withFakeITLHooks(t,
		func(string) error { return nil },
		func(in, out string, ops itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			applyCalls++
			capturedOps = ops
			data, _ := os.ReadFile(in)
			_ = os.WriteFile(out, data, 0o644)
			return &itunes.ITLWriteBackResult{UpdatedCount: 1, OutputPath: out}, nil
		},
	)

	isPrimary := true
	updatedTitle := "Great Book (New Edition)"
	authorID := 42
	authorName := "Jane Author"
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{
				ID:               bookID,
				Title:            updatedTitle, // DB has new title/album
				AuthorID:         &authorID,
				IsPrimaryVersion: &isPrimary,
			}, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{Name: authorName}, nil
		},
		GetBookFilesFunc: func(_ string) ([]database.BookFile, error) {
			return []database.BookFile{
				{
					ITunesPersistentID: pid,
					Title:              "Chapter 1",
					ITunesPath:         `C:\Books\Great Book\Chapter 1.m4b`,
				},
			}, nil
		},
	}

	b := &WriteBackBatcher{
		pendingBooks:        map[string]bool{bookID: true},
		pendingRemoves:      map[string]bool{},
		autoWriteBack:       true,
		itlWriteBackEnabled: true,
		libraryWritePath:    itlPath,
		store:               store,
	}
	b.flush()

	if applyCalls != 1 {
		t.Errorf("single-change: expected 1 apply call, got %d", applyCalls)
	}
	if len(capturedOps.MetadataUpdates) != 1 {
		t.Fatalf("single-change: expected 1 MetadataUpdate, got %d", len(capturedOps.MetadataUpdates))
	}
	if capturedOps.MetadataUpdates[0].Album != updatedTitle {
		t.Errorf("single-change: expected Album=%q, got %q", updatedTitle, capturedOps.MetadataUpdates[0].Album)
	}
	if strings.ToLower(capturedOps.MetadataUpdates[0].PersistentID) != pid {
		t.Errorf("single-change: expected PID=%q, got %q", pid, capturedOps.MetadataUpdates[0].PersistentID)
	}
}
