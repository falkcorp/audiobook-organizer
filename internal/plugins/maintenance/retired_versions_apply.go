// file: internal/plugins/maintenance/retired_versions_apply.go
// version: 1.3.1
// guid: 5e8a2c71-3b94-4d06-a1f7-92c06d3e8b15
// last-edited: 2026-10-28

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The Apply of the retired-versions fixer (retire plan sections 5 and 8; PR 7
// of 9, behind retire_versions_apply_enabled).
//
// WHAT ONE ROW DOES. A row is one trashed book (the shell) and the live book it
// lost to (the holder). Under the global merge lock, after a full re-check, it:
//
//  1. journals the decision (repair_plan_record, on the holder);
//  2. mints the retired book version on the holder (book_version_create);
//  3. retires every live book_file row of the shell onto the holder in ONE
//     atomic batch (RetireBookFiles), journaling one book_file_retire row per
//     file first. This is the commit point: before it the shell still owns its
//     rows, after it the shell owns none;
//  4. reassigns the shell's external ids to the holder (external_id_reassign),
//     so iTunes and other reimports resolve to the holder instead of being
//     tombstoned by the later purge;
//  5. sets the shell's merged-into to the holder when it is empty;
//  6. re-homes any retired versions the shell itself holds onto the holder (the
//     chain case), so the later purge finds the shell owning no row.
//
// No book_file row is ever deleted: the rows are re-keyed and flagged.
//
// WHAT THE ENGINE ALREADY GIVES THIS. repairs.RunApply owns the dry run (only
// an explicit dry_run:false writes), the fingerprint check (changed_since_plan),
// the retry_later and partially_applied outcomes, the checkpoint/resume, and
// the partitioning: rows are grouped by union-find over Row.BookIDs, which is
// {shell, holder}, so every shell retiring into one holder runs in order in
// one partition and no two workers ever work on the same book. The merge lock
// below then serialises the write section across partitions, so the writer
// never sees two retires at once; the partition is what keeps the cheaper
// pre-lock work (the engine's Replan) from racing on one holder.
//
// RESUMABILITY. A book whose rows are retired (a retired version names it AND
// holds rows for it) is never retired twice. With nothing left to do it is
// already_retired (skipped): a plan taken after an apply reports it done, and a
// second apply of the same plan settles it not_applicable and writes nothing.
// A run cut off after the commit point but before steps 4 to 6 finished leaves
// a retire_tail_pending book: APPLICABLE in any plan taken afterwards (a fresh
// plan finishes it, with no need for the plan the cut-off run started from) and
// finished by an Apply that writes only the unfinished steps. A re-plan of the
// cut-off run's own planned row returns it under the PLANNED fingerprint. A run
// cut off before the commit point left a plan record and maybe an empty
// version: a RESUME of that same op adopts the empty version (it is the one the
// op's own journal created) rather than minting another, a version holding no
// rows is not a finished retire so it never moves the shell's external ids, and
// the journal dedupes the plan record and the per-row retire records. A later,
// DIFFERENT op never adopts it: that op journals no create for it, so reverting
// the op that made it would delete a version the later op put rows into
// (emptyRetiredVersion); the later op mints its own and the empty one stays.
//
// THE TAIL MOVES ONLY WHAT THE RETIRE PLANNED. The plan record
// (retirePlanRecord) names the external ids the retire was going to move, read
// under the merge lock before its first write. The tail (retire_tail_pending,
// and the tail of an ordinary retire) moves those ids and no others: an id that
// attached to the shell afterwards (an importer or backfill writing a PID onto a
// trashed row) was never judged by the classifier's guards, and a finish that
// skipped them could hand another work's id to the holder. It stays on the
// shell and the row reports it. A retired version whose plan record cannot be
// found moves no id, fail closed, and the row says so.
//
// WHAT IT DOES NOT DO YET. A shell an unreversed combine journal names as
// absorbed (combine_undo_aware) is held, not retired: plan section 6.5 requires
// the retire to be undone by undoing the combine, and that link is written by
// repairs.Writer.RetireVersion (RetireSpec.CombineJournalID), which this
// fixer does not call yet. The dedup auto-merge journals
// (dedup:automerge:) are not read either; both are pre-conditions of turning the
// flag on in production (docs/executive-summaries and the PR body say so).

// retiredVersionSource is BookVersion.Source of a version made by this fixer.
const retiredVersionSource = "retired_book"

// retiredTail is the part of a retire that follows the commit point and is
// still to do for one shell: its external ids not yet on the holder, a
// merged-into still empty, and retired rows the shell holds for other versions
// (the chain case).
type retiredTail struct {
	// externalIDs counts the shell's live external ids that the retire's plan
	// record names (planned and still on the shell); only these are moved.
	externalIDs int
	mergedInto  bool
	chainRows   int
	// unplanned counts live external ids on the shell that the plan record does
	// not name: they are left where they are. noRecord counts the shell's
	// retired versions that hold rows and have no plan record to read, so no
	// external id of the shell is moved on their account.
	unplanned, noRecord int
}

func (t retiredTail) any() bool { return t.externalIDs > 0 || t.mergedInto || t.chainRows > 0 }

// note says what the tail leaves alone, or "".
func (t retiredTail) note() string {
	var parts []string
	if t.unplanned > 0 {
		parts = append(parts, strconv.Itoa(t.unplanned)+" external id(s) not in the retire's plan record are left on the shell")
	}
	if t.noRecord > 0 {
		parts = append(parts, strconv.Itoa(t.noRecord)+" retired version(s) have no plan record, so no external id is moved on their account")
	}
	return strings.Join(parts, "; ")
}

func (t retiredTail) String() string {
	var parts []string
	if t.externalIDs > 0 {
		parts = append(parts, strconv.Itoa(t.externalIDs)+" external id(s)")
	}
	if t.mergedInto {
		parts = append(parts, "the merged-into link")
	}
	if t.chainRows > 0 {
		parts = append(parts, strconv.Itoa(t.chainRows)+" chained retired row(s)")
	}
	return strings.Join(parts, ", ")
}

// retiredDetail is Row.Detail of a row from a Replan or an under-lock re-check:
// what Apply writes from, read fresh. It is never persisted (Row.Detail is not
// serialised), and a Plan never attaches it (retiredRun.detail).
type retiredDetail struct {
	// minAge is the age gate the row was classified with. Apply is handed no
	// params, so its under-lock re-check classifies with this one: a plan run
	// with min_age_days 0 must not go changed_since_plan at 14.
	minAge time.Duration

	shell        database.BookCore
	files        []database.BookFileCore
	targetID     string
	targetMethod string
	category     string
	groupID      string
	sharedWith   []string
	sharedRows   int
	itunesRows   int

	// done is a book whose rows are already retired (retCatAlreadyRetired or
	// retCatTailPending): holder is where, versionIDs which versions hold them,
	// pending what is unfinished. Apply finishes only the pending tail of such
	// a row.
	done       bool
	holder     string
	versionIDs []string
	pending    retiredTail
	// planned is, for a done row, the external ids (plannedKey) the retire's
	// plan records name: the only ones its tail moves.
	planned map[string]bool
}

// alreadyRetired answers whether shell is a book a finished (or part-finished)
// apply emptied: it owns no live row and a retired version that NAMES it as
// the shell actually holds rows. A version that holds none (a retire cut off
// between creating the version and the commit, or whose rows were purged) does
// not make the shell retired: nothing was moved off it, so it is no holder's
// audio and its external ids must not follow it anywhere.
//
// A retired shell is skipped already_retired once nothing is left to do. If
// the tail of its retire (external ids, merged-into, chained rows) is
// unfinished and the holder is a live book, it is retCatTailPending and
// applicable, so a FRESH plan can finish a retire that was cut off after its
// commit point (not only a re-plan of the row the cut-off run was planned as).
func (r *retiredRun) alreadyRetired(shell database.BookCore) (repairs.Row, bool, error) {
	if r.retired == nil {
		return repairs.Row{}, false, nil
	}
	vs, err := r.retired.GetRetiredVersionsFromBook(shell.ID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read the retired versions of %s: %w", shell.ID, err)
	}
	if len(vs) == 0 {
		return repairs.Row{}, false, nil
	}
	// Newest first: the holder is where the newest version that holds rows is.
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].CreatedAt.After(vs[j].CreatedAt) })
	heldBy := map[string][]database.BookFile{}
	var holder string
	var heldIDs []string
	var heldVersions []database.BookVersion
	multi := false
	for i := range vs {
		rows, ok := heldBy[vs[i].BookID]
		if !ok {
			if rows, err = r.retired.GetRetiredBookFiles(vs[i].BookID); err != nil {
				return repairs.Row{}, false, fmt.Errorf("read the retired rows %s holds: %w", vs[i].BookID, err)
			}
			heldBy[vs[i].BookID] = rows
		}
		n := 0
		for j := range rows {
			if rows[j].VersionID == vs[i].ID {
				n++
			}
		}
		if n == 0 {
			continue
		}
		heldIDs = append(heldIDs, vs[i].ID)
		heldVersions = append(heldVersions, vs[i])
		if holder == "" {
			holder = vs[i].BookID
		} else if vs[i].BookID != holder {
			multi = true
		}
	}
	if len(heldIDs) == 0 {
		return repairs.Row{}, false, nil
	}
	sort.Strings(heldIDs)
	planned, noRecord, err := r.plannedExternalIDs(shell.ID, heldVersions)
	if err != nil {
		return repairs.Row{}, false, err
	}
	tail, err := r.pendingTail(shell.ID, planned)
	if err != nil {
		return repairs.Row{}, false, err
	}
	tail.noRecord = noRecord
	holderLive, err := r.isLive(holder)
	if err != nil {
		return repairs.Row{}, false, err
	}
	finishable := tail.any() && !multi && holderLive
	class, skipped := retCatTailPending, ""
	why := fmt.Sprintf("its file records are already retired onto %s (%d retired version(s) hold rows); finishing: %s", holder, len(heldIDs), tail.String())
	if !finishable {
		class, skipped = retCatAlreadyRetired, retCatAlreadyRetired
		why = fmt.Sprintf("its file records are already retired onto %s (%d retired version(s) hold rows); nothing to write", holder, len(heldIDs))
		switch {
		case !tail.any():
		case multi:
			why = fmt.Sprintf("its file records are retired onto more than one book (%d versions hold rows); unfinished (%s) and left for the owner", len(heldIDs), tail.String())
		default:
			why = fmt.Sprintf("its file records are already retired onto %s, which is not a live book; unfinished (%s) until that is settled", holder, tail.String())
		}
	}
	if n := tail.note(); n != "" {
		why += "; " + n
	}
	cur := retiredCurrent{"rows": "0", "retired_into": holder, "target_book_id": holder}
	cur.putN("retired_versions", len(heldIDs))
	cur.put("tail_pending", tail.String())
	row := repairs.Row{
		RowID: shell.ID, BookIDs: []string{shell.ID, holder}, Title: shell.Title,
		Class: class, Skipped: skipped, SkipReason: skipped, Reason: why,
		Risk: repairs.RiskLow, Current: cur,
		Members: []repairs.RowMember{{BookID: shell.ID, Title: shell.Title, Role: "shell"}, {BookID: holder, Role: "target"}},
	}
	if skipped != "" {
		row.SkipReason = why
	}
	// The fingerprint of an applicable tail row leaves the tail out: finishing
	// is idempotent and shrinks it, so a plan made before a cut-off run made
	// further progress stays applicable. A skipped row keeps it, so a book that
	// became finishable reads as changed.
	parts := []string{class, shell.ID, holder}
	if skipped != "" {
		parts = append(parts, tail.String())
	}
	h := sha256.New()
	for _, p := range append(parts, heldIDs...) {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	row.Fingerprint = hex.EncodeToString(h.Sum(nil))
	if r.detail {
		row.Detail = &retiredDetail{minAge: r.minAge, shell: shell, category: class,
			done: true, holder: holder, versionIDs: heldIDs, pending: tail, planned: planned}
	}
	return row, true, nil
}

// emptyRetiredVersion returns a retired version of shell, on holder, that holds
// no rows and that op opID itself made (nil when there is none): what a retire
// cut off between creating the version and the commit point leaves behind, found
// again by a resume of the SAME op. The oldest is returned so repeated retries
// converge on one.
//
// A version another op made is never returned, even when it is empty: the op
// that adopts it journals no book_version_create for it, so reverting the op that
// MADE it would delete a version holding the adopter's retired rows (review S3 of
// #4144), and an empty version is also what a later restore or unretire leaves,
// which is not a cut-off retire's. A later op mints its own version instead and
// the empty one stays, empty.
func (r *retiredRun) emptyRetiredVersion(shell, holder, opID string) (*database.BookVersion, error) {
	if r.retired == nil {
		return nil, nil
	}
	vs, err := r.retired.GetRetiredVersionsFromBook(shell)
	if err != nil {
		return nil, fmt.Errorf("read the retired versions of %s: %w", shell, err)
	}
	var held []database.BookFile
	heldRead := false
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].CreatedAt.Before(vs[j].CreatedAt) })
	for i := range vs {
		if vs[i].BookID != holder || vs[i].Status != database.BookVersionStatusRetired || vs[i].RetireOpID != opID {
			continue
		}
		if !heldRead {
			if held, err = r.retired.GetRetiredBookFiles(holder); err != nil {
				return nil, fmt.Errorf("read the retired rows %s holds: %w", holder, err)
			}
			heldRead = true
		}
		n := 0
		for j := range held {
			if held[j].VersionID == vs[i].ID {
				n++
			}
		}
		if n == 0 {
			v := vs[i]
			return &v, nil
		}
	}
	return nil, nil
}

// pendingTail reads what steps 4 to 6 of a retire still have to do for shell.
// Of the shell's live external ids only the planned ones count as pending
// (plannedKey); the rest are counted as unplanned and left alone.
func (r *retiredRun) pendingTail(shellID string, planned map[string]bool) (retiredTail, error) {
	var t retiredTail
	exts, err := r.store.GetExternalIDsForBook(shellID)
	if err != nil {
		return t, fmt.Errorf("read the external ids of %s: %w", shellID, err)
	}
	for _, e := range exts {
		switch {
		case e.Tombstoned:
		case planned[plannedKey(e.Source, e.ExternalID)]:
			t.externalIDs++
		default:
			t.unplanned++
		}
	}
	b, err := r.book(shellID)
	if err != nil {
		return t, fmt.Errorf("read book %s: %w", shellID, err)
	}
	t.mergedInto = b != nil && retiredDeref(b.MergedIntoBookID) == ""
	chain, err := r.retired.GetRetiredBookFiles(shellID)
	if err != nil {
		return t, fmt.Errorf("read the retired rows %s holds: %w", shellID, err)
	}
	t.chainRows = len(chain)
	return t, nil
}

// retireRow is one book_file row of a retire: its id and the version it
// carried before (the journal records it so an undo restores it exactly).
type retireRow struct {
	ID            string
	PrevVersionID string
}

// retireWriter is the journaled write surface of one retire, and the seam PR 5
// plugs into. Every method journals before it writes and writes nothing if the
// journal row cannot be recorded. The fixer reaches the store only through it
// for these six writes; tests wrap it to observe the order and the calls.
//
// The rows it journals are the undo package's own (undo.ChangeTypeBookVersionCreate,
// ChangeTypeBookFileRetire, ChangeTypeRetiredVersionRehome, with RetirePlace
// values and BookVersionField), so an op revert undoes them.
//
// TODO(retire plan 6.2): internal/repairs now has Writer.RetireVersion (which
// also links a retire to the combine it absorbed, CombineJournalID) and
// Writer.RehomeRetiredVersion. Moving journaledRetireWriter's
// CreateRetiredVersion, RetireRows and RehomeVersion onto them, and passing the
// combine journal id so combine_undo_aware rows stop being held, is the part-5
// integration still to do; this interface does not change.
type retireWriter interface {
	// RecordPlan journals the decision a run worked from (repair_plan_record,
	// FieldName "row:<shellID>") on the holder, before the run's first write.
	RecordPlan(holderID, shellID string, rec []byte) error
	// CreateRetiredVersion journals and creates the retired version v on the
	// holder. A version that an earlier, cut-off run of the same retire made
	// is found, not made again; on a resume the journaled version replaces v.
	CreateRetiredVersion(holderID string, v *database.BookVersion) error
	// RetireRows journals one book_file_retire row per row, then re-keys all of
	// them from shellID to holderID as retired rows of versionID in one atomic
	// batch. Nothing is deleted.
	RetireRows(shellID, holderID, versionID string, rows []retireRow) error
	// ReassignExternalID moves one external-id mapping from the shell to the
	// holder (external_id_reassign).
	ReassignExternalID(shellID, holderID string, m database.ExternalIDMapping) error
	// SetMergedInto sets the shell's merged-into book to the holder when it is
	// empty (book_merged_into) and says whether it wrote.
	SetMergedInto(shellID, holderID string) (bool, error)
	// RehomeVersion moves a retired version, and its retired rows, the shell
	// holds onto the holder (retired_version_rehome).
	RehomeVersion(versionID, shellID, holderID string) error
}

// retiredRowStore is the store surface the journaled adapter writes through,
// resolved from the run's store with database.AsCapability (it is not on
// database.Store, and a bare assertion would miss it behind the production
// decorator).
type retiredRowStore interface {
	GetBookVersion(id string) (*database.BookVersion, error)
	CreateBookVersion(v *database.BookVersion) (*database.BookVersion, error)
	RetireBookFiles(moves []database.BookFileMove, holderID, versionID string) error
	RehomeRetiredVersion(versionID, newHolderID string) error
}

type retiredBookGetter interface {
	GetBookByID(id string) (*database.Book, error)
}

type retiredExternalIDMover interface {
	ReassignExternalID(source, externalID, newBookID string) error
}

// journaledRetireWriter is retireWriter over repairs.Writer's existing
// journaling primitives (Journal, JournalStep, JournalSteps, Step).
type journaledRetireWriter struct {
	w     *repairs.Writer
	rows  retiredRowStore
	books retiredBookGetter
	ext   retiredExternalIDMover
}

func (f *retiredVersionsFixer) newJournaledRetireWriter(w *repairs.Writer) (retireWriter, error) {
	ops := f.p.deps.OpsStore()
	if ops == nil {
		return nil, errors.New("database not initialized")
	}
	rows, ok := database.AsCapability[retiredRowStore](ops)
	if !ok {
		return nil, errors.New("the store cannot retire book_file rows (no RetiredBookFileWriter), so nothing is written")
	}
	return &journaledRetireWriter{w: w, rows: rows, books: ops, ext: ops}, nil
}

func (a *journaledRetireWriter) RecordPlan(holderID, shellID string, rec []byte) error {
	return a.w.Journal(holderID, undo.ChangeTypeRepairPlanRecord, "row:"+shellID, "", string(rec))
}

func (a *journaledRetireWriter) CreateRetiredVersion(holderID string, v *database.BookVersion) error {
	field := undo.BookVersionField(v.ID)
	// A resumed run reuses the version its cut-off run journaled: its RetiredAt
	// is a clock reading, so a second candidate would journal a second,
	// different row for the same version.
	prev, ok, err := a.w.JournaledValue(holderID, undo.ChangeTypeBookVersionCreate, field)
	if err != nil {
		return err
	}
	if ok {
		var jv database.BookVersion
		if err := json.Unmarshal([]byte(prev), &jv); err != nil {
			return fmt.Errorf("the journaled version %s is unreadable: %w", v.ID, err)
		}
		*v = jv
	}
	enc, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode version %s: %w", v.ID, err)
	}
	return a.w.JournalStep(holderID, repairs.UndoEntry{ChangeType: undo.ChangeTypeBookVersionCreate, Field: field, New: string(enc)}, func() error {
		cur, err := a.rows.GetBookVersion(v.ID)
		if err != nil {
			return fmt.Errorf("read version %s: %w", v.ID, err)
		}
		if cur != nil {
			if cur.Status == database.BookVersionStatusRetired && cur.BookID == holderID && cur.RetiredFromBookID == v.RetiredFromBookID {
				return nil // made by an earlier run of this same retire
			}
			return fmt.Errorf("%w: version %s already exists and is not this retire's", repairs.ErrChangedSincePlan, v.ID)
		}
		_, err = a.rows.CreateBookVersion(v)
		return err
	})
}

func (a *journaledRetireWriter) RetireRows(shellID, holderID, versionID string, rows []retireRow) error {
	steps := make([]repairs.BookStep, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		// The place values the undo's DecodeBookFileRetire reads (plan
		// section 6.2): where the row stood, and where the retire put it.
		old := undo.RetirePlace{BookID: shellID, VersionID: r.PrevVersionID}.Encode()
		nw := undo.RetirePlace{BookID: holderID, VersionID: versionID}.Encode()
		steps[i] = repairs.BookStep{BookID: holderID, Entry: repairs.UndoEntry{
			ChangeType: undo.ChangeTypeBookFileRetire, Field: "book_file:" + r.ID, Old: old, New: nw}}
		ids[i] = r.ID
	}
	return a.w.JournalSteps(steps, func() ([]bool, error) {
		err := a.rows.RetireBookFiles([]database.BookFileMove{{FileIDs: ids, SourceBookID: shellID}}, holderID, versionID)
		// A batch whose fsync failed was still applied: its journal rows must
		// stand (voiding them would leave committed rows nothing can undo).
		applied := err == nil || errors.Is(err, database.ErrBookFileDurabilityUnknown)
		done := make([]bool, len(steps))
		for i := range done {
			done[i] = applied
		}
		return done, err
	})
}

func (a *journaledRetireWriter) ReassignExternalID(shellID, holderID string, m database.ExternalIDMapping) error {
	return a.w.Step(shellID, undo.ChangeTypeExternalIDReassign, "external_id:"+m.Source+"/"+m.ExternalID, shellID, holderID, func() error {
		return a.ext.ReassignExternalID(m.Source, m.ExternalID, holderID)
	})
}

func (a *journaledRetireWriter) SetMergedInto(shellID, holderID string) (bool, error) {
	b, err := a.books.GetBookByID(shellID)
	if err != nil {
		return false, fmt.Errorf("read book %s: %w", shellID, err)
	}
	if b == nil {
		return false, fmt.Errorf("%w: book %s is gone", repairs.ErrChangedSincePlan, shellID)
	}
	if retiredDeref(b.MergedIntoBookID) != "" {
		return false, nil // never overwrite a link something else wrote
	}
	target := holderID
	wrote := false
	// JournalSteps, not JournalStep: a Modify that finds the link set between
	// the read above and the write skips (ErrSkipBookWrite), which is not a
	// write, so its journal row is voided and the caller is told it did not
	// write.
	err = a.w.JournalSteps([]repairs.BookStep{{BookID: shellID, Entry: repairs.UndoEntry{
		ChangeType: undo.ChangeTypeBookMergedInto, Field: "merged_into_book_id", Old: "", New: holderID}}}, func() ([]bool, error) {
		set := false
		_, merr := a.w.Modify(shellID, func(cur *database.Book) error {
			set = false // fn may be retried; only the last attempt counts
			if retiredDeref(cur.MergedIntoBookID) != "" {
				return database.ErrSkipBookWrite
			}
			cur.MergedIntoBookID = &target
			set = true
			return nil
		})
		wrote = merr == nil && set
		return []bool{wrote}, merr
	})
	return wrote && err == nil, err
}

func (a *journaledRetireWriter) RehomeVersion(versionID, shellID, holderID string) error {
	return a.w.JournalStep(holderID, repairs.UndoEntry{ChangeType: undo.ChangeTypeRetiredVersionRehome, Field: undo.BookVersionField(versionID), Old: shellID, New: holderID}, func() error {
		return a.rows.RehomeRetiredVersion(versionID, holderID)
	})
}

// retiredVersionID is the id of the version one apply op mints for one shell.
// It is a function of (op id, shell id) so a resumed run, which replays the
// same op id, finds the version its cut-off run made instead of minting a
// second.
func retiredVersionID(opID, shellID string) string {
	sum := sha256.Sum256([]byte(opID + "\x00" + shellID))
	return "rv" + strings.ToUpper(hex.EncodeToString(sum[:12]))
}

// retirePlanRecord is the decision journaled before the first write.
type retirePlanRecord struct {
	Fixer     string   `json:"fixer"`
	RowID     string   `json:"row_id"`
	ShellID   string   `json:"shell_id"`
	TargetID  string   `json:"target_id"`
	Method    string   `json:"method"`
	Category  string   `json:"category"`
	VersionID string   `json:"version_id"`
	FileIDs   []string `json:"file_ids"`
	// ExternalIDs are the shell's live external ids, read under the merge lock
	// before the first write: the ones the retire moves to the holder, and the
	// only ones its tail ever moves (S2 of the review of #4144).
	ExternalIDs []plannedExternalID `json:"external_ids"`
	Fingerprint string              `json:"fingerprint"`
}

// plannedExternalID is one external id a retire planned to move.
type plannedExternalID struct {
	Source     string `json:"source"`
	ExternalID string `json:"external_id"`
}

func plannedKey(source, externalID string) string { return source + "\x00" + externalID }

// plannedExternalIDs returns the external ids (plannedKey) the plan records of
// the retired versions that hold rows for shell name, and how many of those
// versions have no record to read. A record is found through the version's
// RetireOpID and VersionID; a missing one (a purged journal, a version made
// outside this fixer) contributes nothing, so its ids are never moved.
func (r *retiredRun) plannedExternalIDs(shell string, held []database.BookVersion) (map[string]bool, int, error) {
	planned := map[string]bool{}
	missing := 0
	for i := range held {
		recs, err := r.opPlanRecords(held[i].RetireOpID)
		if err != nil {
			return nil, 0, err
		}
		found := false
		for _, rec := range recs[shell] {
			if rec.VersionID != held[i].ID {
				continue
			}
			found = true
			for _, e := range rec.ExternalIDs {
				planned[plannedKey(e.Source, e.ExternalID)] = true
			}
		}
		if !found {
			missing++
		}
	}
	return planned, missing, nil
}

// opPlanRecords reads the plan records this fixer journaled in op opID, by shell,
// once per apply session or plan (retiredShared.plans): a tail row would
// otherwise read the whole journal of its retire op, under the merge lock, once
// per shell. An op's plan records do not change once its run has ended, and a
// resume of it runs before the next plan, so the cache is not stale.
func (r *retiredRun) opPlanRecords(opID string) (map[string][]retirePlanRecord, error) {
	if opID == "" {
		return nil, nil
	}
	return cachedOnce(r.plans, opID, func() (map[string][]retirePlanRecord, error) {
		changes, err := r.store.GetOperationChanges(opID)
		if err != nil {
			return nil, fmt.Errorf("read the journal of operation %s: %w", opID, err)
		}
		out := map[string][]retirePlanRecord{}
		for _, c := range changes {
			if c == nil || c.Voided || c.ChangeType != undo.ChangeTypeRepairPlanRecord || !strings.HasPrefix(c.FieldName, "row:") {
				continue
			}
			var rec retirePlanRecord
			if json.Unmarshal([]byte(c.NewValue), &rec) != nil || rec.Fixer != retiredVersionsFixerID || rec.ShellID == "" {
				continue
			}
			out[rec.ShellID] = append(out[rec.ShellID], rec)
		}
		return out, nil
	})
}

// retiredProvenance is the version's MetadataJSON: what the shell was, so a
// reader of the retired version (and the "restore as a separate book" action)
// can say where it came from. The purge snapshot carries the rest.
type retiredProvenance struct {
	ShellTitle       string   `json:"shell_title"`
	ShellNarrator    string   `json:"shell_narrator,omitempty"`
	ShellAuthorID    *int     `json:"shell_author_id,omitempty"`
	VersionGroupID   string   `json:"version_group_id,omitempty"`
	Category         string   `json:"category"`
	Method           string   `json:"resolution_method,omitempty"`
	Rows             int      `json:"rows"`
	RowsMissing      int      `json:"rows_missing,omitempty"`
	RowsSamePath     int      `json:"rows_same_path,omitempty"`
	SharesPathsWith  []string `json:"shares_paths_with,omitempty"`
	RowsITunes       int      `json:"rows_itunes,omitempty"`
	TotalSizeBytes   int64    `json:"total_size_bytes,omitempty"`
	TotalDurationSec int      `json:"total_duration_seconds,omitempty"`
}

func (d *retiredDetail) provenance() retiredProvenance {
	p := retiredProvenance{ShellTitle: d.shell.Title, ShellNarrator: retiredDeref(d.shell.Narrator), ShellAuthorID: d.shell.AuthorID,
		VersionGroupID: d.groupID, Category: d.category, Method: d.targetMethod, Rows: len(d.files),
		RowsSamePath: d.sharedRows, SharesPathsWith: d.sharedWith, RowsITunes: d.itunesRows}
	for i := range d.files {
		if d.files[i].Missing {
			p.RowsMissing++
		}
		p.TotalSizeBytes += d.files[i].FileSize
		p.TotalDurationSec += d.files[i].Duration
	}
	return p
}

// dominantFormat is the most common non-empty Format among the rows.
func dominantFormat(files []database.BookFileCore) string {
	count := map[string]int{}
	best := ""
	for i := range files {
		f := strings.ToLower(files[i].Format)
		if f == "" {
			continue
		}
		count[f]++
		if count[f] > count[best] || (count[f] == count[best] && f < best) {
			best = f
		}
	}
	return best
}

// retiredApplyErr maps a store refusal to the engine's outcome. An index key
// that cannot be proven free of another live owner is a transient condition
// (memdb warming up): retry_later, the same plan can be applied again. A holder
// or row that is no longer what the plan named is changed_since_plan.
func retiredApplyErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repairs.ErrChangedSincePlan), errors.Is(err, repairs.ErrRetryLater):
		return err
	case errors.Is(err, database.ErrRetireIndexIncomplete):
		return fmt.Errorf("%w: %w", repairs.ErrRetryLater, err)
	case errors.Is(err, repairs.ErrNoWritesWriter):
		// The engine ran this apply without the scan stand-down (it read the
		// flag off), and the flag is on now: nothing was written, run it again.
		return fmt.Errorf("%w: the apply flag changed as the run started: %w", repairs.ErrRetryLater, err)
	case errors.Is(err, database.ErrBookFileOwnerMissing), errors.Is(err, database.ErrBookFileRetired),
		errors.Is(err, database.ErrRetiredVersionMismatch):
		return fmt.Errorf("%w: %w", repairs.ErrChangedSincePlan, err)
	}
	return err
}

// Apply retires one row (see the file comment). fresh is a row from Replan: its
// Detail carries what Replan read. While the flag is off this refuses, as the
// routes and the engine already did.
func (f *retiredVersionsFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	if !f.applyOnFor(ctx) {
		return retiredFlagFlipped(fresh.RowID)
	}
	d, ok := fresh.Detail.(*retiredDetail)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision (it did not come from a re-plan)", retiredVersionsFixerID, fresh.RowID)
	}
	if w == nil {
		return fmt.Errorf("%s: no writer", retiredVersionsFixerID)
	}
	if slices.Contains(fresh.Flags, retFlagCombineUndoAware) {
		return fmt.Errorf("%w: row %s is combine_undo_aware and its retire cannot yet be undone with its combine", repairs.ErrRetryLater, fresh.RowID)
	}
	rw, err := f.retireWriterFor(w)
	if err != nil {
		return err
	}
	// Held for the whole row: a merge must not interleave with rows moving
	// between books. Counted (LockMergeRMW), because this writes book_file
	// rows (merge/serialize.go). The wait renews the stand-down lease and
	// refuses the row, nothing written, if it lapses.
	if err := w.LockWaiting(ctx, "the merge lock", merge.LockMergeRMW, merge.UnlockMergeRMW); err != nil {
		return err
	}
	defer merge.UnlockMergeRMW()

	run, err := f.newRunWithMinAge(ctx, d.minAge)
	if err != nil {
		return err
	}
	run.detail = true
	if run.retired == nil {
		return fmt.Errorf("%s: the store cannot read retired rows, so a retire cannot be checked", retiredVersionsFixerID)
	}
	b, err := run.store.GetBookByID(fresh.RowID)
	if err != nil {
		return fmt.Errorf("read book %s: %w", fresh.RowID, err)
	}
	if b == nil || !b.IsSoftDeleted() {
		return fmt.Errorf("%w: under the merge lock: the book is no longer in the trash", repairs.ErrChangedSincePlan)
	}
	locked, err := run.classify(ctx, b.Core())
	if err != nil {
		return fmt.Errorf("re-check under the merge lock: %w", err)
	}
	ld, _ := locked.Detail.(*retiredDetail)
	if ld == nil {
		return fmt.Errorf("%s: row %s: the re-check returned no decision", retiredVersionsFixerID, fresh.RowID)
	}
	if d.done {
		// Finishing a book whose rows are already retired: only the tail, and
		// only onto the holder the row was re-planned with. A fresh plan's tail
		// row and the re-plan of a cut-off run's planned row both come here.
		if locked.Class != retCatTailPending || !ld.done || ld.holder != d.holder {
			return fmt.Errorf("%w: under the merge lock: %s", repairs.ErrChangedSincePlan, locked.Reason)
		}
		return f.finishRetire(ctx, rw, run, ld.shell.ID, ld.holder, 0, ld.planned)
	}
	if locked.Fingerprint != fresh.Fingerprint || !locked.Applicable() {
		why := locked.Reason
		if !locked.Applicable() {
			why = locked.SkipReason
		}
		return fmt.Errorf("%w: under the merge lock: %s", repairs.ErrChangedSincePlan, why)
	}
	if slices.Contains(locked.Flags, retFlagCombineUndoAware) {
		return fmt.Errorf("%w: row %s became combine_undo_aware; held", repairs.ErrRetryLater, fresh.RowID)
	}
	return f.retire(ctx, w, rw, run, ld, locked)
}

// retire writes steps 1 to 6 for one re-checked row.
func (f *retiredVersionsFixer) retire(ctx context.Context, w *repairs.Writer, rw retireWriter, run *retiredRun, d *retiredDetail, locked repairs.Row) error {
	shell, holder := d.shell.ID, d.targetID
	if holder == "" || len(d.files) == 0 {
		return fmt.Errorf("%s: row %s has no target or no rows to retire", retiredVersionsFixerID, shell)
	}
	// Before the first write: a canceled run has written nothing, which is not
	// the partial state a cancel after a write is.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: canceled before the first write: %w", repairs.ErrRetryLater, err)
	}
	// A version an earlier, cut-off run of THIS op left with no rows is adopted,
	// not joined by a second empty one; one another op left is not (S3).
	adopted, err := run.emptyRetiredVersion(shell, holder, w.OpID())
	if err != nil {
		return err
	}
	versionID := retiredVersionID(w.OpID(), shell)
	if adopted != nil {
		versionID = adopted.ID
	}
	fileIDs := make([]string, len(d.files))
	rows := make([]retireRow, len(d.files))
	for i := range d.files {
		fileIDs[i] = d.files[i].ID
		rows[i] = retireRow{ID: d.files[i].ID, PrevVersionID: d.files[i].VersionID}
	}
	sort.Strings(fileIDs)
	// The external ids this retire will move, read now, under the merge lock and
	// before the first write: the plan record names them and the tail moves
	// only them.
	exts, err := run.store.GetExternalIDsForBook(shell)
	if err != nil {
		return fmt.Errorf("read the external ids of %s: %w", shell, err)
	}
	planned := map[string]bool{}
	plannedIDs := []plannedExternalID{}
	for _, e := range exts {
		if !e.Tombstoned && !planned[plannedKey(e.Source, e.ExternalID)] {
			planned[plannedKey(e.Source, e.ExternalID)] = true
			plannedIDs = append(plannedIDs, plannedExternalID{Source: e.Source, ExternalID: e.ExternalID})
		}
	}
	sort.Slice(plannedIDs, func(i, j int) bool {
		if plannedIDs[i].Source != plannedIDs[j].Source {
			return plannedIDs[i].Source < plannedIDs[j].Source
		}
		return plannedIDs[i].ExternalID < plannedIDs[j].ExternalID
	})
	rec, err := json.Marshal(retirePlanRecord{Fixer: retiredVersionsFixerID, RowID: shell, ShellID: shell, TargetID: holder,
		Method: d.targetMethod, Category: d.category, VersionID: versionID, FileIDs: fileIDs, ExternalIDs: plannedIDs,
		Fingerprint: locked.Fingerprint})
	if err != nil {
		return fmt.Errorf("encode the plan record of %s: %w", shell, err)
	}
	if err := rw.RecordPlan(holder, shell, rec); err != nil {
		return retiredApplyErr(err)
	}
	prov, err := json.Marshal(d.provenance())
	if err != nil {
		return fmt.Errorf("encode the provenance of %s: %w", shell, err)
	}
	retiredAt := run.now.UTC().Truncate(time.Microsecond)
	v := &database.BookVersion{
		ID: versionID, BookID: holder, Status: database.BookVersionStatusRetired,
		Format: dominantFormat(d.files), Source: retiredVersionSource, SourceOriginalPath: d.shell.FilePath,
		RetiredFromBookID: shell, RetireOpID: w.OpID(), RetiredAt: &retiredAt, MetadataJSON: string(prov),
	}
	if d.shell.CreatedAt != nil {
		v.IngestDate = d.shell.CreatedAt.UTC()
	}
	if adopted != nil {
		// Made by an earlier run of this same op, whose journal already holds its
		// create (see emptyRetiredVersion); the rows retired into it are
		// journaled as usual.
		v = adopted
	} else if err := rw.CreateRetiredVersion(holder, v); err != nil {
		return retiredApplyErr(err)
	}
	// The commit point: one atomic batch. A refusal here wrote no row, so it is
	// not partial.
	steps := 0
	if err := rw.RetireRows(shell, holder, v.ID, rows); err != nil {
		if errors.Is(err, database.ErrBookFileDurabilityUnknown) {
			// Applied, durability unknown: the rows are retired.
			return fmt.Errorf("%w: the retire batch was applied but its fsync failed: %w", repairs.ErrPartiallyApplied, err)
		}
		return retiredApplyErr(err)
	}
	steps++
	// TODO(PR 8, #4143): the retire has committed; record it for the decisions
	// ledger here with recordRetiredVersionApplied(f.p.deps.OpsStore(), ev), ev
	// a decisions.RetiredVersionsEvent keyed by
	// decisions.RetiredVersionsSubjectKey(shell). Not wired in this PR because
	// PR 8 is not in this branch's base; wire it when the two are stacked.
	return f.finishRetire(ctx, rw, run, shell, holder, steps, planned)
}

// finishRetire is steps 4 to 6: the shell's external ids, its merged-into link
// and the chain. Every one is idempotent (an id already moved is no longer on
// the shell; merged-into is set only while empty; re-homed rows are no longer
// held by the shell), so it is also what a resume runs. planned is the set of
// external ids (plannedKey) the retire's plan record names: only those move.
// steps counts the writes
// already made by this call's row, so a failure after the commit point is a
// partial apply.
func (f *retiredVersionsFixer) finishRetire(ctx context.Context, rw retireWriter, run *retiredRun, shell, holder string, steps int, planned map[string]bool) error {
	partial := func(err error) error {
		err = retiredApplyErr(err)
		if steps == 0 {
			return err
		}
		return fmt.Errorf("%w: after %d step(s): %w", repairs.ErrPartiallyApplied, steps, err)
	}
	// A resume that has not written yet is a partial apply too: its rows are
	// retired, so a failure leaves the row unfinished, not untouched.
	if steps == 0 {
		steps = 1
	}
	exts, err := run.store.GetExternalIDsForBook(shell)
	if err != nil {
		return partial(fmt.Errorf("read the external ids of %s: %w", shell, err))
	}
	for _, e := range exts {
		if e.Tombstoned {
			// A tombstoned mapping records an id taken off this book: it stays
			// on the shell rather than land on the holder (as every retire does).
			continue
		}
		if !planned[plannedKey(e.Source, e.ExternalID)] {
			// Attached after the retire's plan record (or no record names it):
			// no guard ever judged it, so it stays on the shell.
			continue
		}
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		if err := rw.ReassignExternalID(shell, holder, e); err != nil {
			return partial(fmt.Errorf("move external id %s/%s to %s: %w", e.Source, e.ExternalID, holder, err))
		}
		steps++
	}
	if err := ctx.Err(); err != nil {
		return partial(err)
	}
	wrote, err := rw.SetMergedInto(shell, holder)
	if err != nil {
		return partial(fmt.Errorf("set merged-into of %s: %w", shell, err))
	}
	if wrote {
		steps++
	}
	chain, err := run.retired.GetRetiredBookFiles(shell)
	if err != nil {
		return partial(fmt.Errorf("read the retired rows %s holds: %w", shell, err))
	}
	seen := map[string]bool{}
	var versions []string
	for i := range chain {
		if v := chain[i].VersionID; v != "" && !seen[v] {
			seen[v] = true
			versions = append(versions, v)
		}
	}
	sort.Strings(versions)
	for _, vid := range versions {
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		if err := rw.RehomeVersion(vid, shell, holder); err != nil {
			return partial(fmt.Errorf("re-home retired version %s onto %s: %w", vid, holder, err))
		}
		steps++
	}
	return nil
}
