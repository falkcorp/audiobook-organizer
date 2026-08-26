// file: internal/metadata/taglib_support.go
// version: 2.5.0
// guid: 0c1d2e3f-4a5b-6c7d-8e9f-0a1b2c3d4e5f
//
// TagLib WASM writer (default, no CGO required).
// For native CGO performance, build with -tags native_taglib.

//go:build !native_taglib

package metadata

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	taglib "go.senan.xyz/taglib"
)

// taglibAvailable indicates native taglib path compiled in
var taglibAvailable = true

// writeMetadataWithTaglib performs metadata writing using TagLib via WASM.
// TagLib edits tag atoms in place and does not corrupt audio data on failure —
// no pre-write file copy is needed. The optional WriteBackupBeforeTagWrite
// config flag handles backups at the call-site layer (backupFileBeforeWrite).
//
// If packageSafeWriteDeps is configured, protected (Deluge-managed) paths are
// imported into the library before the write proceeds.
func writeMetadataWithTaglib(filePath string, metadata map[string]any, _ fileops.OperationConfig) error {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("taglib abs path: %w", err)
	}

	tags := buildWriteTagMap(metadata)
	if len(tags) == 0 {
		return fmt.Errorf("no writable metadata supplied")
	}

	if err := tagger.WriteTagsSafe(context.Background(), abs, tags, 0, packageSafeWriteDeps); err != nil {
		return fmt.Errorf("taglib write: %w", err)
	}

	return nil
}

// writeMetadataWithTaglibInPlace writes tags straight to filePath, skipping the
// tagger.WriteTagsSafe wrapper (which itself wraps fileops.WriteTagsSafe).
//
// Contract: the caller MUST already be operating on a temp copy created by an
// outer fileops.WriteTagsSafe, and MUST have already resolved the protected-path
// question against the real path. See tagger.WriteTagsInPlace for the rationale.
func writeMetadataWithTaglibInPlace(filePath string, metadata map[string]any, _ fileops.OperationConfig) error {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("taglib abs path: %w", err)
	}

	tags := buildWriteTagMap(metadata)
	if len(tags) == 0 {
		return fmt.Errorf("no writable metadata supplied")
	}

	if err := tagger.WriteTagsInPlace(abs, tags, 0); err != nil {
		return fmt.Errorf("taglib write in place: %w", err)
	}
	return nil
}

// writeSingleTagWithTaglib writes one tag property without touching others.
// Pass value="" to clear the property from the file.
//
// If packageSafeWriteDeps is configured, protected (Deluge-managed) paths are
// imported into the library before the write proceeds.
func writeSingleTagWithTaglib(filePath, tagName, value string) error {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("taglib abs: %w", err)
	}
	return tagger.WriteTagsSafe(context.Background(), abs, map[string][]string{tagName: {value}}, 0, packageSafeWriteDeps)
}

// readTagsWithTaglib reads tags from a file via the TagLib WASM runtime.
// Returns a flat key → list-of-values map exactly like TagLib's property
// interface: "TITLE" → ["Foundation and Empire"], "ARTIST" → ["Isaac
// Asimov"], plus any custom properties the file has (AUDIOBOOK_ORGANIZER_*,
// SERIES, SERIES_INDEX, NARRATOR, etc.). Used as a fallback by
// ExtractMetadata when dhowden/tag can't parse a file — typically happens
// on unusual M4B variants, DRM-touched files, or edge-case Vorbis comments.
func readTagsWithTaglib(filePath string) (map[string][]string, error) {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("taglib read abs: %w", err)
	}
	tags, err := taglib.ReadTags(abs)
	if err != nil {
		return nil, fmt.Errorf("taglib read: %w", err)
	}
	return tags, nil
}
