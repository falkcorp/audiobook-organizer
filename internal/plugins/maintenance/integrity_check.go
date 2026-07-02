// file: internal/plugins/maintenance/integrity_check.go
// version: 1.0.0
// guid: 7f4a2b3c-9d1e-4f6a-8b5c-2e0d1f3a4b5c
// last-edited: 2026-07-01

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// integrityCheckDef registers the maintenance.file-integrity-check
// OperationDef. It runs nightly during the maintenance window (02:30 daily),
// between orphan-book-files-cleanup (02:15) and purge-deleted (03:00), without
// competing for the same minute.
func (p *Plugin) integrityCheckDef() sdk.OperationDef {
	sched := "30 2 * * *" // 02:30 daily — nightly maintenance window
	return sdk.OperationDef{
		ID:              "maintenance.file-integrity-check",
		Plugin:          "maintenance",
		DisplayName:     "File integrity check",
		Description:     "Flags book_file rows where file_hash differs from original_file_hash with no AO tag-write on record (post_metadata_hash empty) — a candidate for external modification or bit-rot. Report-only; takes no action.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.file-integrity-check",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runIntegrityCheck,
	}
}

func (p *Plugin) runIntegrityCheck(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting file integrity scan")
	scanProg := sdk.NewProgress(reporter, 0)
	scanProg.Start("Scanning book_files for hash mismatches...")

	flagged, totalFiles, err := findIntegrityMismatches(ctx, store)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	_ = reporter.Log(slog.LevelInfo, "File integrity scan complete",
		slog.Int("flagged_count", len(flagged)),
		slog.Int("total_book_files", totalFiles),
	)

	msg := fmt.Sprintf("File integrity check: %d file(s) flagged out of %d scanned (report-only)",
		len(flagged), totalFiles)
	_ = reporter.Log(slog.LevelInfo, msg)
	scanProg.Done(msg)
	return nil
}

// findIntegrityMismatches returns every BookFile whose current FileHash
// differs from its OriginalFileHash with no AO tag-write on record (i.e.
// PostMetadataHash is empty). Rows with no baseline (empty OriginalFileHash)
// or whose drift is explained by an AO-caused tag write (non-empty
// PostMetadataHash) are not flagged.
//
// This is the testable core of runIntegrityCheck. It is purely a scan — it
// never calls any store write or delete method.
func findIntegrityMismatches(ctx context.Context, store database.Store) (flagged []database.BookFile, totalFiles int, err error) {
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	files, ferr := store.GetAllBookFiles()
	if ferr != nil {
		return nil, 0, fmt.Errorf("GetAllBookFiles: %w", ferr)
	}
	flagged = make([]database.BookFile, 0)
	for _, f := range files {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if f.FileHash != "" && f.OriginalFileHash != "" &&
			f.FileHash != f.OriginalFileHash && f.PostMetadataHash == "" {
			flagged = append(flagged, f)
		}
	}
	return flagged, len(files), nil
}
