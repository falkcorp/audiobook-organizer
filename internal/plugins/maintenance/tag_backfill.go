// file: internal/plugins/maintenance/tag_backfill.go
// version: 1.0.0
// guid: 1f6b3d28-9a47-4c50-8e21-7b0c4a9d6e35
// last-edited: 2026-06-20

// Package maintenance — op maintenance.tag-backfill.
//
// Lossless tag capture for EXISTING BookFiles. New imports now record every tag
// (Metadata.AllTags -> BookFile.RawTags) and the real track/disc position, but rows
// imported before that change carry nil RawTags and often a positional-only
// TrackNumber. This op reads each file's tags and backfills RawTags + track/disc/title
// so the full provenance is recoverable everywhere — the "eventually for everything"
// half of the lossless-capture design. Dry-run by default; reads files only.

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type tagBackfillParams struct {
	DryRun bool `json:"dryRun"`
	// Force re-reads tags even for files that already have RawTags (e.g. after a
	// capture-logic change). Default false: only files missing RawTags are touched.
	Force bool `json:"force"`
	// Limit caps the number of files processed in one run (0 = no cap). Useful for
	// a bounded first pass over a large library.
	Limit int `json:"limit"`
}

func (p *Plugin) tagBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.tag-backfill",
		Plugin:      "maintenance",
		DisplayName: "Backfill lossless file tags (RawTags + track/disc)",
		Description: "Reads each BookFile's audio tags and backfills the lossless RawTags map plus " +
			"track/disc/title for rows imported before lossless capture. Files missing on disk are " +
			"skipped. Default dry-run previews counts; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.tag-backfill",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runTagBackfill,
	}
}

func (p *Plugin) runTagBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := tagBackfillParams{DryRun: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if params.DryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no changes will be written")
	}

	files, err := store.GetAllBookFiles()
	if err != nil {
		return fmt.Errorf("GetAllBookFiles: %w", err)
	}
	total := len(files)
	_ = reporter.UpdateProgress(0, total, fmt.Sprintf("Reading tags for %d files…", total))

	const (
		writeBatchSize = 1000
		logInterval    = 15 * time.Second
	)
	var (
		examined, needed, missing, readErr, written int
		pending                                     = make([]*database.BookFile, 0, writeBatchSize)
		examples                                    = make([]string, 0, 5)
		lastLog                                     = time.Now()
	)
	flush := func() error {
		if params.DryRun || len(pending) == 0 {
			pending = pending[:0]
			return nil
		}
		if err := store.BatchUpsertBookFiles(pending); err != nil {
			return err
		}
		pending = pending[:0]
		return nil
	}

	for i := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if params.Limit > 0 && examined >= params.Limit {
			break
		}
		f := files[i]
		examined++

		// Skip rows that already have tags unless forced.
		if len(f.RawTags) > 0 && !params.Force {
			continue
		}
		if f.FilePath == "" {
			continue
		}
		if _, statErr := os.Stat(f.FilePath); statErr != nil {
			missing++
			continue
		}
		meta, merr := metadata.ExtractMetadata(f.FilePath, nil)
		if merr != nil {
			readErr++
			continue
		}
		if len(meta.AllTags) == 0 {
			continue // nothing capturable
		}
		needed++

		updated := f // copy
		updated.RawTags = meta.AllTags
		if meta.TrackNumber > 0 {
			updated.TrackNumber = meta.TrackNumber
		}
		if meta.TrackTotal > 0 {
			updated.TrackCount = meta.TrackTotal
		}
		if meta.DiscNumber > 0 {
			updated.DiscNumber = meta.DiscNumber
		}
		if meta.DiscTotal > 0 {
			updated.DiscCount = meta.DiscTotal
		}
		if updated.Title == "" && meta.Title != "" {
			updated.Title = meta.Title
		}
		if len(examples) < 5 {
			examples = append(examples, fmt.Sprintf("%s(%d tags,trk %d)", updated.ID, len(meta.AllTags), updated.TrackNumber))
		}

		pending = append(pending, &updated)
		if len(pending) >= writeBatchSize {
			if err := flush(); err != nil {
				return fmt.Errorf("batch write at file %d: %w", examined, err)
			}
		}
		if !params.DryRun {
			written++
		}

		if time.Since(lastLog) >= logInterval {
			_ = reporter.UpdateProgress(examined, total, fmt.Sprintf(
				"%d/%d examined — %d to backfill, %d missing, %d read-err", examined, total, needed, missing, readErr))
			lastLog = time.Now()
		}
	}
	if err := flush(); err != nil {
		return fmt.Errorf("final batch write: %w", err)
	}

	verb := "would backfill"
	if !params.DryRun {
		verb = fmt.Sprintf("backfilled %d;", written)
	}
	summary := fmt.Sprintf("examined=%d %s needed=%d missing-on-disk=%d read-errors=%d | e.g. %s",
		examined, verb, needed, missing, readErr, strings.Join(examples, ", "))
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(total, total, summary)
	return nil
}
