// file: internal/audiobooks/purge_waiting_records_test.go
// version: 1.0.0
// guid: c27a90d3-61e4-4b58-8f0a-d3e19b6a7c42
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// journalGone journals a removal of a file that is not there, in a folder that
// does not exist (waiting for it), the way a pass leaves one behind.
func journalGone(t *testing.T, svc *AudiobookService, bookID, folder, name string) string {
	t.Helper()
	p := filepath.Join(folder, name)
	require.NoError(t, svc.journalPendingFile(bookID, purgeFileVerdict{remove: p, id: fileIdentity{Known: true, Size: 1, MtimeNS: 1}}))
	return p
}

func journalRecord(t *testing.T, store *database.PebbleStore, bookID string) pendingFileRecord {
	t.Helper()
	raw, err := store.GetRaw(pendingFilePrefix + bookID)
	require.NoError(t, err)
	require.NotNil(t, raw, "no journal record for %s", bookID)
	var rec pendingFileRecord
	require.NoError(t, json.Unmarshal(raw, &rec))
	return rec
}

func journalKeys(t *testing.T, store *database.PebbleStore) []string {
	t.Helper()
	var keys []string
	for _, p := range pendingJournal(t, store) {
		keys = append(keys, p.Key)
	}
	return keys
}

// SH-B.1. A pass that finds the file not there and its folder missing or empty
// marks the record, and the journal counts it APART from the removals a purge
// will carry out: "Finish removing" must not promise to remove what no purge can.
func TestReplay_FolderMissingRecordsAreMarkedAndCountedApartFromPendingOnes(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	lib := filepath.Join(base, "lib")
	journalGone(t, svc, "A", filepath.Join(lib, "Wayne"), "hush.m4b") // folder missing
	empty := filepath.Join(lib, "Arkham")
	require.NoError(t, os.MkdirAll(empty, 0o755))
	journalGone(t, svc, "B", empty, "knightfall.m4b") // folder there, but empty
	present := filepath.Join(lib, "Gotham", "year-one.m4b")
	writeFile(t, present)
	require.NoError(t, svc.journalPendingFile("C", purgeFileVerdict{remove: present, id: identityAt(t, present)}))

	c, err := svc.PendingFileRemovals()
	require.NoError(t, err)
	assert.Equal(t, PendingFileCounts{Pending: 3}, c, "before any pass looked at them every record is a pending removal")

	// A lookup failure keeps C pending; A and B cannot be decided for want of a folder.
	hooked := &hookedPathStore{PebbleStore: store, allRowsErr: func(string) error { return errors.New("batcave offline") }}
	svc = NewAudiobookService(hooked)
	_, errs := svc.replayPendingFileRemovals(context.Background())
	require.NotEmpty(t, errs)

	c, err = svc.PendingFileRemovals()
	require.NoError(t, err)
	assert.Equal(t, PendingFileCounts{Pending: 1, WaitingForFolder: 2}, c)
	assert.Equal(t, 3, c.Total())
	assert.True(t, journalRecord(t, store, "A").WaitingForFolder)
	assert.True(t, journalRecord(t, store, "B").WaitingForFolder)
	assert.False(t, journalRecord(t, store, "C").WaitingForFolder)
	assert.True(t, fileExists(present), "nothing was removed")
}

// A record that waited for its folder and is then retried for another reason
// (the folder is back and the store cannot answer) is no longer waiting.
func TestReplay_RecordThatIsRetriedForAnotherReasonLosesItsWaitingMark(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	require.NoError(t, svc.journalPendingFile("T", purgeFileVerdict{remove: x, id: identityAt(t, x)}))
	require.NoError(t, svc.setWaitingForFolder(&pendingFile{bookIDs: []string{"T"}}, true))
	require.True(t, journalRecord(t, store, "T").WaitingForFolder)

	hooked := &hookedPathStore{PebbleStore: store, allRowsErr: func(string) error { return errors.New("batcave offline") }}
	svc = NewAudiobookService(hooked)
	_, errs := svc.replayPendingFileRemovals(context.Background())
	require.Len(t, errs, 1, "%v", errs)
	assert.False(t, journalRecord(t, store, "T").WaitingForFolder)
	c, err := svc.PendingFileRemovals()
	require.NoError(t, err)
	assert.Equal(t, PendingFileCounts{Pending: 1}, c)
}

// A preview counts the two apart, and says so in its summary.
func TestRunPurge_PreviewCountsWaitingRecordsApartFromPendingOnes(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	journalGone(t, svc, "A", filepath.Join(base, "lib", "Wayne"), "hush.m4b")
	require.NoError(t, svc.setWaitingForFolder(&pendingFile{bookIDs: []string{"A"}}, true))
	present := filepath.Join(base, "lib", "Gotham", "year-one.m4b")
	writeFile(t, present)
	require.NoError(t, svc.journalPendingFile("C", purgeFileVerdict{remove: present, id: identityAt(t, present)}))
	_ = store

	pv, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, pv.FilesPendingFromEarlierPass, "%+v", pv)
	assert.Equal(t, 1, pv.FilesWaitingForFolder, "%+v", pv)
	assert.Contains(t, pv.Summary(), "waiting for a folder that is missing or empty")
}

// SH-B.3. Forgetting deletes ONLY the records marked as waiting for a folder:
// not a record a pass will still carry out, no file, no book row, no book_file
// row. It writes an audit row naming the count, and a second call finds nothing.
// One of the records is the crash window: the file was unlinked and the process
// died before its record was forgotten, so the folder is left empty.
func TestForgetWaitingFileRecords_DeletesOnlyWaitingRecordsAndAudits(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	rec := &fakeRecorder{}
	svc.auditOverride = rec
	lib := filepath.Join(base, "lib")
	liveFile := filepath.Join(lib, "Gotham", "dark-victory.m4b")
	liveBookWithFile(t, store, "mounted-witness", liveFile)

	journalGone(t, svc, "A", filepath.Join(lib, "Wayne"), "hush.m4b") // its folder is gone
	crash := filepath.Join(lib, "Arkham")                             // unlinked, record outlived the file
	require.NoError(t, os.MkdirAll(crash, 0o755))
	crashFile := filepath.Join(crash, "knightfall.m4b")
	writeFile(t, crashFile)
	require.NoError(t, svc.journalPendingFile("B", purgeFileVerdict{remove: crashFile, id: identityAt(t, crashFile)}))
	require.NoError(t, os.Remove(crashFile))
	stays := filepath.Join(lib, "Gotham", "year-one.m4b")
	writeFile(t, stays)
	require.NoError(t, svc.journalPendingFile("C", purgeFileVerdict{remove: stays, id: identityAt(t, stays)}))

	hooked := &hookedPathStore{PebbleStore: store, allRowsErr: func(string) error { return errors.New("batcave offline") }}
	svc = NewAudiobookService(hooked)
	svc.auditOverride = rec
	_, _ = svc.replayPendingFileRemovals(context.Background())
	c, err := svc.PendingFileRemovals()
	require.NoError(t, err)
	require.Equal(t, PendingFileCounts{Pending: 1, WaitingForFolder: 2}, c)
	// A mark can outlive the folder's absence: the folder came back and no pass
	// has looked since. Forgetting then deletes the record and leaves the file
	// where it is; it never removes anything.
	back := filepath.Join(lib, "Gotham", "no-mans-land.m4b")
	writeFile(t, back)
	require.NoError(t, svc.journalPendingFile("D", purgeFileVerdict{remove: back, id: identityAt(t, back)}))
	require.NoError(t, svc.setWaitingForFolder(&pendingFile{bookIDs: []string{"D"}}, true))
	bookBefore, err := store.GetBookByID("mounted-witness")
	require.NoError(t, err)
	require.NotNil(t, bookBefore)
	filesBefore, err := store.GetBookFiles("mounted-witness")
	require.NoError(t, err)

	res, err := svc.ForgetWaitingFileRecords(context.Background(), "owner")
	require.NoError(t, err)
	assert.Equal(t, 3, res.Records)
	assert.Equal(t, 3, res.Paths)
	assert.Empty(t, res.Warnings)
	assert.Equal(t, []string{pendingFilePrefix + "C"}, journalKeys(t, store), "only the record a purge will still carry out is left")
	assert.True(t, fileExists(stays), "no file is touched")
	assert.True(t, fileExists(back), "a marked record whose file is back does not cost the file")
	assert.True(t, fileExists(liveFile))
	bookAfter, err := store.GetBookByID("mounted-witness")
	require.NoError(t, err)
	assert.Equal(t, bookBefore, bookAfter, "no book row is touched")
	filesAfter, err := store.GetBookFiles("mounted-witness")
	require.NoError(t, err)
	assert.Equal(t, filesBefore, filesAfter, "no book_file row is touched")

	require.Len(t, rec.entries, 1)
	e := rec.entries[0]
	assert.Equal(t, activity.TypeTrashForgetPendingFileRecords, e.Type)
	assert.Equal(t, "audit", e.Tier)
	assert.Contains(t, e.Summary, "forgot 3 pending file removal record(s) for 3 file(s)")
	assert.Equal(t, "owner", e.Details["actor"])
	assert.Equal(t, 3, e.Details["records_forgotten"])
	assert.ElementsMatch(t, []string{filepath.Join(lib, "Wayne", "hush.m4b"), crashFile, back}, e.Details["paths"])

	again, err := svc.ForgetWaitingFileRecords(context.Background(), "owner")
	require.NoError(t, err)
	assert.Zero(t, again.Records)
	assert.Len(t, rec.entries, 1, "nothing forgotten, nothing recorded")
	assert.Equal(t, []string{pendingFilePrefix + "C"}, journalKeys(t, store))
}

// A record that is not marked stays even when a marked one names the same path:
// a purge will still try it.
func TestForgetWaitingFileRecords_AnUnmarkedRecordOfTheSamePathStays(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	folder := filepath.Join(base, "lib", "Wayne")
	journalGone(t, svc, "T", folder, "hush.m4b")
	journalGone(t, svc, "U", folder, "hush.m4b")
	require.NoError(t, svc.setWaitingForFolder(&pendingFile{bookIDs: []string{"T"}}, true))

	c, err := svc.PendingFileRemovals()
	require.NoError(t, err)
	assert.Equal(t, PendingFileCounts{Pending: 1}, c, "the path still has a record a purge will try")

	res, err := svc.ForgetWaitingFileRecords(context.Background(), "owner")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Records)
	assert.Equal(t, []string{pendingFilePrefix + "U"}, journalKeys(t, store))
}

// Without an activity log the action refuses and deletes nothing.
func TestForgetWaitingFileRecords_RefusesWithoutAnAuditLog(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	journalGone(t, svc, "T", filepath.Join(base, "lib", "Wayne"), "hush.m4b")
	require.NoError(t, svc.setWaitingForFolder(&pendingFile{bookIDs: []string{"T"}}, true))
	svc.auditOverride, svc.activityService = nil, nil

	_, err := svc.ForgetWaitingFileRecords(context.Background(), "owner")
	require.ErrorIs(t, err, ErrAuditUnavailable)
	assert.Len(t, journalKeys(t, store), 1)
}

// An audit row that cannot be written is reported, after the records are gone.
func TestForgetWaitingFileRecords_ReportsAnAuditRowThatCouldNotBeWritten(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{err: errors.New("simulated journal failure")}
	journalGone(t, svc, "T", filepath.Join(base, "lib", "Wayne"), "hush.m4b")
	require.NoError(t, svc.setWaitingForFolder(&pendingFile{bookIDs: []string{"T"}}, true))

	res, err := svc.ForgetWaitingFileRecords(context.Background(), "owner")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Records)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "activity log row was not written")
	assert.Empty(t, journalKeys(t, store))
}
