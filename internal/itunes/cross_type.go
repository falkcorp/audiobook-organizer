// file: internal/itunes/cross_type.go
// version: 1.0.0
// guid: 4b7e1a92-3c6d-4f80-9a15-8e2c7b3d1f64
// last-edited: 2026-07-24
//
// READ-ONLY cross-type PID-collision census — the disjointness backstop for the
// iTunes 2-way-sync relocate path (P0 of the 2-way-sync system design).
//
// The AO writeback .itl is a FULL library: it carries the user's music + podcasts
// alongside AO-managed audiobooks (that is why the design relocates tracks IN
// PLACE instead of rebuilding — a rebuild would delete the ~86k non-audiobook
// tracks). The relocate op rewrites a track's on-disk location keyed by the
// book_file's iTunes PID. The load-bearing invariant is therefore DISJOINTNESS:
// every AO book_file PID must resolve to an AUDIOBOOK track, never a music/podcast
// one. A single AO book_file whose PID points at a non-audiobook track means a
// relocate would rewrite a music/podcast track's location — a cross-type write
// into the hands-off part of the library.
//
// This census classifies every .itl track (isAudiobookITL: Kind/Genre/Location)
// and cross-tabs it against AO book_file ownership. The decision-relevant cell is
// "non-audiobook track owned by an AO book_file" (CrossTypeCollisions): it MUST be
// 0 (or every offender individually explained) before the relocate op is armed.

package itunes

import (
	"log/slog"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// CrossTypeSample is one non-audiobook .itl track that an AO book_file claims.
type CrossTypeSample struct {
	PID             string `json:"pid"`
	TrackName       string `json:"track_name,omitempty"`
	Genre           string `json:"genre,omitempty"`
	Kind            string `json:"kind,omitempty"`
	Location        string `json:"location,omitempty"`
	OwnerFileID     string `json:"owner_file_id,omitempty"`
	OwnerBookID     string `json:"owner_book_id,omitempty"`
	OwnerBookTitle  string `json:"owner_book_title,omitempty"`
	OwnerIsPrimary  bool   `json:"owner_is_primary"`
	OwnerSoftDelete bool   `json:"owner_soft_deleted"`
}

// CrossTypeReport is the disjointness census. The audiobook/non-audiobook counts
// partition TracksInITL; the four ownership cells partition it again.
type CrossTypeReport struct {
	TracksInITL        int `json:"tracks_in_itl"`
	AudiobookTracks    int `json:"audiobook_tracks"`
	NonAudiobookTracks int `json:"non_audiobook_tracks"`

	// Cross-tab of {audiobook?} × {owned by any live AO book_file?}.
	ABOwned      int `json:"ab_owned"`       // audiobook track, AO owns it (correct)
	ABUnowned    int `json:"ab_unowned"`     // audiobook track, no AO owner (unmatched)
	NonABOwned   int `json:"non_ab_owned"`   // non-audiobook track, AO owns it (HAZARD)
	NonABUnowned int `json:"non_ab_unowned"` // non-audiobook track, no AO owner (hands-off)

	// CrossTypeCollisions == NonABOwned — the number that MUST be 0 before relocate
	// is armed. Split by owner status so a stale/soft-deleted owner (lower risk,
	// relocate skips it) is distinguishable from a LIVE PRIMARY owner (the acute
	// hazard: relocate would rewrite this music/podcast track).
	CrossTypeCollisions        int `json:"cross_type_collisions"`
	CollisionsLivePrimaryOwner int `json:"collisions_live_primary_owner"`

	// Genre/Kind distribution over ALL collisions (not just samples) — lets a
	// reviewer confirm whether the "collisions" are real music/podcasts vs
	// audiobooks that isAudiobookITL under-classifies (AO only stores audiobooks,
	// so a distribution of book-shaped genres/kinds proves heuristic false-positives).
	CollisionGenres map[string]int `json:"collision_genres,omitempty"`
	CollisionKinds  map[string]int `json:"collision_kinds,omitempty"`

	Samples []CrossTypeSample `json:"samples"`
}

const crossTypeSampleLimit = 80

// ComputeCrossTypeCollisions classifies every AO-.itl track and cross-tabs it
// against AO book_file ownership. READ-ONLY. store is the same PIDIntegrityStore
// the other censuses use (GetAllBookFilesCore including soft-deleted rows +
// GetBookByID resolving soft-deleted books).
func ComputeCrossTypeCollisions(store PIDIntegrityStore, itlPath string) (*CrossTypeReport, error) {
	lib, err := ParseITL(itlPath)
	if err != nil {
		return nil, err
	}
	return computeCrossTypeCollisions(store, lib.Tracks)
}

// computeCrossTypeCollisions is the pure core: it takes the parsed track slice
// directly so it is unit-testable without a binary .itl.
func computeCrossTypeCollisions(store PIDIntegrityStore, tracks []ITLTrack) (*CrossTypeReport, error) {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, err
	}
	// PID → owning book_file rows. Post-repair PIDs are unique, but a track can
	// legitimately have multiple candidate owners across a version group; we treat
	// a track as AO-owned if ANY book_file carries its PID.
	byPID := make(map[string][]database.BookFileCore)
	for i := range files {
		pid := strings.ToUpper(strings.TrimSpace(files[i].ITunesPersistentID))
		if pid != "" {
			byPID[pid] = append(byPID[pid], files[i])
		}
	}

	// Resolve the owner books only for tracks that are BOTH non-audiobook AND
	// AO-owned (the collision candidates) — bounded, so no full-library book scan.
	// Collect their owner BookIDs first, resolve concurrently.
	type trackRef struct {
		idx int
		pid string
	}
	var collisions []trackRef
	needBook := make(map[string]struct{})
	report := &CrossTypeReport{TracksInITL: len(tracks)}

	for i := range tracks {
		t := &tracks[i]
		ab := isAudiobookITL(t)
		pid := strings.ToUpper(pidToHex(t.PersistentID))
		owners := byPID[pid]
		owned := len(owners) > 0
		switch {
		case ab && owned:
			report.AudiobookTracks++
			report.ABOwned++
		case ab && !owned:
			report.AudiobookTracks++
			report.ABUnowned++
		case !ab && owned:
			report.NonAudiobookTracks++
			report.NonABOwned++
			collisions = append(collisions, trackRef{idx: i, pid: pid})
			for _, f := range owners {
				needBook[f.BookID] = struct{}{}
			}
		default: // !ab && !owned
			report.NonAudiobookTracks++
			report.NonABUnowned++
		}
	}
	report.CrossTypeCollisions = report.NonABOwned

	// Resolve collision owner books concurrently (per CLAUDE.md concurrency rule).
	bookMu := sync.Mutex{}
	books := make(map[string]*database.Book, len(needBook))
	ids := make([]string, 0, len(needBook))
	for id := range needBook {
		ids = append(ids, id)
	}
	g := new(errgroup.Group)
	g.SetLimit(runtime.NumCPU())
	for _, id := range ids {
		g.Go(func() error {
			if b, berr := store.GetBookByID(id); berr == nil && b != nil {
				bookMu.Lock()
				books[id] = b
				bookMu.Unlock()
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	report.CollisionGenres = make(map[string]int)
	report.CollisionKinds = make(map[string]int)
	for _, ref := range collisions {
		t := &tracks[ref.idx]
		owners := byPID[ref.pid]
		livePrimary := false
		genreKey := t.Genre
		if genreKey == "" {
			genreKey = "(none)"
		}
		kindKey := t.Kind
		if kindKey == "" {
			kindKey = "(none)"
		}
		report.CollisionGenres[genreKey]++
		report.CollisionKinds[kindKey]++
		s := CrossTypeSample{
			PID: ref.pid, TrackName: t.Name, Genre: t.Genre, Kind: t.Kind, Location: t.Location,
		}
		if len(owners) > 0 {
			f := owners[0]
			s.OwnerFileID = f.ID
			s.OwnerBookID = f.BookID
			if b := books[f.BookID]; b != nil {
				s.OwnerBookTitle = b.Title
				s.OwnerIsPrimary = b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
				s.OwnerSoftDelete = b.MarkedForDeletion != nil && *b.MarkedForDeletion
			}
		}
		// A collision is acute when ANY owner is a live primary.
		for _, f := range owners {
			if b := books[f.BookID]; b != nil {
				pr := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
				del := b.MarkedForDeletion != nil && *b.MarkedForDeletion
				if pr && !del {
					livePrimary = true
					break
				}
			}
		}
		if livePrimary {
			report.CollisionsLivePrimaryOwner++
		}
		if len(report.Samples) < crossTypeSampleLimit {
			report.Samples = append(report.Samples, s)
		}
	}

	slog.Info("itunes cross-type collision census",
		"tracksInITL", report.TracksInITL, "audiobookTracks", report.AudiobookTracks,
		"nonAudiobookTracks", report.NonAudiobookTracks, "abOwned", report.ABOwned,
		"abUnowned", report.ABUnowned, "nonABOwned", report.NonABOwned,
		"nonABUnowned", report.NonABUnowned, "crossTypeCollisions", report.CrossTypeCollisions,
		"collisionsLivePrimaryOwner", report.CollisionsLivePrimaryOwner)
	return report, nil
}
