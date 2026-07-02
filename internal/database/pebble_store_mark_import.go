// file: internal/database/pebble_store_mark_import.go
// version: 1.1.0
// guid: e9f1a2b3-c4d5-6789-0abc-def123456789
// last-edited: 2026-07-01

package database

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// MarkFileImportedFromDeluge sets ImportedFromDelugeAt and DelugeOriginalPath on
// the matching BookFile. Matching is attempted by originalPath first and then
// by torrentHash (via BookVersion lookup). If no matching row is found, the
// call is a no-op and returns nil.
func (p *PebbleStore) MarkFileImportedFromDeluge(ctx context.Context, originalPath, libraryPath, torrentHash string) error {
	if originalPath == "" && torrentHash == "" {
		return fmt.Errorf("originalPath and torrentHash both empty")
	}

	// Attempt to find by original (download) path first.
	if originalPath != "" {
		bf, err := p.GetBookFileByPath(originalPath)
		if err != nil {
			return err
		}
		if bf != nil {
			now := time.Now()
			bf.DelugeOriginalPath = originalPath
			bf.FilePath = libraryPath
			bf.ImportedFromDelugeAt = &now
			if torrentHash != "" {
				bf.DelugeHash = torrentHash
				if bf.DownloadHash == "" {
					bf.DownloadHash = torrentHash
				}
			}
			return p.UpdateBookFile(bf.ID, bf)
		}
	}

	// Fallback: match by torrent hash to locate the BookVersion, then update
	// a file belonging to that book/version if we can find one.
	if torrentHash != "" {
		bv, err := p.GetBookVersionByTorrentHash(torrentHash)
		if err != nil {
			return err
		}
		if bv == nil {
			// No matching version — nothing to do.
			return nil
		}

		files, err := p.GetBookFiles(bv.BookID)
		if err != nil {
			return err
		}

		base := filepath.Base(libraryPath)
		for i := range files {
			f := &files[i]
			if f.DelugeHash == torrentHash || f.VersionID == bv.ID || filepath.Base(f.FilePath) == base {
				now := time.Now()
				f.DelugeOriginalPath = originalPath
				f.FilePath = libraryPath
				f.ImportedFromDelugeAt = &now
				if torrentHash != "" {
					f.DelugeHash = torrentHash
					if f.DownloadHash == "" {
						f.DownloadHash = torrentHash
					}
				}
				return p.UpdateBookFile(f.ID, f)
			}
		}
	}

	// No match found — treat as non-fatal.
	return nil
}
