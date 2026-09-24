// file: internal/reconcile/elect_primaries.go
// version: 2.0.0
// guid: 25e1f705-9130-4eb0-bd4b-04d45908c704
// last-edited: 2026-09-24

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"golang.org/x/sync/errgroup"
)

// ElectedPrimarySample records one election for the dry-run preview so an
// operator can eyeball what the apply would do before authorising it.
type ElectedPrimarySample struct {
	VersionGroupID string `json:"version_group_id"`
	BookID         string `json:"book_id"`
	Title          string `json:"title"`
	GroupMembers   int    `json:"group_members"`
}

// HeldPrimarySample records one group the rule held back, with its reason,
// so the operator can see why a primary-less group stays that way.
type HeldPrimarySample struct {
	VersionGroupID string `json:"version_group_id"`
	HoldReason     string `json:"hold_reason"`
	Reason         string `json:"reason"`
	GroupMembers   int    `json:"group_members"`
}

// ElectStore is ElectMissingPrimaries' store: the package Store plus the
// chapter table, which versionprimary reads when ffprobe cannot. Kept off
// Store itself so the other reconcile passes, and the maintenance OpsStore
// that feeds them, do not grow a method they never call.
type ElectStore interface {
	Store
	database.ChapterReader
}

// ElectEnv is what the election needs beyond the store: the library root
// (config.AppConfig.RootDir) and the chapter prober. A nil Probe makes every
// chapter count come from the chapter table, or "unknown".
type ElectEnv struct {
	RootDir string
	Probe   versionprimary.ChapterProber
}

// ElectPrimaryResult summarises an ElectMissingPrimaries run.
type ElectPrimaryResult struct {
	DryRun        bool `json:"dry_run"`
	TotalChecked  int  `json:"total_checked"`
	GroupsScanned int  `json:"groups_scanned"`
	// BooksWithoutGroup counts books carrying no VersionGroupID at all.
	// Those are AssignOrphanVGs' responsibility, not this pass's, and are
	// only reported here so the two numbers can be reconciled.
	BooksWithoutGroup int `json:"books_without_group"`
	// GroupsWithoutPrimary is how many groups the scan found electing no
	// primary, split into the two shapes below.
	GroupsWithoutPrimary int `json:"groups_without_primary"`
	SingletonGroups      int `json:"singleton_groups"`
	MultiMemberGroups    int `json:"multi_member_groups"`
	BooksTrapped         int `json:"books_trapped"`
	// Elected counts groups this run actually wrote a primary for (always 0
	// when DryRun).
	Elected int `json:"elected"`
	// SkippedConcurrent counts groups that had gained a primary between the
	// initial scan and the per-group re-read — another worker, a regroup
	// apply, or a merge got there first. Left untouched deliberately.
	SkippedConcurrent int `json:"skipped_concurrent"`
	// SkippedVanished counts groups whose members could no longer be read
	// (deleted or re-grouped mid-run).
	SkippedVanished int `json:"skipped_vanished"`
	// SkippedNoEligible counts groups whose every live member was absorbed
	// by a merge (MergedIntoBookID set) or soft-deleted. Such a group is
	// left with no primary on purpose: its work is represented by the merge
	// survivor, and crowning a loser lists the same work twice.
	SkippedNoEligible int `json:"skipped_no_eligible"`
	// GroupsNoEligible counts groups the scan found with no primary and no
	// electable member. They are not candidates and not in
	// GroupsWithoutPrimary.
	GroupsNoEligible int `json:"groups_no_eligible"`
	// SkippedWinnerChanged counts groups whose chosen winner was merged
	// away or trashed between the group read and the write.
	SkippedWinnerChanged int `json:"skipped_winner_changed"`
	// GroupsHeld counts candidate groups versionprimary.Elect refused to
	// crown (2026-09-24): no member ABS can show
	// (HeldNeedsOrganizeOrRestore), or a copy outside the library with
	// better content than every library copy (HeldBetterCopyNotInLibrary).
	// A held group is never written; the owner decides it.
	GroupsHeld                 int                 `json:"groups_held"`
	HeldNeedsOrganizeOrRestore int                 `json:"held_needs_organize_or_restore"`
	HeldBetterCopyNotInLibrary int                 `json:"held_better_copy_not_in_library"`
	HeldSamples                []HeldPrimarySample `json:"held_samples,omitempty"`
	// GroupsExcluded counts candidate groups the operator's exclude list held
	// back. They ARE still counted in GroupsWithoutPrimary, BooksTrapped and
	// the singleton/multi split — the scan really did find them electing no
	// primary, and an operator diffing this run against an earlier unfiltered
	// dry run must see the same flagged total. The run reconciles as
	// GroupsWithoutPrimary − GroupsExcluded − SkippedConcurrent −
	// SkippedVanished − SkippedNoEligible − SkippedWinnerChanged −
	// GroupsHeld − Errors = Elected.
	GroupsExcluded int `json:"groups_excluded"`
	// BooksExcluded counts the members of those groups: the books this run
	// deliberately leaves invisible. They are counted INSIDE BooksTrapped for
	// the same reason GroupsExcluded is counted inside GroupsWithoutPrimary,
	// so the books an apply actually frees is BooksTrapped − BooksExcluded.
	// Read BooksTrapped alone on a filtered run and you overstate the repair.
	BooksExcluded int `json:"books_excluded"`
	// ExcludedApplied lists the exclude-list ids that actually held a
	// candidate group back.
	ExcludedApplied []string `json:"excluded_applied,omitempty"`
	// ExcludedNotCandidate lists exclude-list ids naming a real group that
	// was not a candidate anyway (it already elects a primary, or has no
	// electable member). Nothing was held for them.
	ExcludedNotCandidate []string `json:"excluded_not_candidate,omitempty"`
	// ExcludedUnmatched lists exclude-list ids naming no group in this scan
	// at all — a typo, or a group since re-split. Reported and logged rather
	// than failing the run: a stale id must not block an otherwise correct
	// apply, but it must never pass unnoticed either, because "held it back"
	// and "matched nothing" look identical from the outside.
	ExcludedUnmatched []string               `json:"excluded_unmatched,omitempty"`
	Errors            int                    `json:"errors"`
	Samples           []ElectedPrimarySample `json:"samples,omitempty"`
}

// normalizeExcludedGroups turns an operator-supplied list into a lookup set:
// trimmed, empties dropped, deduped. Ids are matched EXACTLY — a version
// group id is either an uppercase ULID or a lowercase "vg-<hex>", so a
// case-flipped paste must surface as unmatched rather than silently failing
// to protect the group it names.
func normalizeExcludedGroups(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// sortedGroupIDs renders a set as a stable list for the result payload.
func sortedGroupIDs(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// maxElectionSamples bounds the dry-run preview payload. The full counts are
// always exact; only the illustrative sample list is capped.
const maxElectionSamples = 50

// electPrimaryFor decides a primary-less group with versionprimary.Elect,
// the one rule every primary choice shares (2026-09-24).
//
// Until 2026-09-24 this crowned the earliest-created electable member. In the
// common organize-collision pair that is the organized_source copy, which ABS
// does not list, so running the repair kept those books hidden. The rule now
// crowns only a member ABS can show (live, organized, every active book_file
// present under the library root), ranked by content (m4b with chapters >
// m4b without > other formats), then metadata, then runtime, bitrate and
// creation order; and it HOLDS a group whose better copy exists only outside
// the library rather than crowning the worse one.
//
// Only an electable member (electable) can win: a merge loser is usually the
// OLDEST record in its group, and the 09-19 census found 302 merged books
// flagged primary.
func electPrimaryFor(ctx context.Context, loader versionprimary.Loader, members []database.Book, alive survivorAlive) (versionprimary.Decision, error) {
	ms, err := loader.LoadMembers(ctx, members, alive)
	if err != nil {
		return versionprimary.Decision{}, err
	}
	return versionprimary.Elect(ms), nil
}

// survivorAlive reports whether a merge target still exists and is not
// soft-deleted.
type survivorAlive = func(id string) bool

// electable reports whether b may be (or count as) its group's primary. The
// rule lives in versionprimary.Electable.
func electable(b *database.Book, alive survivorAlive) bool {
	return versionprimary.Electable(b, alive)
}

func electableRow(softDeleted bool, mergedInto *string, alive survivorAlive) bool {
	return versionprimary.ElectableRow(softDeleted, mergedInto, alive)
}

// countsAsPrimary is the primary test the pass uses everywhere: a flagged
// member that is not electable (a merge loser with a live survivor, or
// trashed) does not give its group a primary. The census found 302 merge
// losers flagged primary; counting them left their groups unrepaired.
func countsAsPrimary(primary *bool, softDeleted bool, mergedInto *string, alive survivorAlive) bool {
	return primary != nil && *primary && electableRow(softDeleted, mergedInto, alive)
}

// storeSurvivorAlive checks a merge target with a point read. A read error
// counts as alive: the loser then stays ineligible, which leaves the group as
// it is rather than crowning a book that may still be merged away.
func storeSurvivorAlive(store Store) survivorAlive {
	return func(id string) bool {
		b, err := store.GetBookByID(id)
		if err != nil {
			return true
		}
		return b != nil && !b.IsSoftDeleted()
	}
}

func sameMergeTarget(a, b *string) bool {
	av, bv := "", ""
	if a != nil {
		av = *a
	}
	if b != nil {
		bv = *b
	}
	return av == bv
}

// errNotElectable aborts a ModifyBook whose re-read row stopped being
// electable (merged or soft-deleted) after the group was read.
var errNotElectable = errors.New("elect-missing-primaries: winner no longer electable")

// ElectMissingPrimaries repairs the data invariant "every version group elects
// exactly one primary" for the zero-primary half of that invariant.
//
// Why this exists: the iTunes importer used to mint a fresh version group for
// each newly-created book and then mark that book NON-primary (importer.go,
// fixed 2026-08-13). A group whose only member is not primary can never
// satisfy the web UI's default is_primary_version=true filter, so those books
// became invisible in the browser while remaining perfectly visible to API
// clients that apply no such filter.
//
// This is deliberately a SIBLING of AssignOrphanVGs rather than an extension
// of it. AssignOrphanVGs handles books with no group at all and force-sets
// LibraryState to "organized"; doing that here would clobber the deliberate
// import state the iTunes importer assigns, so this pass touches
// IsPrimaryVersion and nothing else.
//
// Concurrency: whole-library scan with per-group DB work, so per CLAUDE.md it
// runs on a bounded worker pool sized to runtime.NumCPU(). Work is partitioned
// by version group — each group is handled by exactly one worker — so no two
// workers can ever write competing primaries into the same group.
//
// excludeGroups names version groups the election must leave alone, and is
// the operator's only lever: a group named here is counted in the flagged
// totals, reported by id, and never written. It exists because the
// 2026-09-19 census found 15 groups whose "versions" are the CHAPTER FILES
// of one book (.claude/notes/elect-primaries-dryrun-and-band-census-2026-09-19.md
// §4), where crowning a member makes a chapter the book; with no way to hold
// those back, the whole 1,140-group apply was blocked on 15 rows.
//
// Why there is no automatic "this group looks like chapters" refusal here,
// although internal/scanner/version_link.go has exactly that predicate
// (versionLinkIsPart, built on ParseSequenceMarker / ParseFilenameSequence):
//
//  1. It does not cover the cases. Five of the twelve tier-1 chapter groups
//     are folder-shaped — their rows' file paths are extensionless directory
//     names ("Logan Jacobs", "Immortal Mana", "Master of Formalities
//     (Unabridged)") with nothing positional in them, so the predicate reads
//     them as whole books and would elect in them anyway. A predicate that
//     holds 10 of 15 still needs the list for the other 5, and then the list
//     is what is doing the work.
//  2. It fires on ordinary books. config.DefaultFileNamingPattern is
//     "{title} - {track:02d}", and seqStemTrailingRe (chapter_sequence.go)
//     matches exactly that stem, so EVERY organized single-file book reads as
//     a positional part.
//  3. The error asymmetry is inverted between the two callers. For the
//     scanner, refusing to link is the cheap error: the book stays ungrouped
//     and is still its own primary. For this repair, refusing to elect is the
//     expensive error: the group keeps no primary, and every member stays
//     hidden from the library list and from bulk metadata fetch — the exact
//     harm this pass exists to undo. A gate whose false positives are
//     permanent invisibility does not belong on the repair path.
//
// So the shape judgement stays where the evidence is (a census an operator
// reads) and this pass takes the answer as data. If the scanner fix re-splits
// those groups, the ids simply stop matching and are reported as unmatched.
//
// Clobber guard: the initial scan and the per-group write are not atomic, so
// each worker re-reads the group's live membership immediately before writing.
// If a primary has appeared in the meantime the worker skips the group instead
// of creating a second one. This is also what makes the singleton-vs-multi
// distinction trustworthy: membership is confirmed against the live index, not
// inferred from the snapshot.
func ElectMissingPrimaries(store ElectStore, dryRun bool, excludeGroups []string, env ElectEnv) (*ElectPrimaryResult, error) {
	result := &ElectPrimaryResult{DryRun: dryRun}
	excluded := normalizeExcludedGroups(excludeGroups)
	excludedSeen := make(map[string]bool, len(excluded))
	excludedHeld := make(map[string]bool, len(excluded))

	// One-call snapshot enumeration — see loadAllBooksCore in reconcile.go for
	// why offset pages over the async memdb snapshot silently skip rows.
	allBooks, err := loadAllBooksCore(store)
	if err != nil {
		return nil, fmt.Errorf("failed to get books: %w", err)
	}
	result.TotalChecked = len(allBooks)

	// Serial in-memory pass: bucket books by group and count primaries.
	type groupState struct {
		members   int
		electable int
		primaries int
	}
	// Survivor liveness from the same snapshot: a target absent from it, or
	// trashed, is not alive.
	liveIDs := make(map[string]bool, len(allBooks))
	for i := range allBooks {
		liveIDs[allBooks[i].ID] = !allBooks[i].IsSoftDeleted()
	}
	snapAlive := func(id string) bool { return liveIDs[id] }
	groups := make(map[string]*groupState)
	for i := range allBooks {
		b := &allBooks[i]
		if b.VersionGroupID == nil || *b.VersionGroupID == "" {
			result.BooksWithoutGroup++
			continue
		}
		gs := groups[*b.VersionGroupID]
		if gs == nil {
			gs = &groupState{}
			groups[*b.VersionGroupID] = gs
		}
		gs.members++
		if electableRow(b.IsSoftDeleted(), b.MergedIntoBookID, snapAlive) {
			gs.electable++
		}
		if countsAsPrimary(b.IsPrimaryVersion, b.IsSoftDeleted(), b.MergedIntoBookID, snapAlive) {
			gs.primaries++
		}
	}
	result.GroupsScanned = len(groups)

	var candidates []string
	for gid, gs := range groups {
		if excluded[gid] {
			// Seen means "this id names a real group", whatever the group's
			// state. Recorded before the classification below so an id that
			// names a non-candidate group is reported as inert rather than as
			// unknown — the two call for different operator action.
			excludedSeen[gid] = true
		}
		if gs.primaries > 0 {
			continue
		}
		if gs.electable == 0 {
			// Nothing to elect: every member is merged away or trashed.
			// Reported, not a candidate, so "needs repair" can reach zero.
			result.GroupsNoEligible++
			continue
		}
		result.GroupsWithoutPrimary++
		result.BooksTrapped += gs.members
		if gs.members == 1 {
			result.SingletonGroups++
		} else {
			result.MultiMemberGroups++
		}
		if excluded[gid] {
			// Held back by the operator: counted in the flagged totals above
			// (the scan did find it primary-less) but never queued for a
			// write, and kept out of the samples so the preview shows only
			// what would actually change.
			excludedHeld[gid] = true
			result.GroupsExcluded++
			result.BooksExcluded += gs.members
			continue
		}
		candidates = append(candidates, gid)
	}
	// Deterministic order so dry-run samples are stable across runs.
	sort.Strings(candidates)

	result.ExcludedApplied = sortedGroupIDs(excludedHeld)
	for gid := range excluded {
		switch {
		case excludedHeld[gid]:
		case excludedSeen[gid]:
			result.ExcludedNotCandidate = append(result.ExcludedNotCandidate, gid)
		default:
			result.ExcludedUnmatched = append(result.ExcludedUnmatched, gid)
		}
	}
	sort.Strings(result.ExcludedNotCandidate)
	sort.Strings(result.ExcludedUnmatched)
	if len(result.ExcludedUnmatched) > 0 {
		// Logged as well as returned: the result payload is read once, the
		// service log is what gets grepped when someone later asks whether a
		// given group was really held back. Through pkgLog with sanitized
		// ids, not slog: these strings come straight off the query string
		// (internal/logger's slog guard).
		safe := make([]string, 0, len(result.ExcludedUnmatched))
		for _, gid := range result.ExcludedUnmatched {
			safe = append(safe, logger.SanitizeLogValue(gid))
		}
		pkgLog.Warn("elect-missing-primaries: exclude list names %d group(s) this scan did not see (%s); %d matched",
			len(safe), strings.Join(safe, ", "), len(excludedSeen))
	}

	if len(candidates) == 0 {
		slog.Info("elect-missing-primaries: nothing to repair",
			"total_checked", result.TotalChecked, "groups_scanned", result.GroupsScanned)
		return result, nil
	}

	var (
		elected, skippedConcurrent, skippedVanished, skippedNoEligible, skippedWinnerChanged, errCount, processed int64
		heldNeedsOrganize, heldBetterCopy                                                                         int64
		sampleMu                                                                                                  sync.Mutex
	)

	alive := storeSurvivorAlive(store)
	loader := versionprimary.Loader{Files: store, Chapters: store, RootDir: env.RootDir, Probe: env.Probe}
	ctx := context.Background()
	var g errgroup.Group
	g.SetLimit(max(runtime.NumCPU(), 1))
	for _, gid := range candidates {
		g.Go(func() error {
			defer func() {
				if n := atomic.AddInt64(&processed, 1); n%500 == 0 {
					slog.Info("elect-missing-primaries progress", "processed", n, "total", len(candidates))
				}
			}()

			// Re-read live membership: this both refreshes the clobber guard
			// and confirms the member count we are about to act on.
			members, err := store.GetBooksByVersionGroup(gid)
			if err != nil {
				slog.Warn("elect-missing-primaries failed to read group", "group", gid, "err", err)
				atomic.AddInt64(&errCount, 1)
				return nil
			}
			if len(members) == 0 {
				atomic.AddInt64(&skippedVanished, 1)
				return nil
			}
			for i := range members {
				m := &members[i]
				if countsAsPrimary(m.IsPrimaryVersion, m.IsSoftDeleted(), m.MergedIntoBookID, alive) {
					atomic.AddInt64(&skippedConcurrent, 1)
					return nil
				}
			}

			anyElectable := false
			for i := range members {
				if electable(&members[i], alive) {
					anyElectable = true
					break
				}
			}
			if !anyElectable {
				atomic.AddInt64(&skippedNoEligible, 1)
				return nil
			}
			decision, err := electPrimaryFor(ctx, loader, members, alive)
			if err != nil {
				slog.Warn("elect-missing-primaries failed to read member signals", "group", gid, "err", err)
				atomic.AddInt64(&errCount, 1)
				return nil
			}
			if decision.Kind == versionprimary.DecisionHeld {
				if decision.HoldReason == versionprimary.HoldBetterCopyNotInLibrary {
					atomic.AddInt64(&heldBetterCopy, 1)
				} else {
					atomic.AddInt64(&heldNeedsOrganize, 1)
				}
				sampleMu.Lock()
				if len(result.HeldSamples) < maxElectionSamples {
					result.HeldSamples = append(result.HeldSamples, HeldPrimarySample{
						VersionGroupID: gid,
						HoldReason:     decision.HoldReason,
						Reason:         decision.Reason,
						GroupMembers:   len(members),
					})
				}
				sampleMu.Unlock()
				return nil
			}
			var winner *database.Book
			for i := range members {
				if members[i].ID == decision.WinnerID {
					winner = &members[i]
				}
			}
			if winner == nil {
				slog.Warn("elect-missing-primaries: decided winner is not a group member", "group", gid, "winner", decision.WinnerID)
				atomic.AddInt64(&errCount, 1)
				return nil
			}

			sampleMu.Lock()
			if len(result.Samples) < maxElectionSamples {
				result.Samples = append(result.Samples, ElectedPrimarySample{
					VersionGroupID: gid,
					BookID:         winner.ID,
					Title:          winner.Title,
					GroupMembers:   len(members),
				})
			}
			sampleMu.Unlock()

			if dryRun {
				return nil
			}

			// Write through ModifyBook: it re-reads the full row (the group
			// listing may be a slim projection) under the book's write lock
			// and sets only IsPrimaryVersion, so a column another writer
			// commits meanwhile is not reverted (audit A1#15).
			// LibraryState is intentionally left alone — see doc comment.
			isPrimary := true
			observedMerge := winner.MergedIntoBookID
			written, err := store.ModifyBook(winner.ID, func(full *database.Book) error {
				// Re-checked on the locked re-read: a merge that absorbed
				// the winner (or a trash) after the group read must not be
				// undone here. Liveness of a survivor was decided above; any
				// change to the merge target since then aborts, because
				// the callback must not read other books under this lock.
				if full.IsSoftDeleted() || !sameMergeTarget(full.MergedIntoBookID, observedMerge) {
					return errNotElectable
				}
				full.IsPrimaryVersion = &isPrimary
				return nil
			})
			if errors.Is(err, errNotElectable) {
				atomic.AddInt64(&skippedWinnerChanged, 1)
				return nil
			}
			if err != nil {
				slog.Warn("elect-missing-primaries write failed", "book", winner.ID, "err", err)
				atomic.AddInt64(&errCount, 1)
				return nil
			}
			if written == nil {
				slog.Warn("elect-missing-primaries winner vanished before write", "book", winner.ID)
				atomic.AddInt64(&errCount, 1)
				return nil
			}
			atomic.AddInt64(&elected, 1)
			return nil
		})
	}
	_ = g.Wait() // per-group errors are counted, not fatal to the whole run

	result.Elected = int(elected)
	result.SkippedConcurrent = int(skippedConcurrent)
	result.SkippedVanished = int(skippedVanished)
	result.SkippedNoEligible = int(skippedNoEligible)
	result.SkippedWinnerChanged = int(skippedWinnerChanged)
	result.HeldNeedsOrganizeOrRestore = int(heldNeedsOrganize)
	result.HeldBetterCopyNotInLibrary = int(heldBetterCopy)
	result.GroupsHeld = result.HeldNeedsOrganizeOrRestore + result.HeldBetterCopyNotInLibrary
	result.Errors = int(errCount)

	// Keep samples deterministic regardless of worker completion order.
	sort.Slice(result.Samples, func(i, j int) bool {
		return result.Samples[i].VersionGroupID < result.Samples[j].VersionGroupID
	})
	sort.Slice(result.HeldSamples, func(i, j int) bool {
		return result.HeldSamples[i].VersionGroupID < result.HeldSamples[j].VersionGroupID
	})

	slog.Info("elect-missing-primaries summary",
		"dry_run", dryRun,
		"total_checked", result.TotalChecked,
		"groups_scanned", result.GroupsScanned,
		"groups_without_primary", result.GroupsWithoutPrimary,
		"singleton_groups", result.SingletonGroups,
		"multi_member_groups", result.MultiMemberGroups,
		"books_trapped", result.BooksTrapped,
		"elected", result.Elected,
		"skipped_concurrent", result.SkippedConcurrent,
		"skipped_vanished", result.SkippedVanished,
		"skipped_no_eligible", result.SkippedNoEligible,
		"skipped_winner_changed", result.SkippedWinnerChanged,
		"groups_held", result.GroupsHeld,
		"held_needs_organize_or_restore", result.HeldNeedsOrganizeOrRestore,
		"held_better_copy_not_in_library", result.HeldBetterCopyNotInLibrary,
		"groups_no_eligible", result.GroupsNoEligible,
		"groups_excluded", result.GroupsExcluded,
		"books_excluded", result.BooksExcluded,
		"excluded_unmatched", len(result.ExcludedUnmatched),
		"errors", result.Errors,
	)

	return result, nil
}
