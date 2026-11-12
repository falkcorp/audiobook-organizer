// file: internal/metadata/taglib_exported.go
// version: 2.0.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-11-12
//
// Exported wrappers around the internal taglib read/write functions.
// Both the WASM (taglib_support.go) and CGO (taglib_cgo.go) build
// variants define the same internal signatures; this file calls them
// without needing to duplicate the build-tag logic.

package metadata

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// packageSafeWriteDeps holds the optional Deluge-guard dependencies wired
// in at startup by the server via SetSafeWriteDeps. Zero value = no guard
// (writes proceed in-place as before).
var packageSafeWriteDeps tagger.SafeWriteDeps

// SetSafeWriteDeps installs the pre-flight guard dependencies for all taglib
// writes in this package. Must be called once at server startup, before any
// tag writes occur.
//
// The Importer is dropped: every write through this package (the book PATCH
// write-back, single-tag fixes, tag reverts) REFUSES a protected path with
// tagger.ErrProtectedPathWrite instead of importing it. An import here copied
// a Deluge-seeding file to RootDir/<basename> -- outside any book folder, with
// no organize -- and repointed its row there, scattering files into the
// library root. A protected book's tags are written only on its library copy,
// which metafetch resolves before it writes.
func SetSafeWriteDeps(deps tagger.SafeWriteDeps) {
	deps.Importer = nil
	packageSafeWriteDeps = deps
}

// ReadRawTags returns the raw tag key→values map for a file via TagLib.
// Keys are uppercase (e.g. "COMPOSER", "ALBUMARTIST"). Useful for
// maintenance tools that need the actual on-disk value rather than the
// parsed Metadata struct produced by ExtractMetadata.
func ReadRawTags(filePath string) (map[string][]string, error) {
	return readTagsWithTaglib(filePath)
}

// WriteSingleTagContextVia writes one tag property to a file without
// disturbing any other tags. Pass value="" to clear the property. It keeps a
// .bak-* sibling when create_backups is on; a ctx wrapped with
// tagger.WithoutBackup writes none, whatever create_backups says.
//
// The file's new hashes (and, when rec also records provenance, a failed
// verification) are recorded on its book_file row through rec, the store the
// caller was handed (a maintenance job), never one reached through a package
// variable. A nil rec records nothing.
func WriteSingleTagContextVia(ctx context.Context, rec fileops.BookFileHashRecorder, filePath, tagName, value string) error {
	return writeSingleTagWithTaglibVia(ctx, rec, filePath, tagName, value)
}
