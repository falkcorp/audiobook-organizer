// file: internal/plugins/maintenance/retired_versions_fixer.go
// version: 1.1.1
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
	"io/fs"
	"log/slog"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
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
// keeps the merge that trashed it inside its own undo window. The min_age_days
// param overrides it (0 turns the gate off).
const retiredMinAgeDaysDefault = 14

// Categories of a retired-versions row. Each row carries exactly one, as its
// Row.Class (so ByClass counts every row and the rows endpoint filters on it).
// The categories that will not be retired are also its Row.Skipped kind, so
// skipped_by_kind carries them too; the two that WOULD be retired are
// applicable rows.
//
// Precedence, first match wins. The cheap structural facts (the version
// group, the target, the shared paths, the lexical iTunes counts) are
// gathered for every row, so a row carries its target and its evidence
// whatever category it lands in. The reads that cost a store or disk round
// trip (the stat of a no-target row's files, the owner-veto lookup, the
// combine-journal index, the symlink-resolved iTunes check) happen at their own
// gate, so a row that an earlier gate decided never pays for them.
//
//  1. undo_still_possible: an unreversed combine-journal entry names this
//     book as absorbed, so purging or retiring it would make that combine
//     non-undoable. First, because it holds whatever else is true of the
//     book: a combine-absorbed shell is usually labelled "deleted", often
//     has no version group, and may own no file rows any more, and every one
//     of those would otherwise file it under a category a purge reads as
//     safe (the 2026-10-21 purge made 21 combines non-undoable that way).
//     The journal index is read once per run; a read that fails fails the
//     plan, because no row can be classified without it.
//  2. trash_labelled_deleted: library_state is "deleted". Three paths write
//     that label (a merge, a manual delete, a bulk delete), so it does not
//     say the owner deleted the book on purpose; the row is left alone.
//  3. no_book_files: the book owns no book_file row, so there is nothing to
//     retire. It needs no retire to be purged.
//  4. target_ambiguous: the version group has live members but no single
//     primary (versionprimary.Incumbent returns nil). Never guessed.
//  5. no_target_files_missing / no_target_files_present: no live target (no
//     live group member, no group, and no live book at the end of its
//     merged-into chain). NOT retired: the book stays in the trash. Split by
//     whether any of its files is still on disk (an os.Stat per file; a stat
//     that cannot be answered counts the file as present and is reported as
//     rows_stat_unknown).
//  6. owner_vetoed: the owner rejected joining this book and its target
//     (decisions.PairVetoed, same_work or same_copy).
//  7. itunes_needs_owner_approval: a file of the book OR of its target sits
//     under the frozen books/itunes/** tree, symlinks resolved. The owner
//     approves these separately.
//  8. age_unknown / too_recent: soft-deleted less than min_age_days ago, or no
//     soft-delete time on the row.
//  9. has_listening_state / listening_state_unknown: any user has listening
//     state on the book, or it could not be read (fails closed).
//  10. would_retire_same_path: at least one row's path equals a live row's
//     path. Retired anyway; both rows are kept.
//  11. would_retire: everything else.
//
// The plan's rule 5 (a content-hash match between the shell and its target)
// is deferred to a later PR; so are rule 2 (merge.ResolveMergeSurvivor) and a
// veto check on a rejected retired-versions shell proposal.
const (
	retCatTrashLabelled    = "trash_labelled_deleted"
	retCatNoFiles          = "no_book_files"
	retCatAmbiguous        = "target_ambiguous"
	retCatNoTargetMissing  = "no_target_files_missing"
	retCatNoTargetPresent  = "no_target_files_present"
	retCatVetoed           = "owner_vetoed"
	retCatUndoWindow       = "undo_still_possible"
	retCatITunes           = "itunes_needs_owner_approval"
	retCatAgeUnknown       = "age_unknown"
	retCatTooRecent        = "too_recent"
	retCatListening        = "has_listening_state"
	retCatListeningUnknown = "listening_state_unknown"
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

// retiredEntityBook is the decisions store's entity type for a book.
const retiredEntityBook = "book"

// errRetiredApplyNotBuilt is what Apply returns. The retired-version model,
// the reader changes and the undo journal land in later PRs of the plan; until
// then this fixer only previews.
var errRetiredApplyNotBuilt = errors.New("retired-versions: apply is not implemented until PR 7 of the retired-version plan; this fixer only previews")

// retiredMergedHops caps the merged-into chain, as the merge package's own
// forward resolution does.
const retiredMergedHops = 10

// retiredStatMaxAbandoned caps how many of ONE run's timed-out file stats may
// still be hung. A stat that times out is abandoned, not stopped (boundedCall):
// its goroutine, and the OS thread blocked in the syscall, lasts until the
// mount answers. Past this many at once the mount is dead and continuing would
// only stack threads (48 workers on a dead share would pass the runtime's 10k
// thread limit), so the plan fails instead. Per run, so a stat that never
// returns does not fail every later run; the stragglers of earlier runs are
// reported through retiredStatsAbandoned. At most this cap plus one per worker
// can be abandoned, because a worker checks the count after each give-up. A
// var for tests, read once per run.
var retiredStatMaxAbandoned int64 = 8

// retiredStatsAbandoned is the process-wide gauge of abandoned file stats whose
// goroutine has not yet returned, across every run. It gates nothing; a plan
// logs it at start when non-zero.
var retiredStatsAbandoned atomic.Int64

// errRetiredUndoRead fails a plan whose combine journals cannot be read: every
// row's category depends on them, so there is no honest partial preview.
var errRetiredUndoRead = errors.New("retired-versions: the combine journals cannot be read")

// errRetiredStatCap fails a plan whose file stats are hanging in bulk.
var errRetiredStatCap = errors.New("retired-versions: too many file stats are hung")

// retiredVersionsReader is every read the fixer makes from the ops store, and
// nothing else. Naming it keeps the preview's "no writes to books or
// book_file rows" a compile-time fact: none of these methods writes. OpsStore
// is a superset, so the plugin's deps satisfy it.
type retiredVersionsReader interface {
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookFilesForIDsCore(bookIDs []string) (map[string][]database.BookFileCore, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// retiredVersionsFixer previews moving every soft-deleted book's file rows
// onto the live book it was a loser of, as a hidden "retired version"
// (docs: the retired-version plan, PR 1 of 10). It is plan-only and
// read-only: Plan and Replan classify each trashed book and name its target;
// Apply refuses, and the lane offers no Apply for it (PlanOnly). Manual runs
// only: nothing schedules it.
//
// The plan pass is one classification per trashed book, fanned out over a
// bounded worker pool sized to runtime.NumCPU(). Each worker writes only its
// own rows[i]. What the workers share is read once per run through a
// once-per-key cache: a version group's members, any book read by id (a
// liveness check, a merged-into hop, a target), each target's file counts and
// iTunes verdict, and the combine-journal index. A shell's own file rows and
// its path owners are read per shell and not cached. The caches live exactly
// as long as one run.
type retiredVersionsFixer struct {
	p *Plugin
	// now is the clock the age gate reads; tests fix it.
	now func() time.Time
	// statTimeout bounds one stat of a library file, so a stuck share cannot
	// hold a worker for the life of the plan.
	statTimeout time.Duration
	// stat is os.Stat; tests swap it to make a stat fail or hang. A run reads
	// it once, before any bounded call starts.
	stat func(string) (os.FileInfo, error)
}

func newRetiredVersionsFixer(p *Plugin) *retiredVersionsFixer {
	return &retiredVersionsFixer{p: p, now: time.Now, statTimeout: 15 * time.Second, stat: os.Stat}
}

var (
	_ repairs.Fixer           = (*retiredVersionsFixer)(nil)
	_ repairs.NoScanStandDown = (*retiredVersionsFixer)(nil)
	_ repairs.PlanOnly        = (*retiredVersionsFixer)(nil)
)

func (f *retiredVersionsFixer) ID() string { return retiredVersionsFixerID }
func (f *retiredVersionsFixer) Title() string {
	return "Retired versions (preview)"
}
func (f *retiredVersionsFixer) Description() string {
	return "Preview only. Classifies every book in the trash by what retiring it would do: its file records " +
		"moved onto the live book it lost to, as a hidden retired version, never deleted. Each row names its " +
		"target and says why a book would NOT be retired: no live target, trashed under min_age_days ago " +
		"(14 by default; the min_age_days param overrides it, 0 turns the gate off), has listening state, " +
		"files under the iTunes library on either side, labelled deleted, an owner veto on the pair, or a " +
		"merge that can still be undone. The plan's rule 5 (a content-hash match between the book and its " +
		"target) is not applied yet. Nothing is written; Apply is not built, so this fixer has no Apply " +
		"control."
}

// NoScanStandDown: the preview writes nothing through the Writer and nothing a
// library scan writes, so a run of it never needs to park a running scan.
func (f *retiredVersionsFixer) NoScanStandDown() bool { return true }

// PlanOnly: Apply is refused by the routes, hidden by the lane, and refused
// again by Apply itself.
func (f *retiredVersionsFixer) PlanOnly() bool { return true }

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
	f        *retiredVersionsFixer
	store    retiredVersionsReader
	paths    database.BookFilePathLookup
	probe    *merge.UserStateProbe
	vetoes   OwnerVetoReader
	journals CombineJournalScanner
	// resolver settles symlinks for the iTunes check; its cache is shared by
	// every worker and is safe for that.
	resolver *repairs.PathResolver
	minAge   time.Duration
	now      time.Time

	// stat, bound and maxAbandoned are read once, so an abandoned stat never
	// reads a field a test is restoring.
	stat         func(string) (os.FileInfo, error)
	bound        time.Duration
	maxAbandoned int64
	// abandoned counts this run's timed-out stats that have not returned.
	abandoned atomic.Int64

	books   sync.Map // book id -> *onceVal[*database.Book]
	groups  sync.Map // version group id -> *onceVal[[]database.Book]
	targets sync.Map // target book id -> *onceVal[*retiredTarget]
	undo    sync.Map // "journals" -> *onceVal[map[string][]string]
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
	vetoes, journals := f.p.deps.OwnerVetoes(), f.p.deps.CombineJournals()
	if vetoes == nil || journals == nil {
		return nil, fmt.Errorf("%s: the owner's rejects or the combine journal cannot be read on this server; refusing to plan without them", retiredVersionsFixerID)
	}
	probe, err := merge.NewUserStateProbe(users)
	if err != nil {
		return nil, fmt.Errorf("%s: list users for the listening-state check: %w", retiredVersionsFixerID, err)
	}
	now := time.Now
	if f.now != nil {
		now = f.now
	}
	stat := f.stat
	if stat == nil {
		stat = os.Stat
	}
	bound := f.statTimeout
	if bound <= 0 {
		bound = 15 * time.Second
	}
	return &retiredRun{f: f, store: store, paths: paths, probe: probe, vetoes: vetoes, journals: journals,
		resolver: repairs.NewPathResolver(), minAge: minAge, now: now(),
		stat: stat, bound: bound, maxAbandoned: retiredStatMaxAbandoned}, nil
}

// book reads a book once per run; (nil, nil) is a book that does not exist.
func (r *retiredRun) book(id string) (*database.Book, error) {
	return cachedOnce(&r.books, id, func() (*database.Book, error) { return r.store.GetBookByID(id) })
}

// retiredLive reports whether b is a book that is neither soft-deleted nor
// labelled into the trash (database.IsInTrash covers both).
func retiredLive(b *database.Book) bool { return b != nil && !database.IsInTrash(b) }

// isLive reports whether id is a live book (retiredLive).
func (r *retiredRun) isLive(id string) (bool, error) {
	b, err := r.book(id)
	if err != nil {
		return false, fmt.Errorf("read book %s: %w", id, err)
	}
	return retiredLive(b), nil
}

func (r *retiredRun) group(gid string) ([]database.Book, error) {
	return cachedOnce(&r.groups, gid, func() ([]database.Book, error) { return r.store.GetBooksByVersionGroup(gid) })
}

// filesOf reads one book's file rows (the Core projection).
func (r *retiredRun) filesOf(id string) ([]database.BookFileCore, error) {
	m, err := r.store.GetBookFilesForIDsCore([]string{id})
	if err != nil {
		return nil, err
	}
	return m[id], nil
}

// retiredTarget is what a plan needs of a target book, read once per target
// however many shells point at it.
type retiredTarget struct {
	title   string
	files   int
	missing int
	// itunes is set when a file of the target sits under the frozen iTunes
	// tree (or its location could not be settled); itunesWhy says which.
	itunes    bool
	itunesWhy string
}

func (r *retiredRun) target(id string) (*retiredTarget, error) {
	return cachedOnce(&r.targets, id, func() (*retiredTarget, error) {
		b, err := r.book(id)
		if err != nil {
			return nil, fmt.Errorf("read target %s: %w", id, err)
		}
		if b == nil {
			return nil, fmt.Errorf("target %s does not exist", id)
		}
		files, err := r.filesOf(id)
		if err != nil {
			return nil, fmt.Errorf("read book_file rows of target %s: %w", id, err)
		}
		t := &retiredTarget{title: b.Title, files: len(files)}
		paths := []string{b.FilePath, retiredDeref(b.ITunesPath)}
		for i := range files {
			if files[i].Missing {
				t.missing++
			}
			paths = append(paths, files[i].FilePath, files[i].ITunesPath)
		}
		t.itunes, t.itunesWhy = r.itunesVerdict(id, paths)
		return t, nil
	})
}

// itunesVerdict runs the Repairs framework's own hands-off check over paths,
// symlinks resolved (a library symlink into books/itunes/** is under it however
// it is spelled). A path whose location could not be settled counts as a hit:
// it may be under the tree, and a preview does not guess it is not.
func (r *retiredRun) itunesVerdict(id string, paths []string) (bool, string) {
	kind, _ := repairs.GuardBookPathsWith(r.resolver, id, paths, "")
	switch kind {
	case repairs.SkipITunes:
		return true, "has a file under the iTunes library (books/itunes/**)"
	case repairs.SkipGuardUnreadable:
		return true, "could not tell whether a file is under the iTunes library (a path would not resolve)"
	}
	return false, ""
}

// undoShells reads the combine-journal index once per run: absorbed book id ->
// the unreversed journals that name it.
func (r *retiredRun) undoShells() (map[string][]string, error) {
	return cachedOnce(&r.undo, "journals", func() (map[string][]string, error) {
		idx, err := merge.UnreversedCombineShells(r.journals)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errRetiredUndoRead, err)
		}
		return idx, nil
	})
}

// vetoed returns the op family in which the owner rejected joining a and b
// ("" for none). The exact ids are asked about: there is no book merge-redirect
// resolver yet (the plan's RetiredBookResolver is a later PR).
func (r *retiredRun) vetoed(a, b string) (string, error) {
	for _, fam := range []string{decisions.FamilySameWork, decisions.FamilySameCopy} {
		v, err := r.vetoes.PairVetoed(retiredEntityBook, a, b, fam)
		if err != nil {
			return "", fmt.Errorf("read the owner's rejects for %s and %s: %w", a, b, err)
		}
		if v {
			return fam, nil
		}
	}
	return "", nil
}

// retiredFacts is everything one book's category is decided from.
type retiredFacts struct {
	shell database.BookCore
	files []database.BookFileCore

	groupID    string
	groupClass string

	targetID     string
	targetMethod string
	target       *retiredTarget
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
	// target and never used to pick one.
	rule4 string

	// itunesRows and itunesPIDRows are the cheap lexical counts, kept as
	// evidence; the gate itself uses the symlink-resolved check.
	itunesRows    int
	itunesPIDRows int
	// itunesSide and itunesWhy say which side tripped the iTunes gate
	// ("shell" or "target"; "" when neither did).
	itunesSide string
	itunesWhy  string

	missingFlag int
	// absentOnDisk and statUnknown are the stat counts, only computed for a
	// no-target row (-1 otherwise).
	absentOnDisk int
	statUnknown  int

	vetoFamily  string
	undoJournal []string
}

func retiredDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// classify reads everything a trashed book's category depends on and returns
// its row. A read that fails returns an error: the caller turns it into an
// error row, or (for an incomplete ownership lookup or a hung mount) fails the
// plan.
func (r *retiredRun) classify(ctx context.Context, shell database.BookCore) (repairs.Row, error) {
	files, err := r.filesOf(shell.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book_file rows of %s: %w", shell.ID, err)
	}
	fa := retiredFacts{shell: shell, files: files, groupClass: retGroupNone, absentOnDisk: -1, statUnknown: -1}
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
	if fa.targetID != "" {
		if fa.target, err = r.target(fa.targetID); err != nil {
			return repairs.Row{}, err
		}
	}
	if err := r.readPathOwners(&fa); err != nil {
		return repairs.Row{}, err
	}
	r.countITunes(&fa)

	cat, why, err := r.decide(ctx, &fa)
	if err != nil {
		return repairs.Row{}, err
	}
	return r.buildRow(&fa, cat, why), nil
}

// loadGroup reads the shell's version group and sets its shape. A member is
// live by retiredLive, the same test isLive makes, so a member labelled into
// the trash without the deletion flag does not count.
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
		if members[i].ID != fa.shell.ID && retiredLive(&members[i]) {
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
		if retiredLive(b) {
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
// same-path row into a plain one. A holder that is itself in the trash does
// not count (two trashed shells at one path share nothing live).
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

// countITunes counts, as evidence, the shell's rows that spell a path under
// the frozen iTunes tree or carry an iTunes persistent id. The gate does not
// use these counts: it uses the symlink-resolved check.
func (r *retiredRun) countITunes(fa *retiredFacts) {
	for i := range fa.files {
		if pathutil.UnderFrozenITunesTree(fa.files[i].FilePath) || pathutil.UnderFrozenITunesTree(fa.files[i].ITunesPath) {
			fa.itunesRows++
		}
		if fa.files[i].ITunesPersistentID != "" {
			fa.itunesPIDRows++
		}
	}
}

// shellITunes runs the resolved iTunes check over every path of the shell:
// the book's own two and each file row's two.
func (r *retiredRun) shellITunes(fa *retiredFacts) (bool, string) {
	paths := []string{fa.shell.FilePath, retiredDeref(fa.shell.ITunesPath)}
	for i := range fa.files {
		paths = append(paths, fa.files[i].FilePath, fa.files[i].ITunesPath)
	}
	return r.itunesVerdict(fa.shell.ID, paths)
}

// decide applies the category precedence documented on the retCat constants.
func (r *retiredRun) decide(ctx context.Context, fa *retiredFacts) (cat, why string, err error) {
	idx, err := r.undoShells()
	if err != nil {
		return "", "", err
	}
	if js := idx[fa.shell.ID]; len(js) > 0 {
		fa.undoJournal = js
		return retCatUndoWindow, "a merge that absorbed this book can still be undone (combine journal " + retiredJoinCapped(js, 3) + ")", nil
	}
	if strings.EqualFold(strings.TrimSpace(retiredDeref(fa.shell.LibraryState)), "deleted") {
		return retCatTrashLabelled, "labelled \"deleted\" (the trash label; a merge, a manual delete and a bulk delete all write it), so it cannot be told from a deliberate delete", nil
	}
	if len(fa.files) == 0 {
		return retCatNoFiles, "the book owns no file records, so there is nothing to retire", nil
	}
	if fa.targetID == "" && fa.ambiguous != "" {
		return retCatAmbiguous, fa.ambiguous + "; no target is guessed", nil
	}
	if fa.targetID == "" {
		absent, unknown, err := r.countAbsent(ctx, fa.files)
		if err != nil {
			return "", "", err
		}
		fa.absentOnDisk, fa.statUnknown = absent, unknown
		if absent == len(fa.files) {
			return retCatNoTargetMissing, "no live target (" + r.noTargetWhy(fa) + "); every file is missing from disk; stays in the trash", nil
		}
		return retCatNoTargetPresent, "no live target (" + r.noTargetWhy(fa) + "); at least one file is still on disk; stays in the trash", nil
	}

	// From here the row has a target.
	fam, err := r.vetoed(fa.shell.ID, fa.targetID)
	if err != nil {
		return "", "", err
	}
	if fam != "" {
		fa.vetoFamily = fam
		return retCatVetoed, "the owner rejected joining this book and its target (" + fam + ")", nil
	}
	if hit, w := r.shellITunes(fa); hit {
		fa.itunesSide, fa.itunesWhy = "shell", w
		return retCatITunes, "the book " + w + "; retiring it needs the owner's approval", nil
	}
	if fa.target.itunes {
		fa.itunesSide, fa.itunesWhy = "target", fa.target.itunesWhy
		return retCatITunes, "its target " + fa.target.itunesWhy + "; retiring onto it needs the owner's approval", nil
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
	case fa.sharedRows > 0:
		return retCatSamePath, fmt.Sprintf("%d row(s) share a path with a live book's row; both rows are kept", fa.sharedRows), nil
	}
	return retCatWouldRetire, "", nil
}

// retiredJoinCapped joins at most n ids and says how many more there were.
func retiredJoinCapped(ids []string, n int) string {
	if len(ids) <= n {
		return strings.Join(ids, ", ")
	}
	return strings.Join(ids[:n], ", ") + fmt.Sprintf(" and %d more", len(ids)-n)
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

// retiredStat is the answer of one bounded stat.
type retiredStat int

const (
	retStatPresent retiredStat = iota
	retStatAbsent
	retStatUnknown
)

// retiredNotExist reports whether err means the path is not there: not found,
// a path component that is not a directory, or a name too long to exist.
// Anything else (permission denied, I/O error) leaves the file's state
// unknown.
func retiredNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG)
}

// countAbsent counts the files that are not on disk now, and the files whose
// state could not be settled. A stat that fails for any reason other than "does
// not exist", or that outlives the bound, counts the file as present and as
// unknown: the preview never claims a file is missing on a guess, and says how
// many it did not settle. The stat is sequential within a row, the context is
// checked before each, and once this run has more hung stats than the cap the
// plan fails (errRetiredStatCap), as tag-backfill does for tag reads.
func (r *retiredRun) countAbsent(ctx context.Context, files []database.BookFileCore) (absent, unknown int, err error) {
	for i := range files {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		p := files[i].FilePath
		if p == "" {
			absent++
			continue
		}
		stat := r.stat
		got, cerr := boundedCall(ctx, r.bound, func() (retiredStat, error) {
			_, serr := stat(p)
			switch {
			case serr == nil:
				return retStatPresent, nil
			case retiredNotExist(serr):
				return retStatAbsent, nil
			}
			return retStatUnknown, nil
		}, &r.abandoned, &retiredStatsAbandoned)
		if cerr != nil {
			if ctx.Err() != nil {
				return 0, 0, ctx.Err()
			}
			unknown++
			if n := r.abandoned.Load(); n >= r.maxAbandoned {
				return 0, 0, fmt.Errorf("%w: %d of this run's file stats are still hung (cap %d); failing rather than stacking more blocked threads, check the mount", errRetiredStatCap, n, r.maxAbandoned)
			}
			continue
		}
		switch got {
		case retStatAbsent:
			absent++
		case retStatUnknown:
			unknown++
		}
	}
	return absent, unknown, nil
}

// retiredCurrent builds a row's Current map without its zero and empty
// entries: a plan holds one row per trashed book (tens of thousands) in one
// operation's stored result, so a key that says nothing is not stored.
type retiredCurrent map[string]string

func (c retiredCurrent) put(k, v string) {
	if v != "" {
		c[k] = v
	}
}

func (c retiredCurrent) putN(k string, n int) {
	if n > 0 {
		c[k] = strconv.Itoa(n)
	}
}

// buildRow turns a decided book into its Repairs row.
func (r *retiredRun) buildRow(fa *retiredFacts, cat, why string) repairs.Row {
	sh := fa.shell
	cur := retiredCurrent{"group_class": fa.groupClass, "rows": strconv.Itoa(len(fa.files))}
	cur.putN("rows_missing_flag", fa.missingFlag)
	cur.putN("rows_same_path", fa.sharedRows)
	cur.putN("rows_itunes", fa.itunesRows)
	cur.putN("itunes_pid_rows", fa.itunesPIDRows)
	cur.put("shares_paths_with", strings.Join(fa.sharedWith, ","))
	cur.put("target_book_id", fa.targetID)
	cur.put("target_method", fa.targetMethod)
	cur.put("library_state", retiredDeref(sh.LibraryState))
	cur.put("group_id", fa.groupID)
	if at := sh.MarkedForDeletionAt; at != nil && !at.IsZero() {
		cur["soft_deleted_at"] = at.UTC().Format(time.RFC3339)
		cur["age_days"] = strconv.Itoa(int(r.now.Sub(*at).Hours() / 24))
	}
	if fa.absentOnDisk >= 0 {
		cur["rows_absent_on_disk"] = strconv.Itoa(fa.absentOnDisk)
	}
	if fa.statUnknown >= 0 {
		cur.putN("rows_stat_unknown", fa.statUnknown)
	}
	cur.put("owner_veto_family", fa.vetoFamily)
	cur.put("itunes_side", fa.itunesSide)

	row := repairs.Row{
		RowID:   sh.ID,
		BookIDs: []string{sh.ID},
		Title:   sh.Title,
		Class:   cat,
		Risk:    repairs.RiskLow,
		Reason:  why,
		Current: cur,
		Members: []repairs.RowMember{{BookID: sh.ID, Title: sh.Title, Role: "shell", Files: len(fa.files), MissingFiles: fa.missingFlag}},
	}
	if fa.targetID == "" && fa.mergedLive != "" {
		cur["merged_into_target"] = fa.mergedLive
		row.Evidence = append(row.Evidence, "merged-into chain ends at live book "+fa.mergedLive+"; not a target, the book has no live version-group member")
	}
	if fa.targetID == "" && fa.rule4 != "" {
		cur["same_path_holder"] = fa.rule4
		row.Evidence = append(row.Evidence, "live book "+fa.rule4+" holds a row at every path of this book; not used as a target")
	}
	if fa.statUnknown > 0 {
		row.Evidence = append(row.Evidence, fmt.Sprintf("%d file(s) could not be checked on disk (timeout or error); counted as present", fa.statUnknown))
	}
	if len(fa.undoJournal) > 0 {
		row.Evidence = append(row.Evidence, "unreversed combine journal(s): "+retiredJoinCapped(fa.undoJournal, 3))
	}
	if fa.itunesSide != "" {
		row.Evidence = append(row.Evidence, "iTunes check tripped on the "+fa.itunesSide+": "+fa.itunesWhy)
	}
	if fa.targetID != "" {
		row.BookIDs = append(row.BookIDs, fa.targetID)
		row.Members = append(row.Members, repairs.RowMember{BookID: fa.targetID, Title: fa.target.title, Role: "target",
			Files: fa.target.files, MissingFiles: fa.target.missing})
	}
	if fa.sharedRows > 0 && fa.targetID != "" && (len(fa.sharedWith) > 1 || fa.sharedWith[0] != fa.targetID) {
		row.Risk = repairs.RiskReview
		row.Evidence = append(row.Evidence, "shares a path with a live book that is not the target")
	}
	if fa.itunesSide != "" || (fa.targetID == "" && fa.ambiguous != "") {
		row.Risk = repairs.RiskReview
	}

	switch cat {
	case retCatWouldRetire, retCatSamePath:
		row.Proposed = map[string]string{"retired_version_of": fa.targetID}
		if cat == retCatWouldRetire {
			row.Reason = "preview: its file records would move onto " + fa.targetID + " as a hidden retired version"
		} else {
			row.Reason = "preview: " + why + "; its file records would move onto " + fa.targetID + " as a hidden retired version"
		}
	default:
		if cat == retCatITunes && fa.targetID != "" {
			row.Proposed = map[string]string{"retired_version_of": fa.targetID}
		}
		row.Skipped, row.SkipReason = cat, why
	}
	row.Fingerprint = retiredFingerprint(fa, cat)
	return row
}

// retiredFingerprint hashes every input the category was decided from, so a
// re-plan that reads different state fingerprints differently.
func retiredFingerprint(fa *retiredFacts, cat string) string {
	parts := []string{cat, fa.shell.ID, fa.groupID, fa.groupClass, fa.targetID, fa.targetMethod, fa.ambiguous,
		retiredDeref(fa.shell.LibraryState), strconv.Itoa(fa.sharedRows), strings.Join(fa.sharedWith, ","),
		fa.rule4, fa.mergedLive, fa.itunesSide, fa.itunesWhy, strconv.Itoa(fa.absentOnDisk), strconv.Itoa(fa.statUnknown),
		fa.vetoFamily, strings.Join(fa.undoJournal, ",")}
	if fa.target != nil {
		parts = append(parts, strconv.Itoa(fa.target.files), strconv.Itoa(fa.target.missing), strconv.FormatBool(fa.target.itunes))
	}
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
	r.Fingerprint = retiredFingerprint(&retiredFacts{shell: shell, absentOnDisk: -1, statUnknown: -1}, retCatError+":"+err.Error())
	return r
}

// Plan classifies every book in the trash. Read-only.
func (f *retiredVersionsFixer) Plan(ctx context.Context, params json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	run, err := f.newRun(params)
	if err != nil {
		return nil, err
	}
	if n := retiredStatsAbandoned.Load(); n > 0 {
		_ = rep.Log(slog.LevelWarn, fmt.Sprintf("%d file stat(s) abandoned by earlier runs are still hung; check the mount", n))
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
		case errors.Is(err, errRetiredUndoRead):
			// Every row needs the journal index; one unreadable journal row
			// would make 51,000 error rows. Fail the plan.
			return err
		case errors.Is(err, errRetiredStatCap):
			// A hung mount: every further stat would stack another blocked
			// thread. Fail the plan.
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
	_ = rep.Log(slog.LevelInfo, retiredSummary(rows))
	return rows, nil
}

// retiredSummary is the end-of-plan log line: what a no-target row knew that
// the plan did not use as a target.
func retiredSummary(rows []repairs.Row) string {
	var noTarget, merged, holder, statUnknown int
	for i := range rows {
		switch rows[i].Class {
		case retCatNoTargetMissing, retCatNoTargetPresent:
			noTarget++
		}
		if rows[i].Current["merged_into_target"] != "" {
			merged++
		}
		if rows[i].Current["same_path_holder"] != "" {
			holder++
		}
		if rows[i].Current["rows_stat_unknown"] != "" {
			statUnknown++
		}
	}
	return fmt.Sprintf("retired-versions preview: %d trashed book(s); %d have no target, of which %d have a merged-into chain ending at a live book and %d have a live book at every path (neither used as a target); %d row(s) had files whose state on disk could not be settled",
		len(rows), noTarget, merged, holder, statUnknown)
}

// Replan classifies one book again from fresh reads, by the same code Plan
// runs. A book no longer in the trash comes back skipped "gone" with a
// different fingerprint, never an error. The combine-journal read is a full
// scan per call (no per-book index yet).
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
		r.Fingerprint = retiredFingerprint(&retiredFacts{shell: database.BookCore{ID: planned.RowID}, absentOnDisk: -1, statUnknown: -1}, retCatGone)
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
