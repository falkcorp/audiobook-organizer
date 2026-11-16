// file: internal/plugins/maintenance/kinds_t6_g4_spy_test.go
// version: 1.0.4
// guid: 0976ab2f-77df-47d7-b719-f47dbda178bb
// last-edited: 2026-11-15

package maintenance

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// spyT6G4Writer records the name of every write a core makes through it and then
// performs the write on the writer it wraps. recT6G4Writer records and does
// nothing, so it proves which calls a core makes against a zero store; this one
// runs them against a real in-memory store, so a test can assert both the
// recorded resources and the rows that changed. Its methods are generated from
// recT6G4Writer's: a method added to t6g4Writer must be added to both (the build
// fails until it is).
type spyT6G4Writer struct {
	inner t6g4Writer
	rec   *t6g4Calls
}

func (w spyT6G4Writer) AppendFileEvent(a0 database.FileEvent) error {
	w.rec.add("AppendFileEvent")
	return w.inner.AppendFileEvent(a0)
}

func (w spyT6G4Writer) CarryOverFingerprintWindows(a0 []database.FingerprintWindowRef, a1 database.FingerprintWindowRef) (int, error) {
	w.rec.add("CarryOverFingerprintWindows")
	return w.inner.CarryOverFingerprintWindows(a0, a1)
}

func (w spyT6G4Writer) ClearBookFileIntro(a0 string, a1 string, a2 string) (*database.BookFile, bool, error) {
	w.rec.add("ClearBookFileIntro")
	return w.inner.ClearBookFileIntro(a0, a1, a2)
}

func (w spyT6G4Writer) ClearIntroTimeouts(a0 string, a1 string, a2 string) (bool, error) {
	w.rec.add("ClearIntroTimeouts")
	return w.inner.ClearIntroTimeouts(a0, a1, a2)
}

func (w spyT6G4Writer) CreateBookFile(a0 *database.BookFile) error {
	w.rec.add("CreateBookFile")
	return w.inner.CreateBookFile(a0)
}

func (w spyT6G4Writer) CreateOperationChange(a0 *database.OperationChange) error {
	w.rec.add("CreateOperationChange")
	return w.inner.CreateOperationChange(a0)
}

func (w spyT6G4Writer) DedupTriageApply(a0 context.Context) (*TriageReport, error) {
	w.rec.add("DedupTriageApply")
	return w.inner.DedupTriageApply(a0)
}

func (w spyT6G4Writer) DeleteBookFilesByIDs(a0 []database.BookFileRef) ([]database.BookFile, error) {
	w.rec.add("DeleteBookFilesByIDs")
	return w.inner.DeleteBookFilesByIDs(a0)
}

func (w spyT6G4Writer) MarkOperationChangesReverted(a0 string, a1 []string) error {
	w.rec.add("MarkOperationChangesReverted")
	return w.inner.MarkOperationChangesReverted(a0, a1)
}

func (w spyT6G4Writer) ModifyBook(a0 string, a1 func(*database.Book) error) (*database.Book, error) {
	w.rec.add("ModifyBook")
	return w.inner.ModifyBook(a0, a1)
}

func (w spyT6G4Writer) ModifyBookFileDeferAggregates(a0 string, a1 string, a2 func(*database.BookFile) error) (*database.BookFile, error) {
	w.rec.add("ModifyBookFileDeferAggregates")
	return w.inner.ModifyBookFileDeferAggregates(a0, a1, a2)
}

func (w spyT6G4Writer) ModifyBookFileReportingRecompute(a0 string, a1 string, a2 func(*database.BookFile) error) (*database.BookFile, error) {
	w.rec.add("ModifyBookFileReportingRecompute")
	return w.inner.ModifyBookFileReportingRecompute(a0, a1, a2)
}

func (w spyT6G4Writer) MoveBookFilesToBookBulk(a0 []database.BookFileMove, a1 string) error {
	w.rec.add("MoveBookFilesToBookBulk")
	return w.inner.MoveBookFilesToBookBulk(a0, a1)
}

func (w spyT6G4Writer) OpenWhisperJournal() (transcribe.ResultJournal, error) {
	w.rec.add("OpenWhisperJournal")
	return w.inner.OpenWhisperJournal()
}

func (w spyT6G4Writer) ReassignExternalID(a0 string, a1 string, a2 string) error {
	w.rec.add("ReassignExternalID")
	return w.inner.ReassignExternalID(a0, a1, a2)
}

func (w spyT6G4Writer) RebuildIdentIndex(a0 context.Context) (database.IdentRebuildStats, error) {
	w.rec.add("RebuildIdentIndex")
	return w.inner.RebuildIdentIndex(a0)
}

func (w spyT6G4Writer) RecomputeBookAggregates(a0 string) error {
	w.rec.add("RecomputeBookAggregates")
	return w.inner.RecomputeBookAggregates(a0)
}

func (w spyT6G4Writer) RecordIntroTimeout(a0 string, a1 string, a2 database.IntroTimeoutStrike) (database.IntroTimeoutOutcome, error) {
	w.rec.add("RecordIntroTimeout")
	return w.inner.RecordIntroTimeout(a0, a1, a2)
}

func (w spyT6G4Writer) SetBookFileHash(a0 string, a1 string) error {
	w.rec.add("SetBookFileHash")
	return w.inner.SetBookFileHash(a0, a1)
}

func (w spyT6G4Writer) SetBookFileIntro(a0 string, a1 string, a2 database.BookFileIntroResult) (database.BookFileIntroWrite, error) {
	w.rec.add("SetBookFileIntro")
	return w.inner.SetBookFileIntro(a0, a1, a2)
}

func (w spyT6G4Writer) SetFileProvExportCursor(a0 uint64) error {
	w.rec.add("SetFileProvExportCursor")
	return w.inner.SetFileProvExportCursor(a0)
}

func (w spyT6G4Writer) SubmitAuthorDedupJob(a0 context.Context, a1 string, a2 []ai.AuthorDiscoveryInput) (string, error) {
	w.rec.add("SubmitAuthorDedupJob")
	return w.inner.SubmitAuthorDedupJob(a0, a1, a2)
}

func (w spyT6G4Writer) UpdateBookFile(a0 string, a1 *database.BookFile) error {
	w.rec.add("UpdateBookFile")
	return w.inner.UpdateBookFile(a0, a1)
}

func (w spyT6G4Writer) UpdateBookFiles(a0 context.Context, a1 []*database.BookFile, a2 func(i int, applied bool)) (int, error) {
	w.rec.add("UpdateBookFiles")
	return w.inner.UpdateBookFiles(a0, a1, a2)
}
