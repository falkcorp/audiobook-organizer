// file: internal/plugins/maintenance/merge_cleanup_fixer.go
// version: 1.0.1
// guid: 0e4c7d58-a1b3-4f26-8c19-6d5a3e7f9b02
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// mergeCleanupFixerID is the Repairs-lane id of the merge-cleanup fixer. By the
// pattern the existing merge-shaped keys follow (`repairs:retired-versions`,
// `repairs:fragment-consolidation`, the namespaced `repairs:dedup.split-books`)
// its reject op kind is `repairs:` plus this id; nothing in this tree composes
// that string, so TODO-MERGE-CLEANUP-REJECT-FAMILY verifies it end to end.
const mergeCleanupFixerID = "maintenance.merge-cleanup"

// Row flags: facts a row carries beside its class and skip kind. A skip kind
// counts only a row's FIRST hold (PlanResult.SkippedByKind), so every census
// item a later PR depends on is also a flag, which counts across every class.
const (
	mcFlagChainViaMerged    = "chain_via_merged"
	mcFlagChainViaTrashed   = "chain_via_trashed"
	mcFlagGroupNeedsHandoff = "group_needs_handoff"
	mcFlagSyncRedirect      = "sync_redirect_present"
	mcFlagOwnerSaysDistinct = "owner_says_distinct"
	// Origin evidence. MATCH-4 (metafetch FlagMetadataHashDuplicate) is the only
	// writer that leaves a pointer on a book it does not also trash, and it
	// writes exactly two fields: the pointer and an explicit false primary flag.
	// match4: that signature AND the metadata-source hash the pair was merged on.
	// match4_partial: the signature but no shared hash. other: anything else
	// (a revert that put an old pointer back, a writer not yet identified).
	mcFlagOriginMatch4  = "origin_match4"
	mcFlagOriginPartial = "origin_match4_partial"
	mcFlagOriginOther   = "origin_other"
)

// Why a book was in the merged listing but is not a row.
const (
	mcExclMissing   = "missing"
	mcExclTrashed   = "trashed"
	mcExclNotMerged = "not_merged"
	mcExclNoFiles   = "no_files"
)

// mergeCleanupReader is every read this fixer's own code makes of the ops
// store, all of them reads. It is narrower than what the run holds: the run also
// keeps the full OpsStore, for the retired-rows and sync-identity capabilities
// and to hand to the version-group classifier it reuses (vgApplier.planGroup and
// versionprimary.Loader), which takes one. That classifier was read for writes
// and makes none (group, book-file and chapter-table reads, a stat and an
// ffprobe per single-file member), so "the preview writes nothing" is not
// carried by this type; TestMergeCleanupPlanWritesNothing checks it against a
// store snapshot.
type mergeCleanupReader interface {
	GetAllBooksCore(limit int, offset int) ([]database.BookCore, error)
	GetBookByID(id string) (*database.Book, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	BookFilesWithHash(hash string) ([]database.BookFile, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetAuthorByID(id int) (*database.Author, error)
}

var _ mergeCleanupReader = OpsStore(nil)

// mergeCleanupFixer lists every book that is merged into another book but is
// not in the trash and still has files, says what each one's merge really is,
// and proposes the repair: UNMERGE a wrong-work merge, SWAP the keeper when
// the target is a fragment of the loser, REPOINT a chain that ends nowhere
// live. This release is the census only: PlanOnly, no Apply, nothing written
// to books, files, journals or decisions. Every row stays listed with the
// signals it was decided from, applicable or not, so the owner can read it.
//
// Plan is one classification per merged book, fanned out over a bounded worker
// pool. Each worker writes only its own outs[i] and reads the run's caches
// through sync.Map. Nothing is written to a book row, so no two workers can
// touch the same one; a later apply path will partition by loser id (rows that
// share a target are serialized by the engine through BookIDs).
type mergeCleanupFixer struct {
	p *Plugin
	// concurrency is the plan's worker count; 0 means min(NumCPU, 8).
	concurrency int
	// vgProbe replaces ffprobe for the version-group boundary classifier
	// (tests); nil means the version-group fixer's own prober.
	vgProbe versionprimary.ChapterProber
	// vg lends the version-group fixer's series-name cache and prober.
	vg *vgPrimaryFixer
}

func newMergeCleanupFixer(p *Plugin) *mergeCleanupFixer {
	return &mergeCleanupFixer{p: p, vg: newVGPrimaryFixer(p)}
}

var (
	_ repairs.Fixer           = (*mergeCleanupFixer)(nil)
	_ repairs.NoScanStandDown = (*mergeCleanupFixer)(nil)
	_ repairs.PlanOnly        = (*mergeCleanupFixer)(nil)
)

func (f *mergeCleanupFixer) ID() string    { return mergeCleanupFixerID }
func (f *mergeCleanupFixer) Title() string { return "Fix mistaken merges" }
func (f *mergeCleanupFixer) Description() string {
	return "Preview only. Lists every book that is marked as merged into another book but is not in the trash and " +
		"still has files, and says what its merge really is: two different works (the merge should be undone), " +
		"a full copy merged into a short fragment (the full copy should be the one kept), or a merge whose " +
		"target is gone (it should point at the live book, or stand alone). Each row shows both books' titles, " +
		"credits, series positions, runtimes and file counts, the signals the verdict rests on, and why a row is " +
		"held when it is. Nothing is written; this fixer has no Apply control."
}

// Meta implements repairs.Fixer.
func (f *mergeCleanupFixer) Meta() repairs.Meta {
	return repairs.Meta{
		Group:   repairs.GroupDuplicates,
		Rank:    6,
		Summary: "Lists books marked as merged that are still live: wrong-work merges, fragments kept over the full copy, broken chains.",
	}
}

// NoScanStandDown: the plan reads only, so a run of it never parks a library scan.
func (f *mergeCleanupFixer) NoScanStandDown() bool { return true }

// PlanOnly: there is no apply path in this release; the routes and the lane
// refuse an apply, and Apply refuses again itself.
func (f *mergeCleanupFixer) PlanOnly() bool { return true }

// Apply refuses: this release is the census.
func (f *mergeCleanupFixer) Apply(_ context.Context, _ *repairs.Writer, fresh repairs.Row) error {
	return fmt.Errorf("%s: plan-only; row %s was not applied", mergeCleanupFixerID, fresh.RowID)
}

func (f *mergeCleanupFixer) workers() int {
	if f.concurrency > 0 {
		return f.concurrency
	}
	return min(runtime.NumCPU(), 8)
}

// mcCensus is what a plan counted beside its rows: the merged books that are
// not rows, and why.
type mcCensus struct {
	// Merged is the books carrying a merge pointer that the plan classified.
	Merged          int
	Rows            int
	ExcludedTrashed int
	ExcludedNoFiles int
	ExcludedMissing int
	// ExcludedNotMerged: the pointer was cleared between the listing and the read.
	ExcludedNotMerged int
	// HoldsByKind counts every hold of every row, not only each row's first:
	// PlanResult.SkippedByKind counts only the first, so a row held for a
	// version group and for a sync redirect is one skipped kind there and one
	// of each here.
	HoldsByKind map[string]int
}

// mcOutcome is one classification: a row, or the reason the book is not one.
type mcOutcome struct {
	row      repairs.Row
	excluded string
}

// mcLoaded is one book as the signals read it.
type mcLoaded struct {
	book  *database.Book
	files []database.BookFile
	side  mcSide
}

// mcRun is the state of one Plan or one Replan: its readers and caches. The
// caches live exactly as long as the run, so a plan is one consistent snapshot
// and a re-plan never reads a stale one.
type mcRun struct {
	store   mergeCleanupReader
	ops     OpsStore
	vetoes  OwnerVetoReader
	syncIDs database.SyncIdentityStore
	applier *vgApplier
	loader  versionprimary.Loader

	books    sync.Map // id -> *onceVal[*database.Book]
	groups   sync.Map // gid -> *onceVal[[]database.Book]
	loaded   sync.Map // id -> *onceVal[*mcLoaded]
	hashes   sync.Map // file hash -> *onceVal[int]
	authors  sync.Map // author id -> *onceVal[string]
	vgGroups sync.Map // gid -> *onceVal[vgRepairGroupReport]
	members  sync.Map // gid -> *onceVal[[]versionprimary.Member]
}

func (f *mergeCleanupFixer) newRun(ctx context.Context, rep registry.Reporter) (*mcRun, error) {
	st := f.p.fixerStores(ctx)
	if st == nil {
		return nil, fmt.Errorf("%s: database not initialized", mergeCleanupFixerID)
	}
	ops, vps, vetoes := st.OpsStore(), st.VersionPrimaryStore(), st.OwnerVetoes()
	if ops == nil || vps == nil {
		return nil, fmt.Errorf("%s: database not initialized", mergeCleanupFixerID)
	}
	if vetoes == nil {
		// A preview that cannot read the owner's rejects would offer rows he
		// already refused: refuse to plan rather than guess.
		return nil, fmt.Errorf("%s: the owner's rejects are unavailable", mergeCleanupFixerID)
	}
	if err := vetoes.Load(ctx); err != nil {
		return nil, fmt.Errorf("%s: read the owner's rejects: %w", mergeCleanupFixerID, err)
	}
	names, err := f.vg.series(ops)
	if err != nil {
		return nil, err
	}
	probe := f.vgProbe
	if probe == nil {
		probe = f.vg.prober(func(msg string) { _ = rep.Log(slog.LevelWarn, msg) })
	}
	r := &mcRun{store: ops, ops: ops, vetoes: vetoes, syncIDs: database.AsSyncIdentityStore(ops),
		applier: &vgApplier{store: ops, reporter: rep, seriesNames: names, paths: repairs.NewPathResolver()},
		loader:  versionprimary.Loader{Files: ops, Chapters: vps, RootDir: config.AppConfig.RootDir, Probe: probe}}
	return r, nil
}

// ---- reads (each once per run) ----

func (r *mcRun) book(id string) (*database.Book, error) {
	return cachedOnce(&r.books, id, func() (*database.Book, error) { return r.store.GetBookByID(id) })
}

func (r *mcRun) group(gid string) ([]database.Book, error) {
	return cachedOnce(&r.groups, gid, func() ([]database.Book, error) { return r.store.GetBooksByVersionGroup(gid) })
}

// groupMembers is the group's members with their election signals, loaded once
// per run: loading stats every file and probes each single-file member's
// chapters, so a group with several swap rows must not pay for it per row. The
// slice is shared; callers copy before changing a member (reviveMembers does).
func (r *mcRun) groupMembers(ctx context.Context, gid string) ([]versionprimary.Member, error) {
	return cachedOnce(&r.members, gid, func() ([]versionprimary.Member, error) {
		books, err := r.group(gid)
		if err != nil {
			return nil, fmt.Errorf("read version group %s: %w", gid, err)
		}
		return r.loader.LoadMembers(ctx, books, vgStoreAlive(r.ops))
	})
}

// hashShare is how many distinct books carry a file with this hash.
func (r *mcRun) hashShare(hash string) (int, error) {
	return cachedOnce(&r.hashes, hash, func() (int, error) {
		rows, err := r.store.BookFilesWithHash(hash)
		if err != nil {
			return 0, err
		}
		seen := map[string]bool{}
		for i := range rows {
			seen[rows[i].BookID] = true
		}
		return len(seen), nil
	})
}

func (r *mcRun) authorName(id int) (string, error) {
	return cachedOnce(&r.authors, strconv.Itoa(id), func() (string, error) {
		a, err := r.store.GetAuthorByID(id)
		if err != nil || a == nil {
			return "", err
		}
		return dedup.NormalizeAuthorName(a.Name), nil
	})
}

// credits is the book's credited names, normalized, in credit order.
func (r *mcRun) credits(b *database.Book) ([]string, error) {
	rows, err := r.store.GetBookAuthors(b.ID)
	if err != nil {
		return nil, fmt.Errorf("read credits of %s: %w", b.ID, err)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Position < rows[j].Position })
	ids := make([]int, 0, len(rows))
	for _, ba := range rows {
		ids = append(ids, ba.AuthorID)
	}
	if len(ids) == 0 && b.AuthorID != nil {
		ids = append(ids, *b.AuthorID)
	}
	var out []string
	for _, id := range ids {
		n, err := r.authorName(id)
		if err != nil {
			return nil, fmt.Errorf("read author %d: %w", id, err)
		}
		if n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

// load reads a book, its file rows and the signals' view of it. nil, nil is a
// book that does not exist.
func (r *mcRun) load(id string) (*mcLoaded, error) {
	return cachedOnce(&r.loaded, id, func() (*mcLoaded, error) {
		b, err := r.book(id)
		if err != nil || b == nil {
			return nil, err
		}
		files, err := r.store.GetBookFiles(id)
		if err != nil {
			return nil, fmt.Errorf("read files of %s: %w", id, err)
		}
		credits, err := r.credits(b)
		if err != nil {
			return nil, err
		}
		l := &mcLoaded{book: b, files: files}
		l.side = mcSide{ID: id, Title: b.Title, Authors: credits, SeriesID: b.SeriesID, SeriesSeq: b.SeriesSequence,
			ASIN: mcDeref(b.ASIN), GroupID: mcDeref(b.VersionGroupID), Primary: mcPrimary(b.IsPrimaryVersion)}
		var present []database.BookFile
		for i := range files {
			if !files[i].Missing {
				present = append(present, files[i])
			}
		}
		l.side.Files = len(present)
		l.side.Runtime = database.ComputeBookRuntime(b, files)
		l.side.TotalBytes, l.side.BytesComplete = l.side.Runtime.TotalBytes, l.side.Runtime.SizesComplete
		if len(present) == 1 && present[0].FileHash != "" {
			l.side.HashFile = present[0].FileHash
			if l.side.HashShare, err = r.hashShare(present[0].FileHash); err != nil {
				return nil, fmt.Errorf("read the books sharing a file hash with %s: %w", id, err)
			}
		}
		return l, nil
	})
}

func mcDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func mcPrimary(flag *bool) string {
	switch {
	case flag == nil:
		return "nil"
	case *flag:
		return "true"
	}
	return "false"
}

func mcHasPointer(b *database.Book) bool {
	return b != nil && b.MergedIntoBookID != nil && *b.MergedIntoBookID != ""
}

// vgReport is the version-group fixer's own decision for a group, so "no book
// is planned for a write by both fixers" is exact rather than re-derived.
func (r *mcRun) vgReport(ctx context.Context, gid string) (vgRepairGroupReport, error) {
	return cachedOnce(&r.vgGroups, gid, func() (vgRepairGroupReport, error) {
		g := r.applier.planGroup(ctx, r.loader, gid)
		if g.Error != "" {
			return g, fmt.Errorf("version-group classifier for %s: %s", gid, g.Error)
		}
		return g, nil
	})
}

// vgWrittenBooks lists the books the version-group fixer would write for a
// group decision: the winner, a revived merge loser and every demotion.
func vgWrittenBooks(g vgRepairGroupReport) []string {
	if g.Error != "" || !vgWritable(g.Kind) {
		return nil
	}
	set := map[string]bool{}
	for _, id := range append([]string{g.WinnerID, g.RevivedID}, append(slices.Clone(g.DemotedIDs), g.DemoteNonLive...)...) {
		if id != "" {
			set[id] = true
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ---- classification ----

// mcFacts is everything one loser's row is decided and described from.
type mcFacts struct {
	id    string
	l     *mcLoaded
	chain mcChain
	// kind is the row kind the chain and the signals settled ("" when they
	// settled none); class is the row's class (the kind, or no_verdict).
	kind, class string
	target      *mcLoaded
	// repoint is the live book a repoint row would point the loser at.
	repoint string
	// candidates are the live books a dead end could be repointed at.
	candidates []string
	verdict    *mcVerdict
	// chainHold is a skip the chain itself decided.
	chainHold, chainHoldWhy string
	holds                   []repairs.RowHold
	flags                   map[string]bool
	cur, prop               map[string]string
	evidence                []string
	// fpExtra are inputs the decision was made from that Current does not show.
	fpExtra []string
	// groupEffect is "group_less", "leaves_group" or "stays_in_group" for an unmerge.
	groupEffect string
	// cycleHead is the member of a cycle chosen to be unmerged.
	cycleHead string
}

func (fa *mcFacts) flag(name string) { fa.flags[name] = true }

func (fa *mcFacts) hold(kind, why string) {
	fa.holds = append(fa.holds, repairs.RowHold{Kind: kind, Why: why})
}

// classify reads one merged book and returns its row, or the reason it is not
// one. A read that fails returns an error: Plan turns it into an error row.
func (r *mcRun) classify(ctx context.Context, id string) (mcOutcome, error) {
	l, err := r.load(id)
	if err != nil {
		return mcOutcome{}, err
	}
	switch {
	case l == nil:
		return mcOutcome{excluded: mcExclMissing}, nil
	case database.IsInTrash(l.book):
		return mcOutcome{excluded: mcExclTrashed}, nil
	case !mcHasPointer(l.book):
		return mcOutcome{excluded: mcExclNotMerged}, nil
	case l.side.Files == 0:
		return mcOutcome{excluded: mcExclNoFiles}, nil
	}
	fa := &mcFacts{id: id, l: l, flags: map[string]bool{}, cur: map[string]string{}, prop: map[string]string{}}
	fa.cur["merged_into_book_id"] = *l.book.MergedIntoBookID

	held, err := database.RetiredRowsHeldFor(r.ops, id)
	if err != nil {
		return mcOutcome{}, err
	}
	if held > 0 {
		fa.class = mcClassNoVerdict
		fa.hold(mcSkipRetiredShell, fmt.Sprintf("%d of this book's file rows were retired onto another book; it is a retired shell, and a merge cleanup would write a book that holds retired versions", held))
		fa.cur["retired_rows_held"] = strconv.Itoa(held)
		return mcOutcome{row: r.buildRow(fa)}, nil
	}

	chain, err := mcWalkChain(l.book, r.book)
	if err != nil {
		return mcOutcome{}, err
	}
	fa.chain = chain
	if err := r.noteChain(fa); err != nil {
		return mcOutcome{}, err
	}
	r.noteOrigin(fa)
	if err := r.noteSync(fa); err != nil {
		return mcOutcome{}, err
	}

	switch chain.End {
	case mcEndOverCap:
		fa.kind, fa.class = "", mcKindRepoint
		fa.chainHold = mcSkipChainOverCap
		fa.chainHoldWhy = fmt.Sprintf("the merge chain is longer than %d books and still has no end; nothing is proposed for it", mcMaxHops)
	case mcEndCycle:
		err = r.settleCycle(fa)
	case mcEndDead:
		err = r.settleDead(ctx, fa)
	default:
		err = r.settleLive(fa)
	}
	if err != nil {
		return mcOutcome{}, err
	}
	if err := r.gates(ctx, fa); err != nil {
		return mcOutcome{}, err
	}
	return mcOutcome{row: r.buildRow(fa)}, nil
}

// noteChain records the chain on the row.
func (r *mcRun) noteChain(fa *mcFacts) error {
	parts := make([]string, 0, len(fa.chain.Hops))
	for _, h := range fa.chain.Hops {
		parts = append(parts, h.ID+":"+h.State)
		if h.State == mcHopMerged {
			fa.flag(mcFlagChainViaMerged)
		}
	}
	fa.cur["chain"] = strings.Join(parts, ",")
	fa.cur["chain_end"] = string(fa.chain.End)
	if fa.chain.ThroughTrashed {
		fa.flag(mcFlagChainViaTrashed)
	}
	return nil
}

// noteOrigin records where the pointer most likely came from. It is evidence,
// never a gate: a pointer is repaired the same whoever wrote it.
func (r *mcRun) noteOrigin(fa *mcFacts) {
	b := fa.l.book
	origin, why := "other", "the explicit false primary flag MATCH-4 leaves is not set, so another writer left this pointer"
	if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
		origin, why = "match4_partial", "the pointer and an explicit false primary flag (MATCH-4's signature), but no metadata-source hash shared with the book it points at"
		if len(fa.chain.Hops) > 0 && fa.chain.Hops[0].Book != nil {
			lh, th := mcDeref(b.MetadataSourceHash), mcDeref(fa.chain.Hops[0].Book.MetadataSourceHash)
			if lh != "" && lh == th {
				origin, why = "match4", "the pointer, an explicit false primary flag and the metadata-source hash shared with the book it points at: MATCH-4's signature"
			}
		}
	}
	fa.cur["origin"] = origin
	fa.evidence = append(fa.evidence, "origin: "+why)
	switch origin {
	case "match4":
		fa.flag(mcFlagOriginMatch4)
	case "match4_partial":
		fa.flag(mcFlagOriginPartial)
	default:
		fa.flag(mcFlagOriginOther)
	}
}

// noteSync records whether the book's sync identity resolves to another book
// (a merge redirect Audiobookshelf clients follow). Read for every row: the
// census counts them, and a later PR needs to know whether any exist.
func (r *mcRun) noteSync(fa *mcFacts) error {
	if r.syncIDs == nil {
		fa.cur["sync_redirect"] = "unchecked"
		return nil
	}
	sid, ok, err := r.syncIDs.GetSyncIDForBook(fa.id)
	if err != nil {
		return fmt.Errorf("read the sync identity of %s: %w", fa.id, err)
	}
	if !ok {
		return nil
	}
	item, err := r.syncIDs.ResolveSyncItem(sid)
	if err != nil {
		return fmt.Errorf("resolve the sync identity of %s: %w", fa.id, err)
	}
	if item != nil && item.CurrentBookID != "" && item.CurrentBookID != fa.id {
		fa.flag(mcFlagSyncRedirect)
		fa.cur["sync_redirect_to"] = item.CurrentBookID
	}
	return nil
}

// settleLive: the chain ends at a live book; the signals decide.
func (r *mcRun) settleLive(fa *mcFacts) error {
	t, err := r.load(fa.chain.EndID)
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("the live end of the chain, %s, could not be read again", fa.chain.EndID)
	}
	fa.target = t
	v := mcDecide(fa.l.side, t.side)
	fa.verdict = &v
	fa.kind = v.Kind
	fa.class = v.Kind
	if fa.class == "" {
		fa.class = mcClassNoVerdict
	}
	return nil
}

// settleCycle: the pointers loop. One member is chosen to be unmerged; the
// others wait for it, then re-plan as ordinary merged-into-live books.
func (r *mcRun) settleCycle(fa *mcFacts) error {
	members := fa.chain.CycleMembers
	head, err := r.cycleHead(members)
	if err != nil {
		return err
	}
	fa.cycleHead = head
	fa.cur["cycle_members"] = strings.Join(members, ",")
	fa.cur["cycle_head"] = head
	if !slices.Contains(members, fa.id) {
		fa.class = mcKindRepoint
		fa.chainHold = mcSkipCycleNotHead
		fa.chainHoldWhy = "its chain ends in a cycle it is not part of; re-plan after the cycle's head " + head + " is unmerged"
		return nil
	}
	fa.class = mcKindUnmerge
	if head != fa.id {
		fa.chainHold = mcSkipCycleNotHead
		fa.chainHoldWhy = "part of a merge cycle; " + head + " is unmerged first (longest known runtime, then most files, then oldest, then lowest id), and this book is re-planned after it"
		return nil
	}
	fa.kind = mcKindUnmerge
	fa.evidence = append(fa.evidence, "the head of a merge cycle of "+strconv.Itoa(len(members))+" books: its pointer is the one cleared")
	return nil
}

// mcCycleCand is a cycle member as the head choice sees it.
type mcCycleCand struct {
	id    string
	secs  int
	files int
	// created is the book's creation time; hasTime is false when none is stored.
	created time.Time
	hasTime bool
}

// mcPickCycleHead picks the member to unmerge: the longest known runtime, then
// the most files, then the oldest (a member with a stored creation time ahead
// of one without), then the lowest id, so the same cycle always names the same
// head. "" for no candidate.
func mcPickCycleHead(cands []mcCycleCand) string {
	if len(cands) == 0 {
		return ""
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if mcCycleBefore(c, best) {
			best = c
		}
	}
	return best.id
}

// mcCycleBefore reports whether a outranks b as the head.
func mcCycleBefore(a, b mcCycleCand) bool {
	switch {
	case a.secs != b.secs:
		return a.secs > b.secs
	case a.files != b.files:
		return a.files > b.files
	case a.hasTime != b.hasTime:
		return a.hasTime
	case a.hasTime && !a.created.Equal(b.created):
		return a.created.Before(b.created)
	}
	return a.id < b.id
}

// cycleHead picks the head among the cycle's members that are live and have
// files.
func (r *mcRun) cycleHead(members []string) (string, error) {
	var cands []mcCycleCand
	for _, id := range members {
		l, err := r.load(id)
		if err != nil {
			return "", err
		}
		if l == nil || database.IsInTrash(l.book) || l.side.Files == 0 {
			continue
		}
		c := mcCycleCand{id: id, secs: l.side.Runtime.Seconds, files: l.side.Files}
		if l.book.CreatedAt != nil {
			c.created, c.hasTime = *l.book.CreatedAt, true
		}
		cands = append(cands, c)
	}
	head := mcPickCycleHead(cands)
	if head == "" {
		return "", fmt.Errorf("a merge cycle with no live book that has files")
	}
	return head, nil
}

// settleDead: the chain ends at a trashed or missing book. A live book
// reachable another way (the dead hop's version group, or the holder of the
// dead hop's retired versions) is where the pointer is repointed; with none
// the loser stands alone; with two it is never guessed.
func (r *mcRun) settleDead(ctx context.Context, fa *mcFacts) error {
	cands := map[string]bool{}
	ret, haveRet := database.AsCapability[database.RetiredBookFileReader](r.ops)
	for _, h := range fa.chain.Hops {
		if h.State != mcHopTrashed || h.Book == nil {
			continue
		}
		if !haveRet {
			return fmt.Errorf("repoint candidates for %s: %w", h.ID, database.ErrRetiredReaderUnavailable)
		}
		versions, err := ret.GetRetiredVersionsFromBook(h.ID)
		if err != nil {
			return fmt.Errorf("read the retired versions of %s: %w", h.ID, err)
		}
		for _, v := range versions {
			if err := r.addLiveCandidate(fa, cands, v.BookID); err != nil {
				return err
			}
		}
		if gid := mcDeref(h.Book.VersionGroupID); gid != "" {
			members, err := r.group(gid)
			if err != nil {
				return fmt.Errorf("read version group %s: %w", gid, err)
			}
			var readErr error
			inc := versionprimary.Incumbent(members, func(id string) bool {
				b, err := r.book(id)
				if err != nil && readErr == nil {
					readErr = err
				}
				return b != nil && !database.IsInTrash(b)
			})
			if readErr != nil {
				return readErr
			}
			if inc != nil {
				if err := r.addLiveCandidate(fa, cands, inc.ID); err != nil {
					return err
				}
			}
		}
	}
	for id := range cands {
		fa.candidates = append(fa.candidates, id)
	}
	sort.Strings(fa.candidates)
	switch len(fa.candidates) {
	case 0:
		fa.kind, fa.class = mcKindUnmerge, mcKindUnmerge
		fa.evidence = append(fa.evidence, "the merge chain ends at "+fa.chain.LastID()+", which is "+fa.deadWhy()+", and no live book is reachable another way: the book stands alone")
	case 1:
		fa.kind, fa.class = mcKindRepoint, mcKindRepoint
		fa.repoint = fa.candidates[0]
		fa.evidence = append(fa.evidence, "the merge chain ends at "+fa.chain.LastID()+", which is "+fa.deadWhy()+"; the live book "+fa.repoint+" is reachable another way")
	default:
		fa.class = mcKindRepoint
		fa.chainHold = mcSkipRepointAmbiguous
		fa.chainHoldWhy = "more than one live book is reachable from the dead end (" + strings.Join(fa.candidates, ", ") + "); never guessed"
	}
	return nil
}

func (fa *mcFacts) deadWhy() string {
	if n := len(fa.chain.Hops); n > 0 && fa.chain.Hops[n-1].State == mcHopMissing {
		return "not a book that exists"
	}
	return "in the trash"
}

// addLiveCandidate adds id when it is a live book with no pointer of its own
// that is not the loser.
func (r *mcRun) addLiveCandidate(fa *mcFacts, into map[string]bool, id string) error {
	if id == "" || id == fa.id {
		return nil
	}
	b, err := r.book(id)
	if err != nil {
		return fmt.Errorf("read candidate %s: %w", id, err)
	}
	if b != nil && !database.IsInTrash(b) && !mcHasPointer(b) {
		into[id] = true
	}
	return nil
}

// gates runs every check that can hold a row, in a fixed order. The first
// hold is the row's skip kind; every hold is listed. Flags are recorded for
// every row whether or not an earlier hold already settled it.
func (r *mcRun) gates(ctx context.Context, fa *mcFacts) error {
	if fa.chainHold != "" {
		fa.hold(fa.chainHold, fa.chainHoldWhy)
	}
	if why, err := r.vgHold(ctx, fa); err != nil {
		return err
	} else if why != "" {
		fa.hold(mcSkipHandledByVG, why)
	}
	if fa.verdict != nil && fa.verdict.Skip != "" {
		fa.hold(fa.verdict.Skip, fa.verdict.Why)
	}
	switch fa.kind {
	case mcKindSwap:
		if err := r.swapGates(ctx, fa); err != nil {
			return err
		}
	case mcKindUnmerge:
		r.unmergeGroupGate(fa)
	}
	if fa.flags[mcFlagSyncRedirect] && fa.kind != "" {
		fa.hold(mcSkipSyncRedirect, "the book's sync identity resolves to "+fa.cur["sync_redirect_to"]+", a redirect clients follow; clearing it is not part of this release")
	}
	return r.ownerGate(fa)
}

// vgHold says why the version-group fixer owns this row: it would write the
// loser's group in a way that touches a book this row would write.
func (r *mcRun) vgHold(ctx context.Context, fa *mcFacts) (string, error) {
	check := func(b *mcLoaded, id string) (string, error) {
		gid := b.side.GroupID
		if gid == "" {
			return "", nil
		}
		g, err := r.vgReport(ctx, gid)
		if err != nil {
			return "", err
		}
		if slices.Contains(vgWrittenBooks(g), id) {
			return fmt.Sprintf("the version-group repair fixer would write group %s (%s) including book %s; it owns this one", gid, g.Kind, id), nil
		}
		return "", nil
	}
	if why, err := check(fa.l, fa.id); why != "" || err != nil {
		return why, err
	}
	if fa.kind == mcKindSwap && fa.target != nil {
		return check(fa.target, fa.target.side.ID)
	}
	return "", nil
}

// swapGates: a swap retires the target into the loser, so the two must share a
// version group (or neither has one), the target must be the group's primary
// and the loser must be what the group's election crowns afterwards.
func (r *mcRun) swapGates(ctx context.Context, fa *mcFacts) error {
	l, t := fa.l.side, fa.target.side
	if l.GroupID != t.GroupID {
		fa.hold(mcSkipCrossGroup, "the loser is in version group "+mcOrNone(l.GroupID)+" and the target in "+mcOrNone(t.GroupID)+"; a swap across groups is not planned")
		return nil
	}
	if l.GroupID == "" {
		return nil
	}
	members, err := r.group(l.GroupID)
	if err != nil {
		return fmt.Errorf("read version group %s: %w", l.GroupID, err)
	}
	var readErr error
	inc := versionprimary.Incumbent(members, func(id string) bool {
		b, err := r.book(id)
		if err != nil && readErr == nil {
			readErr = err
		}
		return b != nil && !database.IsInTrash(b)
	})
	if readErr != nil {
		return readErr
	}
	if inc == nil || inc.ID != t.ID {
		fa.hold(mcSkipSwapTargetNotPrimary, "the target is not the live primary of version group "+l.GroupID+"; retiring it would not hand the group to the loser")
		return nil
	}
	ms, err := r.groupMembers(ctx, l.GroupID)
	if err != nil {
		return fmt.Errorf("load version group %s: %w", l.GroupID, err)
	}
	sim := reviveMembers(ms, map[string]bool{fa.id: true})
	for i := range sim {
		if sim[i].Book.ID == t.ID {
			sim[i].Signals.Live = false
		}
	}
	winner := versionprimary.Elect(sim).WinnerID
	fa.cur["election_winner_after_swap"] = winner
	if winner != fa.id {
		fa.hold(mcSkipSwapNoCrown, "after the swap the group's election would crown "+mcOrNone(winner)+", not the loser")
	}
	return nil
}

func mcOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// unmergeGroupGate decides what an unmerge would do to the loser's version
// group, and holds it when the group would be left with no primary.
//
// A version group asserts "same work", so a loser leaves the group that holds
// the book it was wrongly merged into (or one of the books on its chain). A
// loser in a group of its own stays: its pointer is cleared and its flag left.
// If that group then has no live primary the row needs a hand-off this release
// does not write.
func (r *mcRun) unmergeGroupGate(fa *mcFacts) {
	gid := fa.l.side.GroupID
	switch {
	case gid == "":
		fa.groupEffect = "group_less"
	case fa.chainHasGroup(gid):
		fa.groupEffect = "leaves_group"
	default:
		fa.groupEffect = "stays_in_group"
	}
	if fa.groupEffect != "stays_in_group" {
		return
	}
	members, err := r.group(gid)
	if err != nil {
		// Unreadable group: hold rather than assume it keeps a primary.
		fa.flag(mcFlagGroupNeedsHandoff)
		fa.hold(mcSkipGroupNeedsHandoff, "version group "+gid+" could not be read to check it keeps a primary: "+err.Error())
		return
	}
	for i := range members {
		m := &members[i]
		if m.ID == fa.id || database.IsInTrash(m) || mcHasPointer(m) {
			continue
		}
		if database.EffectiveIsPrimaryVersion(m.IsPrimaryVersion) {
			return
		}
	}
	fa.flag(mcFlagGroupNeedsHandoff)
	fa.hold(mcSkipGroupNeedsHandoff, "the loser would stay in version group "+gid+" as its only live member with no primary; that needs a primary hand-off, which this release does not write")
}

// chainHasGroup reports whether any book on the loser's chain (the live end
// included: it is the last hop) is in version group gid.
func (fa *mcFacts) chainHasGroup(gid string) bool {
	for _, h := range fa.chain.Hops {
		if h.Book != nil && mcDeref(h.Book.VersionGroupID) == gid {
			return true
		}
	}
	return false
}

// ownerGate reads the owner's rejects for the pair. An owner who said the two
// are not one work contradicts a swap (which calls them one work) and agrees
// with an unmerge; a reject that cannot be ruled out holds a swap.
func (r *mcRun) ownerGate(fa *mcFacts) error {
	if fa.target == nil {
		return nil
	}
	a, b := fa.id, fa.target.side.ID
	var unresolved []decisions.UnresolvedID
	for _, fam := range []string{decisions.FamilySameWork, decisions.FamilySameCopy} {
		v, err := r.vetoes.PairVetoed(retiredEntityBook, a, b, fam)
		var ue *decisions.UnresolvableError
		if errors.As(err, &ue) {
			unresolved = append(unresolved, ue.IDs...)
			continue
		}
		if err != nil {
			return fmt.Errorf("read the owner's rejects for %s and %s: %w", a, b, err)
		}
		if v {
			fa.flag(mcFlagOwnerSaysDistinct)
			fa.cur["owner_veto_family"] = fam
			if fa.kind == mcKindSwap {
				fa.hold(mcSkipOwnerSaysDistinct, "the owner rejected joining these two books ("+fam+"); a swap says they are one work")
			}
			return nil
		}
	}
	if len(unresolved) > 0 {
		ids := make([]string, 0, len(unresolved))
		for _, u := range unresolved {
			ids = append(ids, u.EntityType+" "+u.ID)
		}
		sort.Strings(ids)
		fa.cur["owner_veto_unresolved_ids"] = strings.Join(slices.Compact(ids), ",")
		if fa.kind == mcKindSwap {
			fa.hold(mcSkipVetoUnresolvable, "a reject of the owner's that could touch this pair names an id that cannot be resolved to one book ("+strings.Join(slices.Compact(ids), ", ")+")")
		}
	}
	return nil
}

// ---- rows ----

// buildRow turns the facts into a Repairs row.
func (r *mcRun) buildRow(fa *mcFacts) repairs.Row {
	l := fa.l
	row := repairs.Row{RowID: fa.id, Title: l.book.Title, Class: fa.class, Risk: repairs.RiskReview}

	cur := fa.cur
	cur["loser_title"] = l.book.Title
	cur["loser_group_id"] = l.side.GroupID
	cur["loser_primary"] = l.side.Primary
	cur["loser_files"] = strconv.Itoa(l.side.Files)
	mcPutRuntime(cur, "loser", l.side)
	row.Members = []repairs.RowMember{{BookID: fa.id, Title: l.book.Title, Role: "loser", Files: l.side.Files, MissingFiles: len(l.files) - l.side.Files}}
	row.BookIDs = []string{fa.id}
	fa.fpExtra = append(fa.fpExtra, mcFileSig(l.files))

	for _, h := range fa.chain.Hops {
		if h.Book == nil {
			continue
		}
		row.BookIDs = append(row.BookIDs, h.ID)
		if fa.target == nil || h.ID != fa.target.side.ID {
			row.Members = append(row.Members, repairs.RowMember{BookID: h.ID, Title: h.Book.Title, Role: "hop"})
		}
	}
	if t := fa.target; t != nil {
		cur["target_book_id"] = t.side.ID
		cur["target_title"] = t.book.Title
		cur["target_group_id"] = t.side.GroupID
		cur["target_primary"] = t.side.Primary
		cur["target_files"] = strconv.Itoa(t.side.Files)
		mcPutRuntime(cur, "target", t.side)
		row.Members = append(row.Members, repairs.RowMember{BookID: t.side.ID, Title: t.book.Title, Role: "target",
			Files: t.side.Files, MissingFiles: len(t.files) - t.side.Files})
		fa.fpExtra = append(fa.fpExtra, mcFileSig(t.files))
		if fa.verdict != nil {
			s := fa.verdict.Signals
			cur["signals"] = strings.Join(s.Names(), ",")
			cur["title_similarity"] = strconv.FormatFloat(s.TitleSim, 'f', 2, 64)
			if s.RuntimeKnown {
				cur["runtime_ratio_permille"] = strconv.Itoa(s.RatioPermille)
			}
			if len(s.Names()) > 0 {
				fa.evidence = append(fa.evidence, "signals: "+strings.Join(s.Names(), ", "))
			}
		}
	}
	if fa.repoint != "" {
		row.BookIDs = append(row.BookIDs, fa.repoint)
		row.Members = append(row.Members, repairs.RowMember{BookID: fa.repoint, Role: "repoint_target"})
	}
	for _, id := range fa.candidates {
		if id != fa.repoint {
			row.BookIDs = append(row.BookIDs, id)
		}
	}
	if len(fa.chain.CycleMembers) > 0 {
		for _, id := range fa.chain.CycleMembers {
			if b, _ := r.book(id); b != nil {
				row.BookIDs = append(row.BookIDs, id)
			}
		}
	}
	row.BookIDs = mcUnique(row.BookIDs)

	r.proposeWrites(fa, &row)
	row.Current, row.Proposed = cur, fa.prop
	row.Evidence = fa.evidence
	for name := range fa.flags {
		row.Flags = append(row.Flags, name)
	}
	sort.Strings(row.Flags)

	row.Holds = fa.holds
	if len(fa.holds) > 0 {
		row.Skipped, row.SkipReason = fa.holds[0].Kind, fa.holds[0].Why
		row.Reason = fa.holds[0].Why
	} else {
		row.Reason = mcReason(fa)
	}
	row.Fingerprint = mcFingerprint(&row, fa.fpExtra)
	return row
}

// proposeWrites records what an apply would write, for display. A held row
// shows the same proposal beside its hold. The books a row would write are its
// loser, plus the target for a swap.
func (r *mcRun) proposeWrites(fa *mcFacts, _ *repairs.Row) {
	p := fa.prop
	switch fa.kind {
	case mcKindUnmerge:
		p["merged_into_book_id"] = ""
		switch fa.groupEffect {
		case "stays_in_group":
			p["is_primary_version"], p["version_group_id"] = "unchanged", "unchanged"
		default:
			p["is_primary_version"], p["version_group_id"] = "unset", ""
		}
		fa.cur["group_effect"] = fa.groupEffect
	case mcKindSwap:
		p["keeper_book_id"], p["retired_book_id"] = fa.id, fa.target.side.ID
		p["merged_into_book_id"] = ""
		if fa.l.side.GroupID != "" {
			p["is_primary_version"] = "handoff"
		} else {
			p["is_primary_version"] = "unset"
		}
	case mcKindRepoint:
		p["merged_into_book_id"] = fa.repoint
	}
}

func mcReason(fa *mcFacts) string {
	switch fa.kind {
	case mcKindUnmerge:
		if fa.target == nil {
			return "preview: this book would be unmerged and stand on its own; its merge chain leads nowhere live"
		}
		return "preview: this book would be unmerged from " + fa.target.side.ID + "; the two are different works"
	case mcKindSwap:
		return "preview: " + fa.target.side.ID + " is a fragment of this book; this book would be the one kept and " + fa.target.side.ID + " retired into it"
	case mcKindRepoint:
		return "preview: its merge pointer would move to " + fa.repoint + ", the live book the dead end leads to"
	}
	return "nothing proposed"
}

func mcPutRuntime(cur map[string]string, side string, s mcSide) {
	secs, ok := s.Runtime.KnownSeconds()
	if ok {
		cur[side+"_runtime_seconds"] = strconv.Itoa(secs)
	} else {
		cur[side+"_runtime_seconds"] = ""
	}
	cur[side+"_runtime_complete"] = strconv.FormatBool(ok)
}

// mcFileSig is every file row's identity, state and measures, so a re-plan
// that reads different rows fingerprints differently.
func mcFileSig(files []database.BookFile) string {
	rows := make([]string, len(files))
	for i := range files {
		d := "-"
		if files[i].Duration != nil {
			d = strconv.Itoa(*files[i].Duration)
		}
		rows[i] = strings.Join([]string{files[i].ID, strconv.FormatBool(files[i].Missing), d,
			strconv.FormatInt(files[i].FileSize, 10), files[i].FileHash}, "|")
	}
	sort.Strings(rows)
	return strings.Join(rows, ";")
}

func mcUnique(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// mcFingerprint hashes every input the row was decided from: its class, skip,
// every fact it displays, every proposed write and the file rows behind the
// runtimes, so a re-plan that reads different state fingerprints differently.
func mcFingerprint(row *repairs.Row, extra []string) string {
	h := sha256.New()
	w := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	w(row.Class)
	w(row.Skipped)
	w(row.RowID)
	for _, m := range []map[string]string{row.Current, row.Proposed} {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			w(k)
			w(m[k])
		}
		w("|")
	}
	for _, fl := range row.Flags {
		w(fl)
	}
	ids := slices.Clone(row.BookIDs)
	sort.Strings(ids)
	for _, id := range ids {
		w(id)
	}
	for _, hd := range row.Holds {
		w(hd.Kind)
	}
	for _, e := range extra {
		w(e)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// mcAbsentRow is the Replan answer for a book that is no longer a row.
func mcAbsentRow(id, excluded string) repairs.Row {
	kind, why := mcSkipGone, "the book is no longer a live merged book with files ("+excluded+")"
	if excluded == mcExclNotMerged {
		kind, why = mcSkipNoLongerMerged, "the book is no longer merged into another book"
	}
	row := repairs.Row{RowID: id, BookIDs: []string{id}, Class: kind, Skipped: kind, SkipReason: why, Reason: why, Risk: repairs.RiskLow}
	row.Fingerprint = mcFingerprint(&row, []string{excluded})
	return row
}

// mcErrorRow is the row of a book whose reads failed.
func mcErrorRow(id, title string, err error) repairs.Row {
	row := repairs.Row{RowID: id, BookIDs: []string{id}, Title: title, Class: mcClassError, Skipped: mcSkipError,
		SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
	row.Fingerprint = mcFingerprint(&row, []string{err.Error()})
	return row
}

// ---- plan and replan ----

// Plan classifies every merged book that is not in the trash. Read-only.
func (f *mergeCleanupFixer) Plan(ctx context.Context, params json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	rows, _, err := f.plan(ctx, params, rep)
	return rows, err
}

func (f *mergeCleanupFixer) plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, mcCensus, error) {
	var census mcCensus
	run, err := f.newRun(ctx, rep)
	if err != nil {
		return nil, census, err
	}
	cores, err := run.store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, census, fmt.Errorf("list books: %w", err)
	}
	type listed struct{ id, title string }
	var ids []listed
	for i := range cores {
		c := &cores[i]
		if c.MergedIntoBookID != nil && *c.MergedIntoBookID != "" && !c.IsSoftDeleted() {
			ids = append(ids, listed{c.ID, c.Title})
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].id < ids[j].id })
	census.Merged = len(ids)

	outs := make([]mcOutcome, len(ids))
	var done atomic.Int64
	// Each worker writes only outs[i] for its own i and reads the run's caches
	// through sync.Map. The plan writes no book, so no two workers can touch one.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(ids)), func(ctx context.Context, i int) error {
		defer done.Add(1)
		o, err := run.classify(ctx, ids[i].id)
		switch {
		case err == nil:
			outs[i] = o
		case errors.Is(err, database.ErrRetiredReaderUnavailable):
			// Every row needs the retired-shell check: the plan cannot be made.
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			outs[i] = mcOutcome{row: mcErrorRow(ids[i].id, ids[i].title, err)}
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: f.workers(),
		ErrMode:     registry.ErrModeFail,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string {
			return fmt.Sprintf("Merge cleanup preview %d/%d", done.Load(), total)
		},
	})
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, census, ctx.Err()
		}
		return nil, census, fmt.Errorf("classify the merged books: %w", runErr)
	}
	rows := make([]repairs.Row, 0, len(outs))
	for _, o := range outs {
		switch o.excluded {
		case "":
			rows = append(rows, o.row)
		case mcExclTrashed:
			census.ExcludedTrashed++
		case mcExclNoFiles:
			census.ExcludedNoFiles++
		case mcExclMissing:
			census.ExcludedMissing++
		case mcExclNotMerged:
			census.ExcludedNotMerged++
		}
	}
	census.Rows = len(rows)
	census.HoldsByKind = map[string]int{}
	for i := range rows {
		for _, h := range rows[i].Holds {
			census.HoldsByKind[h.Kind]++
		}
	}
	_ = rep.Log(slog.LevelInfo, mcSummary(rows, census))
	return rows, census, nil
}

// Replan classifies one book again from fresh reads, by the code Plan runs. A
// book that is no longer a live merged book with files comes back skipped
// (gone, or no_longer_merged) with a different fingerprint, never an error.
// Each call reads its own snapshot: an apply path that replans row by row
// should hold one run across the session.
func (f *mergeCleanupFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, rep registry.Reporter) (repairs.Row, error) {
	run, err := f.newRun(ctx, rep)
	if err != nil {
		return repairs.Row{}, err
	}
	o, err := run.classify(ctx, planned.RowID)
	if err != nil {
		return repairs.Row{}, err
	}
	if o.excluded != "" {
		return mcAbsentRow(planned.RowID, o.excluded), nil
	}
	return o.row, nil
}

// mcSummary is the end-of-plan log line: the census in one place.
func mcSummary(rows []repairs.Row, c mcCensus) string {
	classes, skips, flags := map[string]int{}, map[string]int{}, map[string]int{}
	applicable := 0
	for i := range rows {
		classes[rows[i].Class]++
		if rows[i].Skipped != "" {
			skips[rows[i].Skipped]++
		} else {
			applicable++
		}
		for _, fl := range rows[i].Flags {
			flags[fl]++
		}
	}
	return fmt.Sprintf("merge-cleanup preview: %d merged book(s); %d row(s), %d would be written; excluded %d trashed, %d with no live file, %d missing, %d no longer merged; by class %s; by first skip kind %s; by every hold %s; by flag %s",
		c.Merged, c.Rows, applicable, c.ExcludedTrashed, c.ExcludedNoFiles, c.ExcludedMissing, c.ExcludedNotMerged,
		mcCounts(classes), mcCounts(skips), mcCounts(c.HoldsByKind), mcCounts(flags))
}

func mcCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + strconv.Itoa(m[k])
	}
	return "{" + strings.Join(parts, " ") + "}"
}
