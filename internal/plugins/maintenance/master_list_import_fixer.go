// file: internal/plugins/maintenance/master_list_import_fixer.go
// version: 1.3.0
// guid: 4b0c5f4a-6d0a-4f0e-9d8e-3f1f7a9a52c1
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
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/masterlist"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// masterListImportFixerID is the Repairs-lane id of the master-list import
// (design: docs/design/2026-10-23-master-list-import-design.md).
const masterListImportFixerID = "master-list-import"

// mlLibraryPage is the page size of the whole-library read.
const mlLibraryPage = 5000

// masterListImportFixer imports an uploaded master list (a CSV or JSON file of
// books with their ISBN, ASIN and read status) into one user's read state. It
// plans one row per INPUT row, classed:
//
//   - matched_high: matched by an identifier (ASIN, ISBN, source id) to one
//     live book with nothing against it. Applicable: Apply writes the list's
//     read status to that book for the target user (see below) unless the
//     decision pass finds a reason not to, which then is the row's class
//     (skipped_already_finished, skipped_newer_local, skipped_user_abandoned,
//     skipped_no_timestamp, skipped_no_progress, skipped_local_progress,
//     skipped_manual_unstarted, skipped_no_status, skipped_not_started,
//     skipped_list_abandoned, skipped_future_timestamp, error_state_unreadable).
//   - matched_review: a match a person confirms: by title and author, a
//     swapped title, a runtime or series disagreement, a list-side data problem,
//     a trashed copy followed to its survivor, or a book two list rows claim.
//   - ambiguous: several books fit.
//   - unmatched: no live book fits, and the row might still be one.
//   - read_elsewhere: no live book fits and nothing in the library could be it
//     (no title candidate, no trashed copy carrying an identifier). Applicable:
//     Apply keeps the row as a read-elsewhere record (never as a book).
//     skipped_already_recorded when the record exists, under this identifier or
//     any other the same book carries in another list. A row with a data problem
//     of its own (bad identifier, bad or future time, conflicting identifiers) is
//     held as unmatched instead: a dropped identifier may have been the one that
//     matched.
//   - duplicate_in_list: the row names the same book as an earlier row (same
//     ASIN, ISBN-13 or source id, transitively); the earlier row carries the
//     combined status and the match.
//   - invalid: the row names no book at all.
//   - error_unreadable: a store read the row needs failed.
//
// Finer reasons are row flags (the lane filters on flag:<name>).
//
// WRITES, for the target user only (never the caller; required), like the
// Audible import and under the same rules: a finished row sets Finished
// (manual, 100%, finished_at and last activity = the list's finish time; a book
// with no position also gets one at its end so ABS clients list it); an
// in-progress row writes the list's position and percent with its last-listened
// time, only when the user has no local progress on the book or any copy of it.
// Nothing is ever un-finished, newer local activity wins, and a state the user
// set (abandoned, or unstarted by hand) is theirs. A finish with no finish
// time, or an in-progress row with no last-listened time, is skipped: the
// import never dates anything now. Each write goes through the Repairs writer,
// is journaled first (undo.ChangeTypeUserBookStateSet) and is undone by the
// apply op's revert while the state is still what the import wrote.
//
// Plan reads and writes nothing; neither does a dry-run apply.
type masterListImportFixer struct {
	p *Plugin
	// now is the clock the future-timestamp check reads.
	now func() time.Time
}

func newMasterListImportFixer(p *Plugin) *masterListImportFixer {
	return &masterListImportFixer{p: p, now: time.Now}
}

var (
	_ repairs.Fixer            = (*masterListImportFixer)(nil)
	_ repairs.ApplyScoped      = (*masterListImportFixer)(nil)
	_ repairs.PermissionScoped = (*masterListImportFixer)(nil)
)

func (f *masterListImportFixer) ID() string { return masterListImportFixerID }
func (f *masterListImportFixer) Title() string {
	return "Master list read-status import"
}
func (f *masterListImportFixer) Description() string {
	return "Imports an uploaded master list (POST /api/v1/master-lists) into one user's read status. Plan params: " +
		"list_id (the upload's id) and target_user_id (required, never defaulted). Every input row is planned once: " +
		"matched by ASIN, then ISBN, then source id (high confidence), then by title and author (review); rows " +
		"sharing an identifier are folded into the first; a trashed book is followed to its live survivor and never " +
		"a target. Apply writes a matched row's finished or in-progress state for that user (never un-finishing, " +
		"never over newer local activity or a state the user set) and keeps a row that matches no library book as a " +
		"read-elsewhere record. Classes: matched_high, matched_review, ambiguous, unmatched, read_elsewhere, " +
		"duplicate_in_list, invalid, and the skipped_* reasons of a matched row. Apply is a dry run unless told " +
		"otherwise, and every write is journaled and can be undone."
}

// RequiredPermissions implements repairs.PermissionScoped: the plan names any
// user as target_user_id and reads an uploaded list, so starting a plan or an
// apply is a user-management act, above the lane's default
// library.edit_metadata. Without it an editor could plan against another user's
// list. It is the same permission the master-list routes demand
// (wire_master_list_routes.go), so one grant covers the whole import.
//
// It gates the writes (StartPlan, StartApply, OwnerApply) only. The READS of a
// finished plan are not gated by it yet: GET /repairs/:fixer/plan/:op/rows and
// the last_plan / last_apply op ids in GET /repairs need only library.view, and
// each row carries the list row, the target user and (for a matched row) that
// user's current state on the book. Until falkcorp/audiobook-organizer#4148
// (the Repairs framework applies RequiredPermissions to those reads) merges, do
// not read this fixer's rows as private to users.manage holders.
func (f *masterListImportFixer) RequiredPermissions() []auth.Permission {
	return []auth.Permission{auth.PermUsersManage}
}

// mlParams are the plan params.
type mlParams struct {
	ListID       string `json:"list_id"`
	TargetUserID string `json:"target_user_id"`
}

// parseMLParams reads and checks the params the way parseARSParams does: a
// target user that exists and is not the iTunes sync user, never defaulted.
func parseMLParams(raw json.RawMessage, users UserReadStateStore) (mlParams, error) {
	var p mlParams
	if len(raw) == 0 || string(raw) == "null" {
		return p, fmt.Errorf("%s: params are required: list_id and target_user_id", masterListImportFixerID)
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("%s: parse params: %w", masterListImportFixerID, err)
	}
	p.ListID = strings.TrimSpace(p.ListID)
	p.TargetUserID = strings.TrimSpace(p.TargetUserID)
	if p.ListID == "" {
		return p, fmt.Errorf("%s: list_id is required (the id POST /api/v1/master-lists returned)", masterListImportFixerID)
	}
	if p.TargetUserID == "" {
		return p, fmt.Errorf("%s: target_user_id is required (the user whose read status the list is for; it is never defaulted)", masterListImportFixerID)
	}
	if p.TargetUserID == arsLocalUserID {
		return p, fmt.Errorf("%s: target_user_id %q is the iTunes sync user; its finishes are pushed into the iTunes library, which this import must not reach", masterListImportFixerID, arsLocalUserID)
	}
	u, err := users.GetUserByID(p.TargetUserID)
	if err != nil {
		return p, fmt.Errorf("%s: read user %s: %w", masterListImportFixerID, p.TargetUserID, err)
	}
	if u == nil {
		return p, fmt.Errorf("%s: target_user_id %s is not a user", masterListImportFixerID, p.TargetUserID)
	}
	return p, nil
}

// mlRowState is what a planned row stores (Row.State).
type mlRowState struct {
	ListID string `json:"list_id"`
	User   string `json:"user"`
	// Lines are the input lines the row stands for (a folded group's, in
	// input order). Row is the combined list row; for a duplicate_in_list row
	// it is that line's own row.
	Lines []int          `json:"lines"`
	Row   masterlist.Row `json:"row"`
	// Representative is the row id of the group's first row (duplicates only).
	Representative string   `json:"representative,omitempty"`
	Target         string   `json:"target,omitempty"`
	Tier           string   `json:"tier,omitempty"`
	Candidates     []string `json:"candidates,omitempty"`
	Trashed        []string `json:"trashed,omitempty"`
	// Hits are the live books the matching identifier hit, before resolution
	// to a version-group primary: their listening counts as the target's.
	Hits []string `json:"hits,omitempty"`
	// UploadedBy is the uploader of the list the row came from: a read-elsewhere
	// record names who put the book on record.
	UploadedBy string `json:"uploaded_by,omitempty"`
}

func (f *masterListImportFixer) deps() (OpsStore, UserReadStateStore, MasterListStore, ExternalIDBookLookup, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, nil, nil, nil, fmt.Errorf("database not initialized")
	}
	us := f.p.deps.UserReadStateStore()
	if us == nil {
		return nil, nil, nil, nil, fmt.Errorf("%s: the store cannot read user listening state", masterListImportFixerID)
	}
	lists := f.p.deps.MasterLists()
	if lists == nil {
		return nil, nil, nil, nil, fmt.Errorf("%s: the store cannot keep master lists", masterListImportFixerID)
	}
	ext := f.p.deps.ExternalIDBooks()
	if ext == nil {
		return nil, nil, nil, nil, fmt.Errorf("%s: the store cannot look books up by external id", masterListImportFixerID)
	}
	return store, us, lists, ext, nil
}

// readList loads and decodes the list's rows. A row that does not decode is a
// stored list that is damaged: the plan fails rather than skipping it.
func readList(lists MasterListStore, id string) (*database.MasterListMeta, []masterlist.Row, error) {
	meta, err := lists.GetList(id)
	if err != nil {
		if errors.Is(err, database.ErrMasterListNotFound) {
			return nil, nil, fmt.Errorf("%s: master list %q does not exist", masterListImportFixerID, id)
		}
		return nil, nil, fmt.Errorf("%s: read list %s: %w", masterListImportFixerID, id, err)
	}
	rows := make([]masterlist.Row, 0, meta.RowCount)
	err = lists.ScanRows(id, func(n int, raw []byte) error {
		var r masterlist.Row
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("row %d does not decode: %w", n, err)
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: read rows of list %s: %w", masterListImportFixerID, id, err)
	}
	return meta, rows, nil
}

// loadMLLibrary reads the whole library once. GetAllBooksCoreComplete, not
// the memdb-served twin: a missing book would read as "unmatched".
func loadMLLibrary(ctx context.Context, store OpsStore) (*mlLibrary, error) {
	var cores []database.BookCore
	for offset := 0; ; offset += mlLibraryPage {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := store.GetAllBooksCoreComplete(mlLibraryPage, offset)
		if err != nil {
			return nil, fmt.Errorf("GetAllBooksCoreComplete at offset %d: %w", offset, err)
		}
		cores = append(cores, page...)
		if len(page) < mlLibraryPage {
			break
		}
	}
	// The core listings leave the trash out. It is read by its own reader
	// (with the Pebble fallback) because a trashed book is how an identifier
	// reaches the live survivor it was merged into, and how a row learns that
	// only a trashed copy carries it.
	trash, err := store.ListSoftDeletedBooks(0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("ListSoftDeletedBooks: %w", err)
	}
	for i := range trash {
		cores = append(cores, trash[i].Core())
	}
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("GetAllAuthors: %w", err)
	}
	series, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("GetAllSeries: %w", err)
	}
	return buildMLLibrary(cores, authors, series), nil
}

// loadMatcher reads the whole library and the merge journals once and
// builds the matcher every row of a plan, or of an apply run, is classified
// against.
func (f *masterListImportFixer) loadMatcher(ctx context.Context, store OpsStore, ext ExternalIDBookLookup) (*mlMatcher, error) {
	lib, err := loadMLLibrary(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", masterListImportFixerID, err)
	}
	follows, err := merge.FlagHolderFollowsFrom(store)
	if err != nil {
		return nil, fmt.Errorf("%s: read merge journals: %w", masterListImportFixerID, err)
	}
	return &mlMatcher{store: store, ext: ext, lib: lib, follows: follows}, nil
}

// mlApplyScope is the library snapshot of one apply run (ApplyScoped): the
// first row that needs it loads it, every later row's re-check shares it.
type mlApplyScope struct {
	once sync.Once
	m    *mlMatcher
	err  error
	// elsewhere is the run's read-elsewhere index (mlElsewhereIndex).
	elsewhere mlElsewhereIndex
}

type mlApplyScopeKey struct{}

// BeginApply implements repairs.ApplyScoped: the apply's context carries one
// lazily loaded library snapshot, so a re-check is a lookup rather than a
// whole-library read per row, and nothing outlives the run.
func (f *masterListImportFixer) BeginApply(ctx context.Context, _ bool) (context.Context, func()) {
	return context.WithValue(ctx, mlApplyScopeKey{}, &mlApplyScope{}), func() {}
}

// matcherFor is the run's shared snapshot, or a fresh one when Replan is called
// outside an apply run.
func (f *masterListImportFixer) matcherFor(ctx context.Context, store OpsStore, ext ExternalIDBookLookup) (*mlMatcher, error) {
	sc, ok := ctx.Value(mlApplyScopeKey{}).(*mlApplyScope)
	if !ok {
		return f.loadMatcher(ctx, store, ext)
	}
	sc.once.Do(func() { sc.m, sc.err = f.loadMatcher(ctx, store, ext) })
	return sc.m, sc.err
}

// elsewhereFor is the run's shared read-elsewhere index, or a fresh one when
// Replan is called outside an apply run.
func (f *masterListImportFixer) elsewhereFor(ctx context.Context) *mlElsewhereIndex {
	if sc, ok := ctx.Value(mlApplyScopeKey{}).(*mlApplyScope); ok {
		return &sc.elsewhere
	}
	return &mlElsewhereIndex{}
}

// Plan matches every list row against the library, then decides each matched
// or unmatched-for-good row against the target user's state. Read-only.
func (f *masterListImportFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store, us, lists, ext, err := f.deps()
	if err != nil {
		return nil, err
	}
	params, err := parseMLParams(raw, us)
	if err != nil {
		return nil, err
	}
	meta, input, err := readList(lists, params.ListID)
	if err != nil {
		return nil, err
	}
	m, err := f.loadMatcher(ctx, store, ext)
	if err != nil {
		return nil, err
	}
	now := f.now()
	for i := range input {
		// A list time in the future is held for review with the row's other
		// findings; the finding is recorded on the row, not the stored list.
		input[i].Issues = append(slices.Clone(input[i].Issues), input[i].FutureTimestamps(now, mlFutureSlack)...)
	}

	groups := masterlist.Collapse(input)
	outcomes := make([]mlOutcome, len(groups))
	var done atomic.Int64
	// Each worker writes only outcomes[i] for its own i; the library, the
	// merge journals and the store are read-only. Groups are disjoint sets of
	// input rows, so no two workers ever plan the same row.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(groups)), func(_ context.Context, i int) error {
		defer done.Add(1)
		outcomes[i] = m.outcome(groups[i].Row)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: arsPlanWorkers,
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("List rows %d/%d", done.Load(), total) },
	})
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, runErr
	}

	rows := f.assemble(meta, params, m.lib, input, groups, outcomes)
	mlMarkDuplicateTargets(rows)
	mlMarkPossibleDuplicates(rows)

	// The decision pass: what each matched row would write, read against the
	// target user's state as it is now. Each worker writes only rows[i] for
	// its own i; rows of one book never share a target (duplicate targets were
	// demoted above), and the reads are of the user's state, never writes.
	d := &mlDecider{now: f.now, us: us, lists: lists, m: m, elsewhere: &mlElsewhereIndex{}}
	var decide []int
	needs := make([]bool, len(rows))
	states := make([]mlRowState, len(rows))
	for i := range rows {
		if err := json.Unmarshal(rows[i].State, &states[i]); err != nil {
			return nil, fmt.Errorf("%s: row %s: decode own state: %w", masterListImportFixerID, rows[i].RowID, err)
		}
		if needs[i] = mlNeedsDecision(rows[i], states[i]); needs[i] {
			decide = append(decide, i)
		}
	}
	var decided atomic.Int64
	runErr = registry.RunItems(ctx, rep, decide, func(_ context.Context, i int) error {
		defer decided.Add(1)
		rows[i] = d.decide(rows[i], states[i])
		return nil
	}, registry.RunItemsOptions{
		Concurrency: arsPlanWorkers,
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Deciding %d/%d", decided.Load(), total) },
	})
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, runErr
	}
	for i := range rows {
		if !needs[i] {
			mlFinalize(&rows[i])
		}
	}
	return rows, nil
}

// mlGlobalFlags are the flags only the whole plan can set (they compare rows
// with each other). A re-plan of one row carries the plan's over: it cannot
// recompute them, and an applicable row can only have possible_duplicate.
var mlGlobalFlags = []string{mlFlagDuplicateTgt, mlFlagPossibleDup}

// Replan re-runs one row's whole pipeline on the library as it is now: it
// classifies the stored list row again against a snapshot shared by the apply
// run (BeginApply), builds the row, and decides it against the user's state as
// it is now. Everything the plan decided from is therefore re-read, and a
// change in any of it changes the fingerprint (changed_since_plan). The list
// itself is not re-read: the row stored the list row it was planned from, so a
// list deleted or pruned since cannot break a pending apply.
func (f *masterListImportFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	if planned.Skipped != "" {
		return planned, nil // not applicable: nothing to re-check
	}
	store, us, lists, ext, err := f.deps()
	if err != nil {
		return repairs.Row{}, err
	}
	var st mlRowState
	if err := json.Unmarshal(planned.State, &st); err != nil {
		return repairs.Row{}, fmt.Errorf("row %s: decode plan state: %w", planned.RowID, err)
	}
	if st.User == "" || st.ListID == "" {
		return repairs.Row{}, fmt.Errorf("row %s: the plan stored no list or user", planned.RowID)
	}
	// parseMLParams' check of the target user, repeated: the plan stored the id,
	// and a dry run should not report would_apply for a user that is gone.
	if err := f.recheckUser(st.User); err != nil {
		return repairs.Row{}, fmt.Errorf("row %s: %w", planned.RowID, err)
	}
	m, err := f.matcherFor(ctx, store, ext)
	if err != nil {
		return repairs.Row{}, err
	}
	provenance := ""
	if len(planned.Evidence) > 0 {
		provenance = planned.Evidence[0]
	}
	rc := mlRowContext{provenance: provenance, uploadedBy: st.UploadedBy, params: mlParams{ListID: st.ListID, TargetUserID: st.User}}
	fresh := f.matchRow(rc, m.lib, planned.RowID, st.Lines, st.Row, m.outcome(st.Row))
	for _, fl := range mlGlobalFlags {
		if slices.Contains(planned.Flags, fl) && !slices.Contains(fresh.Flags, fl) {
			fresh.Flags = append(fresh.Flags, fl)
		}
	}
	var fst mlRowState
	if err := json.Unmarshal(fresh.State, &fst); err != nil {
		return repairs.Row{}, fmt.Errorf("row %s: decode own state: %w", planned.RowID, err)
	}
	if mlNeedsDecision(fresh, fst) {
		d := &mlDecider{now: f.now, us: us, lists: lists, m: m, elsewhere: f.elsewhereFor(ctx)}
		return d.decide(fresh, fst), nil
	}
	mlFinalize(&fresh)
	return fresh, nil
}

// Apply writes a row through the Writer: a matched row's user state (the Writer
// re-reads it first and refuses, changed_since_plan, when it moved), or a
// read-elsewhere row's record.
//
// The Writer re-reads the TARGET's state under the user-state stripe. The other
// copies' states (arsTarget.related) are read by Replan just before, without
// a lock: the stripe is per book and a holder never takes a second, so a listen
// on another copy that lands between that Replan and the write is not caught.
// The window is one Replan wide.
func (f *masterListImportFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	switch d := fresh.Detail.(type) {
	case *mlDecision:
		if err := w.SetUserStateChecked(ctx, d.user, d.book, d.expect, d.next, func() error { return f.recheckTarget(d.user, d.book) }); err != nil {
			if errors.Is(err, repairs.ErrChangedSincePlan) || errors.Is(err, repairs.ErrPartiallyApplied) {
				return err
			}
			return fmt.Errorf("%s: row %s: %w", masterListImportFixerID, fresh.RowID, err)
		}
		return nil
	case *mlElsewhereDecision:
		return f.applyElsewhere(w, fresh, d)
	}
	return fmt.Errorf("%s: row %s carries no decision", masterListImportFixerID, fresh.RowID)
}

// recheckTarget is what Apply re-reads under the write (the Writer's merge lock
// and the (user, book) stripe), because the apply run's library snapshot is
// loaded once and a row can wait behind thousands of others: the target user
// must still exist, and the matched book must still be a live book. A book
// merged into another is held, not followed: the match was made, and reviewed
// as high confidence, for THIS book, and its survivor's state and copies were
// never read; a re-plan matches the survivor through its trashed twin and files
// it for review (via_trashed). Both refusals wrap repairs.ErrChangedSincePlan,
// so the row reports changed_since_plan with the reason. (The framework guard
// does not cover this: it skips a trashed book as having nothing left to
// protect, it does not refuse it.)
func (f *masterListImportFixer) recheckTarget(user, book string) error {
	if err := f.recheckUser(user); err != nil {
		return err
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("%s: database not initialized", masterListImportFixerID)
	}
	b, err := store.GetBookByID(book)
	switch {
	case err != nil:
		return fmt.Errorf("%s: re-read book %s: %w", masterListImportFixerID, book, err)
	case b == nil:
		return fmt.Errorf("book %s no longer exists: %w", book, repairs.ErrChangedSincePlan)
	case b.MergedIntoBookID != nil && *b.MergedIntoBookID != "":
		return fmt.Errorf("book %s was merged into %s since the plan; re-plan to review the match on the survivor: %w", book, *b.MergedIntoBookID, repairs.ErrChangedSincePlan)
	case b.IsSoftDeleted():
		return fmt.Errorf("book %s was moved to the trash since the plan: %w", book, repairs.ErrChangedSincePlan)
	}
	return nil
}

// recheckUser: the target user still exists, and is still not the iTunes sync
// user (the plan checked both; Replan keeps the plan's user from the row).
func (f *masterListImportFixer) recheckUser(user string) error {
	us := f.p.deps.UserReadStateStore()
	if us == nil {
		return fmt.Errorf("%s: the store cannot read user listening state", masterListImportFixerID)
	}
	if user == arsLocalUserID {
		return fmt.Errorf("%s: target user %q is the iTunes sync user", masterListImportFixerID, user)
	}
	u, err := us.GetUserByID(user)
	if err != nil {
		return fmt.Errorf("%s: re-read user %s: %w", masterListImportFixerID, user, err)
	}
	if u == nil {
		return fmt.Errorf("target user %s no longer exists: %w", user, repairs.ErrChangedSincePlan)
	}
	return nil
}

// applyElsewhere creates the record, journaled first
// (undo.ChangeTypeMasterListElsewhereCreate) so the apply op's revert deletes
// it again while it is still what this apply made. The journal row names no
// book. A record that appeared since the re-check (another apply of a list
// with the same row) is not overwritten: the journal row is voided (the write
// failed) and the row reports changed_since_plan.
func (f *masterListImportFixer) applyElsewhere(w *repairs.Writer, fresh repairs.Row, d *mlElsewhereDecision) error {
	lists := f.p.deps.MasterLists()
	if lists == nil {
		return fmt.Errorf("%s: the store cannot keep master lists", masterListImportFixerID)
	}
	// The record names the target user: one that was removed since the plan
	// gets none. (Nothing is locked here, so the check is as close to the write
	// as the store allows; the create itself is atomic.)
	if err := f.recheckUser(d.rec.UserID); err != nil {
		return err
	}
	rec := d.rec
	rec.CreatedAt = f.now().UTC()
	js, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("%s: row %s: encode record: %w", masterListImportFixerID, fresh.RowID, err)
	}
	err = w.JournalStep("", repairs.UndoEntry{ChangeType: undo.ChangeTypeMasterListElsewhereCreate,
		Field: undo.ElsewhereField(rec.ID), Old: undo.ElsewhereAbsent, New: string(js)}, func() error {
		if err := lists.CreateElsewhere(rec); err != nil {
			if errors.Is(err, database.ErrElsewhereExists) {
				return fmt.Errorf("a read-elsewhere record for this row was created since the plan: %w", repairs.ErrChangedSincePlan)
			}
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, repairs.ErrChangedSincePlan) {
			return err
		}
		return fmt.Errorf("%s: row %s: %w", masterListImportFixerID, fresh.RowID, err)
	}
	return nil
}

// mlGroupKey is a group's row id: the first identifier any of its rows
// carries, else a hash of the folded title and authors, else the input line.
func mlGroupKey(r masterlist.Row) string {
	if k := masterlist.NaturalKey(r); k != "" {
		return k
	}
	if !r.HasIdentity() {
		return "line:" + strconv.Itoa(r.Line)
	}
	sum := sha256.Sum256([]byte(arsKeyOf(r.Title).full + "\x00" + strings.Join(r.Authors, "\x00")))
	return "noid:" + hex.EncodeToString(sum[:])[:16]
}

// assemble turns the outcomes into one plan row per INPUT row. A group's first
// row carries the match; its other rows are duplicate_in_list rows that name
// it.
func (f *masterListImportFixer) assemble(meta *database.MasterListMeta, params mlParams, lib *mlLibrary, input []masterlist.Row, groups []masterlist.Group, outcomes []mlOutcome) []repairs.Row {
	rc := mlRowContext{uploadedBy: meta.UploadedBy, params: params}
	var rows []repairs.Row
	used := map[string]int{}
	for gi, g := range groups {
		key := mlGroupKey(g.Row)
		if used[key]++; used[key] > 1 {
			// Two groups with one key: only possible for the no-identifier
			// hash (identical title and credits, no identifier). They are
			// separate books until matched, so each keeps its own row.
			key += "~" + strconv.Itoa(used[key])
		}
		lines := make([]int, len(g.Members))
		for k, idx := range g.Members {
			lines[k] = input[idx].Line
		}
		rc.provenance = mlProvenance(meta, lines)
		rows = append(rows, f.matchRow(rc, lib, key, lines, g.Row, outcomes[gi]))
		for k := 1; k < len(g.Members); k++ {
			own := input[g.Members[k]]
			rows = append(rows, f.duplicateRow(meta, params, key, k+1, lines[0], own))
		}
	}
	return rows
}

func mlProvenance(meta *database.MasterListMeta, lines []int) string {
	var ls []string
	for _, l := range lines {
		ls = append(ls, strconv.Itoa(l))
	}
	name := meta.Filename
	if name == "" {
		name = meta.ID
	}
	by := ""
	if meta.UploadedBy != "" {
		by = " by user " + meta.UploadedBy
	}
	return fmt.Sprintf("list %s (%s, uploaded %s%s), line %s", name, meta.Format, meta.UploadedAt.UTC().Format(time.RFC3339), by, strings.Join(ls, ", "))
}

// mlIssueFlags are the finding codes of the row's issues, one flag each.
func mlIssueFlags(r masterlist.Row) []string {
	var out []string
	for _, is := range r.Issues {
		if is.Code != "" && !slices.Contains(out, is.Code) {
			out = append(out, is.Code)
		}
	}
	return out
}

func mlIssueEvidence(r masterlist.Row) []string {
	var out []string
	for _, is := range r.Issues {
		s := fmt.Sprintf("line %d: %s", is.Line, is.Code)
		if is.Line == 0 {
			s = fmt.Sprintf("line %d: %s", r.Line, is.Code)
		}
		if is.Field != "" {
			s += " in " + is.Field
		}
		if is.Value != "" {
			s += " (" + is.Value + ")"
		}
		if is.Detail != "" {
			s += ": " + is.Detail
		}
		out = append(out, s)
	}
	return out
}

func mlMarshalState(st mlRowState) json.RawMessage {
	b, err := json.Marshal(st)
	if err != nil {
		// Strings, numbers and slices of them: unreachable.
		return nil
	}
	return b
}

func mlTitleOf(r masterlist.Row) string {
	switch {
	case strings.TrimSpace(r.Title) != "":
		return r.Title
	case r.ASIN != "":
		return r.ASIN
	case r.ISBN13 != "":
		return r.ISBN13
	}
	return ""
}

// mlRowContext is what a row is built with besides the matcher's outcome: the
// provenance line (Evidence[0], where the row came from), the list's uploader,
// and the plan params. Plan builds it from the list; Replan from the row.
type mlRowContext struct {
	provenance string
	uploadedBy string
	params     mlParams
}

// matchRow builds the row that carries a group's match.
func (f *masterListImportFixer) matchRow(rc mlRowContext, lib *mlLibrary, key string, lines []int, row masterlist.Row, o mlOutcome) repairs.Row {
	params := rc.params
	r := repairs.Row{RowID: key, Title: mlTitleOf(row), Author: strings.Join(row.Authors, ", "), Class: o.class, Reason: o.reason, Risk: repairs.RiskReview}
	st := mlRowState{ListID: params.ListID, User: params.TargetUserID, Lines: lines, Row: row,
		Target: o.target, Tier: o.tier, Candidates: o.candidates, Trashed: o.trashed, Hits: o.hits, UploadedBy: rc.uploadedBy}
	r.State = mlMarshalState(st)
	r.Evidence = append([]string{rc.provenance}, o.evidence...)
	r.Evidence = append(r.Evidence, mlIssueEvidence(row)...)
	r.Flags = append(slices.Clone(o.flags), mlIssueFlags(row)...)

	if o.class == mlMatchedHigh {
		// A list-side finding (a dropped bad value, a future time, a conflict
		// between rows) is a person's call, whatever matched.
		if len(row.Issues) > 0 {
			r.Class = mlMatchedReview
			r.Reason = "the list row has data problems (" + strings.Join(mlIssueFlags(row), ", ") + "); confirm the match"
		}
	}
	// The unmatched twin of the demotion above: a row that nothing in the
	// library could be would become a read-elsewhere record, but with a data
	// problem of its own (a dropped bad identifier may have been the one that
	// matched) it is held, still unmatched, for a person.
	if mlElsewhereCandidate(r, st) && len(row.Issues) > 0 {
		r.Reason += "; held for review rather than kept as a read-elsewhere record: the list row has data problems (" + strings.Join(mlIssueFlags(row), ", ") + ")"
	}
	for _, id := range o.candidates {
		if b := lib.books[id]; b != nil {
			role := "candidate"
			if id == o.target {
				role = "target"
			}
			r.BookIDs = append(r.BookIDs, id)
			r.Members = append(r.Members, repairs.RowMember{BookID: id, Title: b.Title, Role: role})
		}
	}
	// A swapped-title or ambiguous outcome may name a target the candidates
	// list already holds; a trashed book is evidence only, never a BookID.
	for _, id := range o.trashed {
		title := ""
		if b := lib.trashed[id]; b != nil {
			title = b.Title
		}
		r.Members = append(r.Members, repairs.RowMember{BookID: id, Title: title, Role: "trashed"})
	}
	if r.Class == mlMatchedHigh {
		if o.runtimeKnown {
			r.Risk = repairs.RiskLow
		} else {
			r.Reason = "matched by identifier; the runtime is unknown on one side, so the length was not checked"
		}
	}
	if r.Class == mlMatchedHigh || r.Class == mlMatchedReview {
		r.Proposed = mlProposed(row, params.TargetUserID)
	}
	if r.Reason == "" {
		r.Reason = "matched by " + strings.ReplaceAll(o.tier, "_", " ")
	}
	return r
}

// mlProposed is what the list says, as the row's display of the import.
func mlProposed(row masterlist.Row, user string) map[string]string {
	p := map[string]string{"user": user}
	if row.Status != "" {
		p["read_status"] = row.Status
	}
	if row.FinishedAt != "" {
		p["finished_at"] = row.FinishedAt
	}
	if row.ProgressPct > 0 {
		p["progress_pct"] = strconv.Itoa(row.ProgressPct)
	}
	if row.PositionSec > 0 {
		p["position_sec"] = strconv.Itoa(row.PositionSec)
	}
	return p
}

// duplicateRow is a list row that names the same book as an earlier row: it is
// listed (every input row has a plan row) and never planned.
func (f *masterListImportFixer) duplicateRow(meta *database.MasterListMeta, params mlParams, key string, ordinal, firstLine int, own masterlist.Row) repairs.Row {
	why := fmt.Sprintf("line %d names the same book as line %d (a shared ASIN, ISBN or source id); line %d carries the combined status and the match", own.Line, firstLine, firstLine)
	r := repairs.Row{RowID: key + "#" + strconv.Itoa(ordinal), Title: mlTitleOf(own), Author: strings.Join(own.Authors, ", "),
		Class: mlDuplicate, Reason: why, Risk: repairs.RiskReview}
	r.State = mlMarshalState(mlRowState{ListID: params.ListID, User: params.TargetUserID, Lines: []int{own.Line}, Row: own, Representative: key})
	r.Evidence = append([]string{mlProvenance(meta, []int{own.Line}), why}, mlIssueEvidence(own)...)
	r.Flags = mlIssueFlags(own)
	return r
}

// mlTargetOf is the book a row was matched to, or "".
func mlTargetOf(r repairs.Row) string {
	if r.Class != mlMatchedHigh && r.Class != mlMatchedReview {
		return ""
	}
	for _, m := range r.Members {
		if m.Role == "target" {
			return m.BookID
		}
	}
	return ""
}

// mlMarkDuplicateTargets: two list entries resolved to one book means at least
// one match is wrong (typically a book row carrying another volume's
// identifier). Every matched row counts, and the wrong one is as likely to be
// the high one: each matched_high row of such a book becomes matched_review.
func mlMarkDuplicateTargets(rows []repairs.Row) {
	byTarget := map[string][]int{}
	for i := range rows {
		if t := mlTargetOf(rows[i]); t != "" {
			byTarget[t] = append(byTarget[t], i)
		}
	}
	for book, idx := range byTarget {
		if len(idx) < 2 {
			continue
		}
		ids := make([]string, 0, len(idx))
		for _, i := range idx {
			ids = append(ids, rows[i].RowID)
		}
		sort.Strings(ids)
		why := fmt.Sprintf("%d list entries match book %s (%s); at most one of them is that book", len(idx), book, strings.Join(ids, ", "))
		for _, i := range idx {
			r := &rows[i]
			r.Flags = append(r.Flags, mlFlagDuplicateTgt)
			r.Evidence = append(r.Evidence, why)
			if r.Class == mlMatchedHigh {
				r.Class, r.Reason, r.Risk = mlMatchedReview, why, repairs.RiskReview
			}
		}
	}
}

// mlMarkPossibleDuplicates flags rows of different groups that read as one
// title and author (two editions of one book, or one book listed twice with no
// shared identifier). A flag only: such rows are not joined.
func mlMarkPossibleDuplicates(rows []repairs.Row) {
	byName := map[string][]int{}
	for i := range rows {
		if rows[i].Class == mlDuplicate || rows[i].Class == mlInvalid {
			continue
		}
		var st mlRowState
		if err := json.Unmarshal(rows[i].State, &st); err != nil {
			continue
		}
		k := arsKeyOf(st.Row.Title).full
		if k == "" {
			continue
		}
		sn := make([]string, 0, 2)
		for s := range arsSurnames(st.Row.Authors) {
			sn = append(sn, s)
		}
		sort.Strings(sn)
		name := k + "\x00" + strings.Join(sn, ",")
		byName[name] = append(byName[name], i)
	}
	for _, idx := range byName {
		if len(idx) < 2 {
			continue
		}
		for _, i := range idx {
			rows[i].Flags = append(rows[i].Flags, mlFlagPossibleDup)
			rows[i].Evidence = append(rows[i].Evidence, "another list row has the same title and author but shares no identifier with it")
		}
	}
}

// mlFinalize is the end of a row the decision pass does not touch: it is
// listed, never written, so its class is also its Skipped kind. Last, the
// fingerprint of every input the class was decided from. (A row the decision
// pass handled has both set already, from the inputs it read.)
func mlFinalize(r *repairs.Row) {
	r.Skipped, r.SkipReason = r.Class, r.Reason
	r.Risk = repairs.RiskReview
	r.Fingerprint = arsFingerprint(r.RowID, r.Class, r.Flags, r.BookIDs, r.Members, r.State)
}
