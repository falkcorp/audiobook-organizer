// file: internal/plugins/maintenance/deps_t6g2.go
// version: 0.1.0
// guid: 8e2b6d40-71c9-4a35-9f08-d5a3c1e7b264
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"fmt"
	"reflect"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// t6g2Source is the store the group-2 builder reads and writes through: the
// reads the cores make and the two row writes it gates. The optional
// capabilities (the at-path index, the hash index, the chapters, the
// signature migration) are not part of it: they are resolved per call with
// database.AsCapability, as HEAD does, because a decorator can hide a method set
// the concrete store has.
type t6g2Source interface {
	t6g2BookReads
	GetBookFiles(bookID string) ([]database.BookFile, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	BatchCreateBookFiles(files []*database.BookFile) error
}

// t6g2BootHost is the boot-time passes the host (the server) runs: the library
// walks and the one-time file fixes. They are not store calls, so the gate
// stamps them with their own rows and the host does the work.
type t6g2BootHost = t6g2BootWriter

// newT6G2Deps is the books module's t6g2Deps. src is the store, host the boot
// passes, rootDir the library root the report path is confined under.
//
// A missing input leaves the field that needs it unset rather than wrapping nil
// in an adapter: the group's env builder refuses an unset Store, NewWriter or
// NewWriter result by name, and an adapter around a nil would pass those
// refusals and fail later on a nil dereference. A nil src leaves everything
// unset; a nil host leaves NewWriter unset; a nil rootDir leaves
// ReportPath unset.
func newT6G2Deps(src t6g2Source, host t6g2BootHost, rootDir func() string) t6g2Deps {
	if src == nil {
		return t6g2Deps{}
	}
	rows := groupRows(nil, reflect.TypeFor[t6g2Writer]())
	d := t6g2Deps{Store: &booksT6G2Store{src: src}}
	if host != nil {
		d.NewWriter = func(w tasks.Writer) t6g2Writer {
			return &booksT6G2Writer{src: src, host: host, gate: newBooksGate(w, rows)}
		}
	}
	if rootDir != nil {
		d.ReportPath = reportPathOver(rootDir)
	}
	return d
}

// booksT6G2Store is the group's reads. The optional ones resolve the
// capability on every call; a store without it answers with t6g2Unsupported,
// which the cores' Supports probe makes unreachable.
type booksT6G2Store struct{ src t6g2Source }

func (s *booksT6G2Store) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	return s.src.GetMetadataFieldStates(bookID)
}

func (s *booksT6G2Store) GetUserPreference(key string) (*database.UserPreference, error) {
	return s.src.GetUserPreference(key)
}
func (s *booksT6G2Store) GetBookByID(id string) (*database.Book, error) { return s.src.GetBookByID(id) }
func (s *booksT6G2Store) ListBookIDs() ([]string, error)                { return s.src.ListBookIDs() }
func (s *booksT6G2Store) GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error) {
	return s.src.GetBookSnapshots(id, limit)
}

func (s *booksT6G2Store) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return s.src.GetAllBooksCore(limit, offset)
}

func (s *booksT6G2Store) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return s.src.GetAllBookFilesCore()
}

func (s *booksT6G2Store) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.src.GetBookFiles(bookID)
}

func (s *booksT6G2Store) LiveBookIDsAtPath(path string) ([]string, error) {
	return s.src.LiveBookIDsAtPath(path)
}

func (s *booksT6G2Store) BookFilesAtPath(path string) ([]database.BookFile, error) {
	f, ok := database.AsCapability[interface {
		BookFilesAtPath(string) ([]database.BookFile, error)
	}](s.src)
	if !ok {
		return nil, t6g2Unsupported(s, t6g2CapFolderBuild)
	}
	return f.BookFilesAtPath(path)
}

func (s *booksT6G2Store) BookAtPathIndexBuilt() (bool, error) {
	f, ok := database.AsCapability[interface{ BookAtPathIndexBuilt() (bool, error) }](s.src)
	if !ok {
		return false, t6g2Unsupported(s, t6g2CapAtPathIndexBuilt)
	}
	return f.BookAtPathIndexBuilt()
}

func (s *booksT6G2Store) GetChaptersForBook(bookID string) ([]database.Chapter, error) {
	c, ok := database.AsCapability[chapterPersister](s.src)
	if !ok {
		return nil, t6g2Unsupported(s, t6g2CapChapters)
	}
	return c.GetChaptersForBook(bookID)
}

func (s *booksT6G2Store) VerifyBookAtPathIndex(ctx context.Context) (database.BookAtPathIndexReport, error) {
	v, ok := database.AsCapability[bookAtPathVerifier](s.src)
	if !ok {
		return database.BookAtPathIndexReport{}, t6g2Unsupported(s, t6g2CapAtPathVerify)
	}
	return v.VerifyBookAtPathIndex(ctx)
}

// CheckBookHashIndex is the read half of the hash-index repair: it writes
// nothing (the store's Check is the repair with apply=false), so a preview run
// can call it.
func (s *booksT6G2Store) CheckBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (t6g2HashIndexRun, error) {
	c, ok := database.AsCapability[bookHashIndexChecker](s.src)
	if !ok {
		return t6g2HashIndexRun{}, t6g2Unsupported(s, t6g2CapHashIndex)
	}
	rep, err := c.CheckBookHashIndex(ctx, progress)
	return t6g2NewHashIndexRun(rep), err
}

func (s *booksT6G2Store) ScanBookFileAcoustIDIndex(after string, limit int) ([]database.BookFileAcoustIDEntry, string, error) {
	r, ok := database.AsCapability[bookFileAcoustIDIndexRepairer](s.src)
	if !ok {
		return nil, "", t6g2Unsupported(s, t6g2CapAcoustIDRepair)
	}
	return r.ScanBookFileAcoustIDIndex(after, limit)
}

func (s *booksT6G2Store) JudgeBookFileAcoustIDEntry(e database.BookFileAcoustIDEntry) (database.BookFileAcoustIDVerdict, error) {
	r, ok := database.AsCapability[bookFileAcoustIDIndexRepairer](s.src)
	if !ok {
		return 0, t6g2Unsupported(s, t6g2CapAcoustIDRepair)
	}
	return r.JudgeBookFileAcoustIDEntry(e)
}

// ClassifyBookSigMigration is MigrateBookSigToSidecar with dryRun=true: the
// outcome a live migration would have, nothing written.
func (s *booksT6G2Store) ClassifyBookSigMigration(id string) (database.BookSigMigrateOutcome, error) {
	m := database.AsBookSigMigrateStore(s.src)
	if m == nil {
		return 0, t6g2Unsupported(s, t6g2CapSigMigrate)
	}
	return m.MigrateBookSigToSidecar(id, true)
}

func (s *booksT6G2Store) Describe() string { return fmt.Sprintf("%T", s.src) }

func (s *booksT6G2Store) Supports(c t6g2Capability) bool {
	switch c {
	case t6g2CapAtPathVerify:
		_, ok := database.AsCapability[bookAtPathVerifier](s.src)
		return ok
	case t6g2CapAtPathRebuild:
		_, ok := database.AsCapability[bookAtPathRebuilder](s.src)
		return ok
	case t6g2CapAtPathBackfill:
		_, ok := database.AsCapability[bookAtPathBackfiller](s.src)
		return ok
	case t6g2CapHashIndex:
		_, ok := database.AsCapability[bookHashIndexChecker](s.src)
		return ok
	case t6g2CapAcoustIDRepair:
		_, ok := database.AsCapability[bookFileAcoustIDIndexRepairer](s.src)
		return ok
	case t6g2CapSigMigrate:
		return database.AsBookSigMigrateStore(s.src) != nil
	case t6g2CapChapters:
		_, ok := database.AsCapability[chapterPersister](s.src)
		return ok
	case t6g2CapFolderBuild:
		_, ok := database.AsCapability[folderBuildStore](s.src)
		return ok
	}
	return false
}

// booksT6G2Writer is the group's writes, each one a gated call. Every method
// is ONE gate.Do stamped with the method's whole row (rows.go), so the executor's
// writer refuses it in a preview, on a fenced run and outside the declared
// effects, with fn uncalled.
type booksT6G2Writer struct {
	src  t6g2Source
	host t6g2BootHost
	gate booksGate
}

func (w *booksT6G2Writer) BackfillExternalIDs(ctx context.Context, ifNeeded bool, progress func(int, int, string), run walkresume.Run) (skipped bool, err error) {
	err = w.gate.do(ctx, "BackfillExternalIDs", kindRef(ops.ResExternalIDs), func(ctx context.Context) error {
		var e error
		skipped, e = w.host.BackfillExternalIDs(ctx, ifNeeded, progress, run)
		return e
	})
	return skipped, err
}

func (w *booksT6G2Writer) StripMovementAtoms(ctx context.Context, run walkresume.Run) (alreadyDone bool, err error) {
	err = w.gate.do(ctx, "StripMovementAtoms", kindRef(ops.ResFiles), func(ctx context.Context) error {
		var e error
		alreadyDone, e = w.host.StripMovementAtoms(ctx, run)
		return e
	})
	return alreadyDone, err
}

func (w *booksT6G2Writer) RemuxMalformedM4BFiles(ctx context.Context, progress func(int, int, string), run walkresume.Run) (alreadyDone bool, err error) {
	err = w.gate.do(ctx, "RemuxMalformedM4BFiles", kindRef(ops.ResFiles), func(ctx context.Context) error {
		var e error
		alreadyDone, e = w.host.RemuxMalformedM4BFiles(ctx, progress, run)
		return e
	})
	return alreadyDone, err
}

func (w *booksT6G2Writer) TranscodeMalformedM4BFiles(ctx context.Context, progress func(int, int, string), run walkresume.Run) (alreadyDone bool, err error) {
	err = w.gate.do(ctx, "TranscodeMalformedM4BFiles", kindRef(ops.ResFiles), func(ctx context.Context) error {
		var e error
		alreadyDone, e = w.host.TranscodeMalformedM4BFiles(ctx, progress, run)
		return e
	})
	return alreadyDone, err
}

func (w *booksT6G2Writer) QuarantineKnownBadFiles(ctx context.Context) (quarantined int, alreadyDone bool, err error) {
	err = w.gate.do(ctx, "QuarantineKnownBadFiles", kindRef(ops.ResBooks), func(ctx context.Context) error {
		var e error
		quarantined, alreadyDone, e = w.host.QuarantineKnownBadFiles(ctx)
		return e
	})
	return quarantined, alreadyDone, err
}

func (w *booksT6G2Writer) RepairBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (run t6g2HashIndexRun, err error) {
	err = w.gate.do(ctx, "RepairBookHashIndex", kindRef(ops.ResIndexes), func(ctx context.Context) error {
		c, ok := database.AsCapability[bookHashIndexChecker](w.src)
		if !ok {
			return t6g2Unsupported(&booksT6G2Store{src: w.src}, t6g2CapHashIndex)
		}
		rep, e := c.RepairBookHashIndex(ctx, progress)
		run = t6g2NewHashIndexRun(rep)
		return e
	})
	return run, err
}

func (w *booksT6G2Writer) RebuildBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (res database.BookAtPathBackfillResult, err error) {
	err = w.gate.do(ctx, "RebuildBookAtPathIndex", kindRef(ops.ResIndexes), func(ctx context.Context) error {
		r, ok := database.AsCapability[bookAtPathRebuilder](w.src)
		if !ok {
			return t6g2Unsupported(&booksT6G2Store{src: w.src}, t6g2CapAtPathRebuild)
		}
		var e error
		res, e = r.RebuildBookAtPathIndex(ctx, progress)
		return e
	})
	return res, err
}

func (w *booksT6G2Writer) BackfillBookAtPathIndex(ctx context.Context, progress database.BookAtPathProgress) (res database.BookAtPathBackfillResult, err error) {
	err = w.gate.do(ctx, "BackfillBookAtPathIndex", kindRef(ops.ResIndexes), func(ctx context.Context) error {
		b, ok := database.AsCapability[bookAtPathBackfiller](w.src)
		if !ok {
			return t6g2Unsupported(&booksT6G2Store{src: w.src}, t6g2CapAtPathBackfill)
		}
		var e error
		res, e = b.BackfillBookAtPathIndex(ctx, progress)
		return e
	})
	return res, err
}

func (w *booksT6G2Writer) DeleteOrphanBookFileAcoustIDEntries(ctx context.Context, entries []database.BookFileAcoustIDEntry) (n int, err error) {
	err = w.gate.do(ctx, "DeleteOrphanBookFileAcoustIDEntries", kindRef(ops.ResIndexes), func(ctx context.Context) error {
		r, ok := database.AsCapability[bookFileAcoustIDIndexRepairer](w.src)
		if !ok {
			return t6g2Unsupported(&booksT6G2Store{src: w.src}, t6g2CapAcoustIDRepair)
		}
		var e error
		n, e = r.DeleteOrphanBookFileAcoustIDEntries(ctx, entries)
		return e
	})
	return n, err
}

// ModifyBook, SaveChaptersForBook, BatchCreateBookFiles and
// MigrateBookSigToSidecar take no context (the store calls take none); they
// gate on runless() (see its doc).
func (w *booksT6G2Writer) ModifyBook(id string, fn func(*database.Book) error) (b *database.Book, err error) {
	err = w.gate.do(runless(), "ModifyBook", bookRef(id), func(context.Context) error {
		var e error
		b, e = w.src.ModifyBook(id, fn)
		return e
	})
	return b, err
}

func (w *booksT6G2Writer) SaveChaptersForBook(bookID string, chapters []database.Chapter) error {
	return w.gate.do(runless(), "SaveChaptersForBook", bookRef(bookID), func(context.Context) error {
		c, ok := database.AsCapability[chapterPersister](w.src)
		if !ok {
			return t6g2Unsupported(&booksT6G2Store{src: w.src}, t6g2CapChapters)
		}
		return c.SaveChaptersForBook(bookID, chapters)
	})
}

func (w *booksT6G2Writer) BatchCreateBookFiles(files []*database.BookFile) error {
	return w.gate.do(runless(), "BatchCreateBookFiles", kindRef(ops.ResBookFiles), func(context.Context) error {
		return w.src.BatchCreateBookFiles(files)
	})
}

func (w *booksT6G2Writer) MigrateBookSigToSidecar(id string) (out database.BookSigMigrateOutcome, err error) {
	err = w.gate.do(runless(), "MigrateBookSigToSidecar", bookRef(id), func(context.Context) error {
		m := database.AsBookSigMigrateStore(w.src)
		if m == nil {
			return t6g2Unsupported(&booksT6G2Store{src: w.src}, t6g2CapSigMigrate)
		}
		var e error
		out, e = m.MigrateBookSigToSidecar(id, false)
		return e
	})
	return out, err
}
