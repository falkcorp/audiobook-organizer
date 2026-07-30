// file: internal/merge/service.go
// version: 1.10.0
// guid: 7d736d2d-e0df-40bd-9f4b-0a07bc2eb6ae
// last-edited: 2026-07-30

package merge

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
)

// WriteBackEnqueuer is satisfied by anything that can enqueue an iTunes
// track removal (e.g. *server.WriteBackBatcher).
type WriteBackEnqueuer interface {
	EnqueueRemove(pid string)
}

// ExternalIDReassigner is the subset of external-ID operations that
// Service needs. Satisfied by the concrete store when it implements
// ReassignExternalIDs.
type ExternalIDReassigner interface {
	ReassignExternalIDs(oldBookID, newBookID string) error
}

// AsExternalIDReassigner returns the ExternalIDReassigner if the given
// store implements it, or nil otherwise.
func AsExternalIDReassigner(s any) ExternalIDReassigner {
	if s == nil {
		return nil
	}
	if eid, ok := s.(ExternalIDReassigner); ok {
		return eid
	}
	return nil
}

// Service handles merging duplicate books into version groups.
//
// MergeBooks and CombineBooks serialize their read-modify-writes on the
// package-level mergeSerializeMu (see serialize.go), shared with the sibling
// dedup.MergeBooks path so any two merges are mutually exclusive on a shared
// book row.
type Service struct {
	db               database.Store
	writeBackBatcher WriteBackEnqueuer
	syncFollower     SyncFollower
}

// SetWriteBackBatcher sets the iTunes write-back batcher.
func (ms *Service) SetWriteBackBatcher(b WriteBackEnqueuer) {
	ms.writeBackBatcher = b
}

// SetSyncFollower overrides the sync-identity follower wired by NewService.
//
// Why a settable field instead of a type assertion at the call site: tests (and
// some production wiring) hand this Service a wrapper that EMBEDS
// database.Store — e.g. serializeProbe in service_concurrent_test.go — and a
// database.AsSyncIdentityStore assertion through such a wrapper returns nil,
// which would silently no-op the whole hook without failing anything. Injecting
// once, mirroring SetWriteBackBatcher, makes that failure mode explicit.
func (ms *Service) SetSyncFollower(f SyncFollower) {
	ms.syncFollower = f
}

// Result contains the outcome of a merge operation.
type Result struct {
	PrimaryID      string `json:"primary_id"`
	VersionGroupID string `json:"version_group_id"`
	MergedCount    int    `json:"merged_count"`
}

// NewService creates a new Service. The sync-identity follower is wired
// automatically when db supports it (the production *PebbleStore does); stores
// that do not — mocks, wrappers that embed database.Store — yield nil and the
// merge paths simply do not touch sync identity. Override with
// SetSyncFollower.
func NewService(db database.Store) *Service {
	follower := database.AsSyncIdentityStore(db)
	if follower == nil {
		// Say so out loud. A nil follower silently disables the whole
		// merge-follow hook, and the only way it can happen in production is
		// someone wrapping the concrete store in a decorator that embeds
		// database.Store — exactly the kind of change whose blast radius is
		// invisible otherwise.
		slog.Warn("merge: store does not implement SyncIdentityStore; merges will NOT carry ABS sync identity or listening progress")
	}
	return &Service{db: db, syncFollower: follower}
}

// MergeBooks merges a set of books into a single version group.
//
// Semantics (confirmed 2026-04-11 after an investigation into
// orphaned ITL entries):
//
//  1. Winner is chosen (user-supplied primaryID or auto-picked
//     via BookIsBetter) and given IsPrimaryVersion=true. Losers
//     get IsPrimaryVersion=false and are soft-deleted.
//  2. External IDs (iTunes PIDs, Audible ASINs, etc.) are
//     reassigned from losers to the winner so lookups still
//     resolve to the surviving entity.
//  3. **iTunes ITL cleanup**: before reassignment, we collect
//     each loser's iTunes PIDs and enqueue them for removal via
//     writeBackBatcher.EnqueueRemove. This matches the
//     behavior of maintenance_fixups.mergeDuplicateBook — the
//     UI merge path used to skip this step, which left the
//     losers' tracks alive in the iTunes library forever.
//  4. Loser DB rows are soft-deleted (MarkedForDeletion=true).
//     They stay recoverable via the existing soft-delete
//     restore flow for at least the retention window.
//  5. Loser files on disk are NOT touched — they remain
//     playable until an archive sweep (not yet implemented)
//     cleans them up.
//
// If primaryID is empty, the best book is auto-selected (M4B
// preferred, then highest bitrate, then largest file).
// If primaryID is provided, that book is set as the primary.
func (ms *Service) MergeBooks(bookIDs []string, primaryID string) (*Result, error) {
	// De-duplicate the incoming ID list before anything else. Every current
	// caller either de-dupes itself or trusts a request body (e.g. the
	// /audiobooks/merge handler passes req.BookIDs straight through) — if
	// duplicate IDs reach here, the version-group loop below writes that
	// book's row twice, and the LAST write wins. A duplicated primary would
	// get its own IsPrimaryVersion=true immediately overwritten back to
	// false by its second occurrence, while the loser-cleanup loop (which
	// skips only book.ID == resolvedPrimaryID) still treats both occurrences
	// as "the primary" and never soft-deletes it — leaving the book neither
	// primary nor soft-deleted, and the version group with NO live primary.
	// This is the exact corruption class applyBookMergeReroute (#2007 /
	// F6-T10) already guards against by de-duping before calling in, but that
	// guard lived only at that one caller; enforcing it here protects every
	// caller, present and future, regardless of whether it remembers to
	// de-dupe first.
	seen := make(map[string]bool, len(bookIDs))
	deduped := make([]string, 0, len(bookIDs))
	for _, id := range bookIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		deduped = append(deduped, id)
	}
	bookIDs = deduped

	if len(bookIDs) < 2 {
		return nil, fmt.Errorf("need at least 2 book IDs to merge")
	}

	// Serialize the entire read-modify-write against every other merge-family
	// path (MergeBooks / CombineBooks / dedup.MergeBooks) — see the
	// mergeSerializeMu doc comment. Scoped to the merge itself; nothing
	// slow/blocking runs while it is held.
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()

	// Fetch all books
	var books []*database.Book
	for _, id := range bookIDs {
		book, err := ms.db.GetBookByID(id)
		if err != nil || book == nil {
			return nil, fmt.Errorf("book %s not found", id)
		}
		books = append(books, book)
	}

	// Determine primary index
	bestIdx := 0
	if primaryID != "" {
		found := false
		for i, b := range books {
			if b.ID == primaryID {
				bestIdx = i
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("primary_id %s not in book_ids", primaryID)
		}
	} else {
		// Auto-select best: M4B preferred, then highest bitrate, then largest file
		for i := 1; i < len(books); i++ {
			if BookIsBetter(books[i], books[bestIdx]) {
				bestIdx = i
			}
		}
	}

	// Determine version group ID (reuse if any book already has one)
	versionGroupID := ""
	for _, b := range books {
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			versionGroupID = *b.VersionGroupID
			break
		}
	}
	if versionGroupID == "" {
		versionGroupID = ulid.Make().String()
	}

	// Update all books to share the version group. Winner is
	// marked primary; losers are marked non-primary. We still
	// persist the flag on losers here so the version group is
	// queryable and the relationship survives through the
	// soft-delete call below.
	resolvedPrimaryID := books[bestIdx].ID
	for i, book := range books {
		book.VersionGroupID = &versionGroupID
		isPrimary := i == bestIdx
		book.IsPrimaryVersion = &isPrimary
		if _, err := ms.db.UpdateBook(book.ID, book); err != nil {
			return nil, fmt.Errorf("failed to update book %s: %w", book.ID, err)
		}
	}

	// --- Per-loser cleanup ---
	//
	// For each non-primary book we:
	//  (a) collect its iTunes PIDs BEFORE reassignment so we
	//      know which tracks to remove from the ITL,
	//  (b) reassign all external IDs to the winner so future
	//      lookups resolve,
	//  (c) enqueue ITL removals for the collected PIDs so
	//      iTunes no longer shows duplicate tracks for this
	//      version group,
	//  (d) soft-delete the loser so it drops off the default
	//      library view. Files on disk are left alone for the
	//      archive sweep to handle later.
	eidStore := AsExternalIDReassigner(ms.db)
	for _, book := range books {
		if book.ID == resolvedPrimaryID {
			continue
		}

		// (a) Collect PIDs before reassignment.
		var dupPIDs []string
		if mappings, err := ms.db.GetExternalIDsForBook(book.ID); err == nil {
			for _, m := range mappings {
				if m.Source == "itunes" && m.ExternalID != "" && !m.Tombstoned {
					dupPIDs = append(dupPIDs, m.ExternalID)
				}
			}
		}

		// (b) Reassign external IDs to the winner.
		if eidStore != nil {
			if err := eidStore.ReassignExternalIDs(book.ID, resolvedPrimaryID); err != nil {
				slog.Warn("merge ReassignExternalIDs", "from", book.ID, "to", resolvedPrimaryID, "err", err)
			}
		}

		// (c) Queue iTunes removals for the loser's tracks so
		// the ITL stops showing them. Best-effort — a nil
		// batcher (e.g. tests, or iTunes write-back disabled)
		// means we just skip.
		if ms.writeBackBatcher != nil && len(dupPIDs) > 0 {
			for _, pid := range dupPIDs {
				ms.writeBackBatcher.EnqueueRemove(pid)
			}
			slog.Info("merge queued ITL removals for loser", "count", len(dupPIDs), "id", book.ID)
		}

		// (d) Soft-delete the loser. If UpdateBook fails inside
		// SoftDeleteBook it falls back to hard delete, so we
		// never leave a zombie non-primary row behind.
		if err := SoftDeleteBook(ms.db, book.ID); err != nil {
			slog.Warn("merge soft-delete", "id", book.ID, "err", err)
		}
	}

	// --- Sync-identity + progress follow ---
	//
	// Still inside mergeSerializeMu (see the Lock above): that single
	// process-wide lock already makes every merge-family read-modify-write
	// mutually exclusive with every other one, so anything added here is
	// exactly-once with respect to concurrent merges by construction. Do NOT
	// "fix" this by adding per-book-pair partitioning on top — CLAUDE.md's
	// partition-by-book-ID guidance targets whole-library batch loops, not a
	// single already-fully-serialized RMW, and a second scheme could subtly
	// fight this lock.
	//
	// The winner's ULID never changes here (resolvedPrimaryID is an existing
	// book's ID) and losers are only soft-deleted, so the winner's reverse
	// index needs no repoint: this is a loser-only redirect. Kept as its own
	// separately-testable step rather than folded into the loop above.
	losers := make([]string, 0, len(books)-1)
	for _, book := range books {
		if book.ID != resolvedPrimaryID {
			losers = append(losers, book.ID)
		}
	}
	FollowMerge(ms.db, ms.syncFollower, resolvedPrimaryID, losers)

	return &Result{
		PrimaryID:      resolvedPrimaryID,
		VersionGroupID: versionGroupID,
		MergedCount:    len(books),
	}, nil
}

// CombineResult is the outcome of a CombineBooks call.
type CombineResult struct {
	PrimaryID    string `json:"primary_id"`
	FilesMoved   int    `json:"files_moved"`
	BooksDeleted int    `json:"books_deleted"`
}

// CombineOverride holds optional metadata fields to apply to the survivor book
// after all files have been reassigned. Only non-empty strings take effect;
// omitting a field leaves the survivor's existing value untouched.
type CombineOverride struct {
	Title    string `json:"title,omitempty"`
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
}

// CombineBooks combines several books into ONE multi-file book — distinct from
// MergeBooks, which links them as alternate VERSIONS in a version group. Every
// selected book's audio files become real BookFiles on the survivor, and the
// absorbed shells are hard-deleted. This is the manual analogue of the
// shattered-book heal (applyFSRegroup): use it to reassemble tracks that were
// imported as one book per file (e.g. an untagged folder of chapters).
//
// DB-only: files stay where they are on disk (most shattered sets are already in
// one folder). Run organize afterward to physically co-locate if desired.
//
// override is optional: non-empty fields overwrite the survivor's metadata after
// the combine. Leave it nil or empty to keep the survivor's existing metadata.
func (ms *Service) CombineBooks(bookIDs []string, primaryID string, override *CombineOverride) (*CombineResult, error) {
	if len(bookIDs) < 2 {
		return nil, fmt.Errorf("need at least 2 books to combine")
	}
	if primaryID == "" {
		return nil, fmt.Errorf("primary_id is required for combine")
	}

	// Serialize the entire read-modify-write against every other merge-family
	// path (MergeBooks / CombineBooks / dedup.MergeBooks). CombineBooks is a
	// synchronous HTTP handler with no concurrency key, so two combines — or a
	// combine racing a MergeBooks on a shared book — can otherwise interleave
	// GetBookByID -> MoveBookFilesToBook -> ReassignExternalIDs -> DeleteBook
	// -> UpdateBook and corrupt the same way #1930 fixed for MergeBooks. Scoped
	// to the combine itself; only local DB work runs while it is held.
	// (CombineBooks never calls MergeBooks or dedup.MergeBooks, so the
	// non-reentrant mutex is taken exactly once.)
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()

	survivor, err := ms.db.GetBookByID(primaryID)
	if err != nil || survivor == nil {
		return nil, fmt.Errorf("primary book %s not found", primaryID)
	}
	// Validate all IDs up front so a bad ID aborts before any mutation.
	seen := map[string]bool{}
	for _, id := range bookIDs {
		if seen[id] {
			return nil, fmt.Errorf("duplicate book id %s", id)
		}
		seen[id] = true
		b, err := ms.db.GetBookByID(id)
		if err != nil || b == nil {
			return nil, fmt.Errorf("book %s not found", id)
		}
	}
	if !seen[primaryID] {
		return nil, fmt.Errorf("primary_id %s not in book_ids", primaryID)
	}

	res := &CombineResult{PrimaryID: primaryID}
	eidStore := AsExternalIDReassigner(ms.db)

	// Materialize the survivor's own single-file (virtual-segment) audio as a
	// BookFile so the combined book owns ALL its files explicitly.
	res.FilesMoved += ms.ensureOwnFile(survivor)

	for _, id := range bookIDs {
		if id == primaryID {
			continue
		}
		book, _ := ms.db.GetBookByID(id)
		if book == nil {
			continue
		}

		// Attach this book's files to the survivor.
		files, _ := ms.db.GetBookFiles(id)
		if len(files) > 0 {
			ids := make([]string, len(files))
			for i := range files {
				ids[i] = files[i].ID
			}
			if err := ms.db.MoveBookFilesToBook(ids, id, survivor.ID); err != nil {
				return nil, fmt.Errorf("move files %s->%s: %w", id, survivor.ID, err)
			}
			res.FilesMoved += len(files)
		} else if book.FilePath != "" {
			res.FilesMoved += ms.attachVirtualFile(book, survivor.ID)
		}

		// Reassign external IDs to the survivor.
		if eidStore != nil {
			if err := eidStore.ReassignExternalIDs(id, survivor.ID); err != nil {
				slog.Warn("combine ReassignExternalIDs", "from", id, "to", survivor.ID, "err", err)
			}
		}

		// Carry the absorbed shell's sync identity and listening position onto
		// the survivor BEFORE the hard delete below. Unlike MergeBooks (which
		// soft-deletes), there is no surviving row to repoint afterwards, so a
		// missed follow here is unrecoverable: the client's stored
		// libraryItemId would resolve to nothing forever. Still inside
		// mergeSerializeMu, so this is exactly-once w.r.t. every other merge.
		FollowMerge(ms.db, ms.syncFollower, survivor.ID, []string{id})

		// Guard (mirrors applyFSRegroup): never delete a book that still owns
		// files — that would orphan audio. After the move it must be empty.
		if remaining, _ := ms.db.GetBookFiles(id); len(remaining) != 0 {
			return nil, fmt.Errorf("book %s still owns %d files after move; aborting delete", id, len(remaining))
		}
		if err := ms.db.DeleteBook(id); err != nil {
			return nil, fmt.Errorf("delete absorbed book %s: %w", id, err)
		}
		res.BooksDeleted++
	}

	if err := ms.db.RecomputeBookAggregates(survivor.ID); err != nil {
		slog.Warn("combine RecomputeBookAggregates", "id", survivor.ID, "err", err)
	}

	// Apply metadata overrides to the survivor. UpdateBook does a full column
	// replacement, so we re-fetch after the aggregate recompute and patch only
	// the non-empty override fields.
	if override != nil && (override.Title != "" || override.Author != "" || override.Narrator != "") {
		fresh, err := ms.db.GetBookByID(primaryID)
		if err == nil && fresh != nil {
			if override.Title != "" {
				fresh.Title = override.Title
			}
			if override.Narrator != "" {
				fresh.Narrator = &override.Narrator
			}
			if _, err := ms.db.UpdateBook(fresh.ID, fresh); err != nil {
				slog.Warn("combine override UpdateBook", "id", fresh.ID, "err", err)
			} else {
				slog.Info("combine applied metadata override", "id", fresh.ID,
					"title", override.Title, "narrator", override.Narrator)
			}
		}
		// Author resolution: find or create by name, then link to the survivor.
		if override.Author != "" {
			author, err := ms.db.GetAuthorByName(override.Author)
			if err == nil && author == nil {
				author, err = ms.db.CreateAuthor(override.Author)
			}
			if err == nil && author != nil {
				// Surface failures instead of swallowing them: a dropped write here
				// silently discards the user's explicit author choice while Combine
				// still reports success (matches the sibling title/narrator path above).
				if saErr := ms.db.SetBookAuthors(primaryID, []database.BookAuthor{
					{BookID: primaryID, AuthorID: author.ID, Role: "author", Position: 0},
				}); saErr != nil {
					slog.Warn("combine override SetBookAuthors", "id", primaryID, "author", override.Author, "err", saErr)
				}
				// Also set AuthorID on the book row for backward compat.
				if b, err2 := ms.db.GetBookByID(primaryID); err2 == nil && b != nil {
					b.AuthorID = &author.ID
					if _, ubErr := ms.db.UpdateBook(b.ID, b); ubErr != nil {
						slog.Warn("combine override author UpdateBook", "id", b.ID, "err", ubErr)
					}
				}
			}
			if err != nil {
				slog.Warn("combine override author", "name", override.Author, "err", err)
			}
		}
	}

	slog.Info("combined books into one", "survivor", survivor.ID,
		"files_moved", res.FilesMoved, "books_deleted", res.BooksDeleted)
	return res, nil
}

// ensureOwnFile materializes a single-file book's FilePath as a BookFile when it
// has no BookFile rows yet (the virtual-segment model). Returns files created (0/1).
func (ms *Service) ensureOwnFile(b *database.Book) int {
	if b.FilePath == "" {
		return 0
	}
	if files, _ := ms.db.GetBookFiles(b.ID); len(files) > 0 {
		return 0 // already materialized
	}
	return ms.attachVirtualFile(b, b.ID)
}

// attachVirtualFile creates (or reattaches) a BookFile at book.FilePath owned by
// targetBookID. Reattach-safe per #1549: an existing row at that path is MOVED
// (its BookID can't be changed in place — the primary key embeds it), never
// duplicated. Returns files attached (0/1).
func (ms *Service) attachVirtualFile(b *database.Book, targetBookID string) int {
	existing, _ := ms.db.GetBookFileByPath(b.FilePath)
	if existing != nil {
		if existing.BookID != targetBookID {
			if err := ms.db.MoveBookFilesToBook([]string{existing.ID}, existing.BookID, targetBookID); err != nil {
				slog.Warn("combine reattach existing file", "path", b.FilePath, "err", err)
				return 0
			}
		}
		return 1
	}
	bf := &database.BookFile{
		ID:       ulid.Make().String(),
		BookID:   targetBookID,
		FilePath: b.FilePath,
		Format:   strings.TrimPrefix(strings.ToLower(filepath.Ext(b.FilePath)), "."),
	}
	if b.Duration != nil {
		bf.Duration = *b.Duration
	}
	if err := ms.db.CreateBookFile(bf); err != nil {
		slog.Warn("combine create file", "path", b.FilePath, "err", err)
		return 0
	}
	return 1
}

// SoftDeleteBook marks a book as deleted using the MarkedForDeletion flag.
// If UpdateBook fails, falls back to hard-delete via DeleteBook.
func SoftDeleteBook(store database.Store, bookID string) error {
	current, err := store.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("GetBookByID %s: %w", bookID, err)
	}
	if current == nil {
		return nil // Already gone
	}

	t := true
	now := time.Now()
	current.MarkedForDeletion = &t
	current.MarkedForDeletionAt = &now

	if _, upErr := store.UpdateBook(bookID, current); upErr != nil {
		// Fall back to hard delete.
		slog.Warn("dedup-books soft-delete failed, falling back to hard delete", "id", bookID, "err", upErr)
		return store.DeleteBook(bookID)
	}
	return nil
}

// IsITunesGhostPath reports whether a book's file path points at the
// iTunes media folder rather than the managed audiobook-organizer library.
func IsITunesGhostPath(p string) bool {
	if p == "" {
		return false
	}
	lower := strings.ToLower(p)
	return strings.Contains(lower, "/itunes media/") || strings.Contains(lower, "/itunes/itunes")
}

// BookCurationScore returns a coarse "how much effort has the user put into
// this entry" score. Higher means more curated.
//
// Signals, each worth one point:
//   - MetadataReviewStatus == "matched" (user explicitly accepted a match)
//   - LastWrittenAt set (tags have been written back to the file)
//   - MetadataUpdatedAt strictly newer than CreatedAt (user-visible metadata
//     field has been edited since the row was created)
func BookCurationScore(b *database.Book) int {
	score := 0
	if b.MetadataReviewStatus != nil && *b.MetadataReviewStatus == "matched" {
		score++
	}
	if b.LastWrittenAt != nil {
		score++
	}
	if b.MetadataUpdatedAt != nil && b.CreatedAt != nil && b.MetadataUpdatedAt.After(*b.CreatedAt) {
		score++
	}
	return score
}

// BookIsBetter returns true if a is a "better" primary version than b.
// Preference order (strongest first):
//  1. Organized library path over iTunes-ghost path
//  2. Higher curation score (user effort beats technical quality)
//  3. M4B over other formats
//  4. Higher bitrate
//  5. Larger file size
func BookIsBetter(a, b *database.Book) bool {
	aGhost := IsITunesGhostPath(a.FilePath)
	bGhost := IsITunesGhostPath(b.FilePath)
	if aGhost != bGhost {
		return !aGhost
	}

	aCur := BookCurationScore(a)
	bCur := BookCurationScore(b)
	if aCur != bCur {
		return aCur > bCur
	}

	aM4B := strings.EqualFold(a.Format, "m4b")
	bM4B := strings.EqualFold(b.Format, "m4b")
	if aM4B != bM4B {
		return aM4B
	}
	aBitrate := 0
	if a.Bitrate != nil {
		aBitrate = *a.Bitrate
	}
	bBitrate := 0
	if b.Bitrate != nil {
		bBitrate = *b.Bitrate
	}
	if aBitrate != bBitrate {
		return aBitrate > bBitrate
	}
	aSize := int64(0)
	if a.FileSize != nil {
		aSize = *a.FileSize
	}
	bSize := int64(0)
	if b.FileSize != nil {
		bSize = *b.FileSize
	}
	return aSize > bSize
}
