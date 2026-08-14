// file: internal/itunes/relocate.go
// version: 1.0.0
// guid: 4b8e2c17-9a06-4d3f-8b21-7e5c0a1f6d92
// last-edited: 2026-07-22
//
// Location-only iTunes writeback ("2-way sync" audiobook relocate). Unlike the
// DB-authoritative rebuild (ComputeITLDiff, which REMOVES every ITL track not in
// the DB and so would gut music/podcasts/playlists against a full library), this
// path is purely additive-safe: it emits ONLY per-file location patches for
// book_files whose iTunes track already exists, keyed on each file's OWN
// BookFile.ITunesPersistentID. It never removes, never adds, and never touches a
// track it did not match — so non-audiobook tracks and all playlists are left
// byte-for-byte intact by ApplyITLOperations. See
// docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md.

package itunes

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// RelocatePreview summarizes a per-file relocate diff without applying it.
type RelocatePreview struct {
	TracksInITL     int `json:"tracks_in_itl"`
	FilesConsidered int `json:"files_considered"` // book_files carrying an iTunes PID
	Matched         int `json:"matched"`          // files whose PID exists in the ITL
	ToRelocate      int `json:"to_relocate"`      // matched files whose location differs
	AlreadyCorrect  int `json:"already_correct"`  // matched files already at the wanted location
	UnmatchedFiles  int `json:"unmatched_files"`  // files whose PID is NOT in the ITL (P2 add-set)
	Unmappable      int `json:"unmappable"`       // files whose FilePath can't canonicalize
}

// ComputeRelocateOps computes the LOCATION-ONLY operation set that repoints each
// iTunes-linked book_file's track at the file's CURRENT canonical location
// (W:\... derived from BookFile.FilePath). Matching is per-FILE, on
// BookFile.ITunesPersistentID — the correct granularity, since iTunes stores one
// track per file and a multi-part book has many tracks. The returned ops contain
// ONLY LocationUpdates: no Removes, no Adds. Files whose PID is absent from the
// ITL (never-imported, AO-only) are counted as UnmatchedFiles and left for the
// P2 add-path — they are NOT removed here.
func ComputeRelocateOps(store RebuildStore, itlPath string, mappings []PathMapping) (*ITLOperationSet, *RelocatePreview, error) {
	lib, err := ParseITL(itlPath)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ITL: %w", err)
	}

	// Index existing ITL tracks by upper-hex PID.
	itlTracks := make(map[string]*ITLTrack, len(lib.Tracks))
	for i := range lib.Tracks {
		itlTracks[strings.ToUpper(pidToHex(lib.Tracks[i].PersistentID))] = &lib.Tracks[i]
	}

	return relocateOpsFromTracks(itlTracks, store, mappings)
}

// relocateOpsFromTracks is the ITL-independent core of ComputeRelocateOps: given
// the existing tracks indexed by upper-hex PID, it walks the DB's book_files and
// builds the location-only op set. Split out so the diff logic is unit-testable
// without minting a real .itl (AddTracksLE assigns random PIDs).
func relocateOpsFromTracks(itlTracks map[string]*ITLTrack, store RebuildStore, mappings []PathMapping) (*ITLOperationSet, *RelocatePreview, error) {
	// Removes stays empty for the whole run — a relocate NEVER removes a track.
	ops := ITLOperationSet{Removes: map[string]bool{}}
	preview := &RelocatePreview{TracksInITL: len(itlTracks)}

	// A PID identifies exactly one iTunes track; guard against the same PID
	// landing on two book_files (would otherwise emit a duplicate update).
	emitted := make(map[string]bool)

	const pageSize = 500
	afterID := ""
	for {
		books, err := store.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			return nil, nil, fmt.Errorf("get books: %w", err)
		}
		if len(books) == 0 {
			break
		}
		for i := range books {
			b := &books[i]
			// Only primary, non-soft-deleted versions get written to iTunes
			// (mirrors ComputeITLDiff / TrackProvisioner.Provision).
			if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
				continue
			}
			if b.IsSoftDeleted() {
				continue
			}
			files, ferr := store.GetBookFiles(b.ID)
			if ferr != nil {
				continue
			}
			for j := range files {
				f := &files[j]
				if f.ITunesPersistentID == "" {
					continue
				}
				preview.FilesConsidered++
				pidUpper := strings.ToUpper(f.ITunesPersistentID)
				track, inITL := itlTracks[pidUpper]
				if !inITL {
					// Never-in-iTunes file → P2 add-path, NOT a removal.
					preview.UnmatchedFiles++
					continue
				}
				preview.Matched++
				wantLoc, ok := canonicalWinLocationForFile(f.FilePath, f.ITunesPersistentID, "relocate_invalid_path", mappings)
				if !ok {
					preview.Unmappable++
					continue
				}
				if track.Location == wantLoc {
					preview.AlreadyCorrect++
					continue
				}
				if emitted[pidUpper] {
					continue
				}
				emitted[pidUpper] = true
				ops.LocationUpdates = append(ops.LocationUpdates, ITLLocationUpdate{
					PersistentID: pidUpper,
					NewLocation:  wantLoc,
				})
				preview.ToRelocate++
			}
		}
		afterID = books[len(books)-1].ID
		if len(books) < pageSize {
			break
		}
	}

	slog.Info("itunes relocate: computed location-only diff",
		"tracksInITL", preview.TracksInITL, "filesConsidered", preview.FilesConsidered,
		"matched", preview.Matched, "toRelocate", preview.ToRelocate,
		"alreadyCorrect", preview.AlreadyCorrect, "unmatched", preview.UnmatchedFiles,
		"unmappable", preview.Unmappable)
	return &ops, preview, nil
}

// canonicalWinLocationForFile canonicalizes a single local FilePath into the
// native Windows ITL 0x0D form (W:\...). ReverseRemapPath yields forward slashes;
// the ITL 0x0D form needs backslashes and isWindowsAbsPath rejects any '/', so we
// flip separators before validating. An unmappable path (still /mnt/... → \mnt\...
// with no drive letter) is rejected → skipped, never written raw (CRIT-2).
// metricLabel distinguishes the caller in the unmappable metric.
func canonicalWinLocationForFile(localPath, pidForLog, metricLabel string, mappings []PathMapping) (string, bool) {
	if localPath == "" {
		return "", false
	}
	winish := strings.ReplaceAll(ReverseRemapPath(localPath, mappings), "/", `\`)
	pair, err := NewLocationPair(winish)
	if err != nil {
		metrics.RecordITunesLocationUnmappable(metricLabel)
		slog.Warn("ITL relocate: skipping file with unmappable location (never written raw — CRIT-2)",
			"pid", pidForLog, "local", localPath, "error", err.Error())
		return "", false
	}
	return pair.WinPath, true
}

// AdoptLibraryIdentity (re)writes the .identity.json sidecar to describe the
// library CURRENTLY at itlPath. Required after reseeding the writeback slot from
// a different library (e.g. swapping the 2 MB prototype for the real 32 MB
// library): the stale sidecar would otherwise trip the K13/K14 identity guards
// and reject every subsequent write. This blesses whatever library is on disk —
// an explicit operator action, never implicit in a write path.
func AdoptLibraryIdentity(itlPath string) (*LibraryIdentity, error) {
	hdr, payload, err := decodeITLForContractFile(itlPath)
	if err != nil {
		return nil, fmt.Errorf("adopt: decode %s: %w", itlPath, err)
	}
	id, err := ComputeLibraryIdentity(payload, hdr)
	if err != nil {
		return nil, fmt.Errorf("adopt: compute identity: %w", err)
	}
	if err := SaveLibraryIdentity(itlPath, id); err != nil {
		return nil, fmt.Errorf("adopt: save sidecar: %w", err)
	}
	slog.Info("itunes relocate: adopted library identity",
		"path", itlPath, "libraryPID", id.LibraryPID, "trackCount", id.TrackCount, "playlistCount", id.PlaylistCount)
	return id, nil
}
