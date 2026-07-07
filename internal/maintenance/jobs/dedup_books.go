// file: internal/maintenance/jobs/dedup_books.go
// version: 2.3.0
// guid: a1000010-0000-0000-0000-000000000010
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&dedupBooksJob{}) }

type dedupBooksJob struct {
	enqueuer maintenance.WriteBackEnqueuer
}

func (j *dedupBooksJob) InjectEnqueuer(e maintenance.WriteBackEnqueuer) { j.enqueuer = e }

func (j *dedupBooksJob) ID() string       { return "dedup-books" }
func (j *dedupBooksJob) Name() string     { return "Deduplicate Books" }
func (j *dedupBooksJob) Category() string { return "dedup" }
func (j *dedupBooksJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *dedupBooksJob) Description() string { return "Detect and merge duplicate books" }
func (j *dedupBooksJob) CanResume() bool     { return false }

func (j *dedupBooksJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	allBooks, err := ddFetchAllBooksPaginated(store)
	if err != nil {
		return fmt.Errorf("failed to list books: %w", err)
	}
	reporter.SetTotal(len(allBooks))

	deletedIDs := make(map[string]bool)

	// Phase 1: Delete junk "read by narrator" records
	phase1 := 0
	for i := range allBooks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		book := &allBooks[i]
		if deletedIDs[book.ID] {
			continue
		}
		if !ddIsJunkReadByNarrator(book) {
			continue
		}
		if !dryRun {
			if delErr := ddSoftDeleteBook(store, book.ID); delErr != nil {
				slog.Error("phase1 delete", "book", book.ID, "delErr", delErr)
				continue
			}
		}
		deletedIDs[book.ID] = true
		phase1++
	}

	// Phase 2: Merge books with the same file_path
	pathGroups := make(map[string][]database.Book)
	for i := range allBooks {
		book := &allBooks[i]
		if deletedIDs[book.ID] || book.FilePath == "" {
			continue
		}
		pathGroups[book.FilePath] = append(pathGroups[book.FilePath], *book)
	}

	phase2 := 0
	for fp, group := range pathGroups {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(group) < 2 {
			continue
		}
		live := ddFilterLive(group, deletedIDs)
		if len(live) < 2 {
			continue
		}
		keepIdx := ddPickKeeperIdx(live)
		keeper := &live[keepIdx]
		for i := range live {
			if i == keepIdx {
				continue
			}
			dup := &live[i]
			if mergeErr := ddMergeDuplicateBook(store, keeper, dup, dryRun, j.enqueuer); mergeErr != nil {
				slog.Error("phase2 merge -> fp", "dup", dup.ID, "keeper", keeper.ID, "fp", fp, "mergeErr", mergeErr)
				continue
			}
			deletedIDs[dup.ID] = true
			phase2++
		}
	}

	// Phase 3: Merge books with same normalised title + author in same dir
	type titleAuthorKey struct {
		NormTitle string
		AuthorID  int
		Dir       string
	}
	taGroups := make(map[titleAuthorKey][]database.Book)
	for i := range allBooks {
		book := &allBooks[i]
		if deletedIDs[book.ID] {
			continue
		}
		normTitle := ddNormalizeDedupTitle(book.Title)
		if normTitle == "" {
			continue
		}
		authorID := 0
		if book.AuthorID != nil {
			authorID = *book.AuthorID
		}
		dir := ""
		if book.FilePath != "" {
			dir = filepath.Dir(book.FilePath)
		}
		key := titleAuthorKey{NormTitle: normTitle, AuthorID: authorID, Dir: dir}
		taGroups[key] = append(taGroups[key], *book)
	}

	phase3 := 0
	for key, group := range taGroups {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(group) < 2 {
			continue
		}
		live := ddFilterLive(group, deletedIDs)
		if len(live) < 2 {
			continue
		}
		if key.AuthorID == 0 {
			titles := make(map[string]bool)
			for _, b := range live {
				titles[strings.ToLower(strings.TrimSpace(b.Title))] = true
			}
			if len(titles) > 1 {
				continue
			}
		}
		keepIdx := ddPickKeeperIdx(live)
		keeper := &live[keepIdx]
		for i := range live {
			if i == keepIdx {
				continue
			}
			dup := &live[i]
			if mergeErr := ddMergeDuplicateBook(store, keeper, dup, dryRun, j.enqueuer); mergeErr != nil {
				slog.Error("phase3 merge ->", "dup", dup.ID, "keeper", keeper.ID, "mergeErr", mergeErr)
				continue
			}
			deletedIDs[dup.ID] = true
			phase3++
		}
	}

	// Phase 4: Clean up duplicate version group entries
	vgGroups := make(map[string][]database.Book)
	for i := range allBooks {
		book := &allBooks[i]
		if deletedIDs[book.ID] || book.VersionGroupID == nil || *book.VersionGroupID == "" {
			continue
		}
		vgGroups[*book.VersionGroupID] = append(vgGroups[*book.VersionGroupID], *book)
	}

	phase4 := 0
	for vgID, group := range vgGroups {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		seen := make(map[string]bool)
		var dupeIDs []string
		for _, b := range group {
			if seen[b.ID] {
				dupeIDs = append(dupeIDs, b.ID)
			}
			seen[b.ID] = true
		}
		if len(dupeIDs) == 0 {
			continue
		}
		if !dryRun {
			for _, dupID := range dupeIDs {
				current, gbErr := store.GetBookByID(dupID)
				if gbErr != nil || current == nil {
					continue
				}
				current.VersionGroupID = nil
				current.IsPrimaryVersion = nil
				if _, upErr := store.UpdateBook(dupID, current); upErr != nil {
					slog.Error("phase4 unlink vg from book", "vgID", vgID, "dupID", dupID, "upErr", upErr)
					continue
				}
				phase4++
			}
		} else {
			phase4 += len(dupeIDs)
		}
	}

	for range allBooks {
		reporter.Increment()
	}

	slog.Info("Done phase1_junk phase2_path phase3_title phase4_vg dryRun", "phase1", phase1, "phase2", phase2, "phase3", phase3, "phase4", phase4, "dryRun", dryRun)
	return nil
}

func ddFetchAllBooksPaginated(store database.Store) ([]database.Book, error) {
	const pageSize = 500
	var all []database.Book
	afterID := ""
	for {
		page, err := store.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			break
		}
		afterID = page[len(page)-1].ID
	}
	return all, nil
}

func ddIsJunkReadByNarrator(book *database.Book) bool {
	t := strings.ToLower(strings.TrimSpace(book.Title))
	if t != "read by narrator" {
		return false
	}
	if book.AuthorID != nil {
		return false
	}
	if book.SeriesID != nil {
		return false
	}
	if book.Description != nil && strings.TrimSpace(*book.Description) != "" {
		return false
	}
	if book.ISBN10 != nil || book.ISBN13 != nil || book.ASIN != nil {
		return false
	}
	if book.ITunesPersistentID != nil {
		return false
	}
	return true
}

func ddPickKeeperIdx(books []database.Book) int {
	best := 0
	for i := 1; i < len(books); i++ {
		if ddBookScore(&books[i]) > ddBookScore(&books[best]) {
			best = i
		}
	}
	return best
}

func ddBookScore(b *database.Book) int {
	score := 0
	if b.AuthorID != nil {
		score += 100
	}
	if b.SeriesID != nil {
		score += 20
	}
	if b.Description != nil && *b.Description != "" {
		score += 10
	}
	if b.Narrator != nil && *b.Narrator != "" {
		score += 5
	}
	if b.Duration != nil {
		score += 5
	}
	if b.ISBN10 != nil || b.ISBN13 != nil || b.ASIN != nil {
		score += 10
	}
	if b.ITunesPersistentID != nil {
		score += 10
	}
	if b.Publisher != nil && *b.Publisher != "" {
		score += 3
	}
	if b.Language != nil && *b.Language != "" {
		score += 2
	}
	if b.Genre != nil && *b.Genre != "" {
		score += 2
	}
	if b.CoverURL != nil && *b.CoverURL != "" {
		score += 3
	}
	if b.CreatedAt != nil {
		score -= int(b.CreatedAt.Unix() / 1_000_000)
	}
	return score
}

func ddMergeDuplicateBook(store database.Store, keeper *database.Book, dup *database.Book, dryRun bool, enqueuer maintenance.WriteBackEnqueuer) error {
	if dryRun {
		return nil
	}

	dupMappings, _ := store.GetExternalIDsForBook(dup.ID)
	var dupPIDs []string
	for _, m := range dupMappings {
		if m.Source == "itunes" && m.ExternalID != "" && !m.Tombstoned {
			dupPIDs = append(dupPIDs, m.ExternalID)
		}
	}

	files, err := store.GetBookFiles(dup.ID)
	if err == nil {
		for i := range files {
			f := &files[i]
			f.BookID = keeper.ID
			if upErr := store.UpsertBookFile(f); upErr != nil {
				slog.Warn("dedup-books UpsertBookFile -> keeper", "f", f.ID, "keeper", keeper.ID, "upErr", upErr)
			}
		}
	}

	if reassignErr := store.ReassignExternalIDs(dup.ID, keeper.ID); reassignErr != nil {
		slog.Warn("dedup-books ReassignExternalIDs ->", "dup", dup.ID, "keeper", keeper.ID, "reassignErr", reassignErr)
	}

	if enqueuer != nil && len(dupPIDs) > 0 {
		for _, pid := range dupPIDs {
			enqueuer.EnqueueRemove(pid)
		}
		slog.Info("dedup-books queued ITL removals for dup", "dupPIDs_count", len(dupPIDs), "dup", dup.ID)
	}

	tags, tagsErr := store.GetBookUserTags(dup.ID)
	if tagsErr == nil && len(tags) > 0 {
		for _, tag := range tags {
			_ = store.AddBookUserTag(keeper.ID, tag)
		}
	}

	current, gbErr := store.GetBookByID(keeper.ID)
	if gbErr != nil {
		return fmt.Errorf("GetBookByID keeper %s: %w", keeper.ID, gbErr)
	}
	if current == nil {
		return fmt.Errorf("keeper book %s not found", keeper.ID)
	}

	ddMergeBookFields(current, dup)

	if _, upErr := store.UpdateBook(keeper.ID, current); upErr != nil {
		return fmt.Errorf("UpdateBook keeper %s: %w", keeper.ID, upErr)
	}

	return ddSoftDeleteBook(store, dup.ID)
}

func ddMergeBookFields(dst, src *database.Book) {
	if dst.AuthorID == nil && src.AuthorID != nil {
		dst.AuthorID = src.AuthorID
	}
	if dst.SeriesID == nil && src.SeriesID != nil {
		dst.SeriesID = src.SeriesID
		if dst.SeriesSequence == nil && src.SeriesSequence != nil {
			dst.SeriesSequence = src.SeriesSequence
		}
	}
	if dst.Narrator == nil && src.Narrator != nil && *src.Narrator != "" {
		dst.Narrator = src.Narrator
	}
	if dst.Description == nil && src.Description != nil && *src.Description != "" {
		dst.Description = src.Description
	}
	if dst.Duration == nil && src.Duration != nil {
		dst.Duration = src.Duration
	}
	if dst.Publisher == nil && src.Publisher != nil {
		dst.Publisher = src.Publisher
	}
	if dst.Language == nil && src.Language != nil {
		dst.Language = src.Language
	}
	if dst.Genre == nil && src.Genre != nil {
		dst.Genre = src.Genre
	}
	if dst.ISBN10 == nil && src.ISBN10 != nil {
		dst.ISBN10 = src.ISBN10
	}
	if dst.ISBN13 == nil && src.ISBN13 != nil {
		dst.ISBN13 = src.ISBN13
	}
	if dst.ASIN == nil && src.ASIN != nil {
		dst.ASIN = src.ASIN
	}
	if dst.ITunesPersistentID == nil && src.ITunesPersistentID != nil {
		dst.ITunesPersistentID = src.ITunesPersistentID
	}
	if dst.ITunesDateAdded == nil && src.ITunesDateAdded != nil {
		dst.ITunesDateAdded = src.ITunesDateAdded
	}
	if dst.ITunesPlayCount == nil && src.ITunesPlayCount != nil {
		dst.ITunesPlayCount = src.ITunesPlayCount
	}
	if dst.ITunesRating == nil && src.ITunesRating != nil {
		dst.ITunesRating = src.ITunesRating
	}
	if dst.ITunesBookmark == nil && src.ITunesBookmark != nil {
		dst.ITunesBookmark = src.ITunesBookmark
	}
	if dst.CoverURL == nil && src.CoverURL != nil {
		dst.CoverURL = src.CoverURL
	}
	if dst.OpenLibraryID == nil && src.OpenLibraryID != nil {
		dst.OpenLibraryID = src.OpenLibraryID
	}
	if dst.GoogleBooksID == nil && src.GoogleBooksID != nil {
		dst.GoogleBooksID = src.GoogleBooksID
	}
	if dst.HardcoverID == nil && src.HardcoverID != nil {
		dst.HardcoverID = src.HardcoverID
	}
	if dst.WorkID == nil && src.WorkID != nil {
		dst.WorkID = src.WorkID
	}
	if (dst.VersionGroupID == nil || *dst.VersionGroupID == "") && src.VersionGroupID != nil && *src.VersionGroupID != "" {
		dst.VersionGroupID = src.VersionGroupID
	}
}

func ddSoftDeleteBook(store database.Store, bookID string) error {
	current, err := store.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("GetBookByID %s: %w", bookID, err)
	}
	if current == nil {
		return nil
	}
	t := true
	now := time.Now()
	current.MarkedForDeletion = &t
	current.MarkedForDeletionAt = &now
	if _, upErr := store.UpdateBook(bookID, current); upErr != nil {
		slog.Warn("dedup-books soft-delete failed for (), falling back to hard delete", "bookID", bookID, "upErr", upErr)
		return store.DeleteBook(bookID)
	}
	return nil
}

var ddNonAlphanumRE = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)

func ddNormalizeDedupTitle(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "(unabridged)", "")
	// Strip leading chapter markers but require an explicit delimiter (dot, dash,
	// colon) with surrounding whitespace — NOT a bare "001 Title" where a space
	// alone follows the number. "001 Title" could be a real title (series position,
	// box-set index); only "001 - Title", "001. Title", "03: Title", or "(76/85)
	// Title" are unambiguous chapter markers.
	reLeadNum := regexp.MustCompile(`^\s*(\(\s*\d+\s*[/\-]\s*\d+\s*\)\s+|\d+\s*[\.\-:]\s+)`)
	s = reLeadNum.ReplaceAllString(s, "")
	s = ddNonAlphanumRE.ReplaceAllString(s, " ")
	fields := strings.FieldsFunc(s, unicode.IsSpace)
	return strings.Join(fields, " ")
}

func ddFilterLive(books []database.Book, deletedIDs map[string]bool) []database.Book {
	out := books[:0:len(books)]
	for _, b := range books {
		if !deletedIDs[b.ID] {
			out = append(out, b)
		}
	}
	return out
}
