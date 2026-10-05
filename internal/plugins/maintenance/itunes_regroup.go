// file: internal/plugins/maintenance/itunes_regroup.go
// version: 1.21.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"golang.org/x/sync/errgroup"
)

// CONS-FRAG-HEAL: the iTunes importer historically grouped tracks with an
// artist+album key (PR #1528 fixed it forward). Existing books accreted under the
// old key are both FRAGMENTED (anthologies split per story) and OVER-MERGED
// (empty-album tracks collapsed by artist). This op re-groups them IN PLACE to the
// books the fixed importer would produce — preserving enrichment / version groups
// / manual edits — instead of delete+reimport (which the canary proved tombstones
// PIDs and blocks recreation; see .claude/notes/itunes-heal-canary-findings.md).
//
// It is computed as a frozen, deterministic, exclusive-claim plan (one existing
// book targets at most one group) so dry-run == apply and over-merges actually
// split. Groups whose moves would change a version group's membership, the
// files of a version-group member other than a non-primary iTunes edition
// receiving its own album's tracks, or the files of a library-folder copy
// (any book with a row under RootDir, or organized) are skipped
// (itunesservice.entanglement holds the exact rule). Trashed and merged-away
// books are never a target or a source, and Doctor Who / Big Finish /
// Torchwood (applygate.IsOwnerManualOnly) are never touched. The apply
// re-reads each group's books and re-runs the same rule just before writing
// it (itunesservice.RegroupPlan.Recheck).
//
// What the apply writes: book rows (CreateBook for a fresh target, a Title-only
// ModifyBook, DeleteBook for books left with no files and no ext-ids),
// book_file rows (MoveBookFilesToBook* re-points the row's book_id; the row's
// file_path is unchanged), and itunes external-id mappings. It never touches a
// file on disk, never writes tags, and never reads or writes the ITL or iTunes
// XML beyond parsing the XML read-only in Phase 1.

type itunesRegroupParams struct {
	// DryRun defaults to TRUE when omitted (opmode.ResolveDryRun): a request
	// that states no mode is a preview. dry_run is accepted as an alias, and
	// sending both with different values is refused rather than guessed.
	DryRun      *bool  `json:"dryRun,omitempty"`
	DryRunSnake *bool  `json:"dry_run,omitempty"`
	XMLPath     string `json:"xmlPath"`
}

func (p *Plugin) itunesRegroupDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID: "itunes.regroup",
		// Renamed 2026-09-25 (naming audit class 8): every iTunes op lives in itunes.*, whichever Go package implements it.
		FormerIDs:       []string{"maintenance.itunes-regroup"},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Re-group fragmented/over-merged iTunes books in place",
		Description:     "Re-groups existing iTunes-imported books to match the FIXED importer grouping (CONS-FRAG): consolidates fragmented anthologies/chapter-parts and splits over-merged books, in place via per-PID external-id + BookFile reassignment (database rows only; no file on disk, tag, or ITL is touched), preserving enrichment and version groups. A group is skipped when its moves would take files out of a version-group member or a library-folder copy, or add them to a library-folder copy, a group's primary or an ambiguous group; a non-primary iTunes edition may receive its own album's ungrouped fragments. Trashed/merged books and Doctor Who / Big Finish / Torchwood are never touched; each group is re-checked on fresh rows just before it is written. Default dry-run reports the plan and how many groups the 2026-10-01 rule change unblocks; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "itunes.regroup",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runITunesRegroup,
	}
}

func (p *Plugin) runITunesRegroup(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params itunesRegroupParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	dryRun, err := opmode.ResolveDryRun("itunes.regroup", params.DryRunSnake, params.DryRun)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	xmlPath := strings.TrimSpace(params.XMLPath)
	if xmlPath == "" {
		xmlPath = strings.TrimSpace(config.AppConfig.ITunes.LibraryReadPath)
	}
	if xmlPath == "" {
		return fmt.Errorf("no iTunes XML path: set params.xmlPath or itunes.library_read_path")
	}
	// Without a root the planner cannot tell a library-folder copy from an
	// iTunes copy (BookMeta.HasLibraryFile would read false everywhere), and
	// that is the check keeping iTunes rows out of library copies. Refuse
	// rather than plan blind -- the dry run too, since its report would
	// count groups the apply must not touch.
	rootDir := strings.TrimSpace(config.AppConfig.RootDir)
	if rootDir == "" {
		return fmt.Errorf("itunes.regroup: root_dir is not configured; cannot tell library copies apart")
	}
	if dryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no changes will be written")
	}

	_ = reporter.UpdateProgress(0, 4, "Phase 1/4: parsing iTunes library…")
	lib, err := itunes.ParseLibrary(xmlPath)
	if err != nil {
		return fmt.Errorf("parse library %q: %w", xmlPath, err)
	}
	groups := itunesservice.GroupLibraryForHeal(lib)

	// The owner-manual check reads each book's tags, which OpsStore does not
	// carry; they come from their own accessor, composed in statically.
	rstore := regroupRunStore{OpsStore: store, BookTagReader: p.deps.BookTagReader()}
	if rstore.BookTagReader == nil {
		return fmt.Errorf("itunes.regroup: no book-tag reader for the owner-manual check")
	}

	_ = reporter.UpdateProgress(1, 4, fmt.Sprintf("Phase 2/4: snapshotting DB for %d target groups…", len(groups)))
	snap, err := p.buildRegroupSnapshot(ctx, rstore, rootDir, reporter)
	if err != nil {
		return err
	}

	_ = reporter.UpdateProgress(2, 4, "Phase 3/4: planning…")
	plan := itunesservice.PlanRegroup(groups, snap)

	summary := fmt.Sprintf(
		"groups=%d already-correct=%d consolidate=%d entangled-skipped=%d manual-only-skipped=%d library-title-kept=%d fresh-books=%d delete-empty=%d | PIDs resolved=%d unresolved=%d | %s",
		plan.TotalGroups, plan.AlreadyCorrect, plan.Consolidated, plan.EntangledSkipped, plan.ManualOnlySkipped, plan.LibraryTitleKept,
		plan.FreshBooks, len(plan.DeleteBooks), plan.PIDsResolved, plan.PIDsUnresolved,
		regroupRuleDelta(plan))
	_ = reporter.Log(slog.LevelInfo, "PLAN: "+summary)
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"RULE CHANGE (2026-10-01): %s | legacy-rule-skipped=%d | skips by reason: %s | unblocked examples: %s",
		regroupRuleDelta(plan), plan.LegacyEntangledSkipped, regroupSkipReasons(plan),
		strings.Join(plan.UnblockedExamples, "; ")))
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"COMPLETENESS: complete-groups=%d partial-groups=%d (missing some tracks) single-file-in-multitrack-album=%d",
		plan.CompleteGroups, plan.PartialGroups, plan.SingleFileChapterBooks))
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"SINGLE-FILE BY DURATION: <15min(true chapter/clip)=%d  15-90min(ambiguous)=%d  >=90min(COMPLETE book, false alarm)=%d | short examples: %s",
		plan.SFCShort, plan.SFCMid, plan.SFCLong, strings.Join(plan.SFCExamples, "; ")))

	if dryRun {
		examples := regroupExamples(plan, 8)
		_ = reporter.Log(slog.LevelInfo, "DRY RUN examples: "+strings.Join(examples, " | "))
		_ = reporter.UpdateProgress(4, 4, "DRY RUN — "+summary)
		return nil
	}

	_ = reporter.UpdateProgress(3, 4, "Phase 4/4: applying plan…")
	if err := p.applyRegroupPlan(ctx, rstore, plan, rootDir, reporter); err != nil {
		return err
	}
	_ = reporter.UpdateProgress(4, 4, "APPLIED — "+summary)
	return nil
}

// regroupRunStore is the run's store: OpsStore plus the tag reads the
// owner-manual check makes (BookTagReader, its own accessor because OpsStore
// is at the interfacebloat cap). The two method sets do not overlap.
type regroupRunStore struct {
	OpsStore
	BookTagReader
}

// itunesRegroupSnapshotReader is what buildRegroupSnapshot reads: the shared
// regroup snapshot reader plus every series row, every live book's tags in
// one call, and the author-credit reads, so the whole-book owner-manual
// check (applygate.BookManualOnly) runs without a per-book file, series or
// tag read.
type itunesRegroupSnapshotReader interface {
	regroupSnapshotReader
	GetAllSeries() ([]database.Series, error)
	GetBookTagsByBookIDs(bookIDs []string) (map[string][]string, error)
	database.BookAuthorReader
}

// manualOnlyFilesByBook serves book_file rows a caller already read to the
// owner-manual check (applygate.ManualOnlyFilesReader), so the check sees
// exactly the rows the caller's own decision was built from -- no second read
// that could disagree with them. itunes.regroup's snapshot and recheck and
// author-strip-merge's relink use it.
type manualOnlyFilesByBook map[string][]database.BookFile

func (r manualOnlyFilesByBook) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return r[bookID], nil
}

// regroupSeriesReader serves series rows from the snapshot's GetAllSeries.
type regroupSeriesReader map[int]*database.Series

func (r regroupSeriesReader) GetSeriesByID(id int) (*database.Series, error) { return r[id], nil }

// regroupTagsReader serves the bulk GetBookTagsByBookIDs result to the
// owner-manual check, which only reads BookTag.Tag.
type regroupTagsReader map[string][]string

func (r regroupTagsReader) GetBookTagsDetailed(bookID string) ([]database.BookTag, error) {
	tags := r[bookID]
	out := make([]database.BookTag, len(tags))
	for i, t := range tags {
		out[i] = database.BookTag{BookID: bookID, Tag: t}
	}
	return out, nil
}

// regroupManualOnlyFile is the part of a book_file row the owner-manual check
// reads (applygate.BulkManualOnlyGuard: the path and the two transcribed
// fields). The snapshot keeps only these: a full BookFile per row for a
// 300,000-row library would be the snapshot's largest allocation by far.
func regroupManualOnlyFile(f *database.BookFileCore) database.BookFile {
	return database.BookFile{ID: f.ID, BookID: f.BookID, FilePath: f.FilePath,
		TranscribedTitle: f.TranscribedTitle, TranscribedAuthor: f.TranscribedAuthor}
}

// regroupManualOnly is the planner's BookMeta.ManualOnly for one book: the
// whole-book owner-manual check. The snapshot and the apply-time recheck both
// call it, with readers over the rows each of them read, so the two can only
// disagree when the data changed between them.
func regroupManualOnly(r applygate.ManualOnlyReaders, b *database.Book) (bool, error) {
	held, _, err := applygate.BookManualOnly(r, b)
	if err != nil {
		return false, fmt.Errorf("owner-manual check of %s: %w", b.ID, err)
	}
	return held, nil
}

// buildRegroupSnapshot reads the immutable DB state the planner reasons over via
// bulk in-memory scans — all books once, all book files once, all series once —
// instead of tens of thousands of per-PID / per-book point queries (which made
// the dry-run take >10min on a 65K/308K library). The file scan yields
// PID→location directly from BookFile.ITunesPersistentID, so no per-PID
// lookups are needed. No mutation.
//
// Only LIVE books (not soft-deleted, not merged away) enter snap.Books, and
// only their files enter snap.PIDLoc: the book scan can omit a trashed book
// while the file scan still returns its rows, and a PID on such a row must
// never make that book a target or a source. Version-group membership still
// counts every scanned member, so the incumbent rule sees the whole group.
func (p *Plugin) buildRegroupSnapshot(ctx context.Context, store itunesRegroupSnapshotReader, rootDir string, reporter sdk.Reporter) (itunesservice.Snapshot, error) {
	snap := itunesservice.Snapshot{
		PIDLoc: make(map[string]itunesservice.PIDLoc),
		Books:  make(map[string]itunesservice.BookMeta),
	}

	seriesRows, err := store.GetAllSeries()
	if err != nil {
		return snap, fmt.Errorf("GetAllSeries: %w", err)
	}
	seriesByID := make(regroupSeriesReader, len(seriesRows))
	for i := range seriesRows {
		seriesByID[seriesRows[i].ID] = &seriesRows[i]
	}

	// Pass 1: all books → per-book rows + version-group membership.
	books := make(map[string]*database.Book)
	live := make(map[string]bool)                  // book id -> eligible: scanned, not soft-deleted, not merged away
	notTrashed := make(map[string]bool)            // book id -> scanned and not soft-deleted (merge-survivor liveness)
	vgMembers := make(map[string][]*database.Book) // version-group id -> members, scan (book-ID) order
	vgLegacyNonPrimary := make(map[string]bool)    // version-group id -> has a member not explicitly true (legacy rule)
	const page = 1000
	afterID := ""
	scanned := 0
	for {
		if ctx.Err() != nil {
			return snap, ctx.Err()
		}
		batch, err := store.GetAllBooksFullFrom(afterID, page)
		if err != nil {
			return snap, fmt.Errorf("GetAllBooksFullFrom afterID=%q: %w", afterID, err)
		}
		if len(batch) == 0 {
			break
		}
		for i := range batch {
			b := &batch[i]
			books[b.ID] = b
			live[b.ID] = regroupEligible(b)
			notTrashed[b.ID] = !b.IsSoftDeleted()
			if b.VersionGroupID != nil && *b.VersionGroupID != "" {
				vg := *b.VersionGroupID
				vgMembers[vg] = append(vgMembers[vg], b)
				if b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion {
					vgLegacyNonPrimary[vg] = true
				}
			}
		}
		scanned += len(batch)
		afterID = batch[len(batch)-1].ID
		_ = reporter.UpdateProgress(1, 4, fmt.Sprintf("Phase 2/4: scanned %d books…", scanned))
		if len(batch) < page {
			break
		}
	}

	// Pass 2: all book files of LIVE books → PID→location + per-book facts.
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return snap, fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	facts := make(map[string]*regroupFileFacts, len(books))
	moFiles := make(manualOnlyFilesByBook, len(books))
	for i := range files {
		f := &files[i]
		if !live[f.BookID] {
			continue
		}
		moFiles[f.BookID] = append(moFiles[f.BookID], regroupManualOnlyFile(f))
		ff := facts[f.BookID]
		if ff == nil {
			ff = &regroupFileFacts{}
			facts[f.BookID] = ff
		}
		pid := strings.TrimSpace(f.ITunesPersistentID)
		ff.add(f.FilePath, pid, rootDir)
		if pid != "" {
			snap.PIDLoc[pid] = itunesservice.PIDLoc{FileID: f.ID, BookID: f.BookID}
		}
	}

	// Every version group's incumbent primary, over ALL its members (a library
	// copy holding no iTunes PID still decides which member is primary).
	// alive is a merge survivor's liveness for ElectableRow: not trashed, as
	// in version_group_primary_repair and reconcile. It is deliberately NOT
	// live (eligible), which also drops merged-away books: in a chain
	// L->S->T that would read S as dead and make L electable again.
	//
	// versionprimary.Incumbent answers an in-group member's liveness from the
	// member row itself (!IsSoftDeleted), which is the same row notTrashed was
	// built from, so routing those through it instead of alive changes
	// nothing. The members are copied per group (Incumbent takes values) and
	// dropped after: the rows themselves are already held in books.
	alive := func(id string) bool { return notTrashed[id] }
	incumbent := make(map[string]string, len(vgMembers))
	for vg, ptrs := range vgMembers {
		members := make([]database.Book, len(ptrs))
		for i, m := range ptrs {
			members[i] = *m
		}
		if inc := versionprimary.Incumbent(members, alive); inc != nil {
			incumbent[vg] = inc.ID
		}
	}

	// The whole-book owner-manual check for every live book. Files, series and
	// tags are served from the bulk reads (one call each); the author credits
	// are the one per-book read (GetBookAuthors, a point get, plus
	// GetAuthorByID per credit) since the store has no bulk credit read.
	// Run on a NumCPU worker pool (CLAUDE.md: a per-item DB read over the
	// whole library). Each worker writes only its own slot of manual, so no
	// lock is needed. A read failure fails the snapshot: the planner cannot be
	// told a book is not owner-manual when that could not be checked.
	liveIDs := make([]string, 0, len(books))
	for id := range books {
		if live[id] {
			liveIDs = append(liveIDs, id)
		}
	}
	sort.Strings(liveIDs)
	tagMap, err := store.GetBookTagsByBookIDs(liveIDs)
	if err != nil {
		return snap, fmt.Errorf("GetBookTagsByBookIDs: %w", err)
	}
	mo := applygate.ManualOnlyReaders{Files: moFiles, Series: seriesByID, Authors: store, Tags: regroupTagsReader(tagMap)}
	manual := make([]bool, len(liveIDs))
	eg, ectx := errgroup.WithContext(ctx)
	eg.SetLimit(runtime.NumCPU())
	for i, id := range liveIDs {
		eg.Go(func() error {
			if err := ectx.Err(); err != nil {
				return err
			}
			held, err := regroupManualOnly(mo, books[id])
			manual[i] = held
			return err
		})
	}
	if err := eg.Wait(); err != nil {
		return snap, err
	}

	for i, id := range liveIDs {
		b := books[id]
		vg := ""
		if b.VersionGroupID != nil {
			vg = *b.VersionGroupID
		}
		ff := facts[id]
		if ff == nil {
			ff = &regroupFileFacts{}
		}
		snap.Books[id] = regroupBookMeta(b, ff, incumbent[vg], vgLegacyNonPrimary[vg], manual[i])
	}
	_ = reporter.UpdateProgress(2, 4, fmt.Sprintf("Phase 2/4: snapshot ready (%d books, %d PID locations)", len(snap.Books), len(snap.PIDLoc)))
	return snap, nil
}

// regroupEligible: the book may be a regroup target or source -- not
// soft-deleted and not merged into another book.
func regroupEligible(b *database.Book) bool {
	return !b.IsSoftDeleted() && (b.MergedIntoBookID == nil || *b.MergedIntoBookID == "")
}

// regroupFileFacts is what the planner needs from one book's book_file rows.
// The full snapshot and the apply-time recheck both build it through add, so
// the two predicates read the same facts.
type regroupFileFacts struct {
	count       int
	withoutPID  int
	libraryFile bool // a row (missing or not) under the root, outside the frozen iTunes tree
	nonITunes   bool // a row outside the frozen iTunes tree (anywhere)
}

func (ff *regroupFileFacts) add(path, pid, rootDir string) {
	ff.count++
	if pid == "" {
		ff.withoutPID++
	}
	// Same library-copy test as itunes.clone-into-library, except a missing
	// row counts too: this decides a refusal, so it errs toward refusing.
	// Both sides are filepath.Clean'd first, as versionprimary's signals do:
	// IsWithin compares raw strings, so a root spelled "/library/./" or
	// "/library/" (or a row path with "//" or "./") would otherwise miss
	// every library copy and let iTunes rows be poured into one. A symlinked
	// root spelling is not resolved here (no filesystem access in the
	// planner); entanglement's organized-target refusal is the backstop.
	clean := filepath.Clean(path)
	inITunes := pathutil.UnderFrozenITunesTree(clean)
	if !inITunes {
		ff.nonITunes = true
	}
	if rootDir != "" && pathutil.IsWithin(clean, filepath.Clean(rootDir)) && !inITunes {
		ff.libraryFile = true
	}
}

// regroupBookMeta assembles the planner's view of one live book. incumbent is
// the ID of its version group's incumbent primary ("" = none or ungrouped).
// manualOnly is regroupManualOnly's answer for the book.
func regroupBookMeta(b *database.Book, ff *regroupFileFacts, incumbent string, legacyNonPrimary bool, manualOnly bool) itunesservice.BookMeta {
	vg := ""
	if b.VersionGroupID != nil {
		vg = *b.VersionGroupID
	}
	dur := 0
	if b.Duration != nil {
		dur = *b.Duration
	}
	// An unknown creation time ranks as newest, so it never wins the
	// older-is-better tiebreak over a row whose age is known. (A nil
	// CreatedAt used to panic the whole snapshot.)
	created := int64(math.MaxInt64)
	if b.CreatedAt != nil {
		created = b.CreatedAt.Unix()
	}
	isPrimary := database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion)
	if vg != "" {
		isPrimary = incumbent == b.ID
	}
	return itunesservice.BookMeta{
		ID:                  b.ID,
		Title:               b.Title,
		IsPrimary:           isPrimary,
		FileCount:           ff.count,
		DurationSec:         dur,
		EnrichScore:         enrichScore(b),
		CreatedAtUnix:       created,
		VersionGroupID:      vg,
		GroupHasNoIncumbent: vg != "" && incumbent == "",
		LegacyEntangled:     vg != "" && legacyNonPrimary,
		HasLibraryFile:      ff.libraryFile,
		HasNonITunesFile:    ff.nonITunes,
		Organized:           b.LibraryState != nil && *b.LibraryState == "organized",
		FilesWithoutPID:     ff.withoutPID,
		ManualOnly:          manualOnly,
	}
}

// regroupRuleDelta formats the per-group rule-change delta for the summary.
func regroupRuleDelta(plan itunesservice.RegroupPlan) string {
	return fmt.Sprintf("rule-delta: unblocked=%d (legacy skipped, now consolidate) newly-blocked=%d (legacy consolidated, now skipped)",
		plan.Unblocked, plan.NewlyBlocked)
}

// regroupSkipReasons formats EntangledByReason deterministically.
func regroupSkipReasons(plan itunesservice.RegroupPlan) string {
	if len(plan.EntangledByReason) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(plan.EntangledByReason))
	for k := range plan.EntangledByReason {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, plan.EntangledByReason[k]))
	}
	return strings.Join(parts, " ")
}

// enrichScore counts populated enrichment fields so the planner prefers the
// richest existing book as the survivor.
func enrichScore(b *database.Book) int {
	score := 0
	nonEmpty := func(s *string) bool { return s != nil && strings.TrimSpace(*s) != "" }
	if nonEmpty(b.ISBN13) {
		score++
	}
	if nonEmpty(b.ISBN10) {
		score++
	}
	if nonEmpty(b.ASIN) {
		score++
	}
	if nonEmpty(b.Description) {
		score++
	}
	if nonEmpty(b.Narrator) {
		score++
	}
	if nonEmpty(b.Publisher) {
		score++
	}
	return score
}

// applyRegroupPlan executes the frozen plan: gather each group's files onto its
// target (creating a fresh book when the target was contested), set the canonical
// title, then delete books that end empty — re-asserting no files AND no ext-id
// mappings before each delete (the canary lesson: zero files ≠ zero PID mappings).
//
// Before each group is written, its target and sources are re-read and the
// plan's refusal rules re-run on the fresh rows (regroupRecheck); a group
// whose books changed since the snapshot is skipped and counted.
func (p *Plugin) applyRegroupPlan(ctx context.Context, store itunesRegroupStore, plan itunesservice.RegroupPlan, rootDir string, reporter sdk.Reporter) error {
	touched := make(map[string]bool)
	var c regroupApplyCounts
	var deleted, deleteSkipped int

	for gi, a := range plan.Groups {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.Entangled || a.ManualOnly || (a.Target == "" && !a.FreshBook) {
			continue // skipped or nothing-in-DB
		}
		applyRegroupGroup(store, plan, gi, rootDir, reporter, touched, &c)
	}
	moved, titled, titleKept, created, recheckSkipped := c.moved, c.titled, c.titleKept, c.created, c.recheckSkipped
	errCount := c.errCount

	// Delete projected-empty books, GUARDED: re-assert no files AND no ext-ids.
	for _, id := range plan.DeleteBooks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		files, ferr := store.GetBookFiles(id)
		exts, eerr := store.GetExternalIDsForBook(id)
		// Fail CLOSED: if either read errored we cannot prove the book is empty,
		// so both slices may be a misleading nil (len 0). Skip the delete rather
		// than risk removing a book that still owns files or iTunes PID ext-ids —
		// the exact canary this guard exists to prevent.
		if ferr != nil || eerr != nil {
			deleteSkipped++
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("skip delete %s: could not verify empty (files err=%v, ext-ids err=%v)", id, ferr, eerr))
			continue
		}
		if len(files) != 0 || len(exts) != 0 {
			deleteSkipped++
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("skip delete %s: %d files, %d ext-ids remain", id, len(files), len(exts)))
			continue
		}
		if err := store.DeleteBook(id); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("delete %s failed: %v", id, err))
			errCount++
			continue
		}
		deleted++
	}

	// Recompute aggregates for every touched target.
	for id := range touched {
		if err := store.RecomputeBookAggregates(id); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("recompute %s failed: %v", id, err))
			errCount++
		}
	}

	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"APPLIED: moved=%d titled=%d library-title-kept=%d fresh=%d deleted=%d delete-skipped=%d recheck-skipped=%d errors=%d",
		moved, titled, titleKept, created, deleted, deleteSkipped, recheckSkipped, errCount))
	if errCount > 0 {
		return fmt.Errorf("%d errors during itunes-regroup (see op log)", errCount)
	}
	return nil
}

// regroupApplyCounts are applyRegroupPlan's per-group tallies.
type regroupApplyCounts struct {
	moved, titled, titleKept, created, recheckSkipped, errCount int
}

// applyRegroupGroup rechecks and writes plan.Groups[gi].
//
// The target's version group lock (versionprimary.LockGroups) is held from
// before the recheck reads the group until the group's last write here, so a
// hand-off (EnsureSinglePrimary, Crown, a trash restore or a combine undo)
// cannot make the target the group's primary between the recheck that found
// it a non-primary member and the moves onto it, and no membership writer
// (each holds the group a book leaves and the one it joins) can move it in
// or out meanwhile. An ungrouped target takes the no-group sentinel instead,
// which every write moving a book into a group from none holds, so it stays
// ungrouped until this group is written.
//
// Lock order: this group lock, then the per-book write locks
// MoveBookFilesToBook(Bulk), ReassignExternalID, CreateBook and ModifyBook
// take and release internally -- the order LockGroup documents. Nothing here
// takes merge.LockMergeRMW or hands off a primary, so the non-reentrant group
// lock is never re-taken on this goroutine.
//
// The group ID is read from the target before locking and compared with the
// one the recheck reads under the lock; a target that changed groups in
// between (including joining one) is refused as changed rather than written
// under the wrong lock or none.
func applyRegroupGroup(store itunesRegroupStore, plan itunesservice.RegroupPlan, gi int, rootDir string, reporter sdk.Reporter, touched map[string]bool, c *regroupApplyCounts) {
	a := plan.Groups[gi]
	lockedGID := ""
	if !a.FreshBook {
		tb, err := store.GetBookByID(a.Target)
		if err != nil {
			c.recheckSkipped++
			c.errCount++
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("skip group %q: apply-time recheck could not read target %s: %v", a.Title, a.Target, err))
			return
		}
		if tb != nil && tb.VersionGroupID != nil {
			lockedGID = *tb.VersionGroupID
		}
		// "" takes the no-group sentinel: every write that moves a book
		// into a group from none holds it, so an ungrouped target stays
		// ungrouped until this group is written.
		defer versionprimary.LockGroups(lockedGID)()
	}

	why, keepTitle, err := regroupRecheck(store, plan, gi, rootDir, lockedGID)
	if err != nil {
		c.recheckSkipped++
		c.errCount++
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("skip group %q: apply-time recheck could not read its books: %v", a.Title, err))
		return
	}
	if why != "" {
		c.recheckSkipped++
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("skip group %q (target %s): changed since the plan, recheck refuses it as %s", a.Title, a.Target, why))
		return
	}

	target := a.Target
	if a.FreshBook {
		nb, err := store.CreateBook(&database.Book{Title: a.Title})
		if err != nil || nb == nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("create fresh book for %q failed: %v", a.Title, err))
			c.errCount++
			return
		}
		target = nb.ID
		c.created++
	}

	// Move this group's files in ONE batch. Each MoveBookFilesToBook call
	// recomputes both of its books, so the previous per-file loop cost two
	// full re-reads of the target's growing file set per file — an O(N^2)
	// shape on a plan that can carry thousands of moves. The bulk form pays
	// one recompute per distinct book for the whole group.
	movedOK := a.Moves
	if len(a.Moves) > 0 {
		bulk := make([]database.BookFileMove, 0, len(a.Moves))
		for _, m := range a.Moves {
			bulk = append(bulk, database.BookFileMove{FileIDs: []string{m.FileID}, SourceBookID: m.From})
		}
		bulkErr := store.MoveBookFilesToBookBulk(bulk, target)
		if errors.Is(bulkErr, database.ErrBookFileDurabilityUnknown) {
			// Everything moved (visible); only the fsync failed. The
			// per-file retry below would find the rows gone from their
			// sources and count every move as failed.
			_ = reporter.Log(slog.LevelError, fmt.Sprintf("bulk move of %d files -> %s was written but its fsync failed (%v): durability unknown; treating it as applied", len(bulk), target, bulkErr))
			bulkErr = nil
		}
		if err := bulkErr; err != nil {
			// The bulk form is atomic, so NOTHING moved. Fall back to the
			// per-file loop rather than failing the whole group: a plan is
			// frozen ahead of the apply, so a single file that vanished in
			// between must not block every other move in the group. This is
			// the resilience the per-file loop always had; the batch is only
			// the fast path.
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
				"bulk move of %d files -> %s failed (%v); retrying per file", len(bulk), target, err))
			// Fresh slice, NOT movedOK[:0] — movedOK aliases a.Moves here, and
			// truncate-then-append would rewrite the frozen plan's own backing
			// array underneath the caller.
			movedOK = make([]itunesservice.FileMove, 0, len(a.Moves))
			for _, m := range a.Moves {
				if err := store.MoveBookFilesToBook([]string{m.FileID}, m.From, target); err != nil {
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("move file %s %s->%s failed: %v", m.FileID, m.From, target, err))
					c.errCount++
					continue
				}
				movedOK = append(movedOK, m)
			}
		}
	}

	for _, m := range movedOK {
		if err := store.ReassignExternalID("itunes", m.PID, target); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("reassign pid %s->%s failed: %v", m.PID, target, err))
			c.errCount++
		}
		c.moved++
	}

	// Set the canonical title (fixes chapter-suffix leaks on survivors too).
	// Write only Title, under the book's write lock (ModifyBook), so a
	// column another writer commits meanwhile is not reverted (audit
	// A1#15); a survivor already carrying the canonical title is skipped.
	// A library-folder copy (planned or as re-read just now) keeps its
	// title: its metadata comes from the metadata pipeline, never from
	// an iTunes album tag.
	if keepTitle {
		c.titleKept++
		touched[target] = true
		return
	}
	retitled := false
	if written, err := store.ModifyBook(target, func(tb *database.Book) error {
		if tb.Title == a.Title {
			return database.ErrSkipBookWrite
		}
		tb.Title = a.Title
		retitled = true
		return nil
	}); err != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("set title on %s failed: %v", target, err))
		c.errCount++
	} else if written != nil && retitled {
		c.titled++
	}
	touched[target] = true
}

// regroupRecheck re-reads plan.Groups[gi]'s target and every move source --
// each book row, its book_file rows, its series name and, for a grouped book,
// its version group's members -- builds the same BookMeta the snapshot builds
// (regroupBookMeta, regroupFileFacts) and runs plan.Recheck on it. It returns
// the refusal reason ("" = still allowed), whether the target must keep its
// title (planned KeepTitle, or the fresh target is now a library copy), or a
// read error, which the caller treats as a refusal (fail closed).
//
// lockedGID is the version group whose hand-off lock the caller holds ("" =
// none, the target was ungrouped when the caller read it). A target whose
// fresh version group differs is refused as EntangleChanged: its incumbent
// would be read without the lock that keeps it valid through the moves.
func regroupRecheck(store itunesRegroupStore, plan itunesservice.RegroupPlan, gi int, rootDir, lockedGID string) (string, bool, error) {
	a := plan.Groups[gi]
	ids := make([]string, 0, len(a.Moves)+1)
	if !a.FreshBook {
		ids = append(ids, a.Target)
	}
	for _, m := range a.Moves {
		ids = append(ids, m.From)
	}
	fresh := itunesservice.Snapshot{
		PIDLoc: make(map[string]itunesservice.PIDLoc),
		Books:  make(map[string]itunesservice.BookMeta),
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		b, err := store.GetBookByID(id)
		if err != nil {
			return "", false, fmt.Errorf("GetBookByID %s: %w", id, err)
		}
		if b == nil || !regroupEligible(b) {
			continue // left out of fresh.Books: Recheck refuses it as unknown-book
		}
		if !a.FreshBook && id == a.Target {
			gid := ""
			if b.VersionGroupID != nil {
				gid = *b.VersionGroupID
			}
			if gid != lockedGID {
				return itunesservice.EntangleChanged, a.KeepTitle, nil
			}
		}
		files, err := store.GetBookFiles(id)
		if err != nil {
			return "", false, fmt.Errorf("GetBookFiles %s: %w", id, err)
		}
		ff := &regroupFileFacts{}
		for i := range files {
			pid := strings.TrimSpace(files[i].ITunesPersistentID)
			ff.add(files[i].FilePath, pid, rootDir)
			if pid != "" {
				fresh.PIDLoc[pid] = itunesservice.PIDLoc{FileID: files[i].ID, BookID: id}
			}
		}
		// The same whole-book check the snapshot made, over the rows read
		// here: these files, and the store's series, credits and tags.
		manual, err := regroupManualOnly(applygate.ManualOnlyReaders{
			Files: manualOnlyFilesByBook{id: files}, Series: store, Authors: store, Tags: store,
		}, b)
		if err != nil {
			return "", false, err
		}
		incumbent, legacy := "", false
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			incumbent, legacy, err = regroupGroupIncumbent(store, *b.VersionGroupID, b)
			if err != nil {
				return "", false, err
			}
		}
		fresh.Books[id] = regroupBookMeta(b, ff, incumbent, legacy, manual)
	}
	keep := a.KeepTitle
	if t, ok := fresh.Books[a.Target]; ok && !a.FreshBook && itunesservice.LibraryCopy(t) {
		keep = true
	}
	return plan.Recheck(gi, fresh), keep, nil
}

// regroupGroupIncumbent reads version group vg's members and returns its
// incumbent primary and whether any member is not explicitly primary (the
// legacy-rule input). self is the book being rechecked: GetBooksByVersionGroup
// omits soft-deleted rows, so it is added if missing. A merge survivor's
// liveness is read from the store; a read error fails the recheck.
func regroupGroupIncumbent(store itunesRegroupStore, vg string, self *database.Book) (string, bool, error) {
	rows, err := store.GetBooksByVersionGroup(vg)
	if err != nil {
		return "", false, fmt.Errorf("GetBooksByVersionGroup %s: %w", vg, err)
	}
	members := make([]database.Book, 0, len(rows)+1)
	legacy, haveSelf := false, false
	for i := range rows {
		members = append(members, rows[i])
		if rows[i].IsPrimaryVersion == nil || !*rows[i].IsPrimaryVersion {
			legacy = true
		}
		haveSelf = haveSelf || rows[i].ID == self.ID
	}
	if !haveSelf {
		members = append(members, *self)
	}
	// Book-ID order, as the snapshot scans: Incumbent's "first explicit
	// true" must pick the same member in both.
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	// Incumbent answers an in-group survivor's liveness from its own row; the
	// rows come from the same read, so that matches a store read. Only a
	// survivor outside the read (GetBooksByVersionGroup omits trashed rows,
	// and a survivor may sit in another group or none) reaches this closure.
	var readErr error
	alive := func(id string) bool {
		b, err := store.GetBookByID(id)
		if err != nil {
			readErr = fmt.Errorf("GetBookByID %s (merge survivor): %w", id, err)
			return false
		}
		return b != nil && !b.IsSoftDeleted() // same liveness as the snapshot's alive
	}
	inc := ""
	if m := versionprimary.Incumbent(members, alive); m != nil {
		inc = m.ID
	}
	if readErr != nil {
		return "", false, readErr
	}
	return inc, legacy, nil
}

// regroupExamples returns a few human-readable sample actions for the dry-run log.
func regroupExamples(plan itunesservice.RegroupPlan, n int) []string {
	out := make([]string, 0, n)
	for _, a := range plan.Groups {
		if len(out) >= n {
			break
		}
		switch {
		case a.ManualOnly:
			out = append(out, fmt.Sprintf("SKIP(owner-manual-only) %q", a.Title))
		case a.Entangled:
			out = append(out, fmt.Sprintf("SKIP(entangled:%s) %q", a.EntangleReason, a.Title))
		case len(a.Moves) > 0 && a.FreshBook:
			out = append(out, fmt.Sprintf("SPLIT→fresh %q (%d files)", a.Title, len(a.Moves)))
		case len(a.Moves) > 0:
			out = append(out, fmt.Sprintf("MERGE→%s %q (%d files)", a.Target, a.Title, len(a.Moves)))
		}
	}
	return out
}
