// file: internal/itunes/xml_library.go
// version: 1.0.0
// guid: 8a2f4c17-3e69-4d05-9b3a-6c1e7f0d24b8
// last-edited: 2026-08-10
// Reads playlists out of an `iTunes Library.xml` export into the same
// *ITLLibrary shape the binary .itl parser produces, so every consumer
// (notably service.MigrateSmartPlaylists) works against either source
// unchanged.

package itunes

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"

	"howett.net/plist"
)

// Why this exists: the binary .itl parser extracts ZERO smart playlists from
// real 12.13.10.3 libraries, while the XML export of the SAME library carries
// 292 of them, each with an intact Smart Criteria blob. The XML is the working
// source for reading playlists. iTunes writes both files, so this is not a
// workaround for missing data — it is reading the copy that is legible.
//
// Read-only: this opens the export for reading and never writes to the iTunes
// tree.

// xmlPlaylist mirrors one entry of the top-level `Playlists` array.
type xmlPlaylist struct {
	Name          string `plist:"Name"`
	PersistentID  string `plist:"Playlist Persistent ID"`
	Folder        bool   `plist:"Folder"`
	SmartInfo     []byte `plist:"Smart Info"`
	SmartCriteria []byte `plist:"Smart Criteria"`
	Items         []struct {
		TrackID int `plist:"Track ID"`
	} `plist:"Playlist Items"`
}

type xmlLibrary struct {
	ApplicationVersion string        `plist:"Application Version"`
	Playlists          []xmlPlaylist `plist:"Playlists"`
}

// ParseXMLLibraryPlaylists reads the playlist section of an iTunes XML export.
//
// Only playlists are populated on the returned library — tracks are not read,
// because the sole consumer needs `Playlists` and decoding a six-figure track
// array would cost memory for nothing. Callers that need tracks must keep using
// the .itl parser.
func ParseXMLLibraryPlaylists(path string) (*ITLLibrary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open iTunes XML: %w", err)
	}
	defer f.Close()

	var raw xmlLibrary
	dec := plist.NewDecoder(f)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode iTunes XML plist: %w", err)
	}

	lib := &ITLLibrary{Version: raw.ApplicationVersion}
	var badPID int
	for _, p := range raw.Playlists {
		// A playlist is smart exactly when it carries a criteria blob. The .itl
		// path has a separate IsSmart flag it must infer; here the presence of
		// the blob IS the evidence, so the two cannot disagree.
		pl := ITLPlaylist{
			Title:         p.Name,
			IsFolder:      p.Folder,
			IsSmart:       len(p.SmartCriteria) > 0,
			SmartInfo:     p.SmartInfo,
			SmartCriteria: p.SmartCriteria,
		}
		if pid, ok := decodePersistentID(p.PersistentID); ok {
			pl.PersistentID = pid
		} else if p.PersistentID != "" {
			// Do not silently import a playlist under a zero PID: the importer
			// keys idempotency on it, so every such playlist would collide with
			// every other one and all but the first would look "already
			// imported".
			badPID++
			continue
		}
		for _, it := range p.Items {
			pl.Items = append(pl.Items, it.TrackID)
		}
		lib.Playlists = append(lib.Playlists, pl)
	}
	if badPID > 0 {
		slog.Warn("itunes-xml: skipped playlists with an undecodable persistent ID",
			"skipped", badPID, "path", path)
	}

	smart := 0
	for _, p := range lib.Playlists {
		if p.IsSmart {
			smart++
		}
	}
	slog.Info("itunes-xml: parsed playlists",
		"path", path, "playlists", len(lib.Playlists), "smart", smart,
		"app_version", raw.ApplicationVersion)
	return lib, nil
}

// decodePersistentID converts iTunes' 16-hex-char persistent ID into the
// 8-byte array the ITL types use. Returns ok=false for anything else.
func decodePersistentID(s string) ([8]byte, bool) {
	var out [8]byte
	if len(s) != 16 {
		return out, false
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		return out, false
	}
	copy(out[:], b)
	return out, true
}
