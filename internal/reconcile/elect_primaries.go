// file: internal/reconcile/elect_primaries.go
// version: 1.0.0
// guid: 25e1f705-9130-4eb0-bd4b-04d45908c704
// last-edited: 2026-08-13

package reconcile

import (
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"

	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
	SkippedVanished int                    `json:"skipped_vanished"`
	Errors          int                    `json:"errors"`
	Samples         []ElectedPrimarySample `json:"samples,omitempty"`
}

// maxElectionSamples bounds the dry-run preview payload. The full counts are
// always exact; only the illustrative sample list is capped.
const maxElectionSamples = 50

// electPrimaryFor picks which member of a primary-less group becomes primary.
//
// Rule: the earliest-created member wins, tie-broken by book ID so the choice
// is deterministic and a re-run converges on the same answer. Earliest-created
// is the original import — the row other records are most likely to already
// reference. This deliberately does NOT try to pick the "best quality" copy;
// that is a separate concern and re-electing later is a cheap, safe operation,
// whereas leaving the group with no primary keeps the book invisible.
func electPrimaryFor(members []database.Book) *database.Book {
	if len(members) == 0 {
		return nil
	}
	sorted := make([]*database.Book, len(members))
	for i := range members {
		sorted[i] = &members[i]
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
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
	return sorted[0]
}

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
// Clobber guard: the initial scan and the per-group write are not atomic, so
// each worker re-reads the group's live membership immediately before writing.
// If a primary has appeared in the meantime the worker skips the group instead
// of creating a second one. This is also what makes the singleton-vs-multi
// distinction trustworthy: membership is confirmed against the live index, not
// inferred from the snapshot.
func ElectMissingPrimaries(store Store, dryRun bool) (*ElectPrimaryResult, error) {
	result := &ElectPrimaryResult{DryRun: dryRun}

	var allBooks []database.BookCore
	const pageSize = 5000
	for offset := 0; ; offset += pageSize {
		books, err := store.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("failed to get books: %w", err)
		}
		allBooks = append(allBooks, books...)
		if len(books) < pageSize {
			break
		}
	}
	result.TotalChecked = len(allBooks)

	// Serial in-memory pass: bucket books by group and count primaries.
	type groupState struct {
		members   int
		primaries int
	}
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
		if b.IsPrimaryVersion != nil && *b.IsPrimaryVersion {
			gs.primaries++
		}
	}
	result.GroupsScanned = len(groups)

	var candidates []string
	for gid, gs := range groups {
		if gs.primaries > 0 {
			continue
		}
		candidates = append(candidates, gid)
		result.GroupsWithoutPrimary++
		result.BooksTrapped += gs.members
		if gs.members == 1 {
			result.SingletonGroups++
		} else {
			result.MultiMemberGroups++
		}
	}
	// Deterministic order so dry-run samples are stable across runs.
	sort.Strings(candidates)

	if len(candidates) == 0 {
		slog.Info("elect-missing-primaries: nothing to repair",
			"total_checked", result.TotalChecked, "groups_scanned", result.GroupsScanned)
		return result, nil
	}

	var (
		elected, skippedConcurrent, skippedVanished, errCount, processed int64
		sampleMu                                                        sync.Mutex
	)

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
				if members[i].IsPrimaryVersion != nil && *members[i].IsPrimaryVersion {
					atomic.AddInt64(&skippedConcurrent, 1)
					return nil
				}
			}

			winner := electPrimaryFor(members)
			if winner == nil {
				atomic.AddInt64(&skippedVanished, 1)
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

			// Hydrate the full row before write-back. GetBooksByVersionGroup
			// may serve a slim projection; GetBookByID is a direct book:<id>
			// point-get and is full fidelity.
			full, herr := store.GetBookByID(winner.ID)
			if herr != nil || full == nil {
				slog.Warn("elect-missing-primaries failed to hydrate winner", "book", winner.ID, "err", herr)
				atomic.AddInt64(&errCount, 1)
				return nil
			}
			isPrimary := true
			full.IsPrimaryVersion = &isPrimary
			// LibraryState is intentionally left alone — see doc comment.

			if _, err := store.UpdateBook(full.ID, full); err != nil {
				slog.Warn("elect-missing-primaries write failed", "book", full.ID, "err", err)
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
	result.Errors = int(errCount)

	// Keep samples deterministic regardless of worker completion order.
	sort.Slice(result.Samples, func(i, j int) bool {
		return result.Samples[i].VersionGroupID < result.Samples[j].VersionGroupID
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
		"errors", result.Errors,
	)

	return result, nil
}
