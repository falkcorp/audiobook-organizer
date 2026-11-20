// file: internal/plugins/maintenance/deps_t6g4.go
// version: 0.1.0
// guid: 7b1e4a68-9d32-4c05-8f17-e60a3c92d5b1
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"fmt"
	"reflect"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// t6g4BootHost is what the server serves that is not a store row: the library
// root, the operation queue, the dedup engine and the AI parser, and the file
// provenance keyspace. The provenance store and the batch submitter are nil
// where the subsystem is not wired.
type t6g4BootHost interface {
	RootDir() string
	OperationQueueStore() OpQueueReader
	HasDedupEngine() bool
	HasAIParsing() bool
	DedupTriageExactPending(ctx context.Context, apply bool) (*TriageReport, error)
	FileProvenanceStore() database.FileProvenanceStore
	AIBatchSubmitter() t6g4AIBatchSubmitter
}

// newT6G4Deps is the books module's t6g4Deps over the ops store src.
//
// A nil src leaves Store, NewWriter and Stats unset, and a nil host leaves Store
// and NewWriter unset (the reads and writes that need it have nowhere to go).
// Stats in particular is never an adapter around nothing: t6g4Kinds refuses a nil
// Stats at registration so that a forgotten wiring fails at startup, and an
// adapter that persists nothing would turn that refusal into silent loss of the
// two transcribe kinds' counters.
func newT6G4Deps(src OpsStore, host t6g4BootHost, scan ScanController) t6g4Deps {
	if src == nil {
		return t6g4Deps{}
	}
	d := t6g4Deps{Scan: scan, Stats: booksT6G4Stats{src: src}}
	if host == nil {
		return d
	}
	rows := groupRows(nil, reflect.TypeFor[t6g4Writer]())
	d.Store = &booksT6G4Store{src: src, host: host}
	d.NewWriter = func(w tasks.Writer) t6g4Writer {
		return &booksT6G4Writer{src: src, host: host, gate: newBooksGate(w, rows)}
	}
	return d
}

// booksT6G4Stats persists the two transcribe kinds' counters straight to the
// store, best effort as HEAD's sink is: a store without the capability persists
// none. They are monitoring aggregates written like a log line, not gated writes
// (t6g4StatsSink says why).
type booksT6G4Stats struct{ src OpsStore }

func (s booksT6G4Stats) PutFileIntroStats(st *database.FileIntroStats) error {
	sink, ok := database.AsCapability[database.FileIntroStatsStore](s.src)
	if !ok {
		return nil
	}
	return sink.PutFileIntroStats(st)
}

func (s booksT6G4Stats) PutTranscribeStats(st *database.TranscribeStats) error {
	sink, ok := database.AsCapability[database.TranscribeStatsStore](s.src)
	if !ok {
		return nil
	}
	return sink.PutTranscribeStats(st)
}

// booksT6G4Store is the group's reads: exactly the methods of t6g4Store, none of
// which writes.
type booksT6G4Store struct {
	src  OpsStore
	host t6g4BootHost
}

func (s *booksT6G4Store) CountAllBooks() (int, error) { return s.src.CountAllBooks() }
func (s *booksT6G4Store) GetAllAuthors() ([]database.Author, error) {
	return s.src.GetAllAuthors()
}

func (s *booksT6G4Store) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.src.GetAllBookFilesCore()
}

func (s *booksT6G4Store) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return s.src.GetAllBooksCore(limit, offset)
}

func (s *booksT6G4Store) GetAllBooksFullFrom(after string, limit int) ([]database.Book, error) {
	return s.src.GetAllBooksFullFrom(after, limit)
}

func (s *booksT6G4Store) GetBookByID(id string) (*database.Book, error) { return s.src.GetBookByID(id) }
func (s *booksT6G4Store) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	return s.src.GetBookFileByID(bookID, fileID)
}

func (s *booksT6G4Store) GetBookFileByPath(path string) (*database.BookFile, error) {
	return s.src.GetBookFileByPath(path)
}

func (s *booksT6G4Store) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.src.GetBookFiles(bookID)
}

func (s *booksT6G4Store) GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error) {
	return s.src.GetBooksByAuthorIDWithRoleCore(authorID)
}

func (s *booksT6G4Store) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	return s.src.GetBooksByVersionGroup(groupID)
}

func (s *booksT6G4Store) GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error) {
	return s.src.GetExternalIDsForBook(bookID)
}

func (s *booksT6G4Store) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	return s.src.GetMetadataFieldStates(bookID)
}

func (s *booksT6G4Store) GetUserPreference(key string) (*database.UserPreference, error) {
	return s.src.GetUserPreference(key)
}
func (s *booksT6G4Store) ListBookIDs() ([]string, error) { return s.src.ListBookIDs() }
func (s *booksT6G4Store) LiveBookIDsAtPath(path string) ([]string, error) {
	return s.src.LiveBookIDsAtPath(path)
}

func (s *booksT6G4Store) BookFilesAtPath(path string) ([]database.BookFile, error) {
	r, err := srcCap[interface {
		BookFilesAtPath(string) ([]database.BookFile, error)
	}](s.src, "BookFilesAtPath")
	if err != nil {
		return nil, fmt.Errorf("%w; refusing to run", err)
	}
	return r.BookFilesAtPath(path)
}

func (s *booksT6G4Store) GetChaptersForBook(bookID string) ([]database.Chapter, error) {
	r, err := srcCap[interface {
		GetChaptersForBook(string) ([]database.Chapter, error)
	}](s.src, "GetChaptersForBook")
	if err != nil {
		return nil, fmt.Errorf("%w; refusing to run", err)
	}
	return r.GetChaptersForBook(bookID)
}

func (s *booksT6G4Store) DeferAggregatesAvailable() bool {
	_, ok := t6g4DeferAggregatesWriter(s.src)
	return ok
}

func (s *booksT6G4Store) ReportingRecomputeAvailable() bool {
	_, ok := database.AsCapability[reportingRecomputeWriter](s.src)
	return ok
}

func (s *booksT6G4Store) FileIntroWritesAvailable() bool {
	_, ok := database.AsCapability[database.BookFileIntroStore](s.src)
	return ok
}

func (s *booksT6G4Store) FileProvenanceAvailable() bool { return s.host.FileProvenanceStore() != nil }

func (s *booksT6G4Store) prov() (database.FileProvenanceStore, error) {
	p := s.host.FileProvenanceStore()
	if p == nil {
		return nil, fmt.Errorf("the file provenance keyspace is not wired")
	}
	return p, nil
}

func (s *booksT6G4Store) FindFileEventsByHash(hash string) ([]database.FileEvent, error) {
	p, err := s.prov()
	if err != nil {
		return nil, err
	}
	return p.FindFileEventsByHash(hash)
}

func (s *booksT6G4Store) GetFileProvExportCursor() (uint64, error) {
	p, err := s.prov()
	if err != nil {
		return 0, err
	}
	return p.GetFileProvExportCursor()
}

func (s *booksT6G4Store) MaxFileEventSeq() (uint64, error) {
	p, err := s.prov()
	if err != nil {
		return 0, err
	}
	return p.MaxFileEventSeq()
}

func (s *booksT6G4Store) ScanFileEventsBySeq(after uint64, limit int) ([]database.FileEventSeqRow, error) {
	p, err := s.prov()
	if err != nil {
		return nil, err
	}
	return p.ScanFileEventsBySeq(after, limit)
}

func (s *booksT6G4Store) GetFileHistory(bookFileID string) ([]database.FileEvent, error) {
	p, err := s.prov()
	if err != nil {
		return nil, err
	}
	return p.GetFileHistory(bookFileID)
}

func (s *booksT6G4Store) HasOperationQueue() bool { return s.host.OperationQueueStore() != nil }

func (s *booksT6G4Store) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	q := s.host.OperationQueueStore()
	if q == nil {
		return nil, fmt.Errorf("no operation queue")
	}
	return q.ListActiveOperationsV2()
}

func (s *booksT6G4Store) RootDir() string      { return s.host.RootDir() }
func (s *booksT6G4Store) HasDedupEngine() bool { return s.host.HasDedupEngine() }
func (s *booksT6G4Store) HasAIParsing() bool   { return s.host.HasAIParsing() }

func (s *booksT6G4Store) DedupTriagePreview(ctx context.Context) (*TriageReport, error) {
	return s.host.DedupTriageExactPending(ctx, false)
}

func (s *booksT6G4Store) ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]database.AIJob, error) {
	j, err := srcCap[database.AIJobsStore](s.src, "ai_jobs")
	if err != nil {
		return nil, err
	}
	return j.ListAIJobs(typeFilter, statusFilter, limit, offset)
}

func (s *booksT6G4Store) VerifyIdentIndex(ctx context.Context) (database.IdentIndexReport, error) {
	i, err := srcCap[identIndexRebuilder](s.src, "identification index")
	if err != nil {
		return database.IdentIndexReport{}, err
	}
	return i.VerifyIdentIndex(ctx)
}

// booksT6G4Writer is the group's writes: every method is one gated call (no
// method of this group has an empty row).
type booksT6G4Writer struct {
	src  OpsStore
	host t6g4BootHost
	gate booksGate
}

func (w *booksT6G4Writer) CreateBookFile(file *database.BookFile) error {
	return w.gate.do(runless(), "CreateBookFile", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.CreateBookFile(file)
	})
}

func (w *booksT6G4Writer) UpdateBookFile(id string, file *database.BookFile) error {
	return w.gate.do(runless(), "UpdateBookFile", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.UpdateBookFile(id, file)
	})
}

func (w *booksT6G4Writer) UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (n int, err error) {
	err = w.gate.do(ctx, "UpdateBookFiles", kindRef(ops.ResBookFiles), func(ctx context.Context) error {
		var e error
		n, e = w.src.UpdateBookFiles(ctx, files, afterRow)
		return e
	})
	return n, err
}

func (w *booksT6G4Writer) SetBookFileHash(id, hash string) error {
	return w.gate.do(runless(), "SetBookFileHash", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.SetBookFileHash(id, hash)
	})
}

func (w *booksT6G4Writer) MoveBookFilesToBookBulk(moves []database.BookFileMove, target string) error {
	return w.gate.do(runless(), "MoveBookFilesToBookBulk", bookRef(target), func(context.Context) error {
		return w.src.MoveBookFilesToBookBulk(moves, target)
	})
}

func (w *booksT6G4Writer) DeleteBookFilesByIDs(refs []database.BookFileRef) (out []database.BookFile, err error) {
	err = w.gate.do(runless(), "DeleteBookFilesByIDs", kindRef(ops.ResBookFiles), func(context.Context) error {
		var e error
		out, e = w.src.DeleteBookFilesByIDs(refs)
		return e
	})
	return out, err
}

func (w *booksT6G4Writer) ReassignExternalID(source, externalID, newBookID string) error {
	return w.gate.do(runless(), "ReassignExternalID", bookRef(newBookID), func(context.Context) error {
		return w.src.ReassignExternalID(source, externalID, newBookID)
	})
}

func (w *booksT6G4Writer) CarryOverFingerprintWindows(from []database.FingerprintWindowRef, to database.FingerprintWindowRef) (n int, err error) {
	err = w.gate.do(runless(), "CarryOverFingerprintWindows", kindRef(ops.ResFingerprintIndex), func(context.Context) error {
		var e error
		n, e = w.src.CarryOverFingerprintWindows(from, to)
		return e
	})
	return n, err
}

func (w *booksT6G4Writer) ModifyBook(id string, fn func(*database.Book) error) (b *database.Book, err error) {
	err = w.gate.do(runless(), "ModifyBook", bookRef(id), func(context.Context) error {
		var e error
		b, e = w.src.ModifyBook(id, fn)
		return e
	})
	return b, err
}

func (w *booksT6G4Writer) RecomputeBookAggregates(bookID string) error {
	return w.gate.do(runless(), "RecomputeBookAggregates", bookRef(bookID), func(context.Context) error {
		return w.src.RecomputeBookAggregates(bookID)
	})
}

// CreateOperationChange goes through the classification check, as HEAD's body
// did; a bare store call is what TestJournalRowsAreCreatedThroughTheClassificationCheck
// forbids.
func (w *booksT6G4Writer) CreateOperationChange(change *database.OperationChange) error {
	return w.gate.do(runless(), "CreateOperationChange", kindRef(ops.ResOperations), func(context.Context) error {
		return database.RecordOperationChange(w.src, change)
	})
}

func (w *booksT6G4Writer) MarkOperationChangesReverted(operationID string, changeIDs []string) error {
	return w.gate.do(runless(), "MarkOperationChangesReverted", kindRef(ops.ResOperations), func(context.Context) error {
		r, err := srcCap[interface {
			MarkOperationChangesReverted(string, []string) error
		}](w.src, "MarkOperationChangesReverted")
		if err != nil {
			return fmt.Errorf("%w; refusing to run", err)
		}
		return r.MarkOperationChangesReverted(operationID, changeIDs)
	})
}

func (w *booksT6G4Writer) ModifyBookFileDeferAggregates(bookID, fileID string, fn func(*database.BookFile) error) (f *database.BookFile, err error) {
	err = w.gate.do(runless(), "ModifyBookFileDeferAggregates", bookRef(bookID), func(context.Context) error {
		d, e := srcCap[segmentFailureWriter](w.src, "deferred-aggregate writer")
		if e != nil {
			return e
		}
		f, e = d.ModifyBookFileDeferAggregates(bookID, fileID, fn)
		return e
	})
	return f, err
}

func (w *booksT6G4Writer) ModifyBookFileReportingRecompute(bookID, fileID string, fn func(*database.BookFile) error) (f *database.BookFile, err error) {
	err = w.gate.do(runless(), "ModifyBookFileReportingRecompute", bookRef(bookID), func(context.Context) error {
		r, e := srcCap[reportingRecomputeWriter](w.src, "reporting-recompute writer")
		if e != nil {
			return e
		}
		f, e = r.ModifyBookFileReportingRecompute(bookID, fileID, fn)
		return e
	})
	return f, err
}

func (w *booksT6G4Writer) intros() (database.BookFileIntroStore, error) {
	return srcCap[database.BookFileIntroStore](w.src, "per-file intro writes")
}

func (w *booksT6G4Writer) SetBookFileIntro(bookID, fileID string, r database.BookFileIntroResult) (out database.BookFileIntroWrite, err error) {
	err = w.gate.do(runless(), "SetBookFileIntro", bookRef(bookID), func(context.Context) error {
		i, e := w.intros()
		if e != nil {
			return e
		}
		out, e = i.SetBookFileIntro(bookID, fileID, r)
		return e
	})
	return out, err
}

func (w *booksT6G4Writer) ClearBookFileIntro(bookID, fileID, runOpID string) (before *database.BookFile, cleared bool, err error) {
	err = w.gate.do(runless(), "ClearBookFileIntro", bookRef(bookID), func(context.Context) error {
		i, e := w.intros()
		if e != nil {
			return e
		}
		before, cleared, e = i.ClearBookFileIntro(bookID, fileID, runOpID)
		return e
	})
	return before, cleared, err
}

func (w *booksT6G4Writer) RecordIntroTimeout(bookID, fileID string, s database.IntroTimeoutStrike) (out database.IntroTimeoutOutcome, err error) {
	err = w.gate.do(runless(), "RecordIntroTimeout", bookRef(bookID), func(context.Context) error {
		i, e := w.intros()
		if e != nil {
			return e
		}
		out, e = i.RecordIntroTimeout(bookID, fileID, s)
		return e
	})
	return out, err
}

func (w *booksT6G4Writer) ClearIntroTimeouts(bookID, fileID, sourceHash string) (cleared bool, err error) {
	err = w.gate.do(runless(), "ClearIntroTimeouts", bookRef(bookID), func(context.Context) error {
		i, e := w.intros()
		if e != nil {
			return e
		}
		cleared, e = i.ClearIntroTimeouts(bookID, fileID, sourceHash)
		return e
	})
	return cleared, err
}

func (w *booksT6G4Writer) prov() (database.FileProvenanceStore, error) {
	p := w.host.FileProvenanceStore()
	if p == nil {
		return nil, fmt.Errorf("the file provenance keyspace is not wired")
	}
	return p, nil
}

func (w *booksT6G4Writer) AppendFileEvent(e database.FileEvent) error {
	return w.gate.do(runless(), "AppendFileEvent", kindRef(ops.ResFileProvenance), func(context.Context) error {
		p, err := w.prov()
		if err != nil {
			return err
		}
		return p.AppendFileEvent(e)
	})
}

func (w *booksT6G4Writer) SetFileProvExportCursor(seq uint64) error {
	return w.gate.do(runless(), "SetFileProvExportCursor", kindRef(ops.ResFileProvenance), func(context.Context) error {
		p, err := w.prov()
		if err != nil {
			return err
		}
		return p.SetFileProvExportCursor(seq)
	})
}

func (w *booksT6G4Writer) DedupTriageApply(ctx context.Context) (rep *TriageReport, err error) {
	err = w.gate.do(ctx, "DedupTriageApply", kindRef(ops.ResEmbeddings), func(ctx context.Context) error {
		var e error
		rep, e = w.host.DedupTriageExactPending(ctx, true)
		return e
	})
	return rep, err
}

func (w *booksT6G4Writer) SubmitAuthorDedupJob(ctx context.Context, sourceID string, inputs []ai.AuthorDiscoveryInput) (id string, err error) {
	err = w.gate.do(ctx, "SubmitAuthorDedupJob", kindRef(ops.ResReviewItems), func(ctx context.Context) error {
		sub := w.host.AIBatchSubmitter()
		if sub == nil {
			return fmt.Errorf("AI parsing is not enabled")
		}
		jobs, e := srcCap[database.AIJobsStore](w.src, "ai_jobs")
		if e != nil {
			return e
		}
		id, e = sub.SubmitAuthorDedupJob(ctx, jobs, sourceID, inputs)
		return e
	})
	return id, err
}

func (w *booksT6G4Writer) RebuildIdentIndex(ctx context.Context) (st database.IdentRebuildStats, err error) {
	err = w.gate.do(ctx, "RebuildIdentIndex", kindRef(ops.ResIndexes), func(ctx context.Context) error {
		i, e := srcCap[identIndexRebuilder](w.src, "identification index")
		if e != nil {
			return e
		}
		st, e = i.RebuildIdentIndex(ctx)
		return e
	})
	return st, err
}

// OpenWhisperJournal stamps the open, which fences a preview and a stopped run,
// and hands back the journal with its appends and removals stamped too
// (gatedResultJournal).
func (w *booksT6G4Writer) OpenWhisperJournal() (j transcribe.ResultJournal, err error) {
	err = w.gate.do(runless(), "OpenWhisperJournal", kindRef(ops.ResAIJournal), func(context.Context) error {
		jr, e := resultjournal.New(w.src, whisperJournalKind)
		if e != nil {
			return e
		}
		j = gatedResultJournal{j: jr, gate: w.gate, method: "OpenWhisperJournal"}
		return nil
	})
	return j, err
}
