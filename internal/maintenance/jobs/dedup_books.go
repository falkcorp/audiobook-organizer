// file: internal/maintenance/jobs/dedup_books.go
// version: 3.5.0
// guid: a1000010-0000-0000-0000-000000000010
// last-edited: 2026-09-13

package jobs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

var ddLog = logger.New("dedup-books")

func init() { maintenance.Register(&dedupBooksJob{}) }

type dedupBooksJob struct {
	enqueuer maintenance.WriteBackEnqueuer
}

func (j *dedupBooksJob) InjectEnqueuer(e maintenance.WriteBackEnqueuer) { j.enqueuer = e }

func (j *dedupBooksJob) ID() string       { return "dedup-books" }
func (j *dedupBooksJob) Name() string     { return "Deduplicate Books" }
func (j *dedupBooksJob) Category() string { return "dedup" }
func (j *dedupBooksJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *dedupBooksJob) Description() string { return "Detect and merge duplicate books" }
func (j *dedupBooksJob) CanResume() bool     { return false }

// ddTally counts one phase's outcomes. Refused is a guard saying no (iTunes
// library, a version group that cannot be handed off); Failed is a store error.
// Dry-run runs the same guards, so its counts are what apply would do.
type ddTally struct{ done, refused, failed int }

// errDDRefused marks a retirement the job declines (a junk row whose own
// files collide with a live book's). Counted as refused, not failed.
var errDDRefused = errors.New("dedup-books refused")

func (t *ddTally) record(err error) {
	switch {
	case err == nil:
		t.done++
	case merge.IsRefusal(err) || errors.Is(err, errDDRefused):
		t.refused++
	default:
		t.failed++
	}
}

func (j *dedupBooksJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	allBooks, err := ddFetchAllBooksPaginated(store)
	if err != nil {
		return fmt.Errorf("failed to list books: %w", err)
	}
	reporter.SetTotal(len(allBooks))

	deletedIDs := make(map[string]bool)
	// skippedJunk holds phase-1 junk rows the job did NOT retire (refused, or a
	// failed read or write). They stay out of phases 2-4: a junk row that
	// shares a real book's path must never become that book's keeper -- the
	// real book's rows would move onto the junk row and the real book would be
	// retired -- nor be merged as anyone's dup.
	skippedJunk := make(map[string]bool)
	// refusedByPath counts phase-1 refusals per covering live path, so one
	// live book whose FilePath is a whole author or root folder is visible
	// when it blocks many junk rows.
	refusedByPath := make(map[string]int)
	// sim tracks retirements and promotions this run has made (or, in dry-run,
	// would have made), so a later primary hand-off in the same run sees the
	// group state apply would see.
	sim := newDDSim()

	// Every path a live, non-junk book names -- its FilePath and each of its
	// book_file rows -- so phase 1 can refuse a junk row whose own files
	// collide with a live book's (see ddJunkSharesLivePath). A read error
	// fails the run before any write: the check cannot be skipped.
	livePaths, err := ddLivePathIndex(store, allBooks)
	if err != nil {
		return fmt.Errorf("index live book paths: %w", err)
	}

	// Phase 1: Delete junk "read by narrator" records
	var phase1 ddTally
	for i := range allBooks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		book := &allBooks[i]
		if deletedIDs[book.ID] {
			continue
		}
		if !ddIsJunkReadByNarrator(book) {
			continue
		}
		// PurgeSoftDeletedBooks with delete-files os.Remove()s a purged book's
		// FilePath (for a directory, the book's own rows' files). Exact-string
		// comparison cannot prove a junk path unshared -- a live book's
		// FilePath can be the directory holding it -- so EVERY junk row is
		// retired with its FilePath cleared and the purge deletes nothing
		// through it. A junk row whose own book_file rows name a live book's
		// file or lie under a live book's path is refused: retiring it would
		// leave that file owned by a deleted row.
		hit, delErr := ddJunkSharesLivePath(store, book, livePaths)
		if delErr == nil {
			delErr = ddRetireBook(store, sim, book, nil, dryRun, true)
		}
		phase1.record(delErr)
		if delErr != nil {
			skippedJunk[book.ID] = true
			if hit != "" {
				refusedByPath[hit]++
				ddLog.Warn("phase1 refused junk book=%s covering_live_path=%s: %s",
					logger.SanitizeLogValue(book.ID), logger.SanitizeLogValue(hit), logger.SanitizeLogValue(delErr.Error()))
			} else {
				ddLog.Error("phase1 retire book=%s: %s", logger.SanitizeLogValue(book.ID), logger.SanitizeLogValue(delErr.Error()))
			}
			continue
		}
		deletedIDs[book.ID] = true
	}

	// Phase 2: Merge books with the same file_path
	pathGroups := make(map[string][]database.Book)
	for i := range allBooks {
		book := &allBooks[i]
		if deletedIDs[book.ID] || skippedJunk[book.ID] || book.FilePath == "" {
			continue
		}
		pathGroups[book.FilePath] = append(pathGroups[book.FilePath], *book)
	}

	var phase2 ddTally
	for _, group := range pathGroups {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(group) < 2 {
			continue
		}
		live := ddFilterLive(group, deletedIDs)
		if len(live) < 2 {
			continue
		}
		keepIdx := ddPickKeeperIdx(live)
		keeper := &live[keepIdx]
		for i := range live {
			if i == keepIdx {
				continue
			}
			dup := &live[i]
			mergeErr := ddMergeDuplicateBookSim(store, sim, keeper, dup, dryRun, j.enqueuer)
			phase2.record(mergeErr)
			if mergeErr != nil {
				ddLog.Error("phase2 merge dup=%s keeper=%s: %s", logger.SanitizeLogValue(dup.ID), logger.SanitizeLogValue(keeper.ID), logger.SanitizeLogValue(mergeErr.Error()))
				continue
			}
			deletedIDs[dup.ID] = true
		}
	}

	// Phase 3: Merge books with same normalised title + author in same dir
	type titleAuthorKey struct {
		NormTitle string
		AuthorID  int
		Dir       string
	}
	taGroups := make(map[titleAuthorKey][]database.Book)
	for i := range allBooks {
		book := &allBooks[i]
		if deletedIDs[book.ID] || skippedJunk[book.ID] {
			continue
		}
		normTitle := ddNormalizeDedupTitle(book.Title)
		if normTitle == "" {
			continue
		}
		authorID := 0
		if book.AuthorID != nil {
			authorID = *book.AuthorID
		}
		dir := ""
		if book.FilePath != "" {
			dir = filepath.Dir(book.FilePath)
		}
		key := titleAuthorKey{NormTitle: normTitle, AuthorID: authorID, Dir: dir}
		taGroups[key] = append(taGroups[key], *book)
	}

	var phase3 ddTally
	for key, group := range taGroups {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(group) < 2 {
			continue
		}
		live := ddFilterLive(group, deletedIDs)
		if len(live) < 2 {
			continue
		}
		if key.AuthorID == 0 {
			titles := make(map[string]bool)
			for _, b := range live {
				titles[strings.ToLower(strings.TrimSpace(b.Title))] = true
			}
			if len(titles) > 1 {
				continue
			}
		}
		keepIdx := ddPickKeeperIdx(live)
		keeper := &live[keepIdx]
		for i := range live {
			if i == keepIdx {
				continue
			}
			dup := &live[i]
			mergeErr := ddMergeDuplicateBookSim(store, sim, keeper, dup, dryRun, j.enqueuer)
			phase3.record(mergeErr)
			if mergeErr != nil {
				ddLog.Error("phase3 merge dup=%s keeper=%s: %s", logger.SanitizeLogValue(dup.ID), logger.SanitizeLogValue(keeper.ID), logger.SanitizeLogValue(mergeErr.Error()))
				continue
			}
			deletedIDs[dup.ID] = true
		}
	}

	// Phase 4: Clean up duplicate version group entries
	vgGroups := make(map[string][]database.Book)
	for i := range allBooks {
		book := &allBooks[i]
		if deletedIDs[book.ID] || skippedJunk[book.ID] || book.VersionGroupID == nil || *book.VersionGroupID == "" {
			continue
		}
		vgGroups[*book.VersionGroupID] = append(vgGroups[*book.VersionGroupID], *book)
	}

	var phase4 ddTally
	for vgID, group := range vgGroups {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		seen := make(map[string]bool)
		var dupeIDs []string
		for _, b := range group {
			if seen[b.ID] {
				dupeIDs = append(dupeIDs, b.ID)
			}
			seen[b.ID] = true
		}
		if len(dupeIDs) == 0 {
			continue
		}
		if dryRun {
			phase4.done += len(dupeIDs)
			continue
		}
		for _, dupID := range dupeIDs {
			_, upErr := store.ModifyBook(dupID, func(b *database.Book) error {
				b.VersionGroupID = nil
				b.IsPrimaryVersion = nil
				return nil
			})
			phase4.record(upErr)
			if upErr != nil {
				ddLog.Error("phase4 unlink vg=%s from book=%s: %s", logger.SanitizeLogValue(vgID), logger.SanitizeLogValue(dupID), logger.SanitizeLogValue(upErr.Error()))
			}
		}
	}

	for range allBooks {
		reporter.Increment()
	}

	summary := fmt.Sprintf("dry_run=%v junk=%d/%d refused/%d failed path_merge=%d/%d refused/%d failed title_merge=%d/%d refused/%d failed vg_unlink=%d/%d failed",
		dryRun, phase1.done, phase1.refused, phase1.failed, phase2.done, phase2.refused, phase2.failed,
		phase3.done, phase3.refused, phase3.failed, phase4.done, phase4.failed)
	reporter.Log("info", summary, nil)
	ddLog.Info("done: %s", summary)
	if len(refusedByPath) > 0 {
		byPath := "phase1 junk refusals by covering live path: " + ddTopPathCounts(refusedByPath, 20)
		reporter.Log("info", byPath, nil)
		ddLog.Warn("%s", logger.SanitizeLogValue(byPath))
	}
	if failed := phase1.failed + phase2.failed + phase3.failed + phase4.failed; failed > 0 {
		return fmt.Errorf("dedup-books: %d write(s) failed; those books were left unmerged (%s)", failed, summary)
	}
	return nil
}

func ddFetchAllBooksPaginated(store bookPager) ([]database.Book, error) {
	const pageSize = 500
	var all []database.Book
	afterID := ""
	for {
		page, err := store.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			break
		}
		afterID = page[len(page)-1].ID
	}
	return all, nil
}

func ddIsJunkReadByNarrator(book *database.Book) bool {
	t := strings.ToLower(strings.TrimSpace(book.Title))
	if t != "read by narrator" {
		return false
	}
	if book.AuthorID != nil {
		return false
	}
	if book.SeriesID != nil {
		return false
	}
	if book.Description != nil && strings.TrimSpace(*book.Description) != "" {
		return false
	}
	if book.ISBN10 != nil || book.ISBN13 != nil || book.ASIN != nil {
		return false
	}
	if book.ITunesPersistentID != nil {
		return false
	}
	return true
}

// ddPickKeeperIdx picks the survivor. A book explicitly marked as its version
// group's primary wins over any non-primary, whatever the metadata score:
// retiring the primary is the one choice that can leave a group without one.
// Only an explicit true counts here; nil is ambiguous across readers (see
// ddCountsAsPrimary), and the retire path handles the nil case by re-electing.
//
// A "read by narrator" junk row is never the keeper while a real book is in
// the group, whatever its score or primary flag: it would absorb the real
// book's rows and the real book would be the one retired.
func ddPickKeeperIdx(books []database.Book) int {
	best := 0
	for i := 1; i < len(books); i++ {
		iJunk, bestJunk := ddIsJunkReadByNarrator(&books[i]), ddIsJunkReadByNarrator(&books[best])
		iPrim, bestPrim := ddExplicitPrimary(&books[i]), ddExplicitPrimary(&books[best])
		switch {
		case iJunk != bestJunk:
			if !iJunk {
				best = i
			}
		case iPrim && !bestPrim:
			best = i
		case iPrim == bestPrim && ddBookScore(&books[i]) > ddBookScore(&books[best]):
			best = i
		}
	}
	return best
}

func ddExplicitPrimary(b *database.Book) bool {
	return b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
}

// ddCountsAsPrimary is the reading of IsPrimaryVersion every DESTRUCTIVE guard
// in this package uses: nil counts as primary. The codebase disagrees about nil
// (pebble_store and memdb read nil as primary; reconcile and sortVersions read
// it as not primary -- see the comment in merge.MergeBooks), so a guard that
// retires a book must take the reading under which retiring it could strand
// the group. Reading `!= nil && *` here would let the job soft-delete a group's
// only nil-flagged member, which is the bug this guard exists to stop.
func ddCountsAsPrimary(b *database.Book) bool {
	return b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
}

func ddBookScore(b *database.Book) int {
	score := 0
	if b.AuthorID != nil {
		score += 100
	}
	if b.SeriesID != nil {
		score += 20
	}
	if b.Description != nil && *b.Description != "" {
		score += 10
	}
	if b.Narrator != nil && *b.Narrator != "" {
		score += 5
	}
	if b.Duration != nil {
		score += 5
	}
	if b.ISBN10 != nil || b.ISBN13 != nil || b.ASIN != nil {
		score += 10
	}
	if b.ITunesPersistentID != nil {
		score += 10
	}
	if b.Publisher != nil && *b.Publisher != "" {
		score += 3
	}
	if b.Language != nil && *b.Language != "" {
		score += 2
	}
	if b.Genre != nil && *b.Genre != "" {
		score += 2
	}
	if b.CoverURL != nil && *b.CoverURL != "" {
		score += 3
	}
	if b.CreatedAt != nil {
		score -= int(b.CreatedAt.Unix() / 1_000_000)
	}
	return score
}

// ddGroupStore is what the retire guards read: the iTunes guard's surface plus
// the version-group membership used to re-elect a primary.
type ddGroupStore interface {
	merge.ITunesGuardStore
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// ddPlanPrimaryHandoff decides who becomes primary of book's version group
// once book is retired. "" means no hand-off is needed: book is not in a
// group, does not count as its primary, another live member is already an
// explicit primary, or book is the group's last live member.
//
// heir is the book absorbing this one (nil in phase 1). When heir is in the
// same group -- or joins it through ddMergeBookFields' VersionGroupID fill --
// heir takes the primary. Otherwise the earliest-created live member does,
// the same rule reconcile.ElectMissingPrimaries uses.
//
// sim overlays what this run has already done (apply) or would have done
// (dry-run): retired members are skipped and promoted members count as
// explicit primaries. In apply the store already agrees; in dry-run it is the
// only thing that makes a second hand-off in the same group match apply.
func ddPlanPrimaryHandoff(store ddGroupStore, sim *ddSim, book, heir *database.Book) (string, error) {
	if book.VersionGroupID == nil || *book.VersionGroupID == "" || !(ddCountsAsPrimary(book) || sim.isPromoted(book.ID)) {
		return "", nil
	}
	vg := *book.VersionGroupID
	members, err := store.GetBooksByVersionGroup(vg)
	if err != nil {
		return "", fmt.Errorf("read version group %s of %s: %w", vg, book.ID, err)
	}
	if sim != nil {
		// Books that joined this group through the keeper fill. In apply the
		// store already lists them; in dry-run only the overlay does.
		seen := make(map[string]bool, len(members))
		for _, m := range members {
			seen[m.ID] = true
		}
		for _, j := range sim.joined[vg] {
			if !seen[j.ID] {
				members = append(members, j)
			}
		}
	}
	var others []database.Book
	for _, m := range members {
		if m.ID == book.ID || m.IsSoftDeleted() || sim.isRetired(m.ID) {
			continue
		}
		if ddExplicitPrimary(&m) || sim.isPromoted(m.ID) {
			return "", nil
		}
		others = append(others, m)
	}
	if heir != nil {
		heirVG := ""
		if heir.VersionGroupID != nil {
			heirVG = *heir.VersionGroupID
		}
		if heirVG == vg || heirVG == "" {
			return heir.ID, nil
		}
	}
	if len(others) == 0 {
		return "", nil
	}
	sort.SliceStable(others, func(i, j int) bool {
		a, b := others[i], others[j]
		switch {
		case a.CreatedAt != nil && b.CreatedAt != nil && !a.CreatedAt.Equal(*b.CreatedAt):
			return a.CreatedAt.Before(*b.CreatedAt)
		case a.CreatedAt != nil && b.CreatedAt == nil:
			return true
		case a.CreatedAt == nil && b.CreatedAt != nil:
			return false
		}
		return a.ID < b.ID
	})
	return others[0].ID, nil
}

// ddRetireStore is everything ddRetireBook and ddMergeDuplicateBook need.
type ddRetireStore interface {
	ddGroupStore
	ddBookModifier
}

// ddBookModifier is the per-book locked read-modify-write.
type ddBookModifier interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// ddRetireBook soft-deletes book after the guards every retirement shares:
// the iTunes guard (nothing under books/itunes/** is mutated) and the primary
// hand-off (a group's primary is never retired without a successor being
// promoted first). heir is the book absorbing this one, or nil.
//
// In dry-run it runs the same guards and writes nothing, so a dry-run count is
// the count apply would reach.
func ddRetireBook(store ddRetireStore, sim *ddSim, book, heir *database.Book, dryRun, clearPath bool) error {
	ids := []string{book.ID}
	if heir != nil {
		ids = append(ids, heir.ID)
	}
	if err := merge.GuardITunesProtected(store, ids); err != nil {
		return err
	}
	successor, err := ddPlanPrimaryHandoff(store, sim, book, heir)
	if err != nil {
		return err
	}
	if !dryRun {
		// Promote before retiring: the other order can leave the group with
		// no primary. If the soft-delete then fails or is refused, this run's
		// promotion is undone (ddRetireFailed), so the group is left as it
		// was rather than with two primaries.
		promoted, prev := false, (*bool)(nil)
		if successor != "" {
			if promoted, prev, err = ddPromotePrimary(store, successor); err != nil {
				return fmt.Errorf("promote %s before retiring primary %s: %w", successor, book.ID, err)
			}
		}
		if err := ddSoftDeleteBook(store, book.ID, clearPath, ddRetireGuard(store, sim, book, successor)); err != nil {
			return ddRetireFailed(store, successor, promoted, prev, err)
		}
	}
	sim.retire(book.ID, successor)
	return nil
}

// ddLivePathIndex returns every path a live, non-junk book names: its
// FilePath and each of its book_file rows, cleaned. The rows come from the
// full file listing, not the book_file_path index, which keeps one owner per
// path (last write wins) and so can name the junk row itself.
func ddLivePathIndex(store maintenance.JobStore, allBooks []database.Book) (map[string]bool, error) {
	live := make(map[string]bool)
	paths := make(map[string]bool)
	junk := 0
	for i := range allBooks {
		b := &allBooks[i]
		if b.IsSoftDeleted() {
			continue
		}
		if ddIsJunkReadByNarrator(b) {
			junk++
			continue
		}
		live[b.ID] = true
		if b.FilePath != "" {
			paths[filepath.Clean(b.FilePath)] = true
		}
	}
	if junk == 0 {
		return paths, nil
	}
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if live[f.BookID] && f.FilePath != "" {
			paths[filepath.Clean(f.FilePath)] = true
		}
	}
	return paths, nil
}

// ddJunkSharesLivePath returns errDDRefused when one of junk's own book_file
// rows names a path in livePaths or lies under one (a live book's FilePath
// directory), along with the covering live path for the refusal log. A read
// error is returned as is, so the book fails rather than being retired on a
// guess.
func ddJunkSharesLivePath(store maintenance.JobStore, junk *database.Book, livePaths map[string]bool) (string, error) {
	files, err := store.GetBookFiles(junk.ID)
	if err != nil {
		return "", fmt.Errorf("read files of %s: %w", junk.ID, err)
	}
	for _, f := range files {
		if f.FilePath == "" {
			continue
		}
		if hit := ddCoveredBy(filepath.Clean(f.FilePath), livePaths); hit != "" {
			return hit, fmt.Errorf("%w: junk book %s owns %s, which is live path %s or lies under it",
				errDDRefused, junk.ID, f.FilePath, hit)
		}
	}
	return "", nil
}

// ddTopPathCounts formats the n highest counts as "path=count, ...".
func ddTopPathCounts(counts map[string]int, n int) string {
	type pc struct {
		path  string
		count int
	}
	all := make([]pc, 0, len(counts))
	for p, c := range counts {
		all = append(all, pc{p, c})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].count != all[j].count {
			return all[i].count > all[j].count
		}
		return all[i].path < all[j].path
	})
	var b strings.Builder
	for i, e := range all {
		if i == n {
			fmt.Fprintf(&b, ", ... %d more path(s)", len(all)-n)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%d", e.path, e.count)
	}
	return b.String()
}

// ddCoveredBy returns the entry of paths that is p or an ancestor of p, or "".
func ddCoveredBy(p string, paths map[string]bool) string {
	for cur := p; ; {
		if paths[cur] {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// ddSim records the retirements, promotions and group joins a run has made,
// or in dry-run would have made. A nil *ddSim is an empty overlay.
type ddSim struct {
	retired  map[string]bool
	promoted map[string]bool
	// joined holds books that joined a version group through the keeper
	// field-fill, by group. In dry-run the store never sees the join.
	joined map[string][]database.Book
	// joinedVG is joined keyed by book, for overlay.
	joinedVG map[string]string
	// handoffs is the successor each retirement promoted ("" for none), in
	// order: what a dry-run and an apply of the same plan must agree on.
	handoffs []string
}

func newDDSim() *ddSim {
	return &ddSim{retired: map[string]bool{}, promoted: map[string]bool{}, joined: map[string][]database.Book{}, joinedVG: map[string]string{}}
}

func (s *ddSim) join(b *database.Book, vg string) {
	if s == nil || vg == "" {
		return
	}
	for _, m := range s.joined[vg] {
		if m.ID == b.ID {
			return
		}
	}
	cp := *b
	cp.VersionGroupID = &vg
	s.joined[vg] = append(s.joined[vg], cp)
	s.joinedVG[b.ID] = vg
}

// overlay applies what this run has recorded to b, a copy loaded before the
// run's earlier merges: a group joined through the keeper fill and a
// promotion. Dry-run's stand-in for re-reading the row.
func (s *ddSim) overlay(b *database.Book) {
	if s == nil || b == nil {
		return
	}
	if vg, ok := s.joinedVG[b.ID]; ok && (b.VersionGroupID == nil || *b.VersionGroupID == "") {
		v := vg
		b.VersionGroupID = &v
	}
	if s.isPromoted(b.ID) {
		t := true
		b.IsPrimaryVersion = &t
	}
}

func (s *ddSim) isRetired(id string) bool  { return s != nil && s.retired[id] }
func (s *ddSim) isPromoted(id string) bool { return s != nil && s.promoted[id] && !s.retired[id] }

func (s *ddSim) retire(id, successor string) {
	if s == nil {
		return
	}
	s.retired[id] = true
	s.handoffs = append(s.handoffs, successor)
	if successor != "" {
		s.promoted[successor] = true
	}
}

// ddPromotePrimary makes id an explicit primary. It reports whether it wrote
// and the flag it replaced, so a caller whose retirement then fails can undo
// exactly its own promotion (ddRetireFailed). A successor retired since the
// hand-off was planned is refused with errDDRefused: promoting a deleted row
// and then demoting the retiring book would leave no live primary.
func ddPromotePrimary(store ddBookModifier, id string) (promoted bool, prev *bool, err error) {
	b, err := store.ModifyBook(id, func(b *database.Book) error {
		if b.IsSoftDeleted() {
			return fmt.Errorf("%w: planned successor %s was retired after the hand-off was planned", errDDRefused, b.ID)
		}
		if ddExplicitPrimary(b) {
			return database.ErrSkipBookWrite
		}
		if b.IsPrimaryVersion != nil {
			v := *b.IsPrimaryVersion
			prev = &v
		}
		t := true
		b.IsPrimaryVersion = &t
		promoted = true
		return nil
	})
	if err != nil {
		return false, nil, err
	}
	if b == nil {
		return false, nil, fmt.Errorf("book %s not found", id)
	}
	return promoted, prev, nil
}

// ddRetireFailed is the error path after a retirement whose successor was
// promoted first: it puts the successor's flag back when this run's promotion
// wrote it, so the group keeps its one old primary instead of gaining a
// second, and returns err (with the undo's own failure, if any).
func ddRetireFailed(store ddBookModifier, successor string, promoted bool, prev *bool, err error) error {
	if !promoted {
		return err
	}
	if _, uErr := store.ModifyBook(successor, func(b *database.Book) error {
		if !ddExplicitPrimary(b) {
			return database.ErrSkipBookWrite
		}
		b.IsPrimaryVersion = prev
		return nil
	}); uErr != nil {
		return fmt.Errorf("%w; undoing the promotion of %s also failed, so its group may have two primaries: %v", err, successor, uErr)
	}
	return err
}

// ddRetireGuard returns the check ddSoftDeleteBook runs on the retiring
// book's stored row, under its lock, immediately before the write.
//
// The primary hand-off (ddPlanPrimaryHandoff) was planned from a read taken
// outside that lock, so a concurrent writer can have changed what it was
// planned on. Retiring on the stale plan leaves the group with two primaries
// (the book was demoted and another member promoted meanwhile, and this run
// then promotes its successor too) or with none (the book was promoted
// meanwhile, no successor was planned, and the soft-delete demotes it). So the
// write is refused with errDDRefused -- nothing written, nothing demoted --
// when the row's version group or primary reading differs from the plan's,
// or when the planned successor is no longer a live explicit primary (it was
// retired or demoted after ddPromotePrimary ran). The caller then undoes its
// own promotion. The successor check is a plain GetBookByID read, which the
// LOCK RULES allow inside a ModifyBook closure.
//
// This narrows the cross-book window; it does not close it. Only the retiring
// book's row is locked: the successor is read, not locked, so a writer can
// still change the successor, or another member of the group, between this
// check and the commit. Closing it needs a group-level lock the store does
// not have.
func ddRetireGuard(store merge.ITunesGuardStore, sim *ddSim, planned *database.Book, successor string) func(*database.Book) error {
	plannedVG := ""
	if planned.VersionGroupID != nil {
		plannedVG = *planned.VersionGroupID
	}
	plannedPrimary := ddCountsAsPrimary(planned) || sim.isPromoted(planned.ID)
	return func(cur *database.Book) error {
		curVG := ""
		if cur.VersionGroupID != nil {
			curVG = *cur.VersionGroupID
		}
		if curVG != plannedVG {
			return fmt.Errorf("%w: book %s moved from version group %q to %q after its primary hand-off was planned; not retiring it",
				errDDRefused, cur.ID, plannedVG, curVG)
		}
		if plannedVG != "" && ddCountsAsPrimary(cur) != plannedPrimary {
			return fmt.Errorf("%w: book %s's primary flag changed after its hand-off was planned (planned primary=%v); not retiring it",
				errDDRefused, cur.ID, plannedPrimary)
		}
		if successor == "" {
			return nil
		}
		heir, err := store.GetBookByID(successor)
		if err != nil {
			return fmt.Errorf("re-read planned successor %s of %s: %w", successor, cur.ID, err)
		}
		if heir == nil || heir.IsSoftDeleted() || !ddExplicitPrimary(heir) {
			return fmt.Errorf("%w: planned successor %s of %s is no longer a live primary; not retiring it", errDDRefused, successor, cur.ID)
		}
		return nil
	}
}

// ddMergeStore is ddMergeDuplicateBook's store: the job's whole surface.
type ddMergeStore = maintenance.JobStore

// ddRetireMergedDupAttempts is how many fresh plans ddRetireMergedDup tries
// before giving up on a dup whose retire keeps being refused.
const ddRetireMergedDupAttempts = 2

// ddRetireMergedDup soft-deletes dup, whose files, external IDs and user tags
// have already moved to keeper, and returns the successor it promoted ("" for
// none).
//
// The primary hand-off is planned from a fresh read of the dup immediately
// before each attempt, not from the read taken before the merge wrote
// anything: a writer that changed the dup's group or primary flag during the
// move is then planned around rather than refused. The retire guard
// (ddRetireGuard) still re-checks the plan under the dup's lock; if it
// refuses, this run's promotion is undone and the plan is made again from a
// new read.
//
// A dup that is still refused after the last attempt -- or whose retire fails
// for any other reason -- is left live and empty. That is returned as a
// failure, not a refusal: the merge has already written, so declining here is
// not "nothing happened", and the error names the emptied book. A dup another
// writer retired or deleted meanwhile needs nothing more.
func ddRetireMergedDup(store ddMergeStore, sim *ddSim, keeper, dup *database.Book) (string, error) {
	var lastErr error
	for attempt := 0; attempt < ddRetireMergedDupAttempts; attempt++ {
		fresh, err := store.GetBookByID(dup.ID)
		if err != nil {
			lastErr = fmt.Errorf("re-read dup %s: %w", dup.ID, err)
			break
		}
		if fresh == nil || fresh.IsSoftDeleted() {
			ddLog.Info("dup was retired by another writer after the merge moved its files dup=%s", logger.SanitizeLogValue(dup.ID))
			return "", nil
		}
		successor, err := ddPlanPrimaryHandoff(store, sim, fresh, keeper)
		if err != nil {
			lastErr = err
			break
		}
		promoted, prev := false, (*bool)(nil)
		if successor != "" {
			if promoted, prev, err = ddPromotePrimary(store, successor); err != nil {
				lastErr = fmt.Errorf("promote %s before retiring primary %s: %w", successor, dup.ID, err)
				if errors.Is(err, errDDRefused) {
					continue
				}
				break
			}
		}
		err = ddSoftDeleteBook(store, dup.ID, true, ddRetireGuard(store, sim, fresh, successor))
		if err == nil {
			*dup = *fresh
			return successor, nil
		}
		lastErr = ddRetireFailed(store, successor, promoted, prev, err)
		if !errors.Is(err, errDDRefused) {
			break
		}
	}
	// %v, not %w: the refusal inside must not make the tally count this as
	// refused.
	return "", fmt.Errorf("dup %s: its files, external IDs and user tags were moved to keeper %s, but retiring it failed, so it is left live and empty: %v",
		dup.ID, keeper.ID, lastErr)
}

// ddMergeDuplicateBook folds dup into keeper and soft-deletes dup.
//
// Order, and why:
//  1. Guards (both modes): iTunes, the dup's file list is readable, and the
//     primary hand-off. Dry-run stops here.
//  2. Keeper field-fill under the keeper's lock (gap-fill only, respecting the
//     user's field locks). A failure here writes nothing anywhere.
//  3. Move the dup's book_file rows with MoveBookFilesToBook. This used to set
//     f.BookID and call UpsertBookFile, which keeps the STORED BookID -- the
//     rows never moved, and the dup was then soft-deleted still owning them
//     while sharing the keeper's FilePath.
//  4. External IDs, user tags, ITL removals.
//  5. Re-check the dup owns zero rows (CombineBooks' guard), then soft-delete
//     it with FilePath cleared: a purge with delete-files on os.Remove()s a
//     purged book's FilePath, and that path is the keeper's audio.
//
// Any failure returns before the soft-delete, so the dup is never retired
// while it still owns anything, and a rerun picks the pair up again: the
// move, the external-ID reassign and the tag copy are all idempotent.
//
// The primary hand-off is written only after the move and the zero-rows
// check, immediately before the soft-delete. Promoting the keeper earlier
// (in the field-fill) left two primaries whenever a later step failed. Step
// 1's plan is a pre-check (and dry-run's answer); apply plans again from a
// fresh read of the dup right before the retire (ddRetireMergedDup), because
// by then steps 2-4 have written and a refusal can no longer leave "nothing
// happened".
func ddMergeDuplicateBook(store ddMergeStore, keeper *database.Book, dup *database.Book, dryRun bool, enqueuer maintenance.WriteBackEnqueuer) error {
	return ddMergeDuplicateBookSim(store, nil, keeper, dup, dryRun, enqueuer)
}

// ddMergeDuplicateBookSim is ddMergeDuplicateBook with the run's overlay.
func ddMergeDuplicateBookSim(store ddMergeStore, sim *ddSim, keeper *database.Book, dup *database.Book, dryRun bool, enqueuer maintenance.WriteBackEnqueuer) error {
	// The caller's copies were loaded before earlier merges in this run: a
	// keeper that joined a group and was promoted in phase 2 can be phase 3's
	// dup and still read as groupless and non-primary, so its group would be
	// left with no primary. Apply re-reads both rows (fail closed); dry-run
	// overlays what this run would have written, so the two modes agree.
	if dryRun {
		// Apply refuses a pair with a book already retired (the re-read
		// below). A book this run would already have retired, or one the
		// caller's copy shows retired, is refused the same way, so dry-run
		// reports what apply would.
		for _, b := range []*database.Book{keeper, dup} {
			if sim.isRetired(b.ID) || b.IsSoftDeleted() {
				return fmt.Errorf("%w: book %s is gone or already retired; not merging", errDDRefused, b.ID)
			}
		}
		sim.overlay(keeper)
		sim.overlay(dup)
	} else {
		for _, b := range []*database.Book{keeper, dup} {
			fresh, err := store.GetBookByID(b.ID)
			if err != nil {
				return fmt.Errorf("re-read book %s: %w", b.ID, err)
			}
			if fresh == nil || fresh.IsSoftDeleted() {
				return fmt.Errorf("%w: book %s is gone or already retired; not merging", errDDRefused, b.ID)
			}
			*b = *fresh
		}
	}
	if err := merge.GuardITunesProtected(store, []string{keeper.ID, dup.ID}); err != nil {
		return err
	}
	files, err := store.GetBookFiles(dup.ID)
	if err != nil {
		return fmt.Errorf("read files of dup %s: %w", dup.ID, err)
	}
	successor, err := ddPlanPrimaryHandoff(store, sim, dup, keeper)
	if err != nil {
		return err
	}
	// The keeper fill gives a groupless keeper the dup's version group.
	joinVG := ""
	if (keeper.VersionGroupID == nil || *keeper.VersionGroupID == "") && dup.VersionGroupID != nil {
		joinVG = *dup.VersionGroupID
	}
	if dryRun {
		// Mirror what apply leaves behind, so a later hand-off in the dup's
		// group counts the keeper as a member (and as primary if promoted).
		if joinVG != "" {
			vg := joinVG
			keeper.VersionGroupID = &vg
			sim.join(keeper, joinVG)
		}
		if successor != "" && successor == keeper.ID {
			t := true
			keeper.IsPrimaryVersion = &t
		}
		sim.retire(dup.ID, successor)
		return nil
	}

	var restored []string
	updated, err := store.ModifyBook(keeper.ID, func(current *database.Book) error {
		// The keeper's user-locked fields win over the dup's values -- a blank
		// the user locked stays blank. Fail closed: if the locks cannot be read
		// the merge does not happen and the dup is NOT deleted (its values would
		// be the only copy of whatever the keeper lacks). Both calls below only
		// READ (field states, series), which the LOCK RULES allow.
		r, lockErr := database.ApplyRespectingLocks(store, current, func(b *database.Book) { ddMergeBookFields(b, dup) })
		if lockErr != nil {
			return fmt.Errorf("keeper %s: %w", keeper.ID, lockErr)
		}
		restored = r
		database.DropDanglingSeriesRef(store, current, "dedup-books.keeper-fill")
		return nil
	})
	if err != nil {
		return fmt.Errorf("update keeper %s: %w", keeper.ID, err)
	}
	if updated == nil {
		return fmt.Errorf("keeper book %s not found", keeper.ID)
	}
	if len(restored) > 0 {
		ddLog.Info("left the keeper's user-locked fields alone keeper=%s locked=%s", logger.SanitizeLogValue(keeper.ID), logger.SanitizeLogValue(strings.Join(restored, ",")))
	}
	*keeper = *updated
	if joinVG != "" && keeper.VersionGroupID != nil && *keeper.VersionGroupID == joinVG {
		sim.join(keeper, joinVG)
	}

	if len(files) > 0 {
		ids := make([]string, len(files))
		for i := range files {
			ids[i] = files[i].ID
		}
		if err := store.MoveBookFilesToBook(ids, dup.ID, keeper.ID); err != nil {
			return fmt.Errorf("move %d file(s) from dup %s to keeper %s: %w", len(ids), dup.ID, keeper.ID, err)
		}
	}

	dupMappings, err := store.GetExternalIDsForBook(dup.ID)
	if err != nil {
		return fmt.Errorf("read external IDs of dup %s: %w", dup.ID, err)
	}
	var dupPIDs []string
	for _, m := range dupMappings {
		if m.Source == "itunes" && m.ExternalID != "" && !m.Tombstoned {
			dupPIDs = append(dupPIDs, m.ExternalID)
		}
	}
	if err := store.ReassignExternalIDs(dup.ID, keeper.ID); err != nil {
		return fmt.Errorf("reassign external IDs %s -> %s: %w", dup.ID, keeper.ID, err)
	}

	tags, err := store.GetBookUserTags(dup.ID)
	if err != nil {
		return fmt.Errorf("read user tags of dup %s: %w", dup.ID, err)
	}
	for _, tag := range tags {
		if err := store.AddBookUserTag(keeper.ID, tag); err != nil {
			return fmt.Errorf("copy user tag to keeper %s: %w", keeper.ID, err)
		}
	}

	// The CombineBooks guard (merge/service.go): never retire a book that still
	// owns files -- that would orphan audio, or let a purge delete it.
	remaining, err := store.GetBookFiles(dup.ID)
	if err != nil {
		return fmt.Errorf("re-read files of dup %s: %w", dup.ID, err)
	}
	if len(remaining) != 0 {
		return fmt.Errorf("dup %s still owns %d file(s) after the move; not soft-deleting it", dup.ID, len(remaining))
	}

	successor, err = ddRetireMergedDup(store, sim, keeper, dup)
	if err != nil {
		return err
	}
	if successor != "" && successor == keeper.ID {
		t := true
		keeper.IsPrimaryVersion = &t
	}
	sim.retire(dup.ID, successor)

	if enqueuer != nil && len(dupPIDs) > 0 {
		for _, pid := range dupPIDs {
			enqueuer.EnqueueRemove(pid)
		}
		ddLog.Info("queued ITL removals for dup count=%d dup=%s", len(dupPIDs), logger.SanitizeLogValue(dup.ID))
	}
	return nil
}

func ddMergeBookFields(dst, src *database.Book) {
	if dst.AuthorID == nil && src.AuthorID != nil {
		dst.AuthorID = src.AuthorID
	}
	if dst.SeriesID == nil && src.SeriesID != nil {
		dst.SeriesID = src.SeriesID
		if dst.SeriesSequence == nil && src.SeriesSequence != nil {
			dst.SeriesSequence = src.SeriesSequence
		}
	}
	if dst.Narrator == nil && src.Narrator != nil && *src.Narrator != "" {
		dst.Narrator = src.Narrator
	}
	if dst.Description == nil && src.Description != nil && *src.Description != "" {
		dst.Description = src.Description
	}
	if dst.Duration == nil && src.Duration != nil {
		dst.Duration = src.Duration
	}
	if dst.Publisher == nil && src.Publisher != nil {
		dst.Publisher = src.Publisher
	}
	if dst.Language == nil && src.Language != nil {
		dst.Language = src.Language
	}
	if dst.Genre == nil && src.Genre != nil {
		dst.Genre = src.Genre
	}
	if dst.ISBN10 == nil && src.ISBN10 != nil {
		dst.ISBN10 = src.ISBN10
	}
	if dst.ISBN13 == nil && src.ISBN13 != nil {
		dst.ISBN13 = src.ISBN13
	}
	if dst.ASIN == nil && src.ASIN != nil {
		dst.ASIN = src.ASIN
	}
	if dst.ITunesPersistentID == nil && src.ITunesPersistentID != nil {
		dst.ITunesPersistentID = src.ITunesPersistentID
	}
	if dst.ITunesDateAdded == nil && src.ITunesDateAdded != nil {
		dst.ITunesDateAdded = src.ITunesDateAdded
	}
	if dst.ITunesPlayCount == nil && src.ITunesPlayCount != nil {
		dst.ITunesPlayCount = src.ITunesPlayCount
	}
	if dst.ITunesRating == nil && src.ITunesRating != nil {
		dst.ITunesRating = src.ITunesRating
	}
	if dst.ITunesBookmark == nil && src.ITunesBookmark != nil {
		dst.ITunesBookmark = src.ITunesBookmark
	}
	if dst.CoverURL == nil && src.CoverURL != nil {
		dst.CoverURL = src.CoverURL
	}
	if dst.OpenLibraryID == nil && src.OpenLibraryID != nil {
		dst.OpenLibraryID = src.OpenLibraryID
	}
	if dst.GoogleBooksID == nil && src.GoogleBooksID != nil {
		dst.GoogleBooksID = src.GoogleBooksID
	}
	if dst.HardcoverID == nil && src.HardcoverID != nil {
		dst.HardcoverID = src.HardcoverID
	}
	if dst.WorkID == nil && src.WorkID != nil {
		dst.WorkID = src.WorkID
	}
	if (dst.VersionGroupID == nil || *dst.VersionGroupID == "") && src.VersionGroupID != nil && *src.VersionGroupID != "" {
		dst.VersionGroupID = src.VersionGroupID
	}
}

// ddSoftDeleteBook marks bookID for deletion under its per-book lock. With
// clearPath it also clears FilePath (merge's softDeleteAbsorbed rule): a book
// whose files went to a keeper must not keep naming the keeper's path, or
// PurgeSoftDeletedBooks with delete-files removes the keeper's audio. Phase 1
// clears it on every junk row it retires.
//
// A retired book is also demoted from primary, so no reader counts a deleted
// row as its group's primary; the caller has already promoted a successor.
//
// guard, when non-nil, runs first on the stored row under the book's lock; an
// error from it aborts with nothing written (see ddRetireGuard).
func ddSoftDeleteBook(store bookSoftDeleter, bookID string, clearPath bool, guard func(*database.Book) error) error {
	_, err := store.ModifyBook(bookID, func(current *database.Book) error {
		if guard != nil {
			if err := guard(current); err != nil {
				return err
			}
		}
		t := true
		now := time.Now()
		current.MarkedForDeletion = &t
		current.MarkedForDeletionAt = &now
		if clearPath {
			current.FilePath = ""
		}
		if current.VersionGroupID != nil && *current.VersionGroupID != "" && ddCountsAsPrimary(current) {
			f := false
			current.IsPrimaryVersion = &f
		}
		return nil
	})
	if err != nil {
		// No hard-delete fallback: see merge.SoftDeleteBook. A failed
		// update means the store is unhealthy; deleting the row on the same
		// store is the one outcome a soft-delete exists to prevent.
		return fmt.Errorf("soft-delete %s: %w", bookID, err)
	}
	return nil
}

var ddNonAlphanumRE = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)

func ddNormalizeDedupTitle(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "(unabridged)", "")
	// Strip leading chapter markers but require an explicit delimiter (dot, dash,
	// colon) with surrounding whitespace — NOT a bare "001 Title" where a space
	// alone follows the number. "001 Title" could be a real title (series position,
	// box-set index); only "001 - Title", "001. Title", "03: Title", or "(76/85)
	// Title" are unambiguous chapter markers.
	reLeadNum := regexp.MustCompile(`^\s*(\(\s*\d+\s*[/\-]\s*\d+\s*\)\s+|\d+\s*[\.\-:]\s+)`)
	s = reLeadNum.ReplaceAllString(s, "")
	s = ddNonAlphanumRE.ReplaceAllString(s, " ")
	fields := strings.FieldsFunc(s, unicode.IsSpace)
	return strings.Join(fields, " ")
}

func ddFilterLive(books []database.Book, deletedIDs map[string]bool) []database.Book {
	out := books[:0:len(books)]
	for _, b := range books {
		if !deletedIDs[b.ID] {
			out = append(out, b)
		}
	}
	return out
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *dedupBooksJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
