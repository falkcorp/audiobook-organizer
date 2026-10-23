// file: internal/plugins/maintenance/master_list_import_fixer.go
// version: 1.0.0
// guid: 4b0c5f4a-6d0a-4f0e-9d8e-3f1f7a9a52c1
// last-edited: 2026-10-23

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
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/masterlist"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// masterListImportFixerID is the Repairs-lane id of the master-list import
// (design: .claude/notes/2026-10-23-master-list-import-design.md).
const masterListImportFixerID = "master-list-import"

// errMasterListApplyNotBuilt is what Apply and Replan return. This is PR 1 of
// the master-list import: the list store, the row schema, the matcher and this
// preview. The user-state writes (and the cut-over of
// maintenance.audible-read-status) are PR 2.
var errMasterListApplyNotBuilt = errors.New("master-list-import: apply is not implemented until PR 2 of the master-list import; this fixer only previews")

// mlLibraryPage is the page size of the whole-library read.
const mlLibraryPage = 5000

// masterListImportFixer previews the import of an uploaded master list (a
// CSV or JSON file of books with their ISBN, ASIN and read status) into one
// user's read state. It plans one row per INPUT row, classed:
//
//   - matched_high: matched by an identifier (ASIN, ISBN, source id) to one
//     live book with nothing against it. The only class PR 2 will apply.
//   - matched_review: a match a person confirms: by title and author, a
//     swapped title, a runtime or series disagreement, a list-side data problem,
//     a trashed copy followed to its survivor, or a book two list rows claim.
//   - ambiguous: several books fit.
//   - unmatched: no live book fits.
//   - duplicate_in_list: the row names the same book as an earlier row (same
//     ASIN, ISBN-13 or source id, transitively); the earlier row carries the
//     combined status and the match.
//   - invalid: the row names no book at all.
//   - error_unreadable: a store read the row needs failed.
//
// Finer reasons are row flags (the lane filters on flag:<name>). The preview
// writes nothing: not the list, not user state, not the book table.
type masterListImportFixer struct {
	p *Plugin
	// now is the clock the future-timestamp check reads.
	now func() time.Time
}

func newMasterListImportFixer(p *Plugin) *masterListImportFixer {
	return &masterListImportFixer{p: p, now: time.Now}
}

var (
	_ repairs.Fixer           = (*masterListImportFixer)(nil)
	_ repairs.NoScanStandDown = (*masterListImportFixer)(nil)
	_ repairs.PlanOnly        = (*masterListImportFixer)(nil)
)

func (f *masterListImportFixer) ID() string { return masterListImportFixerID }
func (f *masterListImportFixer) Title() string {
	return "Master list read-status import (preview)"
}
func (f *masterListImportFixer) Description() string {
	return "Previews the import of an uploaded master list (POST /api/v1/master-lists) into one user's read " +
		"status. Plan params: list_id (the upload's id) and target_user_id (required, never defaulted). Every " +
		"input row is planned once: matched by ASIN, then ISBN, then source id (high confidence), then by title " +
		"and author (review); rows sharing an identifier are folded into the first; a trashed book is followed " +
		"to its live survivor and never a target. Classes: matched_high, matched_review, ambiguous, unmatched, " +
		"duplicate_in_list, invalid. Preview only: nothing is written, and Apply is refused until PR 2."
}

// NoScanStandDown: the preview writes nothing the scan writes.
func (f *masterListImportFixer) NoScanStandDown() bool { return true }

// PlanOnly: Apply is refused by the routes, hidden by the lane, and refused by
// the apply op.
func (f *masterListImportFixer) PlanOnly() bool { return true }

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
}

func (f *masterListImportFixer) deps() (OpsStore, UserReadStateStore, MasterListReader, ExternalIDBookLookup, error) {
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
func readList(lists MasterListReader, id string) (*database.MasterListMeta, []masterlist.Row, error) {
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

// Plan matches every list row against the library. Read-only.
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
	lib, err := loadMLLibrary(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", masterListImportFixerID, err)
	}
	follows, err := merge.FlagHolderFollowsFrom(store)
	if err != nil {
		return nil, fmt.Errorf("%s: read merge journals: %w", masterListImportFixerID, err)
	}
	now := f.now()
	for i := range input {
		// A list time in the future is held for review with the row's other
		// findings; the finding is recorded on the row, not the stored list.
		input[i].Issues = append(slices.Clone(input[i].Issues), input[i].FutureTimestamps(now, mlFutureSlack)...)
	}

	groups := masterlist.Collapse(input)
	m := &mlMatcher{store: store, ext: ext, lib: lib, follows: follows}
	outcomes := make([]mlOutcome, len(groups))
	var done atomic.Int64
	// Each worker writes only outcomes[i] for its own i; lib, follows and the
	// store are read-only. Groups are disjoint sets of input rows, so no two
	// workers ever plan the same row.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(groups)), func(_ context.Context, i int) error {
		defer done.Add(1)
		g := groups[i]
		if !g.Row.HasIdentity() {
			outcomes[i] = mlOutcome{class: mlInvalid, reason: "the row has no title, identifier or source id"}
			return nil
		}
		o, err := m.classify(g.Row)
		if err != nil {
			// Fail the ROW closed: an unreadable book is not "no book".
			o = mlOutcome{class: mlUnreadable, reason: err.Error()}
		}
		outcomes[i] = o
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

	rows := f.assemble(meta, params, lib, input, groups, outcomes)
	mlMarkDuplicateTargets(rows)
	mlMarkPossibleDuplicates(rows)
	for i := range rows {
		mlFinalize(&rows[i])
	}
	return rows, nil
}

// Replan refuses: the preview has no apply, so there is no row to re-check
// before writing.
func (f *masterListImportFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	return repairs.Row{}, fmt.Errorf("%w (row %s)", errMasterListApplyNotBuilt, planned.RowID)
}

// Apply refuses, and takes no write path: the Writer is never touched.
func (f *masterListImportFixer) Apply(_ context.Context, _ *repairs.Writer, fresh repairs.Row) error {
	return fmt.Errorf("%w (row %s)", errMasterListApplyNotBuilt, fresh.RowID)
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
		rows = append(rows, f.matchRow(meta, params, lib, key, lines, g.Row, outcomes[gi]))
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

// matchRow builds the row that carries a group's match.
func (f *masterListImportFixer) matchRow(meta *database.MasterListMeta, params mlParams, lib *mlLibrary, key string, lines []int, row masterlist.Row, o mlOutcome) repairs.Row {
	r := repairs.Row{RowID: key, Title: mlTitleOf(row), Author: strings.Join(row.Authors, ", "), Class: o.class, Reason: o.reason, Risk: repairs.RiskReview}
	st := mlRowState{ListID: params.ListID, User: params.TargetUserID, Lines: lines, Row: row,
		Target: o.target, Tier: o.tier, Candidates: o.candidates, Trashed: o.trashed}
	r.State = mlMarshalState(st)
	r.Evidence = append([]string{mlProvenance(meta, lines)}, o.evidence...)
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

// mlFinalize sets what depends on the final class: only matched_high is
// applicable; every other class is also the row's skip kind. Last, the
// fingerprint of every input the decision was made from.
func mlFinalize(r *repairs.Row) {
	if r.Class == mlMatchedHigh {
		r.Skipped, r.SkipReason = "", ""
	} else {
		r.Skipped, r.SkipReason = r.Class, r.Reason
		r.Risk = repairs.RiskReview
	}
	r.Fingerprint = arsFingerprint(r.RowID, r.Class, r.Flags, r.BookIDs, r.Members, r.State)
}
