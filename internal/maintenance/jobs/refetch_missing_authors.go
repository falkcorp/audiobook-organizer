// file: internal/maintenance/jobs/refetch_missing_authors.go
// version: 2.1.0
// guid: a1000012-0000-0000-0000-000000000012
// last-edited: 2026-07-06

package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func init() { maintenance.Register(&refetchMissingAuthorsJob{}) }

type refetchMissingAuthorsJob struct{}

type rma_params struct {
	DryRun bool `json:"dry_run"`
}

func (j *refetchMissingAuthorsJob) ID() string       { return "refetch-missing-authors" }
func (j *refetchMissingAuthorsJob) Name() string     { return "Refetch Missing Authors" }
func (j *refetchMissingAuthorsJob) Category() string { return "library" }
func (j *refetchMissingAuthorsJob) Description() string {
	return "Re-reads author info from file tags (album_artist > artist > composer) for books where the author field is empty."
}
func (j *refetchMissingAuthorsJob) DefaultParams() any { return &rma_params{DryRun: true} }
func (j *refetchMissingAuthorsJob) CanResume() bool    { return true }

func (j *refetchMissingAuthorsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	opID := maintenance.OperationIDFromCtx(ctx)

	// Load all books without an author.
	allBooks, err := store.GetAllBooks(0, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooks: %w", err)
	}

	// Filter to only books with no author.
	var books []database.Book
	for i := range allBooks {
		if allBooks[i].AuthorID == nil {
			books = append(books, allBooks[i])
		}
	}

	slog.Info("refetch-missing-authors / books have no author", "opID", opID, "books_count", len(books), "allBooks_count", len(allBooks))

	// Load all book files upfront to avoid N+1 queries.
	allFiles, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	filesByBook := make(map[string][]database.BookFileCore, len(allFiles))
	for i := range allFiles {
		f := &allFiles[i]
		filesByBook[f.BookID] = append(filesByBook[f.BookID], *f)
	}

	reporter.SetTotal(len(books))

	audioExts := map[string]bool{
		".m4b": true, ".m4a": true, ".mp3": true,
		".flac": true, ".ogg": true, ".opus": true,
	}

	filled := 0
	skipped := 0
	errors := 0

	for i := range books {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		b := &books[i]
		reporter.Increment()

		if i%100 == 0 && i > 0 {
			slog.Info("refetch-missing-authors progress / (filled, skipped, errors)", "opID", opID, "i", i, "books_count", len(books), "filled", filled, "skipped", skipped, "errors", errors)
		}

		// Pick the first audio file for this book.
		var audioPath string
		for _, f := range filesByBook[b.ID] {
			if f.FilePath == "" || f.Missing {
				continue
			}
			if audioExts[strings.ToLower(fileExt(f.FilePath))] {
				audioPath = f.FilePath
				break
			}
		}

		// Fall back to the book's own FilePath if no book_files row was found.
		if audioPath == "" && b.FilePath != "" && audioExts[strings.ToLower(fileExt(b.FilePath))] {
			audioPath = b.FilePath
		}

		if audioPath == "" {
			slog.Warn("no audio file for book (), skipping", "bookID", b.ID, "bookTitle", b.Title)
			skipped++
			continue
		}

		// Read tags from disk using taglib.
		// Tag priority: ALBUMARTIST > ARTIST > COMPOSER (composer = narrator in audiobooks).
		tags, readErr := metadata.ReadRawTags(audioPath)
		if readErr != nil {
			slog.Error("failed to read tags for", "audioPath", audioPath, "readErr", readErr)
			errors++
			continue
		}

		getRaw := func(keys ...string) string {
			for _, k := range keys {
				if vs, ok := tags[strings.ToUpper(k)]; ok {
					for _, v := range vs {
						v = strings.TrimSpace(v)
						if v != "" {
							return v
						}
					}
				}
			}
			return ""
		}

		authorName := getRaw("ALBUMARTIST", "ALBUM_ARTIST", "ALBUM ARTIST")
		if authorName == "" {
			authorName = getRaw("ARTIST")
		}
		if authorName == "" {
			authorName = getRaw("COMPOSER")
		}

		if authorName == "" {
			slog.Warn("no author found in tags for book ()", "bookID", b.ID, "bookTitle", b.Title)
			skipped++
			continue
		}

		if dryRun {
			slog.Info("[dry] would set author for book", "authorName", authorName, "bookID", b.ID, "bookTitle", b.Title)
			filled++
			continue
		}

		// Find or create the author record, then link it to the book.
		author, err := store.GetAuthorByName(authorName)
		if err != nil || author == nil {
			author, err = store.CreateAuthor(authorName)
			if err != nil {
				slog.Error("failed to create author for book", "authorName", authorName, "bookID", b.ID, "err", err)
				errors++
				continue
			}
			slog.Info("refetch-missing-authors created author", "opID", opID, "authorName", authorName, "authorID", author.ID)
		}

		b.AuthorID = &author.ID
		if _, err := store.UpdateBook(b.ID, b); err != nil {
			slog.Error("failed to update book", "b", b.ID, "err", err)
			errors++
			continue
		}

		slog.Info("refetch-missing-authors set author on book", "opID", opID, "authorName", authorName, "bookID", b.ID, "bookTitle", b.Title)
		filled++
	}

	summary := fmt.Sprintf("refetch-missing-authors complete: total=%d filled=%d skipped=%d errors=%d dryRun=%v",
		len(books), filled, skipped, errors, dryRun)
	slog.Info(summary)
	slog.Info("", "opID", opID, "summary", summary)
	return nil
}

// fileExt returns the lowercase file extension including the leading dot.
func fileExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return strings.ToLower(path[i:])
		}
		if path[i] == '/' {
			break
		}
	}
	return ""
}
