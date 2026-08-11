// file: internal/plugins/maintenance/itunes_playlist_import.go
// version: 1.1.0
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
	// LibraryPath is the iTunes library to read: either an `iTunes
	// Library.xml` export or a binary `iTunes Library.itl`. The reader is
	// chosen by extension.
	//
	// Prefer the XML. The binary ITL parser extracts ZERO smart playlists from
	// real 12.13.10.3 libraries while the XML export of the SAME library yields
	// 292, each with an intact Smart Criteria blob (measured 2026-08-10 on the
	// owner's library, and on an April backup, so it is not writeback damage).
	// iTunes maintains both files; this reads the one that is legible.
	//
	// Defaults to config.ITunes.LibraryReadPath, which resolves to the Original
	// XML export unless import_source is "ao". That default was previously
	// refused precisely because it is an XML — now that XML is the better
	// source, it is the sensible default.
	LibraryPath string `json:"libraryPath"`
	// ITLPath is the previous name for LibraryPath, kept so existing callers
	// and saved op params keep working. LibraryPath wins if both are set.
	ITLPath string `json:"itlPath"`
	// OwnerUserID is stamped as CreatedByUserID on every imported playlist.
	//
	// The playlist API scopes list results per user, and CallingUserID returns
	// "_local" only when nobody is authenticated. Importing under "_local"
	// therefore hides every playlist from every logged-in account while the op
	// still reports a healthy count. Set this to the account that should see
	// them.
	OwnerUserID string `json:"ownerUserId"`
	// AllowEmptyQueries permits an apply run to create playlists whose Smart
	// Criteria did not translate to a query. Off by default: importing a
	// silently-empty playlist looks like success and is the exact confusion
	// this op exists to end. The raw criteria blob is stored on every imported
	// row, so shells imported this way can be re-translated once the parser is
	// fixed.
	AllowEmptyQueries bool `json:"allowEmptyQueries"`
}

func (p *Plugin) itunesPlaylistImportDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.itunes-playlist-import",
		Plugin:          "maintenance",
		DisplayName:     "Import iTunes smart (dynamic) playlists",
		Description:     "Reads smart playlists from an iTunes Library.xml export (preferred) or a binary .itl, translates each Smart Criteria blob into our query DSL, and creates a matching smart UserPlaylist. Read-only with respect to iTunes — writes only to our own store. Idempotent: playlists already imported are skipped by iTunes persistent ID. Default dry-run reports what would be imported; set dryRun=false to apply.",
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

	libPath := strings.TrimSpace(params.LibraryPath)
	if libPath == "" {
		libPath = strings.TrimSpace(params.ITLPath)
	}
	if libPath == "" {
		libPath = strings.TrimSpace(config.AppConfig.ITunes.LibraryReadPath)
	}
	if libPath == "" {
		return fmt.Errorf("itunes-playlist-import: libraryPath is required " +
			"(itunes.library_read_path is empty)")
	}

	st, err := os.Stat(libPath)
	if err != nil {
		return fmt.Errorf("itunes-playlist-import: cannot read %q: %w", libPath, err)
	}

	// Dispatch on extension. Feeding XML to ParseITL fails in a way that looks
	// like "no playlists found", so the choice must never be implicit.
	isXML := strings.HasSuffix(strings.ToLower(libPath), ".xml")
	source := "itl"
	if isXML {
		source = "xml"
	}
	_ = reporter.UpdateProgress(0, 3, fmt.Sprintf("Phase 1/3: parsing %s source %s (%.1f MB)…",
		source, libPath, float64(st.Size())/(1024*1024)))

	var lib *itunes.ITLLibrary
	if isXML {
		lib, err = itunes.ParseXMLLibraryPlaylists(libPath)
	} else {
		reporter.Log(slog.LevelWarn, fmt.Sprintf(
			"reading the BINARY .itl at %s — it extracts 0 smart playlists from real "+
				"12.13.10.3 libraries. Point libraryPath at the iTunes Library.xml export instead.", libPath))
		lib, err = itunes.ParseITL(libPath)
	}
	if err != nil {
		return fmt.Errorf("itunes-playlist-import: parse %s %q: %w", source, libPath, err)
	}
	if lib == nil {
		return fmt.Errorf("itunes-playlist-import: parse %s %q returned no library", source, libPath)
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
		slog.Warn("itunes-playlist-import: no smart playlists extracted", "source", source, "path", libPath, "playlists", len(lib.Playlists))
		_ = reporter.Log(slog.LevelWarn, msg)
		_ = reporter.UpdateProgress(3, 3, "0 smart playlists extracted — see log")
		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	importer := itunesservice.NewPlaylistImporter(p.deps.Store())

	// ALWAYS dry-run first, even when applying. The criteria translator is
	// currently producing empty queries (ITUNES-SMARTCRIT-PARSE): ParseSmartCriteria
	// misreads the SLst format and returns rules with no field, no operator and
	// no operands, WITHOUT erroring — so an apply run would happily create
	// hundreds of playlists that are all empty. Importing 292 empty playlists is
	// worse than importing none: it looks like it worked.
	probe := importer.MigrateSmartPlaylists(lib, itunesservice.PlaylistImportOptions{
		OwnerUserID: strings.TrimSpace(params.OwnerUserID),
		DryRun:      true,
	})
	empties := 0
	for _, it := range probe.Items {
		if it.Status == "would-import" && strings.TrimSpace(it.Query) == "" {
			empties++
		}
	}
	if empties > 0 {
		msg := fmt.Sprintf(
			"%d of %d importable playlists translate to an EMPTY query — their Smart "+
				"Criteria did not survive parsing (ITUNES-SMARTCRIT-PARSE). Importing them "+
				"would create playlists that are silently empty.", empties, probe.Imported)
		_ = reporter.Log(slog.LevelWarn, msg)
		slog.Warn("itunes-playlist-import: empty translated queries",
			"empty", empties, "importable", probe.Imported)

		if !params.DryRun && !params.AllowEmptyQueries {
			return fmt.Errorf("itunes-playlist-import: refusing to apply — %d of %d "+
				"playlists would be created with an empty query. Fix the Smart Criteria "+
				"parser first, or pass allowEmptyQueries=true to import them as empty "+
				"shells anyway (their raw criteria blob is stored, so they can be "+
				"re-translated later)", empties, probe.Imported)
		}
	}

	res := probe
	if !params.DryRun {
		res = importer.MigrateSmartPlaylists(lib, itunesservice.PlaylistImportOptions{
			OwnerUserID: strings.TrimSpace(params.OwnerUserID),
			DryRun:      false,
		})
	}

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
		"mode", mode, "source", source, "path", libPath,
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
