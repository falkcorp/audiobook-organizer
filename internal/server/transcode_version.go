// file: internal/server/transcode_version.go
// version: 1.0.0
// guid: 9e2b7c41-3d58-4a16-8f0e-6c1d4a7b2e93
// last-edited: 2026-09-24

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

var transcodeLog = logger.New("transcode-version")

// transcodeVersionStore is what recording a transcode's output needs: the
// version-primary hand-off, plus creating the output's book and file rows.
type transcodeVersionStore interface {
	versionprimary.EnsureStore
	CreateBook(book *database.Book) (*database.Book, error)
	CreateBookFile(file *database.BookFile) error
}

// recordTranscodedVersion records a finished transcode's output as a new
// version of original and decides the group's primary.
//
// Until 2026-09-24 the op demoted the original, then created the output as
// explicit primary with no library_state and no book_file row. That broke the
// group two ways: when the original was not the group's primary, the output
// became a SECOND primary beside the real one; and the output itself could
// never be shown by ABS (which needs primary AND organized), so a transcoded
// primary hid the book. Now:
//
//   - The output inherits the original's library_state (it is written beside
//     the original), downgraded to "imported" when an organized original's
//     output would land outside the library root, and gets a book_file row
//     for its one file. That is what makes it eligible under the shared rule.
//   - It is created as explicit false, and the original is not demoted up
//     front.
//   - If the original held the group's primary role (explicit true, or it had
//     no group yet) and the output is eligible, the output takes the role
//     with versionprimary.Crown: replacing the original's format is the
//     point of the transcode. Otherwise versionprimary.EnsureSinglePrimary
//     decides, so an ineligible output never takes the primary from a copy
//     ABS can show, and a group whose primary was another member keeps it.
//
// When the output's book row cannot be created the original row is rewritten
// in place to the output (the fallback the op always had); it keeps its own
// flag and the group is handed to EnsureSinglePrimary, instead of being
// forced to true beside a possible other primary. inPlace reports that.
func recordTranscodedVersion(ctx context.Context, store transcodeVersionStore, original *database.Book,
	outputPath string, bitrate int, rootDir string, logf func(level, msg string)) (newBook *database.Book, inPlace bool, err error) {
	groupID := ""
	if original.VersionGroupID != nil && *original.VersionGroupID != "" {
		groupID = *original.VersionGroupID
	} else {
		groupID = ulid.Make().String()
	}
	// The original's role is read from the row under its lock, not from the
	// snapshot taken before the (long) transcode.
	wasPrimary := false
	origNotes := "Original format"
	grouped, err := store.ModifyBook(original.ID, func(b *database.Book) error {
		wasPrimary = b.VersionGroupID == nil || *b.VersionGroupID == "" ||
			(b.IsPrimaryVersion != nil && *b.IsPrimaryVersion)
		b.VersionGroupID = &groupID
		b.VersionNotes = &origNotes
		return nil
	})
	switch {
	case err != nil:
		logf("warn", fmt.Sprintf("Failed to update original book version info: %v", err))
	case grouped == nil:
		logf("warn", fmt.Sprintf("Failed to update original book version info: book %s no longer exists", original.ID))
	}

	m4bFormat, aacCodec := "m4b", "aac"
	notPrimary := false
	m4bNotes := "Transcoded to M4B"
	var state *string
	if original.LibraryState != nil {
		st := *original.LibraryState
		if st == "organized" && (rootDir == "" || !pathutil.IsWithin(outputPath, rootDir)) {
			st = "imported"
		}
		state = &st
	}
	nb := &database.Book{
		ID:                   ulid.Make().String(),
		Title:                original.Title,
		FilePath:             outputPath,
		Format:               m4bFormat,
		Codec:                &aacCodec,
		Bitrate:              &bitrate,
		AuthorID:             original.AuthorID,
		SeriesID:             original.SeriesID,
		SeriesSequence:       original.SeriesSequence,
		Duration:             original.Duration,
		Narrator:             original.Narrator,
		Publisher:            original.Publisher,
		PrintYear:            original.PrintYear,
		AudiobookReleaseYear: original.AudiobookReleaseYear,
		ISBN10:               original.ISBN10,
		ISBN13:               original.ISBN13,
		ASIN:                 original.ASIN,
		Language:             original.Language,
		CoverURL:             original.CoverURL,
		LibraryState:         state,
		IsPrimaryVersion:     &notPrimary,
		VersionGroupID:       &groupID,
		VersionNotes:         &m4bNotes,
	}
	if _, cerr := store.CreateBook(nb); cerr != nil {
		logf("warn", fmt.Sprintf("Failed to create M4B version record, updating original: %v", cerr))
		rewritten, updateErr := store.ModifyBook(original.ID, func(b *database.Book) error {
			fallbackNotes := fmt.Sprintf("Transcoded to M4B (in-place, original was at %s)", b.FilePath)
			b.FilePath = outputPath
			b.Format = m4bFormat
			b.Codec = &aacCodec
			b.Bitrate = &bitrate
			b.VersionGroupID = &groupID
			b.VersionNotes = &fallbackNotes
			return nil
		})
		if updateErr != nil {
			return nil, true, updateErr
		}
		if rewritten == nil {
			return nil, true, fmt.Errorf("transcode: book %s no longer exists; transcoded file left at %s", original.ID, outputPath)
		}
		// The same row, so it keeps the role it had: re-crowned when it was
		// the primary (demoting nobody else unless another member had also
		// been left primary), otherwise the group decides.
		if wasPrimary {
			handOffTranscodeGroup(ctx, store, groupID, original.ID, rootDir, true)
		} else {
			handOffTranscodeGroup(ctx, store, groupID, "", rootDir, false)
		}
		return rewritten, true, nil
	}

	bf := &database.BookFile{
		ID:               ulid.Make().String(),
		BookID:           nb.ID,
		FilePath:         outputPath,
		OriginalFilename: filepath.Base(outputPath),
		Format:           m4bFormat,
		Codec:            aacCodec,
		BitrateKbps:      bitrate,
		TrackNumber:      1,
	}
	if fi, serr := os.Stat(outputPath); serr == nil {
		bf.FileSize = fi.Size()
	}
	if ferr := store.CreateBookFile(bf); ferr != nil {
		// The output then has no active file, so the rule below keeps it
		// from the primary; the next scan of its folder adds the row.
		logf("warn", fmt.Sprintf("Failed to create the M4B version's file row: %v", ferr))
	}

	crown := false
	if wasPrimary {
		loader := versionprimary.Loader{Files: store, Chapters: store, RootDir: rootDir}
		if sig, lerr := loader.Load(ctx, nb, true); lerr != nil {
			transcodeLog.Warn("transcode: reading signals of %s failed: %v", logger.SanitizeLogValue(nb.ID), lerr)
		} else if reason := versionprimary.IneligibleReason(nb, sig); reason == "" {
			crown = true
		} else {
			transcodeLog.Info("transcode: output %s not crowned (%s); version group %s decides its primary",
				logger.SanitizeLogValue(nb.ID), reason, logger.SanitizeLogValue(groupID))
		}
	}
	handOffTranscodeGroup(ctx, store, groupID, nb.ID, rootDir, crown)
	return nb, false, nil
}

// handOffTranscodeGroup crowns keepID when crown is set, and otherwise runs
// EnsureSinglePrimary on gid. Best-effort: the transcode has committed; a
// failure is logged and left for version-group-primary-repair.
func handOffTranscodeGroup(ctx context.Context, store versionprimary.EnsureStore, gid, keepID, rootDir string, crown bool) {
	var (
		res versionprimary.HandoffResult
		err error
	)
	if crown {
		res, err = versionprimary.Crown(store, gid, keepID)
	} else {
		res, err = versionprimary.EnsureSinglePrimary(ctx, store, gid, versionprimary.Env{RootDir: rootDir})
	}
	if err != nil {
		transcodeLog.Warn("transcode: primary hand-off in version group %s failed: %v", logger.SanitizeLogValue(gid), err)
		return
	}
	transcodeLog.Info("transcode: version group %s primary %s (%s)",
		logger.SanitizeLogValue(gid), logger.SanitizeLogValue(res.PrimaryID), res.Outcome)
}
