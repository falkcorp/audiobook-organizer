// file: internal/plugins/maintenance/itunes_playlist_import.go
// version: 1.0.0
// guid: 7c4e91a3-58bd-42f6-9e0a-1d6b3f8c25e4
// last-edited: 2026-08-10

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// iTunes smart-playlist import (spec 3.4 task 5).
//
// The importer itself has existed and been tested since PlaylistSync v2.0 —
// it was simply never called. `MigrateSmartPlaylists` and `PushDirty` had ZERO
// non-test callers, so no dynamic playlist was ever imported and none pushed.
// That produced no error and no failed op, because there was no op. This file
// is the missing invocation.
//
// READ-ONLY with respect to iTunes. It parses an .itl and writes only
// UserPlaylist rows into our own store, so it is safe against the hands-off
// Original tree. It declares CapLibraryRead only — deliberately NOT
// CapLibraryWrite, which would both misdescribe it and invite a future caller
// to assume writing is allowed. The push direction (PushDirty) is a separate,
// still-unwired unit and is intentionally not exposed here.

type itunesPlaylistImportParams struct {
	// DryRun reports what would be imported without creating rows.
	DryRun bool `json:"dryRun"`
	// ITLPath is the binary iTunes Library.itl to read.
	//
	// Required in practice. It is NOT defaulted from config.LibraryReadPath
	// because that field resolves to Libraries.Original.XMLPath whenever
	// ImportSource is "original" or unset (itunes_libraries.go Resolve()) —
	// i.e. an XML file, which the binary ITL parser cannot read. Silently
	// feeding XML to ParseITL would fail in a way that looks like "no
	// playlists found".
	ITLPath string `json:"itlPath"`
	// OwnerUserID is stamped as CreatedByUserID on every imported playlist.
	//
	// The playlist API scopes list results per user, and CallingUserID returns
	// "_local" only when nobody is authenticated. Importing under "_local"
	// therefore hides every playlist from every logged-in account while the op
	// still reports a healthy count. Set this to the account that should see
	// them.
	OwnerUserID string `json:"ownerUserId"`
}

func (p *Plugin) itunesPlaylistImportDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.itunes-playlist-import",
		Plugin:          "maintenance",
		DisplayName:     "Import iTunes smart (dynamic) playlists",
		Description:     "Reads smart playlists from a binary iTunes Library.itl, translates each Smart Criteria blob into our query DSL, and creates a matching smart UserPlaylist. Read-only with respect to iTunes — writes only to our own store. Idempotent: playlists already imported are skipped by iTunes persistent ID. Default dry-run reports what would be imported; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.itunes-playlist-import",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runITunesPlaylistImport,
	}
}

func (p *Plugin) runITunesPlaylistImport(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := itunesPlaylistImportParams{DryRun: true} // safe default
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("itunes-playlist-import: bad params: %w", err)
		}
	}

	itlPath := strings.TrimSpace(params.ITLPath)
	if itlPath == "" {
		// Fall back to the configured read path ONLY if it actually is an ITL.
		// See the ITLPath doc comment: LibraryReadPath is commonly an XML.
		if cand := config.AppConfig.ITunes.LibraryReadPath; strings.HasSuffix(strings.ToLower(cand), ".itl") {
			itlPath = cand
		}
	}
	if itlPath == "" {
		return fmt.Errorf("itunes-playlist-import: itlPath is required " +
			"(the configured itunes.library_read_path is empty or not a .itl — " +
			"it resolves to the Original XML export unless import_source is \"ao\")")
	}

	st, err := os.Stat(itlPath)
	if err != nil {
		return fmt.Errorf("itunes-playlist-import: cannot read %q: %w", itlPath, err)
	}
	_ = reporter.UpdateProgress(0, 3, fmt.Sprintf("Phase 1/3: parsing %s (%.1f MB)…", itlPath, float64(st.Size())/(1024*1024)))

	lib, err := itunes.ParseITL(itlPath)
	if err != nil {
		return fmt.Errorf("itunes-playlist-import: parse %q: %w", itlPath, err)
	}
	if lib == nil {
		return fmt.Errorf("itunes-playlist-import: parse %q returned no library", itlPath)
	}

	smart := 0
	for _, pl := range lib.Playlists {
		if pl.IsSmart && len(pl.SmartCriteria) > 0 {
			smart++
		}
	}
	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Phase 2/3: %d playlists in library, %d smart", len(lib.Playlists), smart))

	// MEASURED 2026-08-10 and unresolved: the binary ITL parser extracts ZERO
	// smart playlists from real iTunes 12.13.10.3 libraries, while the XML
	// export of the SAME library contains 292 "Smart Criteria" blobs (351
	// playlists total). Reproduced on the live library and on an April backup,
	// so it is a parser gap, not writeback data loss. The extraction code does
	// exist (itl_be.go / itl_le.go populate IsSmart + SmartCriteria) — it just
	// does not fire on these files.
	//
	// Without this branch the op's honest output ("0 smart playlists found, 0
	// imported") is indistinguishable from "you have no smart playlists", which
	// is precisely the failure mode this whole op exists to end. Say it loudly.
	if smart == 0 && len(lib.Playlists) > 0 {
		msg := fmt.Sprintf(
			"parsed %d playlists but found 0 with smart criteria — nothing to import. "+
				"This is a KNOWN GAP, not necessarily an empty library: the XML export of the "+
				"same library can contain hundreds of smart playlists the binary ITL parser "+
				"does not surface. Verify with: grep -c 'Smart Criteria' '<library>.xml'",
			len(lib.Playlists))
		slog.Warn("itunes-playlist-import: no smart playlists extracted", "itl", itlPath, "playlists", len(lib.Playlists))
		_ = reporter.Log(slog.LevelWarn, msg)
		_ = reporter.UpdateProgress(3, 3, "0 smart playlists extracted — see log")
		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	importer := itunesservice.NewPlaylistImporter(p.deps.Store())
	res := importer.MigrateSmartPlaylists(lib, itunesservice.PlaylistImportOptions{
		OwnerUserID: strings.TrimSpace(params.OwnerUserID),
		DryRun:      params.DryRun,
	})

	// Report by RE-READING the store, not by trusting the returned counter.
	// On an apply run the authoritative number is how many smart playlists
	// actually exist afterwards.
	verified := -1
	if !params.DryRun {
		if _, total, lerr := p.deps.Store().ListUserPlaylists("smart", 1, 0); lerr == nil {
			verified = total
		} else {
			slog.Warn("itunes-playlist-import: verification re-read failed", "err", lerr)
		}
	}

	mode := "APPLY"
	if params.DryRun {
		mode = "DRY-RUN"
	}
	slog.Info("itunes-playlist-import complete",
		"mode", mode, "itl", itlPath,
		"playlistsInLibrary", len(lib.Playlists),
		"smartFound", res.SmartFound,
		"imported", res.Imported, "skipped", res.Skipped,
		"smartPlaylistsInStoreAfter", verified,
		"owner", params.OwnerUserID)

	summary := fmt.Sprintf("%s: %d smart playlists found, %d imported, %d skipped",
		mode, res.SmartFound, res.Imported, res.Skipped)
	if verified >= 0 {
		summary += fmt.Sprintf("; %d smart playlists in store after", verified)
	}
	_ = reporter.UpdateProgress(3, 3, summary)

	_ = reporter.Log(slog.LevelInfo, summary)
	for _, it := range res.Items {
		if it.Status == "unparseable" || it.Status == "create-failed" {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("%s: %s (%s)", it.Status, it.Title, it.Err))
		}
	}
	return nil
}
