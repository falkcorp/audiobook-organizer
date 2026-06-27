// file: internal/plugins/maintenance/extract_wav_clips.go
// version: 1.0.0
// guid: e1f2a3b4-c5d6-7890-abcd-ef1234567890
// last-edited: 2026-06-27

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const extractWAVPageSize = 500 // larger pages — pure I/O, no GPU wait

func (p *Plugin) extractWAVClipsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.extract-wav-clips",
		Plugin:          "maintenance",
		DisplayName:     "Extract WAV clips for transcription cache",
		Description:     "Extracts the first 90 seconds of each book's first audio file and saves the result in {library}/.wav-cache/{hash}.wav. Run this before 'Transcribe book intros' to pre-fill the clip cache so transcription runs spend zero time on ffmpeg extraction.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.extract-wav-clips",
		Cancellable:     true,
		Timeout:         24 * time.Hour,
		ProgressTimeout: 10 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runExtractWAVClips,
	}
}

type extractWAVParams struct {
	SkipExisting *bool `json:"skip_existing,omitempty"` // default true
}

func (p *Plugin) runExtractWAVClips(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params extractWAVParams
	if len(rawParams) > 0 {
		_ = json.Unmarshal(rawParams, &params)
	}
	skipExisting := params.SkipExisting == nil || *params.SkipExisting

	log := reporter.Logger()
	rootDir := p.deps.RootDir()
	cacheDir := wavCacheDir(rootDir)

	if err := os.MkdirAll(cacheDir, 0o775); err != nil {
		return fmt.Errorf("create wav cache dir %s: %w", cacheDir, err)
	}

	allIDs, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("list book ids: %w", err)
	}
	total := len(allIDs)

	log.Info("extract-wav-clips: starting",
		"total_books", total, "cache_dir", cacheDir, "skip_existing", skipExisting)

	pages := chunkIDs(allIDs, extractWAVPageSize)

	var extracted, skipped, failed int

	err = registry.RunItems(ctx, reporter, pages, func(ctx context.Context, ids []string) error {
		sem := make(chan struct{}, introTranscribeFFWorkers)
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, bookID := range ids {
			b, gerr := store.GetBookByID(bookID)
			if gerr != nil || b == nil {
				continue
			}
			src, cacheKey, _ := firstAudioFile(store, bookID)
			if src == "" || cacheKey == "" {
				continue
			}
			dest := cachedClipPath(cacheDir, cacheKey)

			if skipExisting {
				if _, statErr := os.Stat(dest); statErr == nil {
					mu.Lock()
					skipped++
					mu.Unlock()
					continue
				}
			}

			wg.Add(1)
			go func(bookID, src, dest string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				ffCmd := exec.CommandContext(ctx, "ffmpeg",
					"-y", "-i", src,
					"-t", "90",
					"-vn", "-ar", "16000", "-ac", "1", "-f", "wav",
					dest,
				)
				out, ferr := ffCmd.CombinedOutput()
				mu.Lock()
				defer mu.Unlock()
				if ferr != nil {
					log.Warn("extract-wav-clips: ffmpeg failed",
						"book_id", bookID, "file", src,
						"err", ferr, "output", strings.TrimSpace(string(out)))
					failed++
				} else {
					extracted++
				}
			}(bookID, src, dest)
		}
		wg.Wait()

		_ = reporter.UpdateProgress(extracted+skipped, total,
			fmt.Sprintf("WAV clips: %d extracted, %d skipped (cached), %d failed", extracted, skipped, failed))
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   1,
		ProgressTotal: len(pages),
		Label: func(i, t int) string {
			return fmt.Sprintf("Page %d/%d — %d extracted, %d skipped", i+1, t, extracted, skipped)
		},
	})
	if err != nil {
		return err
	}

	log.Info("extract-wav-clips: complete",
		"extracted", extracted, "skipped", skipped, "failed", failed, "total", total)
	_ = reporter.UpdateProgress(1, 1,
		fmt.Sprintf("Done — %d extracted, %d already cached, %d failed", extracted, skipped, failed))
	return nil
}
