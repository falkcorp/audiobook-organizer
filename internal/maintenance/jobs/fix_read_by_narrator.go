// file: internal/maintenance/jobs/fix_read_by_narrator.go
// version: 2.2.0
// guid: a1000001-0000-0000-0000-000000000001
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog"
)

func init() { maintenance.Register(&fixReadByNarratorJob{}) }

type fixReadByNarratorJob struct{}

func (j *fixReadByNarratorJob) ID() string       { return "fix-read-by-narrator" }
func (j *fixReadByNarratorJob) Name() string     { return "Fix Read-by Narrator" }
func (j *fixReadByNarratorJob) Category() string { return "library" }
func (j *fixReadByNarratorJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *fixReadByNarratorJob) Description() string {
	return "Fix books where title/author metadata is swapped (title starts with 'read by')"
}
func (j *fixReadByNarratorJob) CanResume() bool { return false }

func (j *fixReadByNarratorJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	allBooks, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("failed to list books: %w", err)
	}
	reporter.SetTotal(len(allBooks))

	var applied, found int
	for i := range allBooks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		book := &allBooks[i]
		titleLower := strings.ToLower(book.Title)

		authorName := ""
		if book.AuthorID != nil {
			if author, aErr := store.GetAuthorByID(*book.AuthorID); aErr == nil && author != nil {
				authorName = author.Name
			}
		}

		var fix *rbnrFixResult
		switch {
		case strings.Contains(titleLower, " - read by "):
			fix = rbnrPattern2(book, authorName)
		case strings.HasPrefix(titleLower, "read by ") && strings.HasPrefix(strings.ToLower(authorName), "read by "):
			fix = rbnrPattern3(book, authorName)
		case strings.HasPrefix(titleLower, "read by "):
			fix = rbnrPattern1(book, authorName)
		}

		if fix == nil {
			reporter.Increment()
			continue
		}
		if fix.NewTitle == book.Title && fix.NewNarrator == rbnrStringDeref(book.Narrator) {
			reporter.Increment()
			continue
		}

		found++
		if !dryRun {
			if applyErr := rbnrApplyFix(store, book, fix); applyErr != nil {
				slog.Error("Failed to fix book", "book", book.ID, "applyErr", applyErr)
			} else {
				applied++
			}
		}
		reporter.Increment()
	}

	slog.Info("Done found applied dryRun", "found", found, "applied", applied, "dryRun", dryRun)
	return nil
}

type rbnrFixResult struct {
	BookID      string
	Pattern     string
	OldTitle    string
	OldAuthor   string
	OldNarrator *string
	NewTitle    string
	NewNarrator string
	FilePath    string
}

func rbnrPattern1(book *database.BookCore, authorName string) *rbnrFixResult {
	narrator := strings.TrimSpace(book.Title[len("read by "):])
	if narrator == "" {
		return nil
	}
	newTitle := strings.TrimRight(authorName, "_")
	newTitle = strings.TrimSpace(newTitle)
	if newTitle == "" {
		return nil
	}
	return &rbnrFixResult{
		BookID:      book.ID,
		Pattern:     "read_by_swap",
		OldTitle:    book.Title,
		OldAuthor:   authorName,
		OldNarrator: book.Narrator,
		NewTitle:    newTitle,
		NewNarrator: narrator,
		FilePath:    book.FilePath,
	}
}

func rbnrPattern2(book *database.BookCore, authorName string) *rbnrFixResult {
	idx := rbnrCaseInsensitiveIndex(book.Title, " - read by ")
	if idx < 0 {
		return nil
	}
	beforeReadBy := book.Title[:idx]
	lastDash := strings.LastIndex(beforeReadBy, " - ")
	var newTitle, narrator string
	if lastDash >= 0 {
		newTitle = strings.TrimSpace(beforeReadBy[:lastDash])
		narrator = strings.TrimSpace(beforeReadBy[lastDash+3:])
	} else {
		newTitle = strings.TrimSpace(beforeReadBy)
		narrator = ""
	}
	if newTitle == "" {
		return nil
	}
	return &rbnrFixResult{
		BookID:      book.ID,
		Pattern:     "title_dash_read_by",
		OldTitle:    book.Title,
		OldAuthor:   authorName,
		OldNarrator: book.Narrator,
		NewTitle:    newTitle,
		NewNarrator: narrator,
		FilePath:    book.FilePath,
	}
}

func rbnrPattern3(book *database.BookCore, authorName string) *rbnrFixResult {
	narrator := strings.TrimSpace(book.Title[len("read by "):])
	newTitle := rbnrTitleFromFilePath(book.FilePath)
	if newTitle == "" {
		base := filepath.Base(book.FilePath)
		ext := filepath.Ext(base)
		newTitle = strings.TrimSpace(strings.TrimSuffix(base, ext))
	}
	if newTitle == "" || strings.HasPrefix(strings.ToLower(newTitle), "read by ") {
		return nil
	}
	return &rbnrFixResult{
		BookID:      book.ID,
		Pattern:     "both_broken",
		OldTitle:    book.Title,
		OldAuthor:   authorName,
		OldNarrator: book.Narrator,
		NewTitle:    newTitle,
		NewNarrator: narrator,
		FilePath:    book.FilePath,
	}
}

func rbnrTitleFromFilePath(fp string) string {
	if fp == "" {
		return ""
	}
	dir := filepath.Dir(fp)
	title := filepath.Base(dir)
	if title == "." || title == "/" || title == "" {
		return ""
	}
	return title
}

func rbnrApplyFix(store database.Store, book *database.BookCore, fix *rbnrFixResult) error {
	current, err := store.GetBookByID(book.ID)
	if err != nil {
		return fmt.Errorf("GetBookByID: %w", err)
	}
	if current == nil {
		return fmt.Errorf("book %s not found", book.ID)
	}
	current.Title = fix.NewTitle
	if fix.NewNarrator != "" {
		current.Narrator = &fix.NewNarrator
	}
	_, err = store.UpdateBook(book.ID, current)
	return err
}

func rbnrCaseInsensitiveIndex(s, substr string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(substr))
}

func rbnrStringDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
