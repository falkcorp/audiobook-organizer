// file: internal/itunes/service/importer_integrity_test.go
// version: 1.0.0
// guid: 9c1d7e2f-3a4b-4c5d-8e6f-0a1b2c3d4e5f
// last-edited: 2026-07-17
//
// Integrity-finding regression tests from the 2026-07-17 multi-discipline
// review:
//   - DL-5: deferred ITL location-fix application must mark applied ONLY
//     the rows actually written to the ITL, and a RenameITLFile failure
//     must leave every row pending.
//   - C-6: a blocked-hash soft-delete whose UpdateBook write fails must
//     not be counted (or logged) as a successful deletion.
//   - C-7: a failed UpdateBook after a multi-file organize must NOT rename
//     the target directory back (the per-file rows are already committed);
//     it must fail loudly with a reconcile hint listing the file IDs.

package itunesservice

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// DL-5 — applyDeferredITunesUpdates applied-vs-pending bookkeeping
// ---------------------------------------------------------------------------

// w8DeferredFixture builds an Importer wired with fake ITL write/rename
// seams and a mock store answering GetPendingDeferredITunesUpdates.
// (w8 prefix: task-unique helper name per parallel-worker discipline.)
func w8DeferredFixture(t *testing.T, m *dbmocks.MockStore, updateFn func(string, string, []itunes.ITLLocationUpdate) (*itunes.ITLWriteBackResult, error), renameFn func(string, string) error) *Importer {
	t.Helper()
	return &Importer{
		store: m,
		cfg: Config{
			ITLWriteBackEnabled: true,
			LibraryWritePath:    filepath.Join(t.TempDir(), "iTunes Library.itl"),
		},
		itlUpdateFn: updateFn,
		itlRenameFn: renameFn,
	}
}

// w8PendingRows: row 1 normalizes and IS written; row 2 normalizes but its
// PID never matches a location block in the ITL; row 3 fails normalization
// (relative path — CRIT-2 unmappable).
func w8PendingRows() []database.DeferredITunesUpdate {
	return []database.DeferredITunesUpdate{
		{ID: 1, BookID: "b1", PersistentID: "AABBCCDDEEFF0011", OldPath: `W:\Old\One.m4b`, NewPath: `W:\Audiobooks\One.m4b`, UpdateType: "location"},
		{ID: 2, BookID: "b2", PersistentID: "1122334455667788", OldPath: `W:\Old\Two.m4b`, NewPath: `W:\Audiobooks\Two.m4b`, UpdateType: "location"},
		{ID: 3, BookID: "b3", PersistentID: "99AABBCCDDEEFF00", OldPath: `W:\Old\Three.m4b`, NewPath: `relative-not-absolute.m4b`, UpdateType: "location"},
	}
}

func TestApplyDeferredITunesUpdates_MarksOnlyRowsActuallyWritten(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetPendingDeferredITunesUpdates().Return(w8PendingRows(), nil)
	// ONLY row 1 (the PID actually rewritten in the ITL) may be marked
	// applied. Rows 2 (PID not in ITL) and 3 (unmappable) must stay
	// pending — mockery fails the test on any unexpected Mark call.
	m.EXPECT().MarkDeferredITunesUpdateApplied(1).Return(nil).Once()

	var gotUpdates []itunes.ITLLocationUpdate
	updateFn := func(_, _ string, updates []itunes.ITLLocationUpdate) (*itunes.ITLWriteBackResult, error) {
		gotUpdates = updates
		return &itunes.ITLWriteBackResult{
			UpdatedCount:         1,
			UpdatedPersistentIDs: []string{"aabbccddeeff0011"},
		}, nil
	}
	renamed := false
	renameFn := func(_, _ string) error { renamed = true; return nil }

	imp := w8DeferredFixture(t, m, updateFn, renameFn)
	imp.applyDeferredITunesUpdates(logger.New("test-dl5-subset"))

	require.True(t, renamed, "successful write must be renamed into place")
	// The unmappable row 3 must never reach the ITL writer (CRIT-2).
	require.Len(t, gotUpdates, 2, "only normalizable rows may be sent to the ITL writer")
	pids := []string{gotUpdates[0].PersistentID, gotUpdates[1].PersistentID}
	assert.ElementsMatch(t, []string{"AABBCCDDEEFF0011", "1122334455667788"}, pids)
}

func TestApplyDeferredITunesUpdates_RenameFailureKeepsAllRowsPending(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetPendingDeferredITunesUpdates().Return(w8PendingRows(), nil)
	// No MarkDeferredITunesUpdateApplied expectation: ANY mark call after a
	// rename failure is the DL-5 divergence bug and fails the test.

	updateFn := func(_, _ string, updates []itunes.ITLLocationUpdate) (*itunes.ITLWriteBackResult, error) {
		return &itunes.ITLWriteBackResult{
			UpdatedCount:         2,
			UpdatedPersistentIDs: []string{"aabbccddeeff0011", "1122334455667788"},
		}, nil
	}
	renameFn := func(_, _ string) error { return errors.New("target locked by iTunes") }

	imp := w8DeferredFixture(t, m, updateFn, renameFn)
	imp.applyDeferredITunesUpdates(logger.New("test-dl5-rename"))
}

func TestApplyDeferredITunesUpdates_WriteErrorKeepsAllRowsPending(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetPendingDeferredITunesUpdates().Return(w8PendingRows(), nil)
	// No mark calls allowed on a write error either.

	updateFn := func(_, _ string, _ []itunes.ITLLocationUpdate) (*itunes.ITLWriteBackResult, error) {
		return nil, errors.New("ITL corrupt")
	}
	renameFn := func(_, _ string) error {
		t.Fatal("rename must not be attempted after a write error")
		return nil
	}

	imp := w8DeferredFixture(t, m, updateFn, renameFn)
	imp.applyDeferredITunesUpdates(logger.New("test-dl5-writeerr"))
}

func TestApplyDeferredITunesUpdates_MarkErrorIsLoggedNotFatal(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetPendingDeferredITunesUpdates().Return(w8PendingRows()[:1], nil)
	m.EXPECT().MarkDeferredITunesUpdateApplied(1).Return(errors.New("pebble closed")).Once()

	updateFn := func(_, _ string, _ []itunes.ITLLocationUpdate) (*itunes.ITLWriteBackResult, error) {
		return &itunes.ITLWriteBackResult{
			UpdatedCount:         1,
			UpdatedPersistentIDs: []string{"aabbccddeeff0011"},
		}, nil
	}
	renameFn := func(_, _ string) error { return nil }

	imp := w8DeferredFixture(t, m, updateFn, renameFn)
	// Must not panic; the row simply stays pending and re-applies next sync.
	imp.applyDeferredITunesUpdates(logger.New("test-dl5-markerr"))
}

// ---------------------------------------------------------------------------
// C-6 — blocked-hash soft-delete must check UpdateBook's returns
// ---------------------------------------------------------------------------

func TestSoftDeleteBlockedBook_WriteLands_ReturnsTrue(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpdateBook("blk-1", mock.Anything).Run(func(_ string, book *database.Book) {
		require.NotNil(t, book.MarkedForDeletion)
		assert.True(t, *book.MarkedForDeletion)
		assert.NotNil(t, book.MarkedForDeletionAt)
	}).Return(&database.Book{}, nil).Once()

	imp := &Importer{store: m}
	book := &database.Book{ID: "blk-1", Title: "Blocked Book"}
	assert.True(t, imp.softDeleteBlockedBook(book, logger.New("test-c6-ok")))
}

func TestSoftDeleteBlockedBook_WriteFails_ReturnsFalse(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpdateBook("blk-2", mock.Anything).Return(nil, errors.New("write failed")).Once()

	imp := &Importer{store: m}
	book := &database.Book{ID: "blk-2", Title: "Blocked Book Two"}
	// C-6: the caller must NOT count this as a successful soft-delete.
	assert.False(t, imp.softDeleteBlockedBook(book, logger.New("test-c6-fail")))
}

// ---------------------------------------------------------------------------
// C-7 — multi-file organize + failed UpdateBook → loud reconcile, no dir rename
// ---------------------------------------------------------------------------

// w8MultiFileOrganizer is a BookOrganizer double whose directory organize
// succeeds with a fixed path map. OrganizeBook must never be called for a
// multi-file book.
type w8MultiFileOrganizer struct {
	mu       sync.Mutex
	dirCalls int
}

func (o *w8MultiFileOrganizer) OrganizeBook(book *database.Book) (string, string, error) {
	return "", "", fmt.Errorf("OrganizeBook must not be called for a multi-file book")
}

func (o *w8MultiFileOrganizer) OrganizeBookDirectory(book *database.Book, segmentPaths []string) (string, map[string]string, error) {
	o.mu.Lock()
	o.dirCalls++
	o.mu.Unlock()
	pathMap := make(map[string]string, len(segmentPaths))
	for _, p := range segmentPaths {
		pathMap[p] = "/organized/Multi Book/" + filepath.Base(p)
	}
	return "/organized/Multi Book", pathMap, nil
}

var _ BookOrganizer = (*w8MultiFileOrganizer)(nil)

func TestOrganizeImportedBooks_MultiFileUpdateBookFailure_ReconcileHintNoDirRollback(t *testing.T) {
	imported := "imported"
	src := "/mnt/itunes/Library.xml"
	book := database.Book{
		ID:                 "mfb-1",
		Title:              "Multi Book",
		FilePath:           "/old/common",
		LibraryState:       &imported,
		ITunesImportSource: &src,
	}
	files := []database.BookFile{
		{ID: "bf-1", BookID: "mfb-1", FilePath: "/old/common/a.m4b"},
		{ID: "bf-2", BookID: "mfb-1", FilePath: "/old/common/b.m4b"},
	}

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetAllBooksCore(0, 0).Return([]database.BookCore{book.Core()}, nil)
	b := book
	m.EXPECT().GetBookByID("mfb-1").Return(&b, nil)
	m.EXPECT().GetBookFiles("mfb-1").Return(files, nil)
	// organizeMultiFileBook commits the per-file rows BEFORE the Book write.
	m.EXPECT().UpdateBookFile("bf-1", mock.Anything).Return(nil).Once()
	m.EXPECT().UpdateBookFile("bf-2", mock.Anything).Return(nil).Once()
	// The Book write fails → the C-7 branch under test.
	m.EXPECT().UpdateBook("mfb-1", mock.Anything).Return(nil, errors.New("pebble write stall")).Once()

	org := &w8MultiFileOrganizer{}
	imp := &Importer{
		store:                       m,
		organizerFactory:            func() BookOrganizer { return org },
		organizeConcurrencyOverride: 1,
	}
	status := &itunesImportStatus{}
	imp.organizeImportedBooks(context.Background(), status, logger.New("test-c7"))

	require.Equal(t, 1, org.dirCalls, "multi-file book must route through OrganizeBookDirectory")
	require.Equal(t, 1, status.Failed, "the UpdateBook failure must be recorded as a failure")
	require.NotEmpty(t, status.Errors)
	msg := status.Errors[0]
	// The reconcile hint must name every committed book_file row and both
	// path sides of the divergence.
	assert.Contains(t, msg, "reconcile", "must carry a reconcile hint")
	assert.Contains(t, msg, "bf-1")
	assert.Contains(t, msg, "bf-2")
	assert.Contains(t, msg, "/old/common", "must name the stale book-row path")
	assert.Contains(t, msg, "/organized/Multi Book", "must name the committed organized path")
	assert.True(t, strings.Contains(msg, "CRITICAL"), "must be loud: %s", msg)
}
