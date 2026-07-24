// file: internal/itunes/pid_integrity.go
// version: 1.1.0
// guid: e2f7a1c4-6b90-4d38-8a5e-1c3f9d2b7e60
// last-edited: 2026-07-24
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
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

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

// -----------------------------------------------------------------------------
// Cleanup provenance census — the P3 build/no-op exit-gate (spec §6.5, P0).
// -----------------------------------------------------------------------------

// OrphanBucket labels how one AO-.itl track relates to the live library.
type OrphanBucket string

const (
	// BucketHealthy: the track's PID is owned by a live PRIMARY book_file. Fine.
	BucketHealthy OrphanBucket = "healthy"
	// BucketStaleOwner: owned by a book_file whose book is soft-deleted or
	// non-primary — a version/merge leftover, but only an orphan if provenance
	// attributes it to a merge loser.
	BucketStaleOwner OrphanBucket = "stale_owner"
	// BucketNoLiveOwner: NO live book_file carries the PID. Unattributable — this
	// is EITHER a user's directly-imported non-audiobook track (hands-off) OR a
	// merge orphan whose book_file PID was cleared by the PID-uniqueness repair.
	// Never bulk-removable: we cannot prove it is ours.
	BucketNoLiveOwner OrphanBucket = "no_live_owner"
)

// OrphanSample is one attributed AO-.itl track, kept for the JSON detail view.
type OrphanSample struct {
	PID          string       `json:"pid"`
	Bucket       OrphanBucket `json:"bucket"`
	OwnerFileID  string       `json:"owner_file_id,omitempty"`
	OwnerBookID  string       `json:"owner_book_id,omitempty"`
	Title        string       `json:"title,omitempty"`
	InLoserSet   bool         `json:"in_loser_set"`             // owner book ∈ merge-loser provenance
	LoserSource  string       `json:"loser_source,omitempty"`   // "journal" | "merged_into" | "both"
	SHAMatch     bool         `json:"sha_match"`                // a live-primary book_file shares this file's FileHash
	FileHash     string       `json:"file_hash,omitempty"`
}

// MergeOrphanCensus is the decision-relevant output. The bucket counts partition
// TracksInITL exactly (Healthy + StaleOwner + NoLiveOwner == TracksInITL).
type MergeOrphanCensus struct {
	TracksInITL int `json:"tracks_in_itl"`

	// Loser provenance (spec §6.5 reconciles BOTH sources).
	JournalLoserIDs   int `json:"journal_loser_ids"`    // distinct LoserID in AutoMergeJournalEntry
	MergedIntoLosers  int `json:"merged_into_losers"`   // distinct owner-books with MergedIntoBookID set (among .itl owners)
	DistinctLoserSet  int `json:"distinct_loser_set"`   // |journal ∪ merged_into| restricted to .itl owners actually seen

	// Bucketing of every AO-.itl track by its CURRENT live owner.
	Healthy     int `json:"healthy"`
	StaleOwner  int `json:"stale_owner"`
	NoLiveOwner int `json:"no_live_owner"`

	// The P3 gating number: stale-owner tracks whose owner book is a provable
	// merge loser (journal ∪ MergedIntoBookID). LOWER BOUND — see caveat.
	ProvableMergeOrphans int `json:"provable_merge_orphans"`

	// Narrowing (reported, NOT acted on here): of the provable orphans, those
	// whose FileHash is also carried by a LIVE PRIMARY book_file (spec Decision 1
	// SHA gate). The playlist-membership gate (§7.2) needs the .xml export and is
	// deliberately NOT applied here.
	//
	// FLOOR: livePrimaryHash is built only from books that own a track in this .itl,
	// so a live primary carrying the same audio but no .itl track won't be counted —
	// SHAGatedRemovable can only under-count. Do NOT read it as authoritative if the
	// removal path is ever resurrected.
	SHAGatedRemovable int `json:"sha_gated_removable"`

	// Diagnostics.
	ResidualDuplicatePIDs int `json:"residual_duplicate_pids"` // PIDs on >1 book_file (should be ~0 post-repair)

	Samples []OrphanSample `json:"samples"`
}

// ComputeMergeOrphanCensus buckets every track in the AO .itl by its current live
// owner and intersects the stale-owner tracks with the merge-loser provenance
// set, producing the P3 build/no-op exit-gate. READ-ONLY.
//
// journalLoserIDs is the {LoserID} set from EmbeddingStore.ListAutoMergeJournalEntries
// (the production-authoritative loser record — the itunes package stays decoupled
// from EmbeddingStore, so the caller passes it in). MergedIntoBookID losers are
// discovered per-owner from the store.
//
// IMPORTANT — the count is a LOWER BOUND. There is no durable record of the PIDs a
// loser owned at merge time: MergeBooks reassigns the loser's iTunes external-IDs
// to the winner, the PID-uniqueness repair cleared duplicate book_file PIDs, and
// the journal/book_ver snapshot store no PIDs. So a track that WAS a loser's but
// whose PID→loser link was severed lands in no_live_owner (unattributable), not in
// ProvableMergeOrphans. A ~0 result means "the link is severed," NOT "no orphans
// exist" — never treat it as a licence for bulk removal.
func ComputeMergeOrphanCensus(store PIDIntegrityStore, itlPath string, journalLoserIDs []string) (*MergeOrphanCensus, error) {
	lib, err := ParseITL(itlPath)
	if err != nil {
		return nil, err
	}
	itlPIDs := make([]string, len(lib.Tracks))
	for i := range lib.Tracks {
		itlPIDs[i] = strings.ToUpper(pidToHex(lib.Tracks[i].PersistentID))
	}
	return computeMergeOrphanCensus(store, itlPIDs, journalLoserIDs)
}

// computeMergeOrphanCensus is the pure core: it takes the AO .itl track PID set
// (upper-hex) directly so it is unit-testable without a binary .itl. See
// ComputeMergeOrphanCensus for the contract and the LOWER-BOUND caveat.
func computeMergeOrphanCensus(store PIDIntegrityStore, itlPIDs []string, journalLoserIDs []string) (*MergeOrphanCensus, error) {
	// PID → the book_file(s) that carry it. Post-repair PIDs are unique, but stay
	// defensive: a residual duplicate is a diagnostic, not a crash.
	byPID := make(map[string][]database.BookFileCore)
	// FileHash → book_files carrying it (for the SHA gate, no extra store reads).
	byHash := make(map[string][]database.BookFileCore)
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, err
	}
	for i := range files {
		pid := strings.ToUpper(strings.TrimSpace(files[i].ITunesPersistentID))
		if pid != "" {
			byPID[pid] = append(byPID[pid], files[i])
		}
		if h := files[i].FileHash; h != "" {
			byHash[h] = append(byHash[h], files[i])
		}
	}

	journalSet := make(map[string]bool, len(journalLoserIDs))
	for _, id := range journalLoserIDs {
		if id != "" {
			journalSet[id] = true
		}
	}

	// The distinct owner-books we need to resolve = owners of .itl tracks, plus
	// owners of any FileHash group that a candidate orphan may SHA-match against.
	// Collect the .itl-owner books first; resolve concurrently (whole-library
	// scale, per-item Pebble read → bounded pool per CLAUDE.md concurrency rule).
	needBook := make(map[string]struct{})
	for _, pid := range itlPIDs {
		for _, f := range byPID[pid] {
			needBook[f.BookID] = struct{}{}
		}
	}

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
			b, berr := store.GetBookByID(id)
			if berr != nil || b == nil {
				return nil // best-effort: a missing book leaves the track unattributed
			}
			bookMu.Lock()
			books[id] = b
			bookMu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	isLivePrimary := func(b *database.Book) bool {
		if b == nil {
			return false
		}
		softDeleted := b.MarkedForDeletion != nil && *b.MarkedForDeletion
		primary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
		return primary && !softDeleted
	}

	// Set of FileHashes owned by at least one LIVE PRIMARY book_file — the SHA gate
	// target. Resolving these books reuses the already-fetched .itl-owner cache
	// where possible; misses are looked up lazily below only for candidate orphans.
	livePrimaryHash := make(map[string]bool)
	for h, group := range byHash {
		for _, f := range group {
			if b, ok := books[f.BookID]; ok && isLivePrimary(b) {
				livePrimaryHash[h] = true
				break
			}
		}
	}

	c := &MergeOrphanCensus{TracksInITL: len(itlPIDs), JournalLoserIDs: len(journalSet)}
	mergedIntoSeen := make(map[string]bool)
	loserSeen := make(map[string]bool)

	for _, pid := range itlPIDs {
		owners := byPID[pid]
		if len(owners) > 1 {
			c.ResidualDuplicatePIDs++
		}
		if len(owners) == 0 {
			c.NoLiveOwner++
			c.addSample(OrphanSample{PID: pid, Bucket: BucketNoLiveOwner})
			continue
		}
		// Prefer a live-primary owner if any; else take the first (stale) owner.
		var owner database.BookFileCore
		var ownerBook *database.Book
		picked := false
		for _, f := range owners {
			b := books[f.BookID]
			if isLivePrimary(b) {
				owner, ownerBook, picked = f, b, true
				break
			}
		}
		if !picked {
			owner = owners[0]
			ownerBook = books[owner.BookID]
		}

		if isLivePrimary(ownerBook) {
			c.Healthy++
			continue
		}

		// Stale owner (soft-deleted or non-primary). Attribute by provenance.
		c.StaleOwner++
		inJournal := ownerBook != nil && journalSet[ownerBook.ID]
		mergedInto := ownerBook != nil && ownerBook.MergedIntoBookID != nil
		if mergedInto && !mergedIntoSeen[ownerBook.ID] {
			mergedIntoSeen[ownerBook.ID] = true
			c.MergedIntoLosers++
		}
		if (inJournal || mergedInto) && ownerBook != nil && !loserSeen[ownerBook.ID] {
			loserSeen[ownerBook.ID] = true
			c.DistinctLoserSet++
		}

		sample := OrphanSample{
			PID: pid, Bucket: BucketStaleOwner, OwnerFileID: owner.ID,
			FileHash: owner.FileHash,
		}
		if ownerBook != nil {
			sample.OwnerBookID = ownerBook.ID
			sample.Title = ownerBook.Title
		}
		if inJournal || mergedInto {
			c.ProvableMergeOrphans++
			sample.InLoserSet = true
			switch {
			case inJournal && mergedInto:
				sample.LoserSource = "both"
			case inJournal:
				sample.LoserSource = "journal"
			default:
				sample.LoserSource = "merged_into"
			}
			if owner.FileHash != "" && livePrimaryHash[owner.FileHash] {
				c.SHAGatedRemovable++
				sample.SHAMatch = true
			}
		}
		c.addSample(sample)
	}

	slog.Info("itunes merge-orphan census",
		"tracksInITL", c.TracksInITL, "healthy", c.Healthy, "staleOwner", c.StaleOwner,
		"noLiveOwner", c.NoLiveOwner, "journalLoserIDs", c.JournalLoserIDs,
		"mergedIntoLosers", c.MergedIntoLosers, "provableMergeOrphans", c.ProvableMergeOrphans,
		"shaGatedRemovable", c.SHAGatedRemovable, "residualDuplicatePIDs", c.ResidualDuplicatePIDs)
	return c, nil
}

// addSample keeps up to pidSampleLimit samples, prioritizing attributed orphans
// (InLoserSet) so the detail view surfaces the decision-relevant rows first.
func (c *MergeOrphanCensus) addSample(s OrphanSample) {
	if len(c.Samples) < pidSampleLimit {
		c.Samples = append(c.Samples, s)
		return
	}
	if !s.InLoserSet {
		return
	}
	for i := range c.Samples {
		if !c.Samples[i].InLoserSet {
			c.Samples[i] = s
			return
		}
	}
}
