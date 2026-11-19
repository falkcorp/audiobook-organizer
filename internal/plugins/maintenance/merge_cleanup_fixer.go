// file: internal/plugins/maintenance/merge_cleanup_fixer.go
// version: 1.7.1
// guid: 0e4c7d58-a1b3-4f26-8c19-6d5a3e7f9b02
// last-edited: 2026-11-19

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

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"golang.org/x/sync/errgroup"
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
	// mcFlagOriginUnknown: the loser (or the book it points at) could not be
	// read, so its origin could not be classified. An error row carries this
	// instead of a guessed origin, so the origin flags add up to every row.
	mcFlagOriginUnknown = "origin_unknown"
	// mcFlagSyncBroken: the book's sync identity has a redirect chain that is
	// dangling, cyclic or too long; whether a redirect exists cannot be told.
	mcFlagSyncBroken = "sync_redirect_broken"
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
	// GetAllBooksCoreComplete is the authoritative listing (it falls through to
	// a Pebble scan when the in-memory index is short); CountAllBooks counts the
	// in-memory index. The two disagreeing is how a short index is seen.
	GetAllBooksCoreComplete(limit int, offset int) ([]database.BookCore, error)
	CountAllBooks() (int, error)
	GetBookByID(id string) (*database.Book, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	BookFilesWithHash(hash string) ([]database.BookFile, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetAuthorByID(id int) (*database.Author, error)
}

// mcAltTitleReader is the one read outside OpsStore: a book's alternative
// titles. It is a capability, resolved from the store with
// database.AsCapability, so OpsStore (and its mock and width ratchet) does not
// widen for one read of one fixer; a store without it refuses the plan, because
// a preview that cannot see alternative titles would call a renamed book a
// different work.
type mcAltTitleReader interface {
	GetBookAlternativeTitles(bookID string) ([]database.BookAlternativeTitle, error)
}

// mcTrashReader is the second read outside OpsStore's narrow slice for this
// fixer: the trash. GetBooksByVersionGroup leaves a soft-deleted book out, so a
// trashed member of a dead hop's version group that still points on to a live
// book is found only through this listing. A capability, for the same reason as
// mcAltTitleReader.
type mcTrashReader interface {
	ListSoftDeletedBooks(limit int, offset int, olderThan *time.Time) ([]database.Book, error)
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
		"target is gone (it should point at the live book it leads to, or stand alone when nothing live is reachable). " +
		"Each row shows both books' titles, alternative titles, credits, series positions, ASINs, version groups, " +
		"file counts and runtimes (the runtime of the audio on disk, and with the chapter rows that are off disk counted " +
		"when there are any), the signals the verdict rests on, and why a row is held when it is. A row whose " +
		"titles, credits or sizes are missing, placeholders or ambiguous is held, never decided. Nothing is written; " +
		"this fixer has no Apply control."
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
	alts    mcAltTitleReader
	trash   mcTrashReader
	ops     OpsStore
	vetoes  OwnerVetoReader
	syncIDs database.SyncIdentityStore
	applier *vgApplier
	loader  versionprimary.Loader

	books    sync.Map // id -> *onceVal[*database.Book]
	groups   sync.Map // gid -> *onceVal[[]database.Book]
	loaded   sync.Map // id -> *onceVal[*mcLoaded]
	hashes   sync.Map // file hash -> *onceVal[int]
	authors  sync.Map // author id -> *onceVal[mcAuthor]
	vgGroups sync.Map // gid -> *onceVal[vgRepairGroupReport]
	members  sync.Map // gid -> *onceVal[[]versionprimary.Member]
	// trashedMerged is the one-entry index (key "") of the trashed books that
	// carry a merge pointer, by version group, read once per run.
	trashedMerged sync.Map // "" -> *onceVal[map[string][]database.Book]
	// deadEnds is the one-entry index of where every merged book's chain ends
	// (key ""), built the first time a row needs it.
	deadEnds sync.Map // "" -> *onceVal[map[string][]mcCoLoser]
	// deadSources is, per dead book, the books whose chain enters it and the
	// pointer-carrying hops on those chains (see deadEndSources).
	deadSources sync.Map // dead book id -> *onceVal[[]string]
	// sources is what each anchor source's holders and version group lead to.
	sources sync.Map // source id -> *onceVal[*mcSourceInfo]
	// trashList is the one-entry list (key "") of the soft-deleted books that
	// carry a merge pointer, read once per run.
	trashList sync.Map // "" -> *onceVal[[]database.Book]
	// merged are the ids of the books that carry a merge pointer, when the plan
	// already listed them; nil means the index lists the library itself.
	merged []string
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
	alts, ok := database.AsCapability[mcAltTitleReader](ops)
	if !ok {
		return nil, fmt.Errorf("%s: the store cannot read alternative titles", mergeCleanupFixerID)
	}
	trash, ok := database.AsCapability[mcTrashReader](ops)
	if !ok {
		return nil, fmt.Errorf("%s: the store cannot list the trash", mergeCleanupFixerID)
	}
	r := &mcRun{store: ops, alts: alts, trash: trash, ops: ops, vetoes: vetoes, syncIDs: database.AsSyncIdentityStore(ops),
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

// trashedMergedMembers are the trashed books of version group gid that carry a
// merge pointer. The group read leaves a soft-deleted book out, and such a book
// keeps its group id and its pointer, so it is read from the trash, once per run
// for the whole library.
func (r *mcRun) trashedMergedMembers(gid string) ([]database.Book, error) {
	idx, err := cachedOnce(&r.trashedMerged, "", func() (map[string][]database.Book, error) {
		books, err := r.trashedPointers()
		if err != nil {
			return nil, err
		}
		out := map[string][]database.Book{}
		for i := range books {
			if g := mcDeref(books[i].VersionGroupID); g != "" {
				out[g] = append(out[g], books[i])
			}
		}
		for g := range out {
			sort.Slice(out[g], func(i, j int) bool { return out[g][i].ID < out[g][j].ID })
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return idx[gid], nil
}

// trashedPointers are the soft-deleted books that carry a merge pointer, sorted
// by id: read once per run, for the group index above and for the dead-end
// index (a trashed book whose chain enters a dead book is a source too).
func (r *mcRun) trashedPointers() ([]database.Book, error) {
	return cachedOnce(&r.trashList, "", func() ([]database.Book, error) {
		books, err := r.trash.ListSoftDeletedBooks(0, 0, nil)
		if err != nil {
			return nil, fmt.Errorf("list the trash for merged books: %w", err)
		}
		var out []database.Book
		for i := range books {
			if mcHasPointer(&books[i]) {
				out = append(out, books[i])
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
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

// mcAuthor is one author row as the credits read it.
type mcAuthor struct {
	// name is the normalized name; "" when found is false.
	name string
	// found is false for an id with no author row (a dangling credit).
	found bool
	// placeholder: the stored name is a placeholder (Unknown ...).
	placeholder bool
}

func (r *mcRun) author(id int) (mcAuthor, error) {
	return cachedOnce(&r.authors, strconv.Itoa(id), func() (mcAuthor, error) {
		a, err := r.store.GetAuthorByID(id)
		if err != nil {
			return mcAuthor{}, err
		}
		if a == nil {
			return mcAuthor{}, nil
		}
		return mcAuthor{name: dedup.NormalizeAuthorName(a.Name), found: true, placeholder: authorname.IsPlaceholderAuthor(a.Name)}, nil
	})
}

// mcCredits is a book's credits as the verdict reads them.
type mcCredits struct {
	// names are the credited names, normalized, in credit order, placeholders
	// and dangling ids left out.
	names []string
	// authors are the individual names those credits hold (mcCreditParts).
	authors []string
	// placeholder: some credit is a placeholder name.
	placeholder bool
	// dangling counts credits whose author id has no author row. A dangling
	// credit is counted, never dropped: a book whose only credit points at
	// nothing has unknown authors, not none.
	dangling int
}

// credits reads the book's credited names in credit order.
func (r *mcRun) credits(b *database.Book) (mcCredits, error) {
	var c mcCredits
	rows, err := r.store.GetBookAuthors(b.ID)
	if err != nil {
		return c, fmt.Errorf("read credits of %s: %w", b.ID, err)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Position < rows[j].Position })
	ids := make([]int, 0, len(rows))
	for _, ba := range rows {
		ids = append(ids, ba.AuthorID)
	}
	if len(ids) == 0 && b.AuthorID != nil {
		ids = append(ids, *b.AuthorID)
	}
	for _, id := range ids {
		a, err := r.author(id)
		if err != nil {
			return c, fmt.Errorf("read author %d: %w", id, err)
		}
		switch {
		case !a.found:
			c.dangling++
		case a.placeholder:
			c.placeholder = true
		case a.name != "":
			c.names = append(c.names, a.name)
			for _, part := range mcCreditParts(a.name) {
				if !slices.Contains(c.authors, part) {
					c.authors = append(c.authors, part)
				}
			}
		}
	}
	return c, nil
}

// mcOnDiskRuntime is the runtime of the audio that is on disk: the present
// rows only. Book.Duration may stand in for an unmeasured file only when the
// book has at most one row in all (ComputeBookRuntime's own rule, applied to
// every row, not just the present ones): a book with one measured chapter on
// disk and nine missing is not a book whose Duration is that chapter's. A book
// with no present row has no on-disk runtime.
func mcOnDiskRuntime(b *database.Book, files []database.BookFile) database.BookRuntime {
	var present []database.BookFile
	for i := range files {
		if !files[i].Missing {
			present = append(present, files[i])
		}
	}
	if len(present) == 0 {
		return database.BookRuntime{Source: database.RuntimeSourceNone}
	}
	if len(files) > 1 {
		b = nil
	}
	return database.ComputeBookRuntime(b, present)
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
		alts, err := r.alts.GetBookAlternativeTitles(id)
		if err != nil {
			return nil, fmt.Errorf("read alternative titles of %s: %w", id, err)
		}
		l := &mcLoaded{book: b, files: files}
		l.side = mcSide{ID: id, Title: b.Title, Credits: credits.names, Authors: credits.authors,
			CreditsPlaceholder: credits.placeholder, CreditsDangling: credits.dangling,
			SeriesID: b.SeriesID, SeriesSeq: b.SeriesSequence,
			ASIN: mcDeref(b.ASIN), GroupID: mcDeref(b.VersionGroupID), Primary: mcPrimary(b.IsPrimaryVersion)}
		for _, a := range alts {
			l.side.AltTitles = append(l.side.AltTitles, a.Title)
		}
		sort.Strings(l.side.AltTitles)
		var present []database.BookFile
		for i := range files {
			if !files[i].Missing {
				present = append(present, files[i])
			}
		}
		l.side.Files = len(present)
		// The verdict is made on what is on disk; the runtime with the missing
		// chapter rows counted rides along for the runtime_includes_missing check.
		l.side.Runtime = mcOnDiskRuntime(b, files)
		l.side.TotalBytes, l.side.BytesComplete = l.side.Runtime.TotalBytes, l.side.Runtime.SizesComplete
		l.side.RuntimeAll = database.ComputeBookRuntime(b, files)
		l.side.AllTotalBytes, l.side.AllBytesComplete = l.side.RuntimeAll.TotalBytes, l.side.RuntimeAll.SizesComplete
		if len(present) == 1 {
			f := present[0]
			if f.FileHash != "" {
				l.side.HashFile = f.FileHash
				if l.side.HashShare, err = r.hashShare(f.FileHash); err != nil {
					return nil, fmt.Errorf("read the books sharing a file hash with %s: %w", id, err)
				}
			}
			if f.OriginalFileHash != "" {
				l.side.HashOrig = f.OriginalFileHash
				if l.side.HashOrigShare, err = r.hashShare(f.OriginalFileHash); err != nil {
					return nil, fmt.Errorf("read the books sharing an original file hash with %s: %w", id, err)
				}
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

// mcCoLoser is a book whose merge chain ends at a dead book (trashed or
// missing): the books beside a loser that went to the same dead end.
type mcCoLoser struct {
	id string
	// trashed is true for a book in the trash. Such a book is a source of
	// anchors but is never itself a live copy of the work.
	trashed bool
}

// deadEndIndex maps the id of each dead book that some merge chain ends at to
// the books whose chain ends there, live or in the trash, with or without a
// file. Built once per run from the merge pointers alone (every read is
// cached), in listing order: the live merged books first, then the trashed ones
// that carry a pointer.
//
// Every merged book costs a store read and a chain walk, so the walk is fanned
// out over a bounded pool sized to the CPUs. Each worker writes only its own
// slot, so no two workers touch one entry and no lock is needed; the index is
// folded together afterwards in listing order, so the result does not depend on
// which worker finished first.
//
// The result is cached for the run, but a build stopped by a caller's own
// cancelled context is not: that error says nothing about the library. The
// entry is removed only if it is still the one that failed, and a caller that
// was waiting on that build retries with its own context rather than inherit
// the other caller's cancel.
func (r *mcRun) deadEndIndex(ctx context.Context) (map[string][]mcCoLoser, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		e, _ := r.deadEnds.LoadOrStore("", &onceVal[map[string][]mcCoLoser]{})
		ov := e.(*onceVal[map[string][]mcCoLoser])
		ov.once.Do(func() { ov.v, ov.err = r.buildDeadEndIndex(ctx) })
		if ov.err == nil {
			return ov.v, nil
		}
		if !errors.Is(ov.err, context.Canceled) && !errors.Is(ov.err, context.DeadlineExceeded) {
			return nil, ov.err
		}
		r.deadEnds.CompareAndDelete("", e)
		if err := ctx.Err(); err != nil {
			return nil, ov.err
		}
		lastErr = ov.err
	}
	return nil, lastErr
}

func (r *mcRun) buildDeadEndIndex(ctx context.Context) (map[string][]mcCoLoser, error) {
	ids := r.merged
	if ids == nil {
		cores, err := r.store.GetAllBooksCoreComplete(0, 0)
		if err != nil {
			return nil, fmt.Errorf("list books for the dead-end index: %w", err)
		}
		for i := range cores {
			c := &cores[i]
			if c.MergedIntoBookID != nil && *c.MergedIntoBookID != "" && !c.IsSoftDeleted() {
				ids = append(ids, c.ID)
			}
		}
	}
	trashed, err := r.trashedPointers()
	if err != nil {
		return nil, err
	}
	all := make([]string, 0, len(ids)+len(trashed))
	all = append(all, ids...)
	for i := range trashed {
		all = append(all, trashed[i].ID)
	}
	slots := make([]*mcCoLoser, len(all))
	ends := make([]string, len(all))
	var g errgroup.Group
	g.SetLimit(max(1, runtime.NumCPU()))
	for i := range all {
		g.Go(func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b, err := r.book(all[i])
			if err != nil {
				return fmt.Errorf("read %s for the dead-end index: %w", all[i], err)
			}
			if b == nil || !mcHasPointer(b) {
				return nil
			}
			chain, err := mcWalkChain(b, r.book)
			if err != nil {
				return err
			}
			if chain.End != mcEndDead {
				return nil
			}
			slots[i], ends[i] = &mcCoLoser{id: all[i], trashed: database.IsInTrash(b)}, chain.LastID()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := map[string][]mcCoLoser{}
	for i, c := range slots {
		if c != nil {
			out[ends[i]] = append(out[ends[i]], *c)
		}
	}
	return out, nil
}

// deadEndSources are the books whose merge chain enters dead book d: every
// book whose content may have gone on from d. The pointer-carrying hops on a
// co-loser's chain need no separate entry, because each of them is itself a
// book whose chain enters d and so is in the index (live, or in the trash).
// Sorted, and computed once per dead book, so each loser at one dead end
// subtracts itself from the same list instead of walking every co-loser again.
func (r *mcRun) deadEndSources(ctx context.Context, d string) ([]string, error) {
	idx, err := r.deadEndIndex(ctx)
	if err != nil {
		return nil, err
	}
	return cachedOnce(&r.deadSources, d, func() ([]string, error) {
		set := map[string]bool{}
		for _, c := range idx[d] {
			set[c.id] = true
		}
		out := make([]string, 0, len(set))
		for id := range set {
			out = append(out, id)
		}
		sort.Strings(out)
		return out, nil
	})
}

// deadEndMates are the other live merged books, with files, whose chain ends at
// the same dead book as fa's: the other losers of one dead end. Sorted.
func (r *mcRun) deadEndMates(ctx context.Context, fa *mcFacts) ([]string, error) {
	idx, err := r.deadEndIndex(ctx)
	if err != nil {
		return nil, err
	}
	var mates []string
	for _, c := range idx[fa.chain.LastID()] {
		if c.id == fa.id || c.trashed {
			continue
		}
		l, err := r.load(c.id)
		if err != nil {
			return nil, err
		}
		// A book with no file of its own is not a copy of the work.
		if l != nil && l.side.Files > 0 {
			mates = append(mates, c.id)
		}
	}
	sort.Strings(mates)
	return mates, nil
}

// mcSourceInfo is what one anchor source leads to, read once per run: the
// books holding its retired versions, and its version group.
type mcSourceInfo struct {
	holders []string
	gid     string
	// incumbent is the group's primary, "" when none is elected.
	incumbent string
	// ptrMembers are the group's members, in the trash or not, that carry a
	// merge pointer; liveMembers are its live, unmerged members.
	ptrMembers  []string
	liveMembers []string
}

func (r *mcRun) sourceInfo(src string) (*mcSourceInfo, error) {
	return cachedOnce(&r.sources, src, func() (*mcSourceInfo, error) {
		info := &mcSourceInfo{}
		b, err := r.book(src)
		if err != nil {
			return nil, fmt.Errorf("read anchor source %s: %w", src, err)
		}
		if b == nil {
			return info, nil
		}
		ret, ok := database.AsCapability[database.RetiredBookFileReader](r.ops)
		if !ok {
			return nil, fmt.Errorf("repoint candidates for %s: %w", src, database.ErrRetiredReaderUnavailable)
		}
		versions, err := ret.GetRetiredVersionsFromBook(src)
		if err != nil {
			return nil, fmt.Errorf("read the retired versions of %s: %w", src, err)
		}
		for _, v := range versions {
			info.holders = append(info.holders, v.BookID)
		}
		gid := mcDeref(b.VersionGroupID)
		if gid == "" {
			return info, nil
		}
		info.gid = gid
		members, err := r.group(gid)
		if err != nil {
			return nil, fmt.Errorf("read version group %s: %w", gid, err)
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
			return nil, readErr
		}
		if inc != nil {
			info.incumbent = inc.ID
		}
		// The group read leaves out a soft-deleted member, so the trashed ones
		// that still point on are read from the trash.
		trashedMembers, err := r.trashedMergedMembers(gid)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		all := append(slices.Clone(members), trashedMembers...)
		for i := range all {
			m := &all[i]
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			if mcHasPointer(m) {
				info.ptrMembers = append(info.ptrMembers, m.ID)
			} else if !database.IsInTrash(m) {
				info.liveMembers = append(info.liveMembers, m.ID)
			}
		}
		return info, nil
	})
}

// readSource follows what one anchor source leads to for the loser in fa: the
// holders of its retired versions, then its version group (the group's primary
// decides whether the group is accounted for; a member that carries a merge
// pointer took content on to the live book it points at; a live, unmerged
// member is a live book of the group that nothing walked to unless the primary
// led to a live book). An anchor's own holders and group are not read. A source
// found through a co-loser does not read the loser's own group: the hand-off
// gate covers it.
func (r *mcRun) readSource(fa *mcFacts, rc *mcReach, src string, throughCoLoser bool) error {
	info, err := r.sourceInfo(src)
	if err != nil {
		return err
	}
	for _, h := range info.holders {
		if _, err := r.follow(fa, rc, h, "holds the retired versions of "+src); err != nil {
			return err
		}
	}
	if info.gid == "" || (throughCoLoser && info.gid == fa.l.side.GroupID) {
		return nil
	}
	// Only the group's primary decides whether the group is accounted for (a
	// live non-primary member beside a primary is that primary's sibling, not a
	// book the walk missed). What any other route reached does not stand in for
	// it: a live member beside a merged member's live end, or beside a holder's,
	// is still a second place the loser may belong, whichever route found the
	// first.
	primaryReached := false
	if info.incumbent != "" {
		got, err := r.follow(fa, rc, info.incumbent, "is the primary of version group "+info.gid)
		if err != nil {
			return err
		}
		primaryReached = got != ""
	}
	for _, id := range info.ptrMembers {
		if id == fa.id || fa.onChain(id) {
			continue
		}
		if _, err := r.follow(fa, rc, id, "is a member of version group "+info.gid+" that is merged away"); err != nil {
			return err
		}
	}
	// With no primary that led to a live book (none elected, two unflagged, the
	// primary is the loser itself, or it leads nowhere), its live members are
	// books the loser may belong with. They are not named a candidate (which of
	// them would be a guess), and the loser is not said to stand alone beside
	// them.
	if !primaryReached {
		for _, id := range info.liveMembers {
			rc.groupLive[id] = info.gid
		}
	}
	return nil
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
	// repointTarget and repointVerdict are that book and the signals' verdict
	// on the loser against it. They are their own fields, not target: target
	// drives the unmerge/swap reason, the version-group hold and the swap gates,
	// which a repoint is not.
	repointTarget  *mcLoaded
	repointVerdict *mcVerdict
	// candidates are the live books a dead end could be repointed at; orphans
	// are live books of the dead hop's version group that no walk reached.
	candidates, orphans []string
	verdict             *mcVerdict
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
	// syncBroken is the error text of a sync redirect chain that could not be
	// resolved ("" when it resolved or there is none).
	syncBroken string
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

	// The chain, the pointer's origin and the sync redirect are read for every
	// row, a retired shell included: the census splits rows by them, and the
	// split has to add up to the rows.
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

// mcClassifyOrigin says where a merge pointer most likely came from, from the
// loser and the first book it points at. hop is nil when that book is missing
// (a pointer to nothing is MATCH-4's shape without a shared source hash).
func mcClassifyOrigin(b, hop *database.Book) (origin, why, flag string) {
	if b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
		return "other", "the explicit false primary flag MATCH-4 leaves is not set, so another writer left this pointer", mcFlagOriginOther
	}
	origin, why, flag = "match4_partial", "the pointer and an explicit false primary flag (MATCH-4's signature), but no metadata-source hash shared with the book it points at", mcFlagOriginPartial
	if hop != nil {
		lh, th := mcDeref(b.MetadataSourceHash), mcDeref(hop.MetadataSourceHash)
		if lh != "" && lh == th {
			origin, why, flag = "match4", "the pointer, an explicit false primary flag and the metadata-source hash shared with the book it points at: MATCH-4's signature", mcFlagOriginMatch4
		}
	}
	return origin, why, flag
}

// noteOrigin records where the pointer most likely came from. It is evidence,
// never a gate: a pointer is repaired the same whoever wrote it.
func (r *mcRun) noteOrigin(fa *mcFacts) {
	var hop *database.Book
	if len(fa.chain.Hops) > 0 {
		hop = fa.chain.Hops[0].Book
	}
	origin, why, flag := mcClassifyOrigin(fa.l.book, hop)
	fa.cur["origin"] = origin
	fa.evidence = append(fa.evidence, "origin: "+why)
	fa.flag(flag)
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
	if errors.Is(err, database.ErrSyncRedirectChainBroken) {
		// Permanent, not a read failure: retrying cannot mend it. It is a hold,
		// not an error row, because the book itself read fine; whether a
		// redirect clients follow exists is what cannot be told.
		fa.flag(mcFlagSyncBroken)
		fa.syncBroken = err.Error()
		fa.cur["sync_redirect"] = "broken"
		return nil
	}
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
		// KnownSeconds, not Seconds: a partial runtime is a lower bound, and a
		// member with one unmeasured file must not outrank one measured whole.
		secs, _ := l.side.Runtime.KnownSeconds()
		c := mcCycleCand{id: id, secs: secs, files: l.side.Files}
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

// mcReach is what the books around a dead end lead to.
type mcReach struct {
	// live maps each live, unmerged book reached to how it was reached.
	live map[string]string
	// dead are anchors that were walked (or looked at) and lead nowhere live: a
	// missing book, a trashed book with no pointer or whose chain ends dead, and
	// a merged book with no file of its own whose chain ends dead.
	dead []string
	// liveCopies are anchors that are not in the trash, have a file of their
	// own, and carry a merge pointer whose own chain ends dead: a live copy of
	// the work around this dead end that is not the loser, so the loser is not
	// the only one left.
	liveCopies []string
	// unproven are anchors whose merge chain loops or is longer than the cap:
	// where they lead is not known, so they prove nothing either way.
	unproven []string
	// seen are the anchors already followed: an anchor named by two routes is
	// walked and listed once.
	seen map[string]bool
	// outcome is, for each anchor followed, the live book it led to ("" when it
	// led to none). A route that names an anchor another route already followed
	// reads the answer from here, so "did this route reach a live book" does not
	// depend on which route got there first.
	outcome map[string]string
	// groupLive are the live, unmerged books (other than the loser) of a dead
	// hop's version group that the group's primary did not lead to, each with
	// its group: books a repoint could plausibly name that nothing walked to.
	groupLive map[string]string
}

func (rc *mcReach) add(id, via string) {
	if _, ok := rc.live[id]; !ok {
		rc.live[id] = via
	}
}

// follow says where one anchor book (a holder of the dead hop's retired
// versions, or the primary or a merged-away member of its version group) leads,
// and returns the live book it led to ("" when it led to none).
// An anchor that is a live, unmerged book is itself a place to repoint. An
// anchor that carries a merge pointer is followed along its own chain to the
// live book it was merged into: that book is where the loser's content went,
// and it is a place to repoint. That holds whether the anchor is in the trash or
// not: the loser's own chain walks through a trashed hop that still points on
// (mcWalkChain), and so does this. An anchor that is missing, or in the trash
// with no pointer, or in the trash with a chain that ends dead, leads nowhere.
// An anchor that is not in the trash, has a file and whose chain ends dead is a
// live copy of the work (liveCopies), not nothing: the loser is not the only
// copy left. An anchor whose chain loops or never ends proves nothing, and is
// kept apart so "no live book is reachable" is never claimed over it.
func (r *mcRun) follow(fa *mcFacts, rc *mcReach, id, via string) (string, error) {
	if id == "" || id == fa.id {
		return "", nil
	}
	if rc.seen[id] {
		return rc.outcome[id], nil
	}
	rc.seen[id] = true
	b, err := r.book(id)
	if err != nil {
		return "", fmt.Errorf("read candidate %s: %w", id, err)
	}
	trashed := b != nil && database.IsInTrash(b)
	switch {
	case b == nil:
		rc.dead = append(rc.dead, id)
	case !mcHasPointer(b):
		if trashed {
			rc.dead = append(rc.dead, id)
		} else {
			rc.add(id, via)
			rc.outcome[id] = id
		}
	default:
		chain, err := mcWalkChain(b, r.book)
		if err != nil {
			return "", err
		}
		switch chain.End {
		case mcEndLive:
			state := "merged"
			if trashed {
				state = "in the trash and merged"
			}
			rc.add(chain.EndID, via+", which is "+state+" into "+chain.EndID)
			rc.outcome[id] = chain.EndID
		case mcEndDead:
			if trashed {
				rc.dead = append(rc.dead, id)
				break
			}
			l, err := r.load(id)
			if err != nil {
				return "", err
			}
			if l != nil && l.side.Files > 0 {
				rc.liveCopies = append(rc.liveCopies, id)
			} else {
				rc.dead = append(rc.dead, id)
			}
		default:
			rc.unproven = append(rc.unproven, id)
		}
	}
	return rc.outcome[id], nil
}

// onChain reports whether id is a book on the loser's own merge chain.
func (fa *mcFacts) onChain(id string) bool {
	for _, h := range fa.chain.Hops {
		if h.ID == id {
			return true
		}
	}
	return false
}

// settleDead: the chain ends at a trashed or missing book. A live book
// reachable another way (the dead hop's version group, or the holder of the
// dead hop's retired versions, followed on to the live book it was merged into
// when it is merged itself) is where the pointer is repointed; with none, and
// every walk proved it, the loser stands alone; with two, or with a walk that
// could not prove it, it is held and never guessed.
func (r *mcRun) settleDead(ctx context.Context, fa *mcFacts) error {
	rc := &mcReach{live: map[string]string{}, seen: map[string]bool{}, outcome: map[string]string{}, groupLive: map[string]string{}}
	// The sources of anchors, stated once. A source is a book, other than the
	// loser, whose content may have moved on from the dead book D:
	//   - the pointer-carrying hops on the loser's own chain (live merged ones
	//     as well as trashed ones; the loser itself is not one);
	//   - every other book whose chain enters D, with or without a file, in the
	//     trash or not (the co-losers); that includes every pointer-carrying hop
	//     on their chains, trashed ones, since each is such a book itself.
	// For each source its retired-version holders and its version group are
	// read, and each anchor found is followed along its OWN chain. An anchor's
	// own holders and group are never expanded: the walk is sources, anchors,
	// then each anchor's chain end. The loser's own group is not read through a
	// co-loser (the hand-off gate owns that group), or every grouped stand-alone
	// row would become a repoint at its own group's primary.
	var sources []string
	own := map[string]bool{}
	for _, h := range fa.chain.Hops {
		if h.Book == nil || (h.State != mcHopTrashed && h.State != mcHopMerged) {
			continue
		}
		sources = append(sources, h.ID)
		own[h.ID] = true
	}
	coSources, err := r.deadEndSources(ctx, fa.chain.LastID())
	if err != nil {
		return err
	}
	for _, id := range coSources {
		if id != fa.id && !own[id] {
			sources = append(sources, id)
		}
	}
	for _, src := range sources {
		if err := r.readSource(fa, rc, src, !own[src]); err != nil {
			return err
		}
	}
	for id := range rc.live {
		fa.candidates = append(fa.candidates, id)
	}
	sort.Strings(fa.candidates)
	sort.Strings(rc.dead)
	sort.Strings(rc.liveCopies)
	sort.Strings(rc.unproven)
	for id := range rc.groupLive {
		// Another route may have reached it (a holder that is also a member).
		if _, reached := rc.live[id]; !reached {
			fa.orphans = append(fa.orphans, id)
		}
	}
	sort.Strings(fa.orphans)
	end := fa.chain.LastID()
	switch {
	case len(rc.unproven) > 0:
		fa.class = mcKindRepoint
		fa.chainHold = mcSkipRepointAmbiguous
		fa.chainHoldWhy = "the merge chain ends at " + end + ", which is " + fa.deadWhy() + ", but " + strings.Join(rc.unproven, ", ") +
			" (a holder of its retired versions or the primary of its version group) sits on a merge loop or a chain longer than " +
			strconv.Itoa(mcMaxHops) + " books, so whether a live book is reachable cannot be shown; never guessed"
	case len(fa.orphans) > 0:
		fa.class = mcKindRepoint
		fa.chainHold = mcSkipRepointGroupNoPrimary
		fa.chainHoldWhy = "the merge chain ends at " + end + ", which is " + fa.deadWhy() + ", and its version group holds live book(s) " +
			strings.Join(fa.orphans, ", ") + " that no walk reached because the group has no usable primary; whether the loser stands alone or belongs with one of them cannot be shown, so it is never guessed"
	case len(fa.candidates) == 0:
		fa.kind, fa.class = mcKindUnmerge, mcKindUnmerge
		checked := "no holder of its retired versions and no live book of its version group exists"
		switch {
		case len(rc.dead) > 0:
			checked = "every holder of its retired versions and book of its version group that could be followed (" + strings.Join(rc.dead, ", ") + ") is missing, in the trash, or a merged book with no file of its own, and no live book of its version group is left"
		case fa.chain.LastState() == mcHopMissing:
			checked = "the book it points at no longer exists, so it has no retired versions or version group to read"
		}
		mates, err := r.deadEndMates(ctx, fa)
		if err != nil {
			return err
		}
		// Other live copies of the work around this dead end: the books whose
		// chain ends at the same dead book, and the holders and group members
		// that are themselves merged books whose chain ends at a dead one.
		others := mcUnique(append(slices.Clone(mates), rc.liveCopies...))
		sort.Strings(others)
		if len(mates) > 0 {
			fa.cur["dead_end_other_losers"] = strings.Join(mates, ",")
		}
		if len(rc.liveCopies) > 0 {
			fa.cur["dead_end_live_copies"] = strings.Join(rc.liveCopies, ",")
		}
		// The "stands alone" sentence is only said of a loser that is held by
		// no other live copy; beside one, the hold below says what is left.
		if len(others) == 0 {
			fa.evidence = append(fa.evidence, "the merge chain ends at "+end+", which is "+fa.deadWhy()+"; "+checked+", so no live book is reachable: the book stands alone")
		} else {
			fa.evidence = append(fa.evidence, "the merge chain ends at "+end+", which is "+fa.deadWhy()+", and no live book is reachable from it")
		}
		if len(others) > 0 {
			fa.chainHold = mcSkipDeadEndShared
			fa.chainHoldWhy = "the merge chain ends at " + end + ", which is " + fa.deadWhy() + ", and " + strconv.Itoa(len(others)) +
				" other live merged book(s) with files (" + strings.Join(others, ", ") + ") lead to a dead end too, at the same book or from its retired versions or version group; each standing alone would leave several live copies of one work, and nothing here says they are different works"
		}
	case len(fa.candidates) == 1:
		fa.kind, fa.class = mcKindRepoint, mcKindRepoint
		fa.repoint = fa.candidates[0]
		fa.evidence = append(fa.evidence, "the merge chain ends at "+end+", which is "+fa.deadWhy()+"; the live book "+fa.repoint+" is reachable: "+rc.live[fa.repoint])
		return r.judgeRepoint(fa)
	default:
		fa.class = mcKindRepoint
		fa.chainHold = mcSkipRepointAmbiguous
		fa.chainHoldWhy = "more than one live book is reachable from the dead end (" + strings.Join(fa.candidates, ", ") + "); never guessed"
	}
	return nil
}

// judgeRepoint reads the one live book a repoint would point the loser at and
// decides the two on the same signals as an ordinary merge: a repoint says the
// loser and that book are one work, so it needs the evidence an unmerge or a
// swap needs, not just a path to a live book.
func (r *mcRun) judgeRepoint(fa *mcFacts) error {
	t, err := r.load(fa.repoint)
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("the live book %s a repoint would point at could not be read again", fa.repoint)
	}
	fa.repointTarget = t
	v := mcDecide(fa.l.side, t.side)
	fa.repointVerdict = &v
	return nil
}

func (fa *mcFacts) deadWhy() string {
	if n := len(fa.chain.Hops); n > 0 && fa.chain.Hops[n-1].State == mcHopMissing {
		return "not a book that exists"
	}
	return "in the trash"
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
		for _, x := range fa.verdict.Extra {
			fa.hold(x.Skip, x.Why)
		}
	}
	r.repointGate(fa)
	switch fa.kind {
	case mcKindSwap:
		if err := r.swapGates(ctx, fa); err != nil {
			return err
		}
	case mcKindUnmerge:
		r.unmergeGroupGate(fa)
	}
	if fa.syncBroken != "" && fa.kind != "" {
		fa.hold(mcSkipSyncBroken, "the book's sync redirect chain cannot be followed ("+fa.syncBroken+"), so whether clients are redirected away from it cannot be told")
	}
	if fa.flags[mcFlagSyncRedirect] && fa.kind != "" {
		fa.hold(mcSkipSyncRedirect, "the book's sync identity resolves to "+fa.cur["sync_redirect_to"]+", a redirect clients follow; clearing it is not part of this release")
	}
	return r.ownerGate(fa)
}

// repointGate holds a repoint whose target the signals do not call the same
// work. Only merge_looks_correct passes: a repoint merges the loser into that
// book, so a verdict that says different work, a fragment, or "cannot tell"
// holds it, with the verdict's own reason when it has one.
func (r *mcRun) repointGate(fa *mcFacts) {
	v := fa.repointVerdict
	if v == nil || v.Skip == mcSkipMergeLooksCorrect {
		return
	}
	why := "the signals do not call the loser and " + fa.repoint + " one work, which a repoint says"
	switch {
	case v.Skip != "":
		fa.hold(v.Skip, "repoint target "+fa.repoint+": "+v.Why)
	case v.Kind == mcKindSwap:
		// A swap verdict is the same work with the target the fragment: the hold
		// is right (a repoint would merge the loser into a fragment of itself),
		// and the words say that rather than "not the same work".
		fa.hold(mcSkipRepointNotSameWork, "the signals read "+fa.repoint+" as a fragment of the loser ("+strings.Join(v.Signals.Names(), ", ")+"), not a book to point the loser at; that is a swap of the keeper, not a repoint")
	case v.Kind != "":
		fa.hold(mcSkipRepointNotSameWork, why+": the verdict against it is "+v.Kind+" ("+strings.Join(v.Signals.Names(), ", ")+")")
	default:
		fa.hold(mcSkipRepointNotSameWork, why)
	}
	for _, x := range v.Extra {
		fa.hold(x.Skip, "repoint target "+fa.repoint+": "+x.Why)
	}
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
	case fa.target != nil && fa.chainHasGroup(gid):
		// Only an unmerge that names a book (a wrong-work unmerge) takes the
		// loser out of that book's group. A dead end or a loop has no such
		// book: the loser stays in its group, so the group must keep a primary.
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
	// The loser stays, with its pointer cleared and its flag left as it is: if
	// that flag reads as primary (nil reads as primary too), it is the group's
	// primary itself and the group keeps one.
	if fa.l != nil && fa.l.book != nil && database.EffectiveIsPrimaryVersion(fa.l.book.IsPrimaryVersion) {
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
// with an unmerge; a reject that cannot be ruled out holds a swap or a repoint,
// and so does a reject itself: a repoint merges the loser into the book it
// names, which says the two are one work.
func (r *mcRun) ownerGate(fa *mcFacts) error {
	peer := fa.target
	if peer == nil {
		peer = fa.repointTarget
	}
	if peer == nil {
		return nil
	}
	a, b := fa.id, peer.side.ID
	// A swap and a repoint both say the two books are one work.
	claimsOneWork := fa.kind == mcKindSwap || fa.kind == mcKindRepoint
	what := "swap"
	if fa.kind == mcKindRepoint {
		what = "repoint"
	}
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
			if claimsOneWork {
				fa.hold(mcSkipOwnerSaysDistinct, "the owner rejected joining these two books ("+fam+"); a "+what+" says they are one work")
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
		if claimsOneWork {
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
	mcPutIdentity(cur, "loser", l.side)
	row.Members = []repairs.RowMember{{BookID: fa.id, Title: l.book.Title, Role: "loser", Files: l.side.Files, MissingFiles: len(l.files) - l.side.Files}}
	row.BookIDs = []string{fa.id}
	fa.fpExtra = append(fa.fpExtra, mcFileSig(l.files), mcSideSig(l.side))

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
		mcPutIdentity(cur, "target", t.side)
		row.Members = append(row.Members, repairs.RowMember{BookID: t.side.ID, Title: t.book.Title, Role: "target",
			Files: t.side.Files, MissingFiles: len(t.files) - t.side.Files})
		fa.fpExtra = append(fa.fpExtra, mcFileSig(t.files), mcSideSig(t.side))
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
		m := repairs.RowMember{BookID: fa.repoint, Role: "repoint_target"}
		// The owner approves "point this book at X": X is shown the way an
		// unmerge's or a swap's target is, with what the verdict read of it, and
		// its files and signals are in the fingerprint, so a re-plan sees a change
		// to X that leaves the verdict where it was.
		if t := fa.repointTarget; t != nil {
			m.Title, m.Files, m.MissingFiles = t.book.Title, t.side.Files, len(t.files)-t.side.Files
			cur["repoint_target_title"] = t.book.Title
			cur["repoint_target_group_id"] = t.side.GroupID
			cur["repoint_target_primary"] = t.side.Primary
			cur["repoint_target_files"] = strconv.Itoa(t.side.Files)
			mcPutRuntime(cur, "repoint_target", t.side)
			mcPutIdentity(cur, "repoint_target", t.side)
			fa.fpExtra = append(fa.fpExtra, mcFileSig(t.files), mcSideSig(t.side))
			if v := fa.repointVerdict; v != nil {
				s := v.Signals
				cur["repoint_signals"] = strings.Join(s.Names(), ",")
				cur["repoint_title_similarity"] = strconv.FormatFloat(s.TitleSim, 'f', 2, 64)
				if s.RuntimeKnown {
					cur["repoint_runtime_ratio_permille"] = strconv.Itoa(s.RatioPermille)
				}
				if len(s.Names()) > 0 {
					fa.evidence = append(fa.evidence, "signals against the repoint target: "+strings.Join(s.Names(), ", "))
				}
			}
		}
		row.Members = append(row.Members, m)
	}
	for _, id := range append(slices.Clone(fa.candidates), fa.orphans...) {
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
		switch {
		case fa.target == nil:
			// A dead-end unmerge says nothing against another book, so the apply
			// clears the pointer and leaves the loser's group as it is
			// (todo.d TODO-MERGE-CLEANUP-DEAD-END-APPLY-RULES): the preview shows
			// what will be written.
			p["is_primary_version"], p["version_group_id"] = "unchanged", "unchanged"
		case fa.groupEffect == "stays_in_group":
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

// mcPutRuntime records the on-disk runtime the verdict was made on and, when
// chapter rows are off disk, the runtime with them counted beside it.
func mcPutRuntime(cur map[string]string, side string, s mcSide) {
	secs, ok := s.Runtime.KnownSeconds()
	if ok {
		cur[side+"_runtime_seconds"] = strconv.Itoa(secs)
	} else {
		cur[side+"_runtime_seconds"] = ""
	}
	cur[side+"_runtime_complete"] = strconv.FormatBool(ok)
	if s.RuntimeAll.FilesMissingUnmatched > 0 {
		all, allOK := s.RuntimeAll.KnownSeconds()
		cur[side+"_runtime_with_missing_seconds"] = ""
		if allOK {
			cur[side+"_runtime_with_missing_seconds"] = strconv.Itoa(all)
		}
		cur[side+"_missing_chapter_rows"] = strconv.Itoa(s.RuntimeAll.FilesMissingUnmatched)
	}
}

// mcPutIdentity records what the owner reads to tell two books apart: credits,
// series position and ASIN. Everything the verdict's signals read about a side
// is in Current, so a row can be judged from the row alone.
func mcPutIdentity(cur map[string]string, side string, s mcSide) {
	cur[side+"_credits"] = strings.Join(s.Credits, "; ")
	if s.CreditsDangling > 0 {
		cur[side+"_credits_dangling"] = strconv.Itoa(s.CreditsDangling)
	}
	if s.CreditsPlaceholder {
		cur[side+"_credits_placeholder"] = "true"
	}
	if s.SeriesID != nil {
		cur[side+"_series_id"] = strconv.Itoa(*s.SeriesID)
	}
	if s.SeriesSeq != nil {
		cur[side+"_series_position"] = strconv.Itoa(*s.SeriesSeq)
	}
	cur[side+"_asin"] = s.ASIN
	if len(s.AltTitles) > 0 {
		cur[side+"_alternative_titles"] = strings.Join(s.AltTitles, "; ")
	}
}

// mcSideSig is every input the verdict reads off a side that Current does not
// already show, for the fingerprint.
func mcSideSig(s mcSide) string {
	return strings.Join([]string{s.ID, strings.Join(s.AltTitles, "\x1f"), strings.Join(s.Credits, "\x1f"),
		strings.Join(s.Authors, "\x1f"), strconv.FormatBool(s.CreditsPlaceholder), strconv.Itoa(s.CreditsDangling),
		s.HashFile, strconv.Itoa(s.HashShare), s.HashOrig, strconv.Itoa(s.HashOrigShare)}, "|")
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
			strconv.FormatInt(files[i].FileSize, 10), files[i].FileHash, files[i].OriginalFileHash}, "|")
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

// mcListingShort is the refusal when the in-memory book index holds fewer books
// than the authoritative listing: nil when it holds all of them.
func mcListingShort(listed, indexed int) error {
	if indexed >= listed {
		return nil
	}
	return fmt.Errorf("%s: the in-memory book index holds %d books but the authoritative listing has %d (%d short); "+
		"per-book reads would call live books missing, so no census is made until the index is rebuilt (restart the server)",
		mergeCleanupFixerID, indexed, listed, listed-indexed)
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

// errorFlags are the flags an error row carries, read again on a best-effort
// basis: the read that failed is usually one of the files or the chain, not the
// book itself, and the census splits every row by origin. A loser whose origin
// cannot be read again is origin_unknown, never a guessed one, so the origin
// flags add up to the rows. The sync redirect is read the same way; a failure
// there leaves its flags off (the row already says a read failed).
func (r *mcRun) errorFlags(id string) []string {
	flags := map[string]bool{}
	b, err := r.book(id)
	known := false
	if err == nil && b != nil && mcHasPointer(b) {
		if b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
			_, _, f := mcClassifyOrigin(b, nil)
			flags[f], known = true, true
		} else if hop, herr := r.book(*b.MergedIntoBookID); herr == nil {
			_, _, f := mcClassifyOrigin(b, hop)
			flags[f], known = true, true
		}
	}
	if !known {
		flags[mcFlagOriginUnknown] = true
	}
	if b != nil && r.syncIDs != nil {
		if sid, ok, err := r.syncIDs.GetSyncIDForBook(id); err == nil && ok {
			item, err := r.syncIDs.ResolveSyncItem(sid)
			switch {
			case errors.Is(err, database.ErrSyncRedirectChainBroken):
				flags[mcFlagSyncBroken] = true
			case err == nil && item != nil && item.CurrentBookID != "" && item.CurrentBookID != id:
				flags[mcFlagSyncRedirect] = true
			}
		}
	}
	out := make([]string, 0, len(flags))
	for f := range flags {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// mcErrorRow is the row of a book whose reads failed.
func mcErrorRow(id, title string, err error, flags []string) repairs.Row {
	row := repairs.Row{RowID: id, BookIDs: []string{id}, Title: title, Class: mcClassError, Skipped: mcSkipError,
		SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview, Flags: flags}
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
	cores, err := run.store.GetAllBooksCoreComplete(0, 0)
	if err != nil {
		return nil, census, fmt.Errorf("list books: %w", err)
	}
	// The listing above is authoritative, but every per-book read after it comes
	// from the in-memory index. When that index is short, those reads would call
	// a live book missing: refuse the census, with the counts, rather than
	// classify against a library that is not whole.
	indexed, err := run.store.CountAllBooks()
	if err != nil {
		return nil, census, fmt.Errorf("count the indexed books: %w", err)
	}
	if err := mcListingShort(len(cores), indexed); err != nil {
		_ = rep.Log(slog.LevelWarn, err.Error())
		return nil, census, err
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
	run.merged = make([]string, len(ids))
	for i := range ids {
		run.merged[i] = ids[i].id
	}

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
			outs[i] = mcOutcome{row: mcErrorRow(ids[i].id, ids[i].title, err, run.errorFlags(ids[i].id))}
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
	return fmt.Sprintf("merge-cleanup preview: %d merged book(s); %d row(s), %d would propose a repair; excluded %d trashed, %d with no live file, %d missing, %d no longer merged; by class %s; by first skip kind %s; by every hold %s; by flag %s",
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
