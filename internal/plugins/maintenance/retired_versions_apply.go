// file: internal/plugins/maintenance/retired_versions_apply.go
// version: 1.0.0
// guid: 5e8a2c71-3b94-4d06-a1f7-92c06d3e8b15
// last-edited: 2026-10-24

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
// RESUMABILITY. A row already retired is a no-op. Replan answers a book whose
// rows are retired as already_retired (skipped); a plan taken after an apply
// therefore reports it done, and a second apply of the same plan applies
// nothing. A run cut off after the commit point but before steps 4 to 6
// finished is finished by the next apply: Replan returns the row applicable
// again under the PLANNED fingerprint with the unfinished steps, and Apply runs
// only those. A run cut off before the commit point left a plan record and
// maybe the version; the version id is derived from (op id, shell id), so the
// resumed run finds it instead of minting a second, and the journal dedupes the
// plan record and the per-row retire records.
//
// WHAT IT DOES NOT DO YET. A shell an unreversed combine journal names as
// absorbed (combine_undo_aware) is held, not retired: plan section 6.5 requires
// the retire to be undone by undoing the combine, and that link lives in
// merge/combine_journal.go, which PR 5 owns. The dedup auto-merge journals
// (dedup:automerge:) are not read either; both are pre-conditions of turning the
// flag on in production (docs/executive-summaries and the PR body say so).

// The change types this PR journals. They are declared HERE because the undo
// package is PR 5's: its revert cases, preflight classification and restorable
// set for these three strings land there.
//
// TODO(PR 5, retire plan 6.2): move these into internal/undo as
// ChangeTypeBookVersionCreate, ChangeTypeBookFileRetire and
// ChangeTypeRetiredVersionRehome (same strings), and let the journaledRetireWriter
// below call the Writer primitives PR 5 adds instead of Writer.JournalStep and
// Writer.JournalSteps. Until then an op revert of a retire does not know how to
// undo these rows; nothing creates one while the flag is off (the default).
const (
	retChangeBookVersionCreate = "book_version_create"
	retChangeBookFileRetire    = "book_file_retire"
	retChangeVersionRehome     = "retired_version_rehome"
)

// retiredVersionSource is BookVersion.Source of a version made by this fixer.
const retiredVersionSource = "retired_book"

// retiredTail is the part of a retire that follows the commit point and is
// still to do for one shell: its external ids not yet on the holder, a
// merged-into still empty, and retired rows the shell holds for other versions
// (the chain case).
type retiredTail struct {
	externalIDs int
	mergedInto  bool
	chainRows   int
}

func (t retiredTail) any() bool { return t.externalIDs > 0 || t.mergedInto || t.chainRows > 0 }

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

	// done is a book whose rows are already retired (retCatAlreadyRetired):
	// holder is where, versionIDs which versions, pending what is unfinished.
	// resume is set by Replan on such a row when the planned row is to be
	// finished rather than skipped.
	done       bool
	resume     bool
	holder     string
	versionIDs []string
	pending    retiredTail
}

// alreadyRetired answers whether shell is a book a finished (or part-finished)
// apply emptied: it owns no live row and a retired version names it as the
// shell. The row is skipped already_retired, with the unfinished steps counted.
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
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].CreatedAt.Before(vs[j].CreatedAt) })
	holder := vs[len(vs)-1].BookID
	ids := make([]string, len(vs))
	for i := range vs {
		ids[i] = vs[i].ID
	}
	tail, err := r.pendingTail(shell.ID)
	if err != nil {
		return repairs.Row{}, false, err
	}
	why := fmt.Sprintf("its file records are already retired onto %s (%d retired version(s)); nothing to write", holder, len(vs))
	if tail.any() {
		why += "; unfinished: " + tail.String()
	}
	cur := retiredCurrent{"rows": "0", "retired_into": holder, "target_book_id": holder}
	cur.putN("retired_versions", len(vs))
	cur.put("tail_pending", tail.String())
	row := repairs.Row{
		RowID: shell.ID, BookIDs: []string{shell.ID, holder}, Title: shell.Title,
		Class: retCatAlreadyRetired, Skipped: retCatAlreadyRetired, SkipReason: why, Reason: why,
		Risk: repairs.RiskLow, Current: cur,
		Members: []repairs.RowMember{{BookID: shell.ID, Title: shell.Title, Role: "shell"}, {BookID: holder, Role: "target"}},
	}
	h := sha256.New()
	for _, p := range append([]string{retCatAlreadyRetired, shell.ID, holder, tail.String()}, ids...) {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	row.Fingerprint = hex.EncodeToString(h.Sum(nil))
	if r.detail {
		row.Detail = &retiredDetail{minAge: r.minAge, shell: shell, category: retCatAlreadyRetired,
			done: true, holder: holder, versionIDs: ids, pending: tail}
	}
	return row, true, nil
}

// pendingTail reads what steps 4 to 6 of a retire still have to do for shell.
func (r *retiredRun) pendingTail(shellID string) (retiredTail, error) {
	var t retiredTail
	exts, err := r.store.GetExternalIDsForBook(shellID)
	if err != nil {
		return t, fmt.Errorf("read the external ids of %s: %w", shellID, err)
	}
	for _, e := range exts {
		if !e.Tombstoned {
			t.externalIDs++
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
// TODO(PR 5, retire plan 6.2): the Writer primitives PR 5 adds in
// internal/repairs (RetireBookFiles, CreateRetiredVersion, RehomeRetiredVersion,
// with their undo.ChangeType* constants and revert cases) replace the bodies of
// journaledRetireWriter's RetireRows, CreateRetiredVersion and RehomeVersion.
// This interface does not change.
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
	field := "book_version:" + v.ID
	// A resumed run reuses the version its cut-off run journaled: its RetiredAt
	// is a clock reading, so a second candidate would journal a second,
	// different row for the same version.
	prev, ok, err := a.w.JournaledValue(holderID, retChangeBookVersionCreate, field)
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
	return a.w.JournalStep(holderID, repairs.UndoEntry{ChangeType: retChangeBookVersionCreate, Field: field, New: string(enc)}, func() error {
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

// retireRowState is the book and version a retired row names, before and after
// (OldValue and NewValue of a book_file_retire row, plan section 6.2).
type retireRowState struct {
	BookID    string `json:"book_id"`
	VersionID string `json:"version_id"`
}

func (a *journaledRetireWriter) RetireRows(shellID, holderID, versionID string, rows []retireRow) error {
	steps := make([]repairs.BookStep, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		old, err := json.Marshal(retireRowState{BookID: shellID, VersionID: r.PrevVersionID})
		if err != nil {
			return err
		}
		nw, err := json.Marshal(retireRowState{BookID: holderID, VersionID: versionID})
		if err != nil {
			return err
		}
		steps[i] = repairs.BookStep{BookID: holderID, Entry: repairs.UndoEntry{
			ChangeType: retChangeBookFileRetire, Field: "book_file:" + r.ID, Old: string(old), New: string(nw)}}
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
	err = a.w.JournalStep(shellID, repairs.UndoEntry{ChangeType: undo.ChangeTypeBookMergedInto, Field: "merged_into_book_id", Old: "", New: holderID}, func() error {
		_, merr := a.w.Modify(shellID, func(cur *database.Book) error {
			if retiredDeref(cur.MergedIntoBookID) != "" {
				return database.ErrSkipBookWrite
			}
			cur.MergedIntoBookID = &target
			return nil
		})
		return merr
	})
	return err == nil, err
}

func (a *journaledRetireWriter) RehomeVersion(versionID, shellID, holderID string) error {
	return a.w.JournalStep(holderID, repairs.UndoEntry{ChangeType: retChangeVersionRehome, Field: "book_version:" + versionID, Old: shellID, New: holderID}, func() error {
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
	Fixer       string   `json:"fixer"`
	RowID       string   `json:"row_id"`
	ShellID     string   `json:"shell_id"`
	TargetID    string   `json:"target_id"`
	Method      string   `json:"method"`
	Category    string   `json:"category"`
	VersionID   string   `json:"version_id"`
	FileIDs     []string `json:"file_ids"`
	Fingerprint string   `json:"fingerprint"`
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
	if !f.applyOn() {
		return fmt.Errorf("%w (row %s)", errRetiredApplyDisabled, fresh.RowID)
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

	run, err := f.newRunWithMinAge(d.minAge)
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
	if d.resume {
		// Finishing a run whose rows are already retired: only the tail.
		if locked.Class != retCatAlreadyRetired || !ld.pending.any() || ld.holder != d.holder {
			return fmt.Errorf("%w: under the merge lock: %s", repairs.ErrChangedSincePlan, locked.Reason)
		}
		return f.finishRetire(ctx, rw, run, ld.shell.ID, ld.holder, 0)
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
	versionID := retiredVersionID(w.OpID(), shell)
	fileIDs := make([]string, len(d.files))
	rows := make([]retireRow, len(d.files))
	for i := range d.files {
		fileIDs[i] = d.files[i].ID
		rows[i] = retireRow{ID: d.files[i].ID, PrevVersionID: d.files[i].VersionID}
	}
	sort.Strings(fileIDs)
	rec, err := json.Marshal(retirePlanRecord{Fixer: retiredVersionsFixerID, RowID: shell, ShellID: shell, TargetID: holder,
		Method: d.targetMethod, Category: d.category, VersionID: versionID, FileIDs: fileIDs, Fingerprint: locked.Fingerprint})
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
	if err := rw.CreateRetiredVersion(holder, v); err != nil {
		return retiredApplyErr(err)
	}
	if err := ctx.Err(); err != nil {
		return err
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
	return f.finishRetire(ctx, rw, run, shell, holder, steps)
}

// finishRetire is steps 4 to 6: the shell's external ids, its merged-into link
// and the chain. Every one is idempotent (an id already moved is no longer on
// the shell; merged-into is set only while empty; re-homed rows are no longer
// held by the shell), so it is also what a resume runs. steps counts the writes
// already made by this call's row, so a failure after the commit point is a
// partial apply.
func (f *retiredVersionsFixer) finishRetire(ctx context.Context, rw retireWriter, run *retiredRun, shell, holder string, steps int) error {
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
