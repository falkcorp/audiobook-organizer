// file: internal/dedup/book_dedup.go
// version: 1.14.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-10-17

// Package dedup: book_dedup.go contains the extracted execution logic for the
// "dedup.book-scan" and "dedup.book-merge" async operations.  The *Server
// wrappers in internal/server/duplicates_ops.go are now thin callers that hand
// results back to server-owned state (dedupCache, etc.).
package dedup

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

const (
	metadataDuplicateThreshold = 0.85
	metadataBorderlineFloor    = 0.80
	metadataBorderlineCeiling  = 0.88
)

// bookMergeLog is the printf-style logger for the book-merge primary hand-off.
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

// handOffRetiredPrimaries runs versionprimary.EnsureSinglePrimary on each
// group a retired loser was primary of. Without a probe: chapter counts come
// from the chapter table, which is enough to restore the one-primary
// invariant; re-ranking is version-group-primary-repair's job. A held group
// is left as it is (EnsureSinglePrimary logs it).
func handOffRetiredPrimaries(ctx context.Context, store Store, groups map[string]bool) {
	env := versionprimary.Env{RootDir: config.AppConfig.RootDir}
	for gid := range groups {
		res, err := versionprimary.EnsureSinglePrimary(ctx, store, gid, env)
		if err != nil {
			bookMergeLog.Warn("book merge: primary hand-off for group %s failed: %v", logger.SanitizeLogValue(gid), err)
			continue
		}
		if len(res.Writes) > 0 {
			bookMergeLog.Info("book merge: group %s primary handed to %s (%s)",
				logger.SanitizeLogValue(gid), logger.SanitizeLogValue(res.PrimaryID), res.Outcome)
		}
	}
}
