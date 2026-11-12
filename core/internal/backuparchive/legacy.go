// file: core/internal/backuparchive/legacy.go
// version: 1.0.0
// guid: 3879a968-243a-4935-b0f7-3e82b7eb7bb0
// last-edited: 2026-11-12

package backuparchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// LegacyInfo is what ScanLegacy found in a legacy-abo/0 archive: HEAD's
// `audiobooks_<dbtype>_<ts>.tar[.gz|.zst]` format (05-backup B7), one tar of
// one top-level directory, the store (HEAD names it after the store's or the
// checkpoint's directory, `audiobooks.pebble` in practice), with an optional
// `.sha256` sidecar beside the archive.
type LegacyInfo struct {
	SHA256  string // of the archive file, as `sha256sum` prints it
	Root    string // the one top-level directory
	Files   int
	Bytes   int64
	Codec   string // gzip, zstd or none, from the magic bytes
	scanned int64  // the file's size at the scan, so the extraction notices a change
}

// ErrNotAStore: the archive is not one store directory: it holds files at its
// top level, several top-level entries, or nothing a store holds.
var ErrNotAStore = errors.New("backuparchive: the archive is not one store directory")

// ErrChanged: the archive differs between the scan that vetted it and the pass
// that extracts it.
var ErrChanged = errors.New("backuparchive: the archive changed between the scan and the extraction")

// ScanLegacy reads a legacy archive end to end and writes nothing: it
// computes the file's SHA-256, validates every entry (a clean relative name,
// regular files and directories only, within lim) and requires the one
// top-level directory. HEAD's live extraction is not ported (§5B.10): a
// legacy archive is only ever extracted into staging, and only after this scan
// and the sidecar check have passed, so a bad archive costs no extraction
// beside the live store.
func ScanLegacy(ctx context.Context, archive string, lim Limits) (LegacyInfo, error) {
	var info LegacyInfo
	lim = lim.withDefaults()
	f, st, err := OpenRegular(archive, lim.MaxArchiveBytes)
	if err != nil {
		return info, err
	}
	defer f.Close()
	h := sha256.New()
	rd, err := NewReader(io.TeeReader(f, h), lim, true)
	if err != nil {
		return info, err
	}
	defer rd.Close()
	info.Codec = rd.Codec()
	roots := map[string]struct{}{}
	for {
		if err := ctx.Err(); err != nil {
			return info, err
		}
		hd, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return info, err
		}
		root, _, nested := strings.Cut(hd.Name, "/")
		if !nested && !hd.Dir {
			return info, fmt.Errorf("%w: %q is a file at the top level", ErrNotAStore, hd.Name)
		}
		roots[root] = struct{}{}
		if len(roots) > 1 {
			return info, fmt.Errorf("%w: more than one top-level entry", ErrNotAStore)
		}
		info.Root = root
		if hd.Dir {
			continue
		}
		if _, err := io.Copy(io.Discard, rd); err != nil {
			return info, fmt.Errorf("backuparchive: read %s: %w", hd.Name, err)
		}
		info.Files++
		info.Bytes += hd.Size
	}
	if rd.Count() == 0 {
		return info, ErrEmptyArchive
	}
	if info.Files == 0 {
		return info, fmt.Errorf("%w: it holds no file", ErrNotAStore)
	}
	if err := rd.Finish(); err != nil {
		return info, err
	}
	info.SHA256, info.scanned = hex.EncodeToString(h.Sum(nil)), st.Size()
	return info, nil
}

// ExtractLegacy extracts the store of a scanned legacy archive into storeDir,
// an empty directory the caller made, stripping the top-level directory: its
// entries land directly in storeDir. The archive is read again and its
// SHA-256 must equal info's, so an archive swapped after the scan is refused.
// Files are 0600 and directories 0700, every file is fsynced, and every
// directory by the final Sync.
func ExtractLegacy(ctx context.Context, archive string, info LegacyInfo, storeDir string, lim Limits) error {
	lim = lim.withDefaults()
	if entries, err := os.ReadDir(storeDir); err != nil {
		return err
	} else if len(entries) > 0 {
		return fmt.Errorf("backuparchive: extraction directory %s is not empty", storeDir)
	}
	ex, err := NewExtractor(storeDir)
	if err != nil {
		return err
	}
	defer ex.Close()
	f, st, err := OpenRegular(archive, lim.MaxArchiveBytes)
	if err != nil {
		return err
	}
	defer f.Close()
	if st.Size() != info.scanned {
		return ErrChanged
	}
	h := sha256.New()
	rd, err := NewReader(io.TeeReader(f, h), lim, true)
	if err != nil {
		return err
	}
	defer rd.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hd, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		var rel string
		switch {
		case hd.Name == info.Root && hd.Dir: // the root's own directory entry
		case strings.HasPrefix(hd.Name, info.Root+"/"):
			rel = strings.TrimPrefix(hd.Name, info.Root+"/")
		default:
			return fmt.Errorf("%w: %q is outside %q", ErrNotAStore, hd.Name, info.Root)
		}
		switch {
		case rel == "": // the root's own directory entry
		case hd.Dir:
			err = ex.Mkdir(rel)
		default:
			_, err = ex.WriteFile(rel, rd, nil)
		}
		if err != nil {
			return err
		}
	}
	if err := rd.Finish(); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != info.SHA256 {
		return ErrChanged
	}
	return ex.Sync()
}
