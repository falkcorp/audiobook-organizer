// file: internal/dedup/book_dedup.go
// version: 1.10.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-09-14

// Package dedup: book_dedup.go contains the extracted execution logic for the
// "dedup.book-scan" and "dedup.book-merge" async operations.  The *Server
// wrappers in internal/server/duplicates_ops.go are now thin callers that hand
// results back to server-owned state (dedupCache, etc.).
package dedup

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	ulid "github.com/oklog/ulid/v2"
)

const (
	metadataDuplicateThreshold = 0.85
	metadataBorderlineFloor    = 0.80
	metadataBorderlineCeiling  = 0.88
)

// bookMergeLog is MergeBooks' logger.New printf-style logger.
var bookMergeLog = logger.New("dedup.book-merge")

// BookDupGroup is a group of books that are likely duplicates of each other.
type BookDupGroup struct {
	Books      []database.Book `json:"books"`
	Confidence string          `json:"confidence"` // "high", "medium", "low"
	Reason     string          `json:"reason"`
	GroupKey   string          `json:"group_key"`
}

// BookScanResult is the result of ScanBookDuplicates.
type BookScanResult struct {
	Groups          []BookDupGroup
	TotalDuplicates int
}

// coreGroupsToBookGroups widens [][]database.BookCore to [][]database.Book
// via .ToBook() (heavy fields left at their zero value — never populated by
// GetFolderDuplicatesCore/GetDuplicateBooksByMetadataCore in the first
// place, so this is not a lossy conversion). Used at the ScanBookDuplicates
// store boundary so the shared addGroups/tiebreaker pipeline (which stays
// []Book to match hashGroups from GetDuplicateBooks and the public
// BookDupGroup.Books JSON contract) doesn't need its own Core variant.
func coreGroupsToBookGroups(groups [][]database.BookCore) [][]database.Book {
	if groups == nil {
		return nil
	}
	out := make([][]database.Book, len(groups))
	for i, group := range groups {
		converted := make([]database.Book, len(group))
		for j, c := range group {
			converted[j] = c.ToBook()
		}
		out[i] = converted
	}
	return out
}

// ScanBookDuplicates runs the three-tier duplicate-book scan (hash, folder,
// metadata fuzzy) against store, filters out keys present in dismissed, and
// returns a consolidated list of BookDupGroup values along with the total
// number of duplicate books (i.e. sum of len(group.Books)-1 across all
// groups).
//
// dismissed maps stable group keys (sorted book IDs joined by "+") to true;
// any group whose key is in the map is silently dropped.
//
// progress may be nil; all calls are guarded.
func ScanBookDuplicates(
	_ context.Context,
	store Store,
	dismissed map[string]bool,
	progress ProgressReporter,
) (BookScanResult, error) {
	// Fixed-step scan: 5 stages (start, hash, folder, metadata, merge, done).
	const totalSteps = 5
	report := func(step int, msg string) {
		if progress != nil {
			pct := float64(step) * 100.0 / float64(totalSteps)
			_ = progress.UpdateProgress(step, totalSteps,
				fmt.Sprintf("%s (%d/%d, %.2f%%)", msg, step, totalSteps, pct))
		}
	}

	report(0, "Scanning for duplicate books...")

	// Step 1: Hash-based duplicates (high confidence)
	report(1, "Finding hash-based duplicates...")
	hashGroups, err := store.GetDuplicateBooks()
	if err != nil {
		return BookScanResult{}, fmt.Errorf("hash-based dedup failed: %w", err)
	}

	// Step 2: Folder duplicates (same title in same folder)
	report(2, "Finding folder-based duplicates...")
	folderGroupsCore, err := store.GetFolderDuplicatesCore()
	if err != nil {
		slog.Warn("folder dedup failed", "error", err)
		folderGroupsCore = nil
	}
	// Converted immediately at the store boundary — everything downstream
	// (addGroups, dismissed-key lookups) only ever reads Core-safe fields
	// (ID, Title), so .ToBook() here is a lossless widen, not the
	// hydrate-before-writeback landmine (this pipeline never writes a book
	// back; it only reports duplicate groups).
	folderGroups := coreGroupsToBookGroups(folderGroupsCore)

	// Step 3: Metadata-based fuzzy matching
	report(3, "Finding metadata-based duplicates...")
	metadataGroupsCore, err := store.GetDuplicateBooksByMetadataCore(metadataBorderlineFloor)
	if err != nil {
		slog.Warn("metadata dedup failed", "error", err)
		metadataGroupsCore = nil
	}
	metadataGroups := coreGroupsToBookGroups(metadataGroupsCore)
	metadataGroups, metadataConfidence, metadataReason := applyTranscriptionMetadataTiebreaker(store, metadataGroups)

	report(4, "Merging results...")

	// Combine all groups, deduplicating by book ID.
	seenBookIDs := map[string]bool{}
	var allGroups []BookDupGroup

	addGroups := func(groups [][]database.Book, confidence, reason string) {
		for _, group := range groups {
			// Skip if every book in this group has already been claimed.
			allSeen := true
			for _, b := range group {
				if !seenBookIDs[b.ID] {
					allSeen = false
					break
				}
			}
			if allSeen {
				continue
			}
			// Generate a stable group key from sorted book IDs.
			ids := make([]string, len(group))
			for i, b := range group {
				ids[i] = b.ID
			}
			groupKey := strings.Join(ids, "+")
			if dismissed[groupKey] {
				continue
			}
			allGroups = append(allGroups, BookDupGroup{
				Books:      group,
				Confidence: confidence,
				Reason:     reason,
				GroupKey:   groupKey,
			})
			for _, b := range group {
				seenBookIDs[b.ID] = true
			}
		}
	}

	addGroups(hashGroups, "high", "Identical file hash")
	addGroups(folderGroups, "medium", "Same title in same folder")
	addGroups(metadataGroups, "low", "Similar title and author")
	for key, confidence := range metadataConfidence {
		for i := range allGroups {
			if allGroups[i].GroupKey == key {
				allGroups[i].Confidence = confidence
				allGroups[i].Reason = metadataReason[key]
			}
		}
	}

	totalDuplicates := 0
	for _, g := range allGroups {
		totalDuplicates += len(g.Books) - 1
	}

	report(5, fmt.Sprintf("Found %d duplicate groups (%d duplicates)", len(allGroups), totalDuplicates))

	return BookScanResult{
		Groups:          allGroups,
		TotalDuplicates: totalDuplicates,
	}, nil
}

func applyTranscriptionMetadataTiebreaker(
	store Store,
	groups [][]database.Book,
) ([][]database.Book, map[string]string, map[string]string) {
	confidence := map[string]string{}
	reason := map[string]string{}
	filtered := make([][]database.Book, 0, len(groups))

	for _, group := range groups {
		if len(group) != 2 {
			filtered = append(filtered, group)
			continue
		}

		sim := metadataPairSimilarity(store, &group[0], &group[1])
		if sim < metadataBorderlineFloor {
			continue
		}

		agreement := 0
		if sim >= metadataBorderlineFloor && sim <= metadataBorderlineCeiling {
			agreement = transcriptionAgreement(&group[0], &group[1])
		}

		if sim < metadataDuplicateThreshold && agreement <= 0 {
			continue
		}
		if sim >= metadataDuplicateThreshold && agreement < 0 {
			continue
		}

		filtered = append(filtered, group)
		if agreement > 0 {
			key := groupKeyForBooks(group)
			confidence[key] = "medium"
			reason[key] = "Similar metadata with matching transcribed title"
		}
	}

	return filtered, confidence, reason
}

// transcriptionAgreement returns +1 when both books have matching transcribed
// titles, -1 when present transcribed titles clearly differ, and 0 when
// transcription is absent or unusable.
func transcriptionAgreement(a, b *database.Book) int {
	aTitle, okA := normalizedTranscribedTitle(a)
	bTitle, okB := normalizedTranscribedTitle(b)
	if !okA || !okB {
		return 0
	}
	if aTitle != bTitle {
		return -1
	}

	aAuthor, aAuthorOK := stringPtrValue(a.TranscribedAuthor)
	bAuthor, bAuthorOK := stringPtrValue(b.TranscribedAuthor)
	if aAuthorOK && bAuthorOK && !containsCI(aAuthor, bAuthor) {
		return -1
	}

	return 1
}

func metadataPairSimilarity(store Store, a, b *database.Book) float64 {
	aAuthor := bookAuthorName(store, a)
	bAuthor := bookAuthorName(store, b)
	return metaTitleAuthorSimilarity(
		[]string{normalizeTitle(a.Title)},
		aAuthor,
		[]string{normalizeTitle(b.Title)},
		bAuthor,
	)
}

func bookAuthorName(store Store, book *database.Book) string {
	if book == nil || book.AuthorID == nil {
		return ""
	}
	author, err := store.GetAuthorByID(*book.AuthorID)
	if err != nil || author == nil {
		return ""
	}
	return author.Name
}

func normalizedTranscribedTitle(book *database.Book) (string, bool) {
	if book == nil {
		return "", false
	}
	title, ok := stringPtrValue(book.TranscribedTitle)
	if !ok {
		return "", false
	}
	title = util.NormalizeTitle(title)
	if len([]rune(title)) < 3 {
		return "", false
	}
	switch title {
	case "unknown", "untitled", "title", "n/a", "na":
		return "", false
	default:
		return title, true
	}
}

func stringPtrValue(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

func containsCI(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	al, bl := strings.ToLower(a), strings.ToLower(b)
	return strings.Contains(al, bl) || strings.Contains(bl, al)
}

func groupKeyForBooks(group []database.Book) string {
	ids := make([]string, len(group))
	for i, b := range group {
		ids[i] = b.ID
	}
	return strings.Join(ids, "+")
}

// BookMergeResult summarises the outcome of MergeBooks.
type BookMergeResult struct {
	// UpdatedKeepBook is the keep book after iTunes metadata has been transferred
	// in and the UpdateBook call has been issued.  Callers may use it to
	// invalidate caches or issue further side-effects.
	UpdatedKeepBook *database.Book
	MergedCount     int
	Errors          []string
}

// MergeBooks transfers useful metadata from each merge book to the keep book,
// deletes the merge books, and records OperationChange rows.  It does NOT
// invalidate any server-side cache — the caller is responsible for that.
//
// opID is the legacy operation ID written into OperationChange records.
// keepID is the ID of the book to keep; every ID in mergeIDs is deleted.
//
// TransferITunesMetadataFirstWin forwards to merge.TransferITunesMetadataFirstWin,
// where the copy now lives so merge.Service can apply it inside its merge lock
// (merge cannot import dedup). See that function for the semantics.
func TransferITunesMetadataFirstWin(keep, from *database.Book) {
	merge.TransferITunesMetadataFirstWin(keep, from)
}

// guardKeeperAudioRoute refuses a merge whose keeper cannot reach any audio
// while a book it is about to hard-delete can (TODO.md:2304).
//
// Why this path needs it at all: MergeBooks takes keepID exactly as the caller
// hands it over and removes every mergeIDs row with store.DeleteBook. Unlike
// merge.Service.MergeBooks — which soft-deletes and elects its survivor with
// merge.HasAudioRoute — there is nothing left to restore here, so a heal that
// nominates a ghost row (no book_file rows, empty FilePath) as the keeper
// destroys the only rows that reached the audio, permanently.
//
// The tier is the sibling's, not a stricter one: the refusal fires only when
// the KEEPER is file-less and some loser is not. A merge in which no
// participant has a route is still allowed — there is no audio to lose, and
// refusing it would make the file-less ghost class (exactly what
// internal/reconcile/itunes_heal.go exists to collapse) impossible to tidy.
//
// Fail CLOSED on a read error, as the Service guard does: if we cannot tell
// whether a row has a route, we do not delete it. Note this is stricter than
// the merge loop below, which records a per-loser failure in result.Errors and
// carries on — a store too sick to answer "does this book have files" is not a
// store we hard-delete rows in.
//
// The typed refusal is merge.FilelessPrimaryError rather than a dedup-local
// type so callers get merge.IsRefusal(err) and the same errors.As shape the
// HTTP merge paths already use.
func guardKeeperAudioRoute(store Store, keepID string, keepBook *database.Book, mergeIDs []string) error {
	keepFiles, err := store.GetBookFiles(keepID)
	if err != nil {
		return fmt.Errorf("cannot verify the audio route of keep book %s, refusing to merge: %w", keepID, err)
	}
	if merge.HasAudioRoute(keepBook, keepFiles) {
		return nil
	}

	// seen pre-loaded with keepID: a self-merge entry is skipped by the merge
	// loop and is never deleted, so it is not a reason to refuse.
	seen := map[string]bool{keepID: true}
	var fileBearing []string
	for _, mergeID := range mergeIDs {
		if seen[mergeID] {
			continue
		}
		seen[mergeID] = true
		mergeBook, err := store.GetBookByID(mergeID)
		if err != nil {
			return fmt.Errorf("cannot verify the audio route of book %s, refusing to merge: %w", mergeID, err)
		}
		if mergeBook == nil {
			// The merge loop already records this as "book %s not found";
			// a row that does not exist cannot be deleted or lose audio.
			continue
		}
		files, err := store.GetBookFiles(mergeID)
		if err != nil {
			return fmt.Errorf("cannot verify the audio route of book %s, refusing to merge: %w", mergeID, err)
		}
		if merge.HasAudioRoute(mergeBook, files) {
			fileBearing = append(fileBearing, mergeID)
		}
	}
	if len(fileBearing) > 0 {
		return &merge.FilelessPrimaryError{PrimaryID: keepID, FileBearing: fileBearing}
	}
	return nil
}

// bookAudioPaths returns the cleaned set of paths a book reaches audio
// through: its own FilePath and every book_file FilePath.
func bookAudioPaths(store Store, b *database.Book) (map[string]bool, error) {
	files, err := store.GetBookFiles(b.ID)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]bool, len(files)+1)
	add := func(p string) {
		if p != "" {
			paths[filepath.Clean(p)] = true
		}
	}
	add(b.FilePath)
	for _, f := range files {
		add(f.FilePath)
	}
	return paths, nil
}

// retireMergedLoser retires one live loser of dedup.MergeBooks in the order
// merge.Service.MergeBooks uses, and returns an error (leaving the loser LIVE)
// if any step fails, so a retried heal re-enters it:
//
//  1. Shared-path check. A purge with delete-files removes a soft-deleted
//     book's own FilePath (os.Remove when it is not a directory) and its
//     book_file paths. If any loser path equals, or lies inside, one of the
//     kept book's audio paths, soft-deleting the loser would put audio the
//     kept book reaches on the purge clock, so it is refused. "Inside" matters
//     for the ~20% of books whose FilePath is a directory with no book_file
//     rows: a loser file under that directory is the kept book's audio too.
//     The reverse direction (a loser directory above the kept files) is also
//     refused; purge would only rmdir it once empty, so this is conservative.
//  2. Reassign the loser's external IDs to the kept book. Done before the
//     soft delete: purge tombstones every ext_id still on a soft-deleted
//     book, so a PID left on the loser would later be tombstoned and block
//     its own re-import. A nil eidStore means no external IDs.
//  3. Soft-delete. The row, its metadata, locks and book_file rows survive,
//     so the loser can be restored.
//
// The caller skips an already-soft-deleted loser before calling this.
func retireMergedLoser(store Store, eidStore merge.ExternalIDReassigner, keepID string, loser *database.Book, keepAudioPaths map[string]bool) error {
	loserPaths, err := bookAudioPaths(store, loser)
	if err != nil {
		return fmt.Errorf("read audio paths of loser %s: %w", loser.ID, err)
	}
	var shared []string
	for lp := range loserPaths {
		for kp := range keepAudioPaths {
			// IsWithin counts equality, so each direction also catches an
			// exact match.
			if pathutil.IsWithin(lp, kp) || pathutil.IsWithin(kp, lp) {
				shared = append(shared, lp)
				break
			}
		}
	}
	if len(shared) > 0 {
		sort.Strings(shared)
		return fmt.Errorf("loser %s shares audio path(s) %s with keep book %s; a purge with file deletion would remove the kept audio",
			loser.ID, strings.Join(shared, ", "), keepID)
	}
	if eidStore != nil {
		if err := eidStore.ReassignExternalIDs(loser.ID, keepID); err != nil {
			return fmt.Errorf("reassign external IDs of loser %s to %s: %w", loser.ID, keepID, err)
		}
	}
	if err := merge.SoftDeleteBook(store, loser.ID); err != nil {
		return fmt.Errorf("soft-delete loser %s: %w", loser.ID, err)
	}
	return nil
}

// Concurrency: this is a package-level function (NOT merge.Service.MergeBooks)
// with its own unguarded read-modify-write, reached from two async ops with
// DIFFERENT ConcurrencyKeys (dedup.book-merge and the iTunes-heal op), so it can
// race itself AND merge.Service.MergeBooks/CombineBooks on a shared book row. It
// therefore acquires the SAME process-wide lock those Service methods use
// (merge.LockMergeRMW, see internal/merge/serialize.go) for the whole
// read-modify-write, making every merge atomic w.r.t. every other merge. The
// per-book loop is bounded by one request's merge set and does only local DB /
// in-memory progress work under the lock — nothing network/large-scan. Semantics
// differ from merge.Service.MergeBooks (iTunes-metadata transfer, no version
// group), so it only shares the lock. Non-reentrant: this never calls the
// Service merge paths, so the lock is taken exactly once.
//
// F6 (2026-07-18): the POST /audiobooks/merge endpoint (dedup.book-merge op) no
// longer uses this function — it was rerouted to merge.Service.MergeBooks.
// This function is retained solely for internal/reconcile/itunes_heal.go,
// which collapses acoustically identical organize-bug duplicate rows.
//
// Loser retirement (A1#11, 2026-09-14): losers used to be HARD-deleted with
// store.DeleteBook, which destroyed the row and its metadata for good, left
// its book_file rows pointing at no book, and left its ext_id:* mappings
// naming a book that no longer existed. A loser is now retired the way
// merge.Service.MergeBooks retires one (retireMergedLoser): its external IDs
// move to the kept book first, then it is soft-deleted, so it can be restored
// with its files. If either step fails, or the loser names one of the kept
// book's audio paths, it is left live and reported in result.Errors.
//
// Refusals, all before any write: a soft-deleted keep
// (merge.SoftDeletedInputError, AsPrimary); a file-less keep with a
// file-bearing loser (guardKeeperAudioRoute, TODO.md:2304); any participant
// under the active iTunes library (merge.GuardITunesProtected).
//
// Still open: unlike merge.Service.MergeBooks this path has no iTunes
// write-back batcher, so no ITL removal is queued for the loser's PIDs here.
// The PIDs now point at the kept book, which is what the ITL should show.
func MergeBooks(
	_ context.Context,
	store Store,
	opID, keepID string,
	mergeIDs []string,
	progress ProgressReporter,
) (BookMergeResult, error) {
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()

	keepBook, err := store.GetBookByID(keepID)
	if err != nil || keepBook == nil {
		return BookMergeResult{}, fmt.Errorf("keep book %s not found", keepID)
	}

	// A soft-deleted keep is already on the purge clock: collapsing live rows
	// into it would retire the only live copies and leave the survivor to be
	// purged. Same refusal merge.Service.MergeBooks makes for its primary.
	if keepBook.IsSoftDeleted() {
		return BookMergeResult{}, &merge.SoftDeletedInputError{BookID: keepID, AsPrimary: true}
	}

	// Audio-route guard — runs before ANY write (before the iTunes-metadata
	// transfer, before FollowMergeWithStore, before a loser is retired), inside
	// the merge lock so the rows it reads cannot move under it.
	if err := guardKeeperAudioRoute(store, keepID, keepBook, mergeIDs); err != nil {
		return BookMergeResult{}, err
	}
	// iTunes guard — same position and lock: this path retires losers, so a
	// book with a file under the active iTunes library must never reach it.
	if err := merge.GuardITunesProtected(store, append([]string{keepID}, mergeIDs...)); err != nil {
		return BookMergeResult{}, err
	}

	total := len(mergeIDs)
	if progress != nil {
		_ = progress.Log("info",
			fmt.Sprintf("Merging %d book(s) into %q", total, keepBook.Title), nil)
		denom := total
		if denom == 0 {
			denom = 1
		}
		_ = progress.UpdateProgress(0, denom,
			fmt.Sprintf("Starting book merge... (0/%d, 0.00%%)", total))
	}

	kBook, err := store.GetBookByID(keepID)
	if err != nil || kBook == nil {
		return BookMergeResult{}, fmt.Errorf("keep book %s not found", keepID)
	}

	// The kept book's audio paths, for retireMergedLoser's shared-path check.
	// Fail closed: without them a loser naming the kept audio could be put on
	// the purge clock.
	keepAudioPaths, err := bookAudioPaths(store, kBook)
	if err != nil {
		return BookMergeResult{}, fmt.Errorf("cannot read audio paths of keep book %s, refusing to merge: %w", keepID, err)
	}
	// nil means the backend has no external IDs (the merge.Service rule).
	eidStore := merge.AsExternalIDReassigner(store)

	var result BookMergeResult
	for i, mergeID := range mergeIDs {
		if progress != nil && progress.IsCanceled() {
			return result, fmt.Errorf("cancelled")
		}
		if mergeID == keepID {
			continue
		}
		mergeBook, err := store.GetBookByID(mergeID)
		if err != nil || mergeBook == nil {
			result.Errors = append(result.Errors, fmt.Sprintf("book %s not found", mergeID))
			continue
		}

		// Transfer useful iTunes metadata from merge book to keep book (first-win).
		TransferITunesMetadataFirstWin(kBook, mergeBook)

		// Carry the loser's ABS sync identity (libraryItemId) and each user's
		// listening position onto the kept book BEFORE the loser is retired.
		// Done first so a crash between the two fails in the recoverable
		// direction (a redirect for a book that still exists). Best-effort and
		// never fails the merge; it logs at ERROR with both book IDs on failure.
		// Idempotent, so it also runs for an already-retired loser: a replay is
		// the only repair for a first merge that crashed after the soft delete
		// but before this follow. We already hold the process-wide
		// merge.LockMergeRMW taken at the top of this function, so this is
		// exactly-once w.r.t. every other merge-family path.
		merge.FollowMergeWithStore(store, keepID, []string{mergeID})

		// Already soft-deleted: collapsed by an earlier heal, or deleted by the
		// user. Leave the row exactly as it is — re-marking it would restart
		// its retention clock, and reassigning its external IDs would strip the
		// ones a restore brings back. Same rule as merge.Service.MergeBooks.
		if mergeBook.IsSoftDeleted() {
			bookMergeLog.Debug("book merge: loser already soft-deleted; left as is loser_id=%s keep_id=%s", mergeID, keepID)
		} else if err := retireMergedLoser(store, eidStore, keepID, mergeBook, keepAudioPaths); err != nil {
			bookMergeLog.Error("book merge left loser live loser_id=%s keep_id=%s: %v", mergeID, keepID, err)
			result.Errors = append(result.Errors, fmt.Sprintf("book %s left live: %v", mergeID, err))
		} else {
			if err := store.CreateOperationChange(&database.OperationChange{
				ID:          ulid.Make().String(),
				OperationID: opID,
				BookID:      mergeID,
				ChangeType:  "book_soft_delete",
				FieldName:   "book",
				OldValue:    fmt.Sprintf("%s (%s)", mergeBook.Title, mergeBook.FilePath),
				NewValue:    fmt.Sprintf("merged_into:%s", keepID),
			}); err != nil {
				bookMergeLog.Warn("book merge: could not journal the soft delete loser_id=%s keep_id=%s: %v", mergeID, keepID, err)
			}
			result.MergedCount++
		}

		if progress != nil {
			pct := float64(i+1) * 100.0 / float64(total)
			_ = progress.UpdateProgress(i+1, total,
				fmt.Sprintf("Merged %d/%d books (%.2f%%)", i+1, total, pct))
		}
	}

	if _, err := store.UpdateBook(kBook.ID, kBook); err != nil {
		result.Errors = append(result.Errors,
			fmt.Sprintf("failed to update keep book: %v", err))
	}

	if progress != nil {
		msg := fmt.Sprintf("Book merge complete: merged %d, %d errors",
			result.MergedCount, len(result.Errors))
		_ = progress.Log("info", msg, nil)
		denom := total
		if denom == 0 {
			denom = 1
		}
		_ = progress.UpdateProgress(denom, denom,
			fmt.Sprintf("%s (%d/%d, 100.00%%)", msg, total, total))
	}

	result.UpdatedKeepBook = kBook
	return result, nil
}
