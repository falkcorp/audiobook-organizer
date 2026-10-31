// file: internal/repairs/writer_retire.go
// version: 1.2.0
// guid: a82a965b-b258-4d39-b08b-abd352a891e7
// last-edited: 2026-10-30

package repairs

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Retiring a trashed book's files into a retired version of a live holder
// (retired-versions plan, sections 3.3 and 6) moves book_file rows without
// deleting one, so it is a journaled step like every other Repairs write, in
// the change types the op revert reverses (undo.ChangeTypeBookVersionCreate,
// undo.ChangeTypeBookFileRetire, undo.ChangeTypeRetiredVersionRehome).
//
// The caller (the retire fixer) holds the merge lock for the whole row
// (LockWaiting with merge.LockMergeRMW), as every fixer that moves file rows
// does. That lock is also what UndoCombine runs under, so a retire and the
// undo of the combine it is linked to can never interleave; no second keyed
// lock is needed.

// retireVersionStore is the version read and write RetireVersion needs beyond
// the book_file surface. Resolved with database.AsCapability from the Writer's
// book_file store (the production store has both), so BookFileWriter, which has
// no delete on purpose, does not grow.
type retireVersionStore interface {
	GetBookVersion(id string) (*database.BookVersion, error)
	CreateBookVersion(v *database.BookVersion) (*database.BookVersion, error)
}

// rawKVWriter writes the combine-retire link.
type rawKVWriter interface {
	SetRaw(key string, value []byte) error
}

// shellReader reads the shell book, so RetireVersion can check it is still in
// the trash. Resolved with database.AsCapability from the Writer's stores.
type shellReader interface {
	GetBookByID(id string) (*database.Book, error)
}

// RetireSpec names one shell's retire into a holder.
type RetireSpec struct {
	// Shell is the trashed book the rows leave; Holder the live book that keeps
	// them as a retired version. They must differ.
	Shell, Holder string
	// VersionID is the retired version's id. It is minted by the planner and
	// recorded in its plan record, so a resumed apply reuses it and finds the
	// version and rows its cut-off run already wrote.
	VersionID string
	// FileIDs are the shell's book_file rows to retire. A row already retired
	// into VersionID under the holder (a resumed run) is left as it is. May be
	// empty: a shell whose rows a combine moved off it has none, and its
	// external ids and merged-into pointer (journaled by the caller) are still
	// what the combine's undo has to reverse, so CombineJournalID links anyway.
	FileIDs []string
	// Format and Source describe the retired version (BookVersion.Format and
	// .Source); both may be empty.
	Format, Source string
	// SourceOriginalPath, MetadataJSON and IngestDate fill the same-named
	// BookVersion fields: where the shell's audio came from, the provenance a
	// reader of the retired version shows (what the shell was), and when the
	// shell entered the library. All may be empty; a zero IngestDate is Now.
	SourceOriginalPath, MetadataJSON string
	IngestDate                       time.Time
	// CombineJournalID, when set, links the retire to the combine journal that
	// absorbed Shell (plan section 6.5), so UndoCombine reverses it first.
	CombineJournalID string
	// Now stamps the version's RetiredAt and creation time; the zero value is
	// the current time. A resumed run reuses the journaled version, so the
	// stamp is made once.
	Now time.Time
}

// RetireVersion retires spec.FileIDs from spec.Shell into the retired version
// spec.VersionID of spec.Holder: the version row, then the rows, each
// journaled before its write (JOURNAL FIRST) and voided when the store refuses,
// so the op revert undoes exactly what happened.
//
// Stage order, every stage idempotent so a resumed apply (same op id) picks up
// where its cut-off run stopped:
//
//  1. When spec.CombineJournalID is set, the raw link
//     merge:combine-retire:<combine id>:<shell id>:<this op id>, BEFORE the
//     first change row, and whether or not there are file rows (a combine-
//     absorbed shell has none: CombineBooks moved them all to the survivor).
//     A link whose retire never wrote anything is dead (UndoCombine finds no
//     unreverted row under it) and harmless; rows without a link would be the
//     unreversible case, so the link goes first. The shell must still be in
//     the trash (ErrChangedSincePlan otherwise): a resumed run after the
//     combine was undone would otherwise retire, and link, a restored book.
//  2. undo.ChangeTypeBookVersionCreate, then CreateBookVersion. A version
//     already in the store (a resumed run) is verified, not created again.
//  3. One undo.ChangeTypeBookFileRetire row per row, all created in ONE commit
//     (JournalSteps), then ONE RetireBookFiles batch. A refusal voids all of
//     them. Rows already retired into the version are skipped.
//
// The shell's external ids and merged-into pointer are journaled by the caller
// (Step with undo.ChangeTypeExternalIDReassign, Journal with
// undo.ChangeTypeBookMergedInto), as every retire does.
//
// Errors: database.ErrRetireIndexIncomplete (the store cannot prove a path or
// hash key has no other live owner until memdb serves) wraps ErrRetryLater;
// a row that is no longer on the shell, is already retired elsewhere, or whose
// version no longer matches wraps ErrChangedSincePlan.
func (w *Writer) RetireVersion(spec RetireSpec) error {
	if err := w.denyTagsOnly("RetireVersion"); err != nil {
		return err
	}
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	switch {
	case spec.Shell == "" || spec.Holder == "" || spec.Shell == spec.Holder:
		return fmt.Errorf("repairs: retire needs two different books, got shell %q holder %q", spec.Shell, spec.Holder)
	case spec.VersionID == "":
		return errors.New("repairs: retire needs the version id its plan minted")
	}
	if err := w.requireShellTrashed(spec.Shell); err != nil {
		return err
	}
	// The link goes before anything else is decided, including the "nothing to
	// retire" return below: a shell with no file rows still has the external-id
	// and merged-into rows the caller journals, and those are what the
	// combine's undo has to find.
	if spec.CombineJournalID != "" {
		if err := w.linkCombineRetire(spec); err != nil {
			return err
		}
	}
	if len(spec.FileIDs) == 0 {
		return nil
	}
	retirer, versions, err := w.retireStores()
	if err != nil {
		return err
	}
	if err := w.ensureRetiredVersion(versions, spec); err != nil {
		return err
	}
	return w.retireRows(retirer, spec)
}

// retireStores resolves, from the Writer's book_file store, the two surfaces
// RetireVersion and RehomeRetiredVersion write through. A store that lacks
// either (a decorator that does not unwrap, say) is refused here, naming what
// is missing, rather than half way through a retire.
func (w *Writer) retireStores() (database.RetiredBookFileWriter, retireVersionStore, error) {
	if w.files == nil {
		return nil, nil, errors.New("repairs: writer has no book_file store")
	}
	retirer, ok := database.AsCapability[database.RetiredBookFileWriter](w.files)
	if !ok {
		return nil, nil, errors.New("repairs: the store cannot retire book_file rows")
	}
	versions, ok := database.AsCapability[retireVersionStore](w.files)
	if !ok {
		return nil, nil, errors.New("repairs: the store cannot read and create book versions")
	}
	return retirer, versions, nil
}

// CheckRetireSupport reports whether the Writer's store can run RetireVersion
// and RehomeRetiredVersion, and writes nothing. A caller that journals
// something before it retires (the retired-versions fixer's plan record) asks
// first, so a store that cannot retire is refused before the first journal row
// rather than after it.
func (w *Writer) CheckRetireSupport() error {
	if err := w.denyTagsOnly("RetireVersion"); err != nil {
		return err
	}
	_, _, err := w.retireStores()
	return err
}

// requireShellTrashed refuses (ErrChangedSincePlan) a shell that is no longer
// in the trash: it was restored since the plan (a combine undone, say), and
// retiring its rows would take them from a live book. A shell that is gone is
// left to the store's own refusal.
func (w *Writer) requireShellTrashed(shellID string) error {
	r, ok := database.AsCapability[shellReader](w.files)
	if !ok {
		if r, ok = database.AsCapability[shellReader](w.store); !ok {
			return errors.New("repairs: the store cannot read the shell book")
		}
	}
	b, err := r.GetBookByID(shellID)
	if err != nil {
		return fmt.Errorf("read shell book %s: %w", shellID, err)
	}
	if b != nil && !b.IsSoftDeleted() {
		return fmt.Errorf("%w: book %s is no longer in the trash, so its files are not retired", ErrChangedSincePlan, shellID)
	}
	return nil
}

// linkCombineRetire writes the raw combine-retire link (RetireVersion, stage
// 1). A failed write refuses the retire before any change row exists: a retire
// that cannot be found from its combine would make that combine's undo
// silently skip it.
func (w *Writer) linkCombineRetire(spec RetireSpec) error {
	kv, ok := database.AsCapability[rawKVWriter](w.files)
	if !ok {
		kv, ok = database.AsCapability[rawKVWriter](w.store)
	}
	if !ok {
		return errors.New("repairs: the store cannot write the combine-retire link")
	}
	if err := w.beat("combine-retire link of book " + spec.Shell); err != nil {
		return err
	}
	if err := merge.WriteCombineRetireLink(kv, spec.CombineJournalID, spec.Shell, w.opID); err != nil {
		return fmt.Errorf("%w: %v", ErrNotJournaled, err)
	}
	return nil
}

// ensureRetiredVersion journals and creates the retired version (RetireVersion,
// stage 2). The version a cut-off run journaled is reused as journaled, so a
// resumed run neither journals a second row for a different RetiredAt nor
// creates a second version.
func (w *Writer) ensureRetiredVersion(versions retireVersionStore, spec RetireSpec) error {
	field := undo.BookVersionField(spec.VersionID)
	cur, err := versions.GetBookVersion(spec.VersionID)
	if err != nil {
		return fmt.Errorf("read book version %s: %w", spec.VersionID, err)
	}
	if cur != nil {
		if cur.Status != database.BookVersionStatusRetired || cur.BookID != spec.Holder || cur.RetiredFromBookID != spec.Shell {
			return fmt.Errorf("%w: book version %s is %q of book %s (from %q), not the retired version of %s from %s the plan minted",
				ErrChangedSincePlan, cur.ID, cur.Status, cur.BookID, cur.RetiredFromBookID, spec.Holder, spec.Shell)
		}
		return nil
	}
	v := database.BookVersion{}
	if prev, ok, err := w.JournaledValue(spec.Holder, undo.ChangeTypeBookVersionCreate, field); err != nil {
		return err
	} else if ok {
		var j database.BookVersion
		if jerr := json.Unmarshal([]byte(prev), &j); jerr != nil {
			return fmt.Errorf("repairs: the journaled version %s is unreadable: %w", spec.VersionID, jerr)
		}
		v = j
	} else {
		now := spec.Now
		if now.IsZero() {
			now = time.Now()
		}
		now = now.UTC().Truncate(time.Microsecond)
		ingest := now
		if !spec.IngestDate.IsZero() {
			ingest = spec.IngestDate.UTC()
		}
		v = database.BookVersion{
			ID: spec.VersionID, BookID: spec.Holder, Status: database.BookVersionStatusRetired,
			Format: spec.Format, Source: spec.Source, SourceOriginalPath: spec.SourceOriginalPath,
			RetiredFromBookID: spec.Shell, RetireOpID: w.opID, RetiredAt: &now, MetadataJSON: spec.MetadataJSON,
			IngestDate: ingest, CreatedAt: now, UpdatedAt: now, Version: 1,
		}
	}
	newValue, err := undo.EncodeBookVersionCreate(v)
	if err != nil {
		return err
	}
	return w.JournalStep(spec.Holder, UndoEntry{ChangeType: undo.ChangeTypeBookVersionCreate, Field: field, Old: "", New: newValue}, func() error {
		created := v
		if _, err := versions.CreateBookVersion(&created); err != nil {
			return retireStoreError(err)
		}
		w.writes.Add(1)
		return nil
	})
}

// retireRows journals one retire row per file and retires them in one batch
// (RetireVersion, stage 3).
func (w *Writer) retireRows(retirer database.RetiredBookFileWriter, spec RetireSpec) error {
	var steps []BookStep
	var pending []string
	for _, id := range spec.FileIDs {
		onShell, err := w.files.GetBookFileByID(spec.Shell, id)
		if err != nil {
			return fmt.Errorf("read book_file %s on %s: %w", id, spec.Shell, err)
		}
		if onShell == nil {
			// A resumed run: the row may already be retired under the holder
			// into this version, journaled by the cut-off run.
			onHolder, err := w.files.GetBookFileByID(spec.Holder, id)
			if err != nil {
				return fmt.Errorf("read book_file %s on %s: %w", id, spec.Holder, err)
			}
			if onHolder != nil && onHolder.Retired && onHolder.VersionID == spec.VersionID {
				continue
			}
			return fmt.Errorf("%w: book_file %s is no longer on book %s", ErrChangedSincePlan, id, spec.Shell)
		}
		if onShell.Retired {
			return fmt.Errorf("%w: book_file %s is already retired", ErrChangedSincePlan, id)
		}
		steps = append(steps, BookStep{BookID: spec.Holder, Entry: UndoEntry{
			ChangeType: undo.ChangeTypeBookFileRetire,
			Field:      "book_file:" + id,
			Old:        undo.RetirePlace{BookID: spec.Shell, VersionID: onShell.VersionID}.Encode(),
			New:        undo.RetirePlace{BookID: spec.Holder, VersionID: spec.VersionID, CombineJournalID: spec.CombineJournalID}.Encode(),
		}})
		pending = append(pending, id)
	}
	if len(pending) == 0 {
		return nil
	}
	return w.JournalSteps(steps, func() ([]bool, error) {
		done := make([]bool, len(steps))
		err := retirer.RetireBookFiles([]database.BookFileMove{{FileIDs: slices.Clone(pending), SourceBookID: spec.Shell}}, spec.Holder, spec.VersionID)
		if err == nil {
			w.writes.Add(1)
			for i := range done {
				done[i] = true
			}
			return done, nil
		}
		if errors.Is(err, database.ErrBookFileDurabilityUnknown) {
			// The batch was applied; only its fsync failed. The rows stand
			// (JOURNAL FIRST), so a revert undoes what may have happened.
			for i := range done {
				done[i] = true
			}
		}
		return done, retireStoreError(err)
	})
}

// RehomeRetiredVersion moves the retired version versionID and all its rows to
// newHolder (the chain case: the holder is itself being trashed and retired),
// journaled first as undo.ChangeTypeRetiredVersionRehome and voided when the
// store refuses. A version already held by newHolder (a resumed run) is left
// alone.
func (w *Writer) RehomeRetiredVersion(versionID, newHolder string) error {
	if err := w.denyTagsOnly("RehomeRetiredVersion"); err != nil {
		return err
	}
	if w.files == nil {
		return errors.New("repairs: writer has no book_file store")
	}
	if versionID == "" || newHolder == "" {
		return fmt.Errorf("repairs: rehome needs a version and a holder, got %q and %q", versionID, newHolder)
	}
	retirer, versions, err := w.retireStores()
	if err != nil {
		return err
	}
	cur, err := versions.GetBookVersion(versionID)
	if err != nil {
		return fmt.Errorf("read book version %s: %w", versionID, err)
	}
	switch {
	case cur == nil || cur.Status != database.BookVersionStatusRetired:
		return fmt.Errorf("%w: book version %s is not a retired version any more", ErrChangedSincePlan, versionID)
	case cur.BookID == newHolder:
		return nil
	}
	e := UndoEntry{ChangeType: undo.ChangeTypeRetiredVersionRehome, Field: undo.BookVersionField(versionID), Old: cur.BookID, New: newHolder}
	return w.JournalStep(newHolder, e, func() error {
		if err := retirer.RehomeRetiredVersion(versionID, newHolder); err != nil {
			return retireStoreError(err)
		}
		w.writes.Add(1)
		return nil
	})
}

// retireStoreError classifies a store refusal for the fixer: a path or hash
// key the store cannot prove free yet is retry-later, a row or version that no
// longer matches is changed-since-plan, anything else is returned as is.
func retireStoreError(err error) error {
	switch {
	case errors.Is(err, database.ErrRetireIndexIncomplete):
		return fmt.Errorf("%w: %w", ErrRetryLater, err)
	case errors.Is(err, database.ErrBookFileRetired),
		errors.Is(err, database.ErrBookFileNotRetired),
		errors.Is(err, database.ErrRetiredVersionMismatch),
		errors.Is(err, database.ErrRetiredVersionHasFiles),
		errors.Is(err, database.ErrBookFileOwnerMissing):
		return fmt.Errorf("%w: %w", ErrChangedSincePlan, err)
	}
	return err
}
