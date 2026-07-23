// file: internal/itunes/pid_repair.go
// version: 1.0.0
// guid: 5a9d3c62-7e14-4b80-9f26-1c8b0a3e6d47
// last-edited: 2026-07-23
//
// Backfill repair for duplicate book_file iTunes PIDs (the ~8,987 the census
// found). A PID must identify exactly one book_file; where several rows share one
// (version-split copies), this keeps the PID on ONE canonical row and CLEARS it
// from the rest. It NEVER deletes a row or touches an audio file — only the
// itunes_persistent_id / itunes_path DB fields move — so it cannot lose data.
//
// Keep policy (fail-safe: only clears when the keeper is unambiguous):
//   - same_file (all owners share FilePath): keep a live primary (tie-break lowest
//     file ID). Any owner is equivalent for the ITL — same location — so the track
//     link is unaffected regardless of which is kept.
//   - diff_file (owners point at different files): keep the owner whose canonical
//     location equals the live ITL track's location for that PID, so the track stays
//     linked to the right file. If the ITL doesn't disambiguate (no owner matches,
//     or more than one does), the PID is left UNTOUCHED for review — never guessed.
//
// After a full repair every PID has one owner, which also makes the relocate path
// deterministic (removes the order-dependent first-wins hazard the census counted
// as pids_on_multiple_primaries_diff_path). Apply is dry-run-gated. See
// docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md.

package itunes

import (
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// PIDRepairStore is the read+write slice of the store the repair needs.
type PIDRepairStore interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookByID(id string) (*database.Book, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// pidClear is one row that will lose the PID (its full row, so UpdateBookFile
// preserves every other field incl. the raw fingerprint).
type pidClear struct {
	file database.BookFile
}

// pidRepairGroup is a resolved duplicate PID: exactly one keeper, ≥1 losers.
type pidRepairGroup struct {
	pid    string
	keep   database.BookFile
	losers []pidClear
}

// PIDRepairSample surfaces one resolved group for operator review.
type PIDRepairSample struct {
	PID          string   `json:"pid"`
	Class        string   `json:"class"` // same_file | diff_file
	KeepFileID   string   `json:"keep_file_id"`
	KeepPath     string   `json:"keep_path"`
	ClearedPaths []string `json:"cleared_paths"`
}

// PIDRepairPreview summarizes the repair without applying it.
type PIDRepairPreview struct {
	DuplicatePIDs   int `json:"duplicate_pids"`
	SameFileGroups  int `json:"same_file_groups"`
	DiffFileGroups  int `json:"diff_file_groups"`
	AmbiguousGroups int `json:"ambiguous_groups"` // diff_file the ITL can't disambiguate → left for review
	FilesToClear    int `json:"files_to_clear"`

	Sample []PIDRepairSample `json:"sample"`
}

const pidRepairSampleLimit = 50

// ComputePIDRepairPlan builds the repair groups (read-only). itlPath drives the
// diff_file keep-by-location decision; mappings canonicalize a FilePath to the ITL
// 0x0D form for comparison against the track location.
func ComputePIDRepairPlan(store PIDRepairStore, itlPath string, mappings []PathMapping) ([]pidRepairGroup, *PIDRepairPreview, error) {
	// ITL track location by upper-hex PID (for diff_file disambiguation).
	trackLoc := map[string]string{}
	if itlPath != "" {
		lib, err := ParseITL(itlPath)
		if err != nil {
			return nil, nil, fmt.Errorf("parse ITL: %w", err)
		}
		for i := range lib.Tracks {
			trackLoc[strings.ToUpper(pidToHex(lib.Tracks[i].PersistentID))] = lib.Tracks[i].Location
		}
	}

	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, nil, fmt.Errorf("scan book_files: %w", err)
	}
	byPID := map[string][]database.BookFileCore{}
	for i := range cores {
		pid := strings.ToUpper(strings.TrimSpace(cores[i].ITunesPersistentID))
		if pid != "" {
			byPID[pid] = append(byPID[pid], cores[i])
		}
	}

	preview := &PIDRepairPreview{}
	var groups []pidRepairGroup
	bookCache := map[string]*database.Book{}

	// Deterministic PID order so the sample + apply are stable across runs.
	pids := make([]string, 0, len(byPID))
	for pid, owners := range byPID {
		if len(owners) > 1 {
			pids = append(pids, pid)
		}
	}
	sort.Strings(pids)

	for _, pid := range pids {
		owners := byPID[pid]
		preview.DuplicatePIDs++

		sameFile := true
		for i := 1; i < len(owners); i++ {
			if owners[i].FilePath != owners[0].FilePath {
				sameFile = false
				break
			}
		}

		// Load full rows (fingerprint-preserving) + book status.
		full := make([]database.BookFile, 0, len(owners))
		for i := range owners {
			bf := fetchFullBookFile(store, owners[i])
			if bf == nil {
				continue
			}
			full = append(full, *bf)
		}
		if len(full) < 2 {
			continue // couldn't resolve rows → skip (fail-safe)
		}

		var keepIdx int
		if sameFile {
			keepIdx = pickSameFileKeeper(store, full, bookCache)
			preview.SameFileGroups++
		} else {
			preview.DiffFileGroups++
			idx, ok := pickDiffFileKeeper(full, trackLoc[pid], mappings)
			if !ok {
				preview.AmbiguousGroups++ // ITL can't disambiguate → leave untouched
				continue
			}
			keepIdx = idx
		}

		g := pidRepairGroup{pid: pid, keep: full[keepIdx]}
		for i := range full {
			if i != keepIdx {
				g.losers = append(g.losers, pidClear{file: full[i]})
			}
		}
		preview.FilesToClear += len(g.losers)
		groups = append(groups, g)

		if len(preview.Sample) < pidRepairSampleLimit {
			cls := "diff_file"
			if sameFile {
				cls = "same_file"
			}
			cleared := make([]string, 0, len(g.losers))
			for _, l := range g.losers {
				cleared = append(cleared, l.file.FilePath)
			}
			preview.Sample = append(preview.Sample, PIDRepairSample{
				PID: pid, Class: cls, KeepFileID: g.keep.ID, KeepPath: g.keep.FilePath, ClearedPaths: cleared,
			})
		}
	}

	slog.Info("itunes pid-repair: computed plan",
		"duplicatePIDs", preview.DuplicatePIDs, "sameFile", preview.SameFileGroups,
		"diffFile", preview.DiffFileGroups, "ambiguous", preview.AmbiguousGroups,
		"filesToClear", preview.FilesToClear)
	return groups, preview, nil
}

// fetchFullBookFile loads the full (non-stripped) BookFile matching a core row.
func fetchFullBookFile(store PIDRepairStore, core database.BookFileCore) *database.BookFile {
	files, err := store.GetBookFiles(core.BookID)
	if err != nil {
		return nil
	}
	for i := range files {
		if files[i].ID == core.ID {
			return &files[i]
		}
	}
	return nil
}

// pickSameFileKeeper prefers a live primary owner; tie-break lowest file ID. All
// owners share the path, so the ITL track link is identical whichever is kept.
func pickSameFileKeeper(store PIDRepairStore, owners []database.BookFile, cache map[string]*database.Book) int {
	best := -1
	for i := range owners {
		if isLivePrimary(store, owners[i].BookID, cache) {
			if best == -1 || owners[i].ID < owners[best].ID {
				best = i
			}
		}
	}
	if best != -1 {
		return best
	}
	// No live primary → lowest file ID among all owners.
	best = 0
	for i := range owners {
		if owners[i].ID < owners[best].ID {
			best = i
		}
	}
	return best
}

// pickDiffFileKeeper returns the index of the owner whose canonical location equals
// the ITL track location. ok=false when the ITL doesn't disambiguate (no match, or
// >1 match) — the caller then leaves the PID untouched for review.
func pickDiffFileKeeper(owners []database.BookFile, trackLocation string, mappings []PathMapping) (int, bool) {
	if trackLocation == "" {
		return 0, false
	}
	match := -1
	for i := range owners {
		loc, ok := canonicalWinLocationForFile(owners[i].FilePath, owners[i].ITunesPersistentID, "pid_repair", mappings)
		if ok && strings.EqualFold(loc, trackLocation) {
			if match != -1 {
				return 0, false // ambiguous: two owners canonicalize to the track loc
			}
			match = i
		}
	}
	if match == -1 {
		return 0, false
	}
	return match, true
}

func isLivePrimary(store PIDRepairStore, bookID string, cache map[string]*database.Book) bool {
	b, ok := cache[bookID]
	if !ok {
		b, _ = store.GetBookByID(bookID)
		cache[bookID] = b
	}
	if b == nil {
		return false
	}
	primary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
	deleted := b.MarkedForDeletion != nil && *b.MarkedForDeletion
	return primary && !deleted
}

// PIDRepairResult reports what an apply actually did.
type PIDRepairResult struct {
	GroupsRepaired int `json:"groups_repaired"`
	FilesCleared   int `json:"files_cleared"`
	Errors         int `json:"errors"`
}

// ApplyPIDRepairPlan executes the groups: for each PID, clear the PID/path fields
// on every loser row, then re-assert the keeper so the book_file_pid index points
// at it. Groups are disjoint by PID, so a bounded worker pool over groups is safe
// (no two workers touch the same row or index key). No audio file is touched.
func ApplyPIDRepairPlan(store PIDRepairStore, groups []pidRepairGroup) (*PIDRepairResult, error) {
	res := &PIDRepairResult{}
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	results := make([]struct{ cleared, errs int }, len(groups))

	for i := range groups {
		grp := groups[i]
		g.Go(func() error {
			for _, l := range grp.losers {
				f := l.file
				f.ITunesPersistentID = ""
				f.ITunesPath = ""
				if err := store.UpdateBookFile(f.ID, &f); err != nil {
					slog.Warn("pid-repair: clear loser failed", "pid", grp.pid, "file", f.ID, "err", err)
					results[i].errs++
					continue
				}
				results[i].cleared++
			}
			// Re-assert the keeper so book_file_pid:<pid> points at it (clearing the
			// losers deleted the shared index key).
			keep := grp.keep
			if err := store.UpdateBookFile(keep.ID, &keep); err != nil {
				slog.Error("pid-repair: re-assert keeper failed (PID index may be unset)",
					"pid", grp.pid, "keep", keep.ID, "err", err)
				results[i].errs++
			}
			return nil
		})
	}
	_ = g.Wait()

	for i := range results {
		res.FilesCleared += results[i].cleared
		res.Errors += results[i].errs
		if results[i].errs == 0 {
			res.GroupsRepaired++
		}
	}
	slog.Info("itunes pid-repair: applied", "groupsRepaired", res.GroupsRepaired,
		"filesCleared", res.FilesCleared, "errors", res.Errors)
	return res, nil
}
