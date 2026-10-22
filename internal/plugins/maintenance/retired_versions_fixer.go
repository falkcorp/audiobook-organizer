// file: internal/plugins/maintenance/retired_versions_fixer.go
// version: 1.0.0
// guid: b1bac43f-5852-47a1-abc2-fe00e90a031a
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// retiredVersionsFixerID is the Repairs-lane id of the retired-versions
// preview fixer. The plan names it `retired-versions`, and the decisions
// store files its rejects under op_kind `repairs:retired-versions`.
const retiredVersionsFixerID = "retired-versions"

// retiredMinAgeDaysDefault is the owner's minimum age (2026-10-22): a trashed
// book is not a candidate until it has sat in the trash this many days, which
// keeps the merge that trashed it inside its own undo window.
const retiredMinAgeDaysDefault = 14

// Categories of a retired-versions row. Each row carries exactly one, as its
// Row.Class (so ByClass counts every row and the rows endpoint filters on it).
// The categories that will not be retired are also its Row.Skipped kind, so
// skipped_by_kind carries them too; the two that WOULD be retired are
// applicable rows.
//
// Precedence, first match wins. Every fact a category is decided from (the
// target, the shared paths, the iTunes flag) is gathered for every row first,
// so a row carries its target and its evidence whatever category it lands in;
// only the label is decided by this order. Structural facts come before the
// time and state gates, so the no-target counts the owner asked for are not
// thinned out by the age gate.
//
//  1. user_deleted: library_state is "deleted". The owner deleted the book on
//     purpose; it is not a merge loser.
//  2. no_book_files: the book owns no book_file row, so there is nothing to
//     retire. It needs no retire to be purged.
//  3. target_ambiguous: the version group has live members but no single
//     primary (versionprimary.Incumbent returns nil). Never guessed.
//  4. no_target_files_missing / no_target_files_present: no live target (no
//     live group member, no group, and no live book at the end of its
//     merged-into chain). NOT retired: the book stays in the trash. Split by
//     whether any of its files is still on disk (an os.Stat per row).
//  5. age_unknown / too_recent: soft-deleted less than min_age_days ago, or no
//     soft-delete time on the row.
//  6. has_listening_state / listening_state_unknown: any user has listening
//     state on the book, or it could not be read (fails closed).
//  7. itunes_needs_owner_approval: a file of the book sits under the frozen
//     books/itunes/** tree. The owner approves these separately.
//  8. would_retire_same_path: at least one row's path equals a live row's
//     path. Retired anyway; both rows are kept.
//  9. would_retire: everything else.
const (
	retCatUserDeleted      = "user_deleted"
	retCatNoFiles          = "no_book_files"
	retCatAmbiguous        = "target_ambiguous"
	retCatNoTargetMissing  = "no_target_files_missing"
	retCatNoTargetPresent  = "no_target_files_present"
	retCatAgeUnknown       = "age_unknown"
	retCatTooRecent        = "too_recent"
	retCatListening        = "has_listening_state"
	retCatListeningUnknown = "listening_state_unknown"
	retCatITunes           = "itunes_needs_owner_approval"
	retCatSamePath         = "would_retire_same_path"
	retCatWouldRetire      = "would_retire"
	// retCatGone is only ever produced by a re-plan: the book left the trash
	// (restored or purged) after the plan.
	retCatGone = "gone"
	// retCatError is a row whose reads failed; the reason says which.
	retCatError = "error"
)

// How a target was found.
const (
	retMethodMergedInto   = "merged_into"
	retMethodGroupPrimary = "group_primary"
)

// Version-group shapes of a row (Current["group_class"]).
const (
	retGroupWithLive    = "group_with_live_member"
	retGroupWithoutLive = "group_without_live_member"
	retGroupNone        = "no_group"
)

// errRetiredApplyNotBuilt is what Apply returns. The retired-version model,
// the reader changes and the undo journal land in later PRs of the plan; until
// then this fixer only previews.
var errRetiredApplyNotBuilt = errors.New("retired-versions: apply is not implemented until PR 7 of the retired-version plan; this fixer only previews")

// retiredMergedHops caps the merged-into chain, as the merge package's own
// forward resolution does.
const retiredMergedHops = 10

// retiredVersionsReader is every read the fixer makes from the ops store, and
// nothing else. Naming it keeps the preview's "no writes to books or
// book_file rows" a compile-time fact: none of these methods writes. OpsStore
// is a superset, so the plugin's deps satisfy it.
type retiredVersionsReader interface {
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// retiredVersionsFixer previews moving every soft-deleted book's file rows
// onto the live book it was a loser of, as a hidden "retired version"
// (docs: the retired-version plan, PR 1 of 10). It is plan-only and
// read-only: Plan and Replan classify each trashed book and name its target;
// Apply refuses. Manual runs only: nothing schedules it.
//
// The plan pass is one classification per trashed book, fanned out over a
// bounded worker pool sized to runtime.NumCPU(). Each worker writes only its
// own rows[i]. The reads a book's classification makes (the version group, a
// book's liveness) are shared across workers through a once-per-key cache, so
// 51,000 trashed books do not read a group of N losers N times.
type retiredVersionsFixer struct {
	p *Plugin
	// now is the clock the age gate reads; tests fix it.
	now func() time.Time
	// statTimeout bounds one os.Stat of a library file, so a stuck share
	// cannot hold a worker for the life of the plan.
	statTimeout time.Duration
}

func newRetiredVersionsFixer(p *Plugin) *retiredVersionsFixer {
	return &retiredVersionsFixer{p: p, now: time.Now, statTimeout: 15 * time.Second}
}

var _ repairs.Fixer = (*retiredVersionsFixer)(nil)

func (f *retiredVersionsFixer) ID() string { return retiredVersionsFixerID }
func (f *retiredVersionsFixer) Title() string {
	return "Retired versions (preview)"
}
func (f *retiredVersionsFixer) Description() string {
	return "Preview only. Classifies every book in the trash by what retiring it would do: its file records " +
		"moved onto the live book it lost to, as a hidden retired version, never deleted. Each row names its " +
		"target and says why a book would NOT be retired (no live target, trashed under 14 days ago, has " +
		"listening state, files under the iTunes library, deleted by hand). Nothing is written; Apply is not " +
		"built yet."
}

// retiredVersionsParams are the plan's own params.
type retiredVersionsParams struct {
	// MinAgeDays overrides the 14-day minimum age. Zero is allowed (no gate).
	MinAgeDays *int `json:"min_age_days,omitempty"`
}

func parseRetiredVersionsParams(raw json.RawMessage) (time.Duration, error) {
	days := retiredMinAgeDaysDefault
	if len(raw) > 0 && string(raw) != "null" {
		var p retiredVersionsParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return 0, fmt.Errorf("%s: bad params: %w", retiredVersionsFixerID, err)
		}
		if p.MinAgeDays != nil {
			if *p.MinAgeDays < 0 {
				return 0, fmt.Errorf("%s: min_age_days must not be negative", retiredVersionsFixerID)
			}
			days = *p.MinAgeDays
		}
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

// onceVal is one lazily loaded, shared value of a retiredRun cache.
type onceVal[T any] struct {
	once sync.Once
	v    T
	err  error
}

// cachedOnce loads key once across every worker and hands all of them the
// same result, error included.
func cachedOnce[T any](m *sync.Map, key string, load func() (T, error)) (T, error) {
	e, _ := m.LoadOrStore(key, &onceVal[T]{})
	ov := e.(*onceVal[T])
	ov.once.Do(func() { ov.v, ov.err = load() })
	return ov.v, ov.err
}

// retiredRun is the state of one Plan or one Replan: its readers, the age
// gate and the caches. The caches live exactly as long as the run, so a plan
// is a consistent snapshot and a re-plan never reads a stale one.
type retiredRun struct {
	f      *retiredVersionsFixer
	store  retiredVersionsReader
	paths  database.BookFilePathLookup
	probe  *merge.UserStateProbe
	minAge time.Duration
	now    time.Time

	books  sync.Map // book id -> *onceVal[*database.Book]
	groups sync.Map // version group id -> *onceVal[[]database.Book]
}

func (f *retiredVersionsFixer) newRun(params json.RawMessage) (*retiredRun, error) {
	minAge, err := parseRetiredVersionsParams(params)
	if err != nil {
		return nil, err
	}
	store := f.p.deps.OpsStore()
	paths := f.p.deps.FragmentRepairReader()
	users := f.p.deps.MergeUserStateStore()
	if store == nil || paths == nil || users == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	probe, err := merge.NewUserStateProbe(users)
	if err != nil {
		return nil, fmt.Errorf("%s: list users for the listening-state check: %w", retiredVersionsFixerID, err)
	}
	now := time.Now
	if f.now != nil {
		now = f.now
	}
	return &retiredRun{f: f, store: store, paths: paths, probe: probe, minAge: minAge, now: now()}, nil
}

// book reads a book once per run; (nil, nil) is a book that does not exist.
func (r *retiredRun) book(id string) (*database.Book, error) {
	return cachedOnce(&r.books, id, func() (*database.Book, error) { return r.store.GetBookByID(id) })
}

// isLive reports whether id is a book that is neither soft-deleted nor
// labelled into the trash.
func (r *retiredRun) isLive(id string) (bool, error) {
	b, err := r.book(id)
	if err != nil {
		return false, fmt.Errorf("read book %s: %w", id, err)
	}
	return b != nil && !b.IsSoftDeleted() && !database.IsInTrash(b), nil
}

func (r *retiredRun) group(gid string) ([]database.Book, error) {
	return cachedOnce(&r.groups, gid, func() ([]database.Book, error) { return r.store.GetBooksByVersionGroup(gid) })
}

// retiredFacts is everything one book's category is decided from.
type retiredFacts struct {
	shell database.BookCore
	files []database.BookFile

	groupID    string
	groupClass string

	targetID     string
	targetMethod string
	// ambiguous is why a group with live members yielded no single target.
	ambiguous string

	// sharedRows counts the shell's rows whose path a live book's row also
	// holds; sharedWith lists those live books.
	sharedRows int
	sharedWith []string
	// mergedLive is the live book the shell's merged-into chain ends at.
	// It is a target only when the shell also has a version group with a live
	// member; otherwise it is evidence on a no-target row, never a target
	// (the owner's answer: no live group member, or no group, is no target).
	mergedLive string
	// rule4 is the one live book that holds a row at EVERY path of the shell:
	// the plan's "same-path holder" heuristic, reported on a row with no
	// target and never used to pick one (the owner's answer is that a book
	// with no live group member, or no group, is not retired).
	rule4 string

	itunesRows    int
	itunesPIDRows int
	itunes        bool

	missingFlag int
	// absentOnDisk is the os.Stat count, only computed for a no-target row
	// (-1 otherwise).
	absentOnDisk int
}

func retiredDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// classify reads everything a trashed book's category depends on and returns
// its row. A read that fails returns an error: the caller turns it into an
// error row, or (for an incomplete ownership lookup) fails the plan.
func (r *retiredRun) classify(ctx context.Context, shell database.BookCore) (repairs.Row, error) {
	files, err := r.store.GetBookFiles(shell.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book_file rows of %s: %w", shell.ID, err)
	}
	fa := retiredFacts{shell: shell, files: files, groupClass: retGroupNone, absentOnDisk: -1}
	for i := range files {
		if files[i].Missing {
			fa.missingFlag++
		}
	}
	members, err := r.loadGroup(&fa)
	if err != nil {
		return repairs.Row{}, err
	}
	if err := r.resolveTarget(&fa, members); err != nil {
		return repairs.Row{}, err
	}
	if err := r.readPathOwners(&fa); err != nil {
		return repairs.Row{}, err
	}
	r.readITunes(&fa)

	cat, why, err := r.decide(ctx, &fa)
	if err != nil {
		return repairs.Row{}, err
	}
	return r.buildRow(&fa, cat, why)
}

// loadGroup reads the shell's version group and sets its shape.
func (r *retiredRun) loadGroup(fa *retiredFacts) ([]database.Book, error) {
	fa.groupID = retiredDeref(fa.shell.VersionGroupID)
	if fa.groupID == "" {
		return nil, nil
	}
	members, err := r.group(fa.groupID)
	if err != nil {
		return nil, fmt.Errorf("read version group %s: %w", fa.groupID, err)
	}
	fa.groupClass = retGroupWithoutLive
	for i := range members {
		if members[i].ID != fa.shell.ID && !members[i].IsSoftDeleted() {
			fa.groupClass = retGroupWithLive
			break
		}
	}
	return members, nil
}

// resolveTarget finds the one live book the shell's rows would be retired
// onto: first the end of its merged-into chain, then its version group's
// primary. Both need a version group with a live member (the owner's answer
// on 2026-10-22); a merged-into chain on a shell without one is recorded as
// evidence only. A tie is ambiguous, never guessed. The plan's rule 2
// (merge.ResolveMergeSurvivor) is not used: without the journal's
// flag-holder follows it degrades to the USER-STATE redirect, which can name
// the flag holder rather than the book that kept the audio.
func (r *retiredRun) resolveTarget(fa *retiredFacts, members []database.Book) error {
	seen := map[string]bool{fa.shell.ID: true}
	cur := retiredDeref(fa.shell.MergedIntoBookID)
	for hop := 0; hop < retiredMergedHops && cur != "" && !seen[cur]; hop++ {
		seen[cur] = true
		b, err := r.book(cur)
		if err != nil {
			return fmt.Errorf("read merged-into book %s: %w", cur, err)
		}
		if b == nil {
			break
		}
		if !b.IsSoftDeleted() && !database.IsInTrash(b) {
			fa.mergedLive = cur
			if fa.groupClass == retGroupWithLive {
				fa.targetID, fa.targetMethod = cur, retMethodMergedInto
			}
			return nil
		}
		cur = retiredDeref(b.MergedIntoBookID)
	}
	if fa.groupClass != retGroupWithLive {
		return nil
	}
	var aliveErr error
	inc := versionprimary.Incumbent(members, func(id string) bool {
		live, err := r.isLive(id)
		if err != nil && aliveErr == nil {
			aliveErr = err
		}
		return live
	})
	if aliveErr != nil {
		return aliveErr
	}
	switch {
	case inc == nil:
		fa.ambiguous = "the version group has live members but no single primary"
	case database.IsInTrash(inc):
		fa.ambiguous = "the group's primary " + inc.ID + " is labelled into the trash"
	default:
		fa.targetID, fa.targetMethod = inc.ID, retMethodGroupPrimary
	}
	return nil
}

// readPathOwners finds, for each distinct path of the shell's rows, the live
// books that also hold a row there. The lookup is the complete multi-row one
// and fails closed: the single-owner index under-reports, which would turn a
// same-path row into a plain one.
func (r *retiredRun) readPathOwners(fa *retiredFacts) error {
	ownersAt := map[string][]string{}
	var order []string
	for i := range fa.files {
		p := fa.files[i].FilePath
		if p == "" {
			continue
		}
		if _, done := ownersAt[p]; done {
			continue
		}
		rows, err := database.BookFileRowsAtPathStrict(r.paths, p)
		if err != nil {
			return fmt.Errorf("who holds %q: %w", p, err)
		}
		owners := []string{}
		for j := range rows {
			if rows[j].BookID == fa.shell.ID {
				continue
			}
			live, err := r.isLive(rows[j].BookID)
			if err != nil {
				return err
			}
			if live && !retiredContains(owners, rows[j].BookID) {
				owners = append(owners, rows[j].BookID)
			}
		}
		sort.Strings(owners)
		ownersAt[p] = owners
		order = append(order, p)
	}
	union := map[string]bool{}
	for i := range fa.files {
		if owners := ownersAt[fa.files[i].FilePath]; len(owners) > 0 {
			fa.sharedRows++
			for _, o := range owners {
				union[o] = true
			}
		}
	}
	for o := range union {
		fa.sharedWith = append(fa.sharedWith, o)
	}
	sort.Strings(fa.sharedWith)
	// rule4: one live book at every path.
	if len(order) > 0 {
		common := append([]string(nil), ownersAt[order[0]]...)
		for _, p := range order[1:] {
			var next []string
			for _, o := range common {
				if retiredContains(ownersAt[p], o) {
					next = append(next, o)
				}
			}
			common = next
		}
		if len(common) == 1 {
			fa.rule4 = common[0]
		}
	}
	return nil
}

func retiredContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// readITunes flags the files that sit under the frozen books/itunes/** tree,
// the same predicate the Repairs framework guard uses.
func (r *retiredRun) readITunes(fa *retiredFacts) {
	for i := range fa.files {
		if pathutil.UnderFrozenITunesTree(fa.files[i].FilePath) || pathutil.UnderFrozenITunesTree(fa.files[i].ITunesPath) {
			fa.itunesRows++
		}
		if fa.files[i].ITunesPersistentID != "" {
			fa.itunesPIDRows++
		}
	}
	fa.itunes = fa.itunesRows > 0 ||
		pathutil.UnderFrozenITunesTree(fa.shell.FilePath) ||
		pathutil.UnderFrozenITunesTree(retiredDeref(fa.shell.ITunesPath))
}

// decide applies the category precedence documented on the retCat constants.
func (r *retiredRun) decide(ctx context.Context, fa *retiredFacts) (cat, why string, err error) {
	switch {
	case strings.EqualFold(strings.TrimSpace(retiredDeref(fa.shell.LibraryState)), "deleted"):
		return retCatUserDeleted, "the book was deleted by hand (library state \"deleted\"), not lost in a merge", nil
	case len(fa.files) == 0:
		return retCatNoFiles, "the book owns no file records, so there is nothing to retire", nil
	case fa.targetID == "" && fa.ambiguous != "":
		return retCatAmbiguous, fa.ambiguous + "; no target is guessed", nil
	case fa.targetID == "":
		fa.absentOnDisk = r.countAbsent(ctx, fa.files)
		if fa.absentOnDisk == len(fa.files) {
			return retCatNoTargetMissing, "no live target (" + r.noTargetWhy(fa) + "); every file is missing from disk; stays in the trash", nil
		}
		return retCatNoTargetPresent, "no live target (" + r.noTargetWhy(fa) + "); at least one file is still on disk; stays in the trash", nil
	}
	at := fa.shell.MarkedForDeletionAt
	switch {
	case at == nil || at.IsZero():
		return retCatAgeUnknown, "the row has no soft-delete time, so its age cannot be shown to be at least the minimum", nil
	case r.now.Sub(*at) < r.minAge:
		return retCatTooRecent, fmt.Sprintf("trashed %d day(s) ago; the minimum age is %d day(s)",
			int(r.now.Sub(*at).Hours()/24), int(r.minAge.Hours()/24)), nil
	}
	has, perr := r.probe.Has(fa.shell.ID)
	switch {
	case perr != nil:
		return retCatListeningUnknown, "listening state could not be read: " + perr.Error(), nil
	case has:
		return retCatListening, "a user has listening state on this book", nil
	case fa.itunes:
		return retCatITunes, "a file sits under the iTunes library; retiring it needs the owner's approval", nil
	case fa.sharedRows > 0:
		return retCatSamePath, fmt.Sprintf("%d row(s) share a path with a live book's row; both rows are kept", fa.sharedRows), nil
	}
	return retCatWouldRetire, "", nil
}

func (r *retiredRun) noTargetWhy(fa *retiredFacts) string {
	switch fa.groupClass {
	case retGroupWithoutLive:
		return "its version group has no live member"
	case retGroupWithLive:
		return "its version group has no usable primary"
	}
	return "it has no version group"
}

// countAbsent counts the rows whose file is not on disk now. A stat that
// fails for any reason other than "does not exist", or that outlives the
// bound, counts the file as present: the preview never claims a file is
// missing on a guess.
func (r *retiredRun) countAbsent(ctx context.Context, files []database.BookFile) int {
	bound := r.f.statTimeout
	if bound <= 0 {
		bound = 15 * time.Second
	}
	absent := 0
	for i := range files {
		p := files[i].FilePath
		if p == "" {
			absent++
			continue
		}
		present, err := boundedCall(ctx, bound, func() (bool, error) {
			_, serr := os.Stat(p)
			if serr == nil {
				return true, nil
			}
			return !os.IsNotExist(serr), nil
		})
		if err == nil && !present {
			absent++
		}
	}
	return absent
}

// buildRow turns a decided book into its Repairs row.
func (r *retiredRun) buildRow(fa *retiredFacts, cat, why string) (repairs.Row, error) {
	sh := fa.shell
	row := repairs.Row{
		RowID:   sh.ID,
		BookIDs: []string{sh.ID},
		Title:   sh.Title,
		Class:   cat,
		Risk:    repairs.RiskLow,
		Reason:  why,
		Current: map[string]string{
			"group_class":       fa.groupClass,
			"rows":              strconv.Itoa(len(fa.files)),
			"rows_missing_flag": strconv.Itoa(fa.missingFlag),
			"rows_same_path":    strconv.Itoa(fa.sharedRows),
			"rows_itunes":       strconv.Itoa(fa.itunesRows),
			"itunes_pid_rows":   strconv.Itoa(fa.itunesPIDRows),
			"shares_paths_with": strings.Join(fa.sharedWith, ","),
			"target_book_id":    fa.targetID,
			"target_method":     fa.targetMethod,
		},
	}
	if ls := retiredDeref(sh.LibraryState); ls != "" {
		row.Current["library_state"] = ls
	}
	if fa.groupID != "" {
		row.Current["group_id"] = fa.groupID
	}
	if at := sh.MarkedForDeletionAt; at != nil && !at.IsZero() {
		row.Current["soft_deleted_at"] = at.UTC().Format(time.RFC3339)
		row.Current["age_days"] = strconv.Itoa(int(r.now.Sub(*at).Hours() / 24))
	}
	if fa.absentOnDisk >= 0 {
		row.Current["rows_absent_on_disk"] = strconv.Itoa(fa.absentOnDisk)
	}
	if fa.targetID == "" && fa.mergedLive != "" {
		row.Current["merged_into_target"] = fa.mergedLive
		row.Evidence = append(row.Evidence, "informational only, not used as a target: its merged-into chain ends at live book "+fa.mergedLive+", but the book has no live version-group member")
	}
	if fa.targetID == "" && fa.rule4 != "" {
		row.Current["same_path_holder"] = fa.rule4
		row.Evidence = append(row.Evidence, "informational only, not used as a target: live book "+fa.rule4+" holds a row at every path of this book")
	}

	shellMember := repairs.RowMember{BookID: sh.ID, Title: sh.Title, Role: "shell", Files: len(fa.files), MissingFiles: fa.missingFlag}
	row.Members = []repairs.RowMember{shellMember}
	if fa.targetID != "" {
		row.BookIDs = append(row.BookIDs, fa.targetID)
		row.Evidence = append(row.Evidence, "target "+fa.targetID+" found by "+fa.targetMethod)
		tm := repairs.RowMember{BookID: fa.targetID, Role: "target"}
		if tb, err := r.book(fa.targetID); err == nil && tb != nil {
			tm.Title = tb.Title
		}
		tfiles, err := r.store.GetBookFiles(fa.targetID)
		if err != nil {
			return repairs.Row{}, fmt.Errorf("read book_file rows of target %s: %w", fa.targetID, err)
		}
		tm.Files = len(tfiles)
		for i := range tfiles {
			if tfiles[i].Missing {
				tm.MissingFiles++
			}
		}
		row.Members = append(row.Members, tm)
	}
	if fa.sharedRows > 0 {
		row.Evidence = append(row.Evidence, fmt.Sprintf("%d row(s) share a path with live book(s) %s", fa.sharedRows, strings.Join(fa.sharedWith, ", ")))
		if fa.targetID != "" && (len(fa.sharedWith) > 1 || fa.sharedWith[0] != fa.targetID) {
			row.Risk = repairs.RiskReview
			row.Evidence = append(row.Evidence, "shares a path with a live book that is not the target")
		}
	}
	if fa.itunes || (fa.targetID == "" && fa.ambiguous != "") {
		row.Risk = repairs.RiskReview
	}

	switch cat {
	case retCatWouldRetire, retCatSamePath:
		row.Proposed = map[string]string{"retired_version_of": fa.targetID}
		if cat == retCatWouldRetire {
			row.Reason = "preview: its file records would move onto " + fa.targetID + " as a hidden retired version; nothing is written"
		} else {
			row.Reason = "preview: " + why + "; its file records would move onto " + fa.targetID + " as a hidden retired version; nothing is written"
		}
	default:
		if cat == retCatITunes && fa.targetID != "" {
			row.Proposed = map[string]string{"retired_version_of": fa.targetID}
		}
		row.Skipped, row.SkipReason = cat, why
		row.Reason = why
	}
	row.Fingerprint = retiredFingerprint(fa, cat)
	return row, nil
}

// retiredFingerprint hashes every input the category was decided from, so a
// re-plan that reads different state fingerprints differently.
func retiredFingerprint(fa *retiredFacts, cat string) string {
	parts := []string{cat, fa.shell.ID, fa.groupID, fa.groupClass, fa.targetID, fa.targetMethod, fa.ambiguous,
		retiredDeref(fa.shell.LibraryState), strconv.Itoa(fa.sharedRows), strings.Join(fa.sharedWith, ","),
		fa.rule4, fa.mergedLive, strconv.FormatBool(fa.itunes), strconv.Itoa(fa.absentOnDisk)}
	if at := fa.shell.MarkedForDeletionAt; at != nil {
		parts = append(parts, strconv.FormatInt(at.Unix(), 10))
	}
	rows := make([]string, len(fa.files))
	for i := range fa.files {
		rows[i] = fa.files[i].ID + "|" + fa.files[i].FilePath + "|" + strconv.FormatBool(fa.files[i].Missing)
	}
	sort.Strings(rows)
	parts = append(parts, rows...)
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// retiredErrorRow is the row of a book whose reads failed.
func retiredErrorRow(shell database.BookCore, err error) repairs.Row {
	r := repairs.Row{RowID: shell.ID, BookIDs: []string{shell.ID}, Title: shell.Title, Class: retCatError,
		Skipped: retCatError, SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
	r.Fingerprint = retiredFingerprint(&retiredFacts{shell: shell, absentOnDisk: -1}, retCatError+":"+err.Error())
	return r
}

// Plan classifies every book in the trash. Read-only.
func (f *retiredVersionsFixer) Plan(ctx context.Context, params json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	run, err := f.newRun(params)
	if err != nil {
		return nil, err
	}
	// The trash is listed by its own reader: the memdb core listings leave
	// soft-deleted books out, and this one falls back to a Pebble scan when
	// memdb is not serving. Limit and offset 0 mean the whole trash.
	trash, err := run.store.ListSoftDeletedBooks(0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("ListSoftDeletedBooks: %w", err)
	}
	shells := make([]database.BookCore, 0, len(trash))
	for i := range trash {
		shells = append(shells, trash[i].Core())
	}
	rows := make([]repairs.Row, len(shells))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i, and reads the run's
	// caches through sync.Map. No two workers touch a book row: the plan
	// writes none.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(shells)), func(ctx context.Context, i int) error {
		defer done.Add(1)
		row, err := run.classify(ctx, shells[i])
		switch {
		case err == nil:
			rows[i] = row
		case errors.Is(err, database.ErrOwnershipLookupIncomplete):
			// The ownership lookup is not complete (memdb warming up): every
			// row would fail the same way, and a plan of 51,000 error rows
			// is no preview. Fail the plan; it can be run again.
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			rows[i] = retiredErrorRow(shells[i], err)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeFail,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string {
			return fmt.Sprintf("Retired versions preview %d/%d", done.Load(), total)
		},
	})
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("classify the trash: %w", runErr)
	}
	return rows, nil
}

// Replan classifies one book again from fresh reads, by the same code Plan
// runs. A book no longer in the trash comes back skipped "gone" with a
// different fingerprint, never an error.
func (f *retiredVersionsFixer) Replan(ctx context.Context, params json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	run, err := f.newRun(params)
	if err != nil {
		return repairs.Row{}, err
	}
	b, err := run.store.GetBookByID(planned.RowID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", planned.RowID, err)
	}
	if b == nil || !b.IsSoftDeleted() {
		r := repairs.Row{RowID: planned.RowID, BookIDs: []string{planned.RowID}, Class: retCatGone, Skipped: retCatGone,
			SkipReason: "the book is no longer in the trash", Reason: "the book is no longer in the trash", Risk: repairs.RiskLow}
		r.Fingerprint = retiredFingerprint(&retiredFacts{shell: database.BookCore{ID: planned.RowID}, absentOnDisk: -1}, retCatGone)
		return r, nil
	}
	return run.classify(ctx, b.Core())
}

// Apply refuses. The retire writes (the retired marker, the version row, the
// journal, the reader changes that make a retired row safe) are PRs 2-7 of the
// plan; until they land this fixer only previews, and it takes no write path:
// the Writer is never touched.
func (f *retiredVersionsFixer) Apply(_ context.Context, _ *repairs.Writer, fresh repairs.Row) error {
	return fmt.Errorf("%w (row %s)", errRetiredApplyNotBuilt, fresh.RowID)
}
