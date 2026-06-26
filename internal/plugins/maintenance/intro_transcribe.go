// file: internal/plugins/maintenance/intro_transcribe.go
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-06-26

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// introTranscribeParams is the checkpoint state for a transcription run.
type introTranscribeParams struct {
	LastBookID string `json:"last_book_id,omitempty"`
	// OnlyMissing skips books that already have an IntroTranscription.
	// Defaults to true so re-runs are incremental.
	OnlyMissing *bool `json:"only_missing,omitempty"`
}

func (p *Plugin) introTranscribeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.transcribe-book-intros",
		Plugin:          "maintenance",
		DisplayName:     "Transcribe book intros",
		Description:     "Extracts the first 30 seconds of each book's first audio file and transcribes it with Whisper. Stores the result in Book.IntroTranscription for disambiguation, narrator search, and dedup cross-checks.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.transcribe-book-intros",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         8 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead},
		Run:             p.runIntroTranscribe,
	}
}

func (p *Plugin) runIntroTranscribe(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params introTranscribeParams
	if len(rawParams) > 0 {
		_ = json.Unmarshal(rawParams, &params)
	}
	onlyMissing := params.OnlyMissing == nil || *params.OnlyMissing

	log := reporter.Logger()
	log.Info("transcribe-book-intros: loading books")

	// Load all books (paginated to avoid OOM on 50K library).
	// We only need ID, FilePath, IntroTranscription — GetAllBooks returns full structs
	// but we filter in-memory to avoid N+1.
	// GetAllBooksFrom is O(1) seek-based — safe on 50K+ libraries.
	const pageSize = 500
	cursor := params.LastBookID
	var toProcess []database.Book
	for {
		page, err := store.GetAllBooksFrom(cursor, pageSize)
		if err != nil {
			return fmt.Errorf("load books: %w", err)
		}
		for _, b := range page {
			if onlyMissing && b.IntroTranscription != nil && *b.IntroTranscription != "" {
				continue
			}
			toProcess = append(toProcess, b)
		}
		if len(page) < pageSize {
			break
		}
		cursor = page[len(page)-1].ID
	}

	log.Info("transcribe-book-intros: books to process", "count", len(toProcess))
	if len(toProcess) == 0 {
		_ = reporter.UpdateProgress(1, 1, "All books already have intro transcriptions")
		return nil
	}

	return registry.RunItems(ctx, reporter, toProcess,
		func(ctx context.Context, book database.Book) error {
			firstFile, err := firstAudioFile(store, book.ID)
			if err != nil || firstFile == "" {
				return nil // skip books with no accessible files
			}

			text, err := transcribe.TranscribeFirst30s(ctx, firstFile)
			if err != nil {
				log.Warn("transcribe-book-intros: transcription failed",
					"book_id", book.ID, "file", firstFile, "err", err)
				return nil
			}

			// Store raw transcript + parsed fields.
			// Parsed fields never overwrite Title/Author/Narrator — they live
			// in TranscribedTitle/Author/Narrator so transcription errors are
			// isolated from curated metadata.
			fields := transcribe.ParseAudiobookIntro(text)
			now := time.Now()
			book.IntroTranscription = &text
			book.IntroTranscribedAt = &now
			if fields.Title != "" {
				book.TranscribedTitle = &fields.Title
			}
			if fields.Author != "" {
				book.TranscribedAuthor = &fields.Author
			}
			if fields.Narrator != "" {
				book.TranscribedNarrator = &fields.Narrator
			}
			if _, err := store.UpdateBook(book.ID, &book); err != nil {
				log.Warn("transcribe-book-intros: update failed", "book_id", book.ID, "err", err)
			}
			return nil
		},
		registry.RunItemsOptions{
			Concurrency: 4, // ffmpeg + Whisper is CPU-heavy; limit parallelism
			ErrMode:     registry.ErrModeCollect,
			Label: func(i, total int) string {
				return fmt.Sprintf("Book %d/%d", i+1, total)
			},
		},
	)
}

// firstAudioFile returns the path of the first audio file for the given book
// (lowest TrackNumber, falling back to alphabetical by FilePath).
func firstAudioFile(store database.Store, bookID string) (string, error) {
	files, err := store.GetBookFiles(bookID)
	if err != nil || len(files) == 0 {
		return "", err
	}

	extSet := map[string]bool{
		".m4b": true, ".mp3": true, ".m4a": true,
		".flac": true, ".aac": true, ".ogg": true, ".wma": true,
	}
	var audio []database.BookFile
	for _, f := range files {
		if extSet[strings.ToLower(filepath.Ext(f.FilePath))] && f.FilePath != "" {
			audio = append(audio, f)
		}
	}
	if len(audio) == 0 {
		return "", nil
	}

	sort.Slice(audio, func(i, j int) bool {
		ti := trackNum(audio[i])
		tj := trackNum(audio[j])
		if ti != tj {
			return ti < tj
		}
		return audio[i].FilePath < audio[j].FilePath
	})
	return audio[0].FilePath, nil
}

func trackNum(f database.BookFile) int {
	return f.TrackNumber
}
