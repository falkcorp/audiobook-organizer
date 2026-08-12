// file: internal/server/library_core_ops.go
// version: 1.5.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-08-12

// library_core_ops registers the scan, organize, and transcode OperationDefs
// that previously went through the legacy BridgeQueue.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/transcode"
	ulid "github.com/oklog/ulid/v2"
)

type libraryScanParams struct {
	FolderPath  *string `json:"folder_path,omitempty"`
	ForceUpdate *bool   `json:"force_update,omitempty"`
}

type libraryOrganizeParams struct {
	FolderPath         *string  `json:"folder_path,omitempty"`
	BookIDs            []string `json:"book_ids,omitempty"`
	FetchMetadataFirst bool     `json:"fetch_metadata_first"`
	SyncITunesFirst    bool     `json:"sync_itunes_first"`
}

type libraryTranscodeParams struct {
	BookID       string `json:"book_id"`
	OutputFormat string `json:"output_format"`
	Bitrate      int    `json:"bitrate"`
	KeepOriginal bool   `json:"keep_original"`
}

// RegisterLibraryScanOp registers the "library.scan" v2 OperationDef.
func (s *Server) RegisterLibraryScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.scan",
		Plugin:          "library",
		DisplayName:     "Library Scan",
		Description:     "Scan the library root directory for new, changed, or removed audiobook files.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "library.scan",
		Permissions:     []auth.Permission{auth.PermScanTrigger},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p libraryScanParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("library.scan: decode params: %w", err)
				}
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     ulid.Make().String(),
				Type:   "library.scan",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			folderPath := ""
			if p.FolderPath != nil {
				folderPath = *p.FolderPath
			}
			logging.Info(ctx, "library scan starting", "folder_path", folderPath)

			scanReq := &scanner.ScanRequest{
				FolderPath:  p.FolderPath,
				ForceUpdate: p.ForceUpdate,
			}
			progress := registryProgressAdapter{r: reporter}
			err := s.scanService.PerformScan(ctx, scanReq, operations.LoggerFromReporter(progress))
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "library scan failed", "err", err)
				return err
			}
			op.SetStatus("success")
			logging.Info(ctx, "library scan complete")
			return nil
		},
	})
}

// libraryImportParams are the JSON parameters for the manual-import op.
type libraryImportParams struct {
	// Path is the folder OR single file to import. It MUST resolve under a
	// configured import path (validated via fileops.ValidateUserPath, the
	// SEC-AUDIT path-injection guard).
	Path string `json:"path"`
	// Confirm bypasses the import circuit breaker (the > N-new-books hard stop).
	// Wired by the import-guard feature; accepted here so the param contract is
	// stable.
	Confirm bool `json:"confirm,omitempty"`
}

// RegisterLibraryImportOp registers the "library.import" v2 OperationDef — a
// manual, targeted import of one folder or file. Unlike library.scan it:
//   - takes its own ConcurrencyKey, so it never queues behind a full library scan;
//   - scans ONLY the given path (no full-library removal pass);
//   - validates the user-supplied path against configured import paths.
//
// It reuses the scanner's PerformScan (WalkDir handles both a directory and a
// single file), so a manual import goes through the exact same assembly + dedup +
// create pipeline as a scan — including (once wired) the import circuit breaker.
func (s *Server) RegisterLibraryImportOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.import",
		Plugin:          "library",
		DisplayName:     "Manual Import",
		Description:     "Import audiobooks from a specific folder or file (no full-library scan). The path must resolve under a configured import path.",
		DefaultPriority: opsregistry.PriorityHigh, // user-triggered, should not wait behind background scans
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "library.import",
		Permissions:     []auth.Permission{auth.PermScanTrigger},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p libraryImportParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("parse import params: %w", err)
				}
			}
			if p.Path == "" {
				return fmt.Errorf("path is required")
			}

			// Security: the path is user-supplied. Validate + canonicalize it
			// against the configured import paths before touching the filesystem.
			cleanPath, err := fileops.ValidateUserPath(s.Store(), p.Path)
			if err != nil {
				return fmt.Errorf("import path rejected: %w", err)
			}

			op := &logging.OpContext{
				ID:     ulid.Make().String(),
				Type:   "library.import",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)
			logging.Info(ctx, "manual import starting", "path", cleanPath)

			scanReq := &scanner.ScanRequest{FolderPath: &cleanPath}
			progress := registryProgressAdapter{r: reporter}
			if err := s.scanService.PerformScan(ctx, scanReq, operations.LoggerFromReporter(progress)); err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "manual import failed", "path", cleanPath, "err", err)
				return err
			}
			op.SetStatus("success")
			logging.Info(ctx, "manual import complete", "path", cleanPath)
			return nil
		},
	})
}

// RegisterLibraryOrganizeOp registers the "library.organize" v2 OperationDef.
func (s *Server) RegisterLibraryOrganizeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.organize",
		Plugin:          "library",
		DisplayName:     "Organize Library",
		Description:     "Move audiobook files into the canonical directory structure based on current metadata.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "library.organize",
		Permissions:     []auth.Permission{auth.PermScanTrigger},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p libraryOrganizeParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("library.organize: decode params: %w", err)
				}
			}
			opID := ulid.Make().String()

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     opID,
				Type:   "library.organize",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			op.AddEntity("books", p.BookIDs...)
			folderPath := ""
			if p.FolderPath != nil {
				folderPath = *p.FolderPath
			}
			logging.Info(ctx, "library organize starting",
				"book_count", len(p.BookIDs),
				"folder_path", folderPath,
				"fetch_metadata_first", p.FetchMetadataFirst,
				"sync_itunes_first", p.SyncITunesFirst)

			progress := registryProgressAdapter{r: reporter}
			organizeReq := &OrganizeRequest{
				FolderPath:         p.FolderPath,
				BookIDs:            p.BookIDs,
				FetchMetadataFirst: p.FetchMetadataFirst,
				SyncITunesFirst:    p.SyncITunesFirst,
				OperationID:        opID,
			}
			err := s.organizeService.PerformOrganize(ctx, organizeReq, operations.LoggerFromReporter(progress))
			// A cancelled run is not a failed one. PerformOrganize returns an
			// error for both now (it used to return nil unconditionally, so a
			// total failure was recorded as success), and marking a deliberate
			// cancel "failed" would just swap one misreport for another. The v2
			// registry worker already distinguishes these — it checks ctxCanceled
			// before runErr — so this only aligns the logged op status with it.
			if errors.Is(err, organizer.ErrOrganizeCanceled) {
				op.SetStatus("canceled")
				logging.Info(ctx, "library organize canceled", "err", err)
				return err
			}
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "library organize failed", "err", err)
				return err
			}
			op.SetStatus("success")
			logging.Info(ctx, "library organize complete", "book_count", len(p.BookIDs))
			return nil
		},
	})
}

// RegisterLibraryTranscodeOp registers the "library.transcode" v2 OperationDef.
func (s *Server) RegisterLibraryTranscodeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.transcode",
		Plugin:          "library",
		DisplayName:     "Transcode to M4B",
		Description:     "Transcode an audiobook file to M4B format and register it as a new version.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "", // transcodes can run in parallel
		Permissions:     []auth.Permission{auth.PermLibraryOrganize},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p libraryTranscodeParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("transcode: decode params: %w", err)
				}
			}
			if p.BookID == "" {
				return fmt.Errorf("transcode: book_id is required")
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     ulid.Make().String(),
				Type:   "library.transcode",
				Status: "pending",
			}
			op.AddEntity("books", p.BookID)
			ctx = logging.WithOp(ctx, op)
			logging.Info(ctx, "transcode starting",
				"book_id", p.BookID,
				"output_format", p.OutputFormat,
				"bitrate", p.Bitrate,
				"keep_original", p.KeepOriginal)

			progress := registryProgressAdapter{r: reporter}

			opts := transcode.TranscodeOpts{
				BookID:       p.BookID,
				OutputFormat: p.OutputFormat,
				Bitrate:      p.Bitrate,
				KeepOriginal: p.KeepOriginal,
			}

			outputPath, err := transcode.Transcode(ctx, opts, s.Store(), progress)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "transcode failed", "book_id", p.BookID, "err", err)
				return err
			}

			originalBook, err := s.Store().GetBookByID(p.BookID)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "transcode: failed to get original book", "book_id", p.BookID, "err", err)
				return fmt.Errorf("failed to get original book: %w", err)
			}

			groupID := ""
			if originalBook.VersionGroupID != nil && *originalBook.VersionGroupID != "" {
				groupID = *originalBook.VersionGroupID
			} else {
				groupID = ulid.Make().String()
			}

			notPrimary := false
			origNotes := "Original format"
			originalBook.IsPrimaryVersion = &notPrimary
			originalBook.VersionGroupID = &groupID
			originalBook.VersionNotes = &origNotes
			if _, err := s.Store().UpdateBook(p.BookID, originalBook); err != nil {
				progress.Log("warn", fmt.Sprintf("Failed to update original book version info: %v", err), nil)
			}

			m4bFormat := "m4b"
			aacCodec := "aac"
			bitrateVal := opts.Bitrate
			if bitrateVal <= 0 {
				bitrateVal = 128
			}
			isPrimary := true
			m4bNotes := "Transcoded to M4B"

			newBook := &database.Book{
				ID:                   ulid.Make().String(),
				Title:                originalBook.Title,
				FilePath:             outputPath,
				Format:               m4bFormat,
				Codec:                &aacCodec,
				Bitrate:              &bitrateVal,
				AuthorID:             originalBook.AuthorID,
				SeriesID:             originalBook.SeriesID,
				SeriesSequence:       originalBook.SeriesSequence,
				Duration:             originalBook.Duration,
				Narrator:             originalBook.Narrator,
				Publisher:            originalBook.Publisher,
				PrintYear:            originalBook.PrintYear,
				AudiobookReleaseYear: originalBook.AudiobookReleaseYear,
				ISBN10:               originalBook.ISBN10,
				ISBN13:               originalBook.ISBN13,
				ASIN:                 originalBook.ASIN,
				Language:             originalBook.Language,
				CoverURL:             originalBook.CoverURL,
				IsPrimaryVersion:     &isPrimary,
				VersionGroupID:       &groupID,
				VersionNotes:         &m4bNotes,
			}
			if _, err := s.Store().CreateBook(newBook); err != nil {
				progress.Log("warn", fmt.Sprintf("Failed to create M4B version record, updating original: %v", err), nil)
				isPrim := true
				fallbackNotes := fmt.Sprintf("Transcoded to M4B (in-place, original was at %s)", originalBook.FilePath)
				originalBook.FilePath = outputPath
				originalBook.Format = m4bFormat
				originalBook.Codec = &aacCodec
				originalBook.Bitrate = &bitrateVal
				originalBook.IsPrimaryVersion = &isPrim
				originalBook.VersionGroupID = &groupID
				originalBook.VersionNotes = &fallbackNotes
				if _, updateErr := s.Store().UpdateBook(p.BookID, originalBook); updateErr != nil {
					return updateErr
				}
				return nil
			}

			op.AddEntity("books", newBook.ID)
			progress.Log("info", fmt.Sprintf("Created M4B version %s (group %s), original %s demoted to non-primary", newBook.ID, groupID, p.BookID), nil)

			if !config.AppConfig.ITunes.WriteBackEnabled &&
				originalBook.ITunesPersistentID != nil &&
				*originalBook.ITunesPersistentID != "" {
				if err := s.Store().CreateDeferredITunesUpdate(
					originalBook.ID,
					*originalBook.ITunesPersistentID,
					originalBook.FilePath,
					newBook.FilePath,
					"transcode",
				); err != nil {
					progress.Log("warn", fmt.Sprintf("Failed to create deferred iTunes update: %v", err), nil)
				} else {
					progress.Log("info", "M4B created. iTunes library update deferred until write-back is enabled.", nil)
				}
			}

			op.SetStatus("success")
			logging.Info(ctx, "transcode complete", "book_id", p.BookID, "new_book_id", newBook.ID, "output_path", outputPath)
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterLibraryScanOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterLibraryImportOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterLibraryOrganizeOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterLibraryTranscodeOp(reg) })
}
