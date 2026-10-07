// file: internal/itunes/writeback_root.go
// version: 1.0.0
// guid: 3c7d1e52-8a4f-4b96-9e2d-6f0a1b8c5d47
// last-edited: 2026-10-07
//
// The AO writeback library's own media root, derived from the library's path.
//
// WHY this exists (2026-10-07): the location-form guard rejects any location
// containing ".itunes-writeback/" as a staging-dir leak unless
// ContractConfig.AllowedWritebackRoot scopes it. Only RunRelocateSyncCycle ever
// set the root. Every production writer of `itunes.library_write_path` (the
// write-back batcher, /itunes/relocate, /itunes/rebuild, /itunes/write-back[-all],
// the importer's deferred updates) wrote the AO library with the strict default,
// so once iTunes was pointed at that library (about 46k tracks under
// W:/audiobook-organizer/.itunes-writeback/iTunes Media/) every write was
// rejected and the batcher dropped the batch. See
// docs/plans/2026-10-07-itunes-writeback-drops.md.

package itunes

import (
	"path/filepath"
	"strings"
)

// writebackDirName is the directory the AO writeback library lives in.
const writebackDirName = ".itunes-writeback"

// WritebackRootForLibrary returns the AllowedWritebackRoot fragment for the
// library at itlPath, or "" (strict guard) when itlPath is not an AO writeback
// library.
//
// The library is an AO writeback library exactly when its .itl sits directly in
// a directory named ".itunes-writeback". The returned fragment is
// "<parent>/.itunes-writeback/" (for prod "audiobook-organizer/.itunes-writeback/"),
// the same value cmd/pid-census passes. The guard lowercases both sides and
// normalizes separators, so the fragment matches the 0x0D form
// (W:\audiobook-organizer\.itunes-writeback\...) and the 0x0B form
// (file://localhost/W:/audiobook-organizer/.itunes-writeback/...) alike.
//
// The host-side prefix (/mnt/bigdata/books vs W:) differs between the server and
// the Windows paths iTunes stores, so only the tail from the parent directory
// onward is common to both, which is why the root is a fragment and not a full
// path. Limitation: a parent name that URL-escapes (a space, '%') would not match
// the escaped 0x0B form; such a library stays effectively strict for 0x0B, which
// fails closed.
//
// A library under books/itunes (the Original, hands-off library) never sits in a
// ".itunes-writeback" directory, so it always gets "" and stays strict.
func WritebackRootForLibrary(itlPath string) string {
	if itlPath == "" {
		return ""
	}
	clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(itlPath, "\\", "/")))
	dir := filepath.Dir(clean)
	if filepath.Base(dir) != writebackDirName {
		return ""
	}
	parent := filepath.Base(filepath.Dir(dir))
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		// ".itunes-writeback" at a filesystem root: no parent to anchor on.
		// Stay strict rather than allow every ".itunes-writeback/" marker.
		return ""
	}
	return parent + "/" + writebackDirName + "/"
}

// withWritebackRoot fills an empty cfg.AllowedWritebackRoot from the library
// being written. An explicit root from the caller is kept as is.
func withWritebackRoot(cfg ContractConfig, itlPath string) ContractConfig {
	if cfg.AllowedWritebackRoot == "" {
		cfg.AllowedWritebackRoot = WritebackRootForLibrary(itlPath)
	}
	return cfg
}

// WritebackContractConfig returns the default (bounded) contract config scoped
// to the library at itlPath: DefaultContractConfig plus the library's
// AllowedWritebackRoot. Writers of the configured write-back library use it so
// the write, and any re-read audit of the written file, run under one config.
func WritebackContractConfig(itlPath string) ContractConfig {
	return withWritebackRoot(DefaultContractConfig(), itlPath)
}
