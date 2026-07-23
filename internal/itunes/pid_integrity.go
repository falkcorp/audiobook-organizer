// file: internal/itunes/pid_integrity.go
// version: 1.0.0
// guid: e2f7a1c4-6b90-4d38-8a5e-1c3f9d2b7e60
// last-edited: 2026-07-23
//
// READ-ONLY book_file iTunes-PID integrity census. A PID is minted unique per
// book_file (TrackProvisioner.Provision → GeneratePIDHex → crypto/rand), so the
// same PID on two book_file rows is an anomaly: it was COPIED (a field-merge left
// it on both src and dst) rather than minted. This census groups every book_file
// by PID, and for each PID owned by more than one row classifies the shape so the
// repair (pid_repair) can act without data loss:
//
//   - same_file : all owner rows point at the SAME FilePath → a duplicate row; the
//     repair keeps the PID on one canonical row and clears it from the rest. No ITL
//     change (the kept row still links the track).
//   - diff_file : owner rows point at DIFFERENT files → a mis-copied PID; only ONE
//     row is the real iTunes track. The repair keeps the PID on the row whose path
//     matches the live ITL track and clears/re-mints the others. NEVER the reverse
//     (clearing the matching row would orphan the track).
//
// It also runs the relocate-correctness probe (PIDs on >1 PRIMARY live book_file
// with differing paths → relocate's first-wins is order-dependent) from
// docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md §1.5. Nothing here
// mutates: it only reads the store and the ITL.

package itunes

import (
	"log/slog"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// PIDIntegrityStore is the read-only slice of the store the census needs.
type PIDIntegrityStore interface {
	// GetAllBookFilesCore returns every book_file (including those whose parent
	// book is soft-deleted — it scans book_file rows directly), carrying ID,
	// BookID, FilePath, and ITunesPersistentID.
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	// GetBookByID resolves a book by id, INCLUDING soft-deleted books (needed to
	// read a merge-loser's IsPrimaryVersion / MarkedForDeletion / MergedIntoBookID).
	GetBookByID(id string) (*database.Book, error)
}

// pidOwner is one book_file that carries a given PID, plus its book's status.
type PIDOwner struct {
	FileID         string `json:"file_id"`
	BookID         string `json:"book_id"`
	FilePath       string `json:"file_path"`
	IsPrimary      bool   `json:"is_primary"`
	SoftDeleted    bool   `json:"soft_deleted"`
	HasMergeLink   bool   `json:"has_merge_link"`             // MergedIntoBookID set
	MergedInto     string `json:"merged_into_book_id,omitempty"`
	VersionGroupID string `json:"version_group_id,omitempty"`
	Title          string `json:"title,omitempty"`
}

// DuplicatePID is one PID owned by more than one book_file, with its shape.
type DuplicatePID struct {
	PID            string     `json:"pid"`
	Owners         []PIDOwner `json:"owners"`
	Classification string     `json:"classification"` // "same_file" | "diff_file"
	InITL          bool       `json:"in_itl"`
	PrimaryOwners  int        `json:"primary_owners"`  // live primary owners
	DistinctPaths  int        `json:"distinct_paths"`  // distinct FilePaths among owners
}

// PIDIntegrityReport is the full census. All counts are over book_file rows.
type PIDIntegrityReport struct {
	TracksInITL         int `json:"tracks_in_itl"`
	FilesWithPID        int `json:"files_with_pid"`          // book_file rows carrying a non-empty PID
	DistinctPIDs        int `json:"distinct_pids"`
	DuplicatePIDs       int `json:"duplicate_pids"`          // PIDs owned by >1 book_file
	DupSameFile         int `json:"dup_same_file"`           // all owners share FilePath
	DupDiffFile         int `json:"dup_diff_file"`           // owners point at different files
	DupInITL            int `json:"dup_in_itl"`              // duplicate PIDs present in the library
	FilesToClear        int `json:"files_to_clear"`          // owner rows losing the PID (owners−1 per dup PID)
	// Relocate-correctness probe (findings §1.5): a PID on >1 PRIMARY, live
	// book_file with differing paths makes relocate's first-wins order-dependent.
	PIDsOnMultiplePrimariesDiffPath int `json:"pids_on_multiple_primaries_diff_path"`

	Samples []DuplicatePID `json:"samples"` // up to pidSampleLimit, worst-shape first
}

const pidSampleLimit = 60

// ComputePIDIntegrity scans every book_file, groups by PID, and reports the
// duplicate-PID census + relocate probe. Read-only. itlPath is used only to mark
// which duplicate PIDs are actually present in the library (informs whether the
// repair touches the ITL at all).
func ComputePIDIntegrity(store PIDIntegrityStore, itlPath string) (*PIDIntegrityReport, error) {
	inITL := map[string]bool{}
	tracksInITL := 0
	if itlPath != "" {
		lib, err := ParseITL(itlPath)
		if err != nil {
			return nil, err
		}
		tracksInITL = len(lib.Tracks)
		for i := range lib.Tracks {
			inITL[strings.ToUpper(pidToHex(lib.Tracks[i].PersistentID))] = true
		}
	}

	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, err
	}

	// Group book_file rows by upper-hex PID.
	byPID := make(map[string][]database.BookFileCore)
	for i := range files {
		pid := strings.ToUpper(strings.TrimSpace(files[i].ITunesPersistentID))
		if pid == "" {
			continue
		}
		byPID[pid] = append(byPID[pid], files[i])
	}

	report := &PIDIntegrityReport{TracksInITL: tracksInITL, DistinctPIDs: len(byPID)}
	bookCache := map[string]*database.Book{} // bounded: only dup-PID owners are looked up

	for pid, owners := range byPID {
		report.FilesWithPID += len(owners)
		if len(owners) < 2 {
			continue
		}
		report.DuplicatePIDs++
		report.FilesToClear += len(owners) - 1

		dup := DuplicatePID{PID: pid, InITL: inITL[pid]}
		if dup.InITL {
			report.DupInITL++
		}

		paths := map[string]bool{}
		primaryPaths := map[string]bool{} // distinct paths among live-primary owners
		for j := range owners {
			f := owners[j]
			paths[f.FilePath] = true
			b := bookCache[f.BookID]
			if b == nil {
				if bk, berr := store.GetBookByID(f.BookID); berr == nil && bk != nil {
					b = bk
					bookCache[f.BookID] = bk
				}
			}
			po := PIDOwner{FileID: f.ID, BookID: f.BookID, FilePath: f.FilePath}
			if b != nil {
				po.IsPrimary = b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
				po.SoftDeleted = b.MarkedForDeletion != nil && *b.MarkedForDeletion
				if b.MergedIntoBookID != nil {
					po.HasMergeLink = true
					po.MergedInto = *b.MergedIntoBookID
				}
				if b.VersionGroupID != nil {
					po.VersionGroupID = *b.VersionGroupID
				}
				po.Title = b.Title
				if po.IsPrimary && !po.SoftDeleted {
					dup.PrimaryOwners++
					primaryPaths[f.FilePath] = true
				}
			}
			dup.Owners = append(dup.Owners, po)
		}

		dup.DistinctPaths = len(paths)
		if len(paths) == 1 {
			dup.Classification = "same_file"
			report.DupSameFile++
		} else {
			dup.Classification = "diff_file"
			report.DupDiffFile++
		}
		if len(primaryPaths) > 1 {
			report.PIDsOnMultiplePrimariesDiffPath++
		}

		if len(report.Samples) < pidSampleLimit {
			report.Samples = append(report.Samples, dup)
		}
	}

	slog.Info("itunes pid-integrity census",
		"tracksInITL", report.TracksInITL, "filesWithPID", report.FilesWithPID,
		"distinctPIDs", report.DistinctPIDs, "duplicatePIDs", report.DuplicatePIDs,
		"dupSameFile", report.DupSameFile, "dupDiffFile", report.DupDiffFile,
		"dupInITL", report.DupInITL, "filesToClear", report.FilesToClear,
		"pidsOnMultiplePrimariesDiffPath", report.PIDsOnMultiplePrimariesDiffPath)
	return report, nil
}
