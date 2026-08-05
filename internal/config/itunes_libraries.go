// file: internal/config/itunes_libraries.go
// version: 1.0.0
// guid: 5b2e9c47-1a08-4d63-8f92-3c7a0e6b1d54
// last-edited: 2026-07-23
//
// The 4-state iTunes library model + its config-load Resolve/Validate. Two physical
// libraries (Original = the real hands-off tree under books/itunes/**; AO = the
// writeback library under .itunes-writeback/), each with a binary .itl (the only
// write/authority surface) and a read-only .xml export iTunes regenerates and never
// reads back. See docs/specs/2026-07-23-itunes-2way-sync-system-design.md §2.
//
// This is SCAFFOLD (P0): the types + derivation + fail-closed assertions. It is
// inert until an operator populates ITunesConfig.Libraries — when Libraries is
// empty, Resolve() is a no-op and the legacy LibraryReadPath/LibraryWritePath are
// left exactly as loaded, so nothing changes for existing deployments.

package config

import (
	"fmt"
	"strings"
)

// LibraryRef is one physical iTunes library: its live binary DB and its read-only
// regenerated export. iTunes only ever READS the .itl; the .xml is a one-way
// convenience export (never read back by iTunes).
type LibraryRef struct {
	ITLPath string `json:"itl_path" mapstructure:"itl_path"` // binary runtime DB — the write/authority surface
	XMLPath string `json:"xml_path" mapstructure:"xml_path"` // read-only export — parse-convenience only
	Frozen  bool   `json:"frozen"   mapstructure:"frozen"`   // true => HANDS-OFF, read-only recoverable fallback
}

// LibrarySet models the full 4-state world plus the two orthogonal mode facts.
type LibrarySet struct {
	// Original: the REAL externally-managed iTunes library under books/itunes/**.
	// ALWAYS Frozen=true in steady state. NEVER a write target.
	Original LibraryRef `json:"original" mapstructure:"original"`

	// AO: the writeback library iTunes is pointed at, under .itunes-writeback/.
	// Its ITLPath is the sole live edit target and, post-cutover, the sole runtime truth.
	AO LibraryRef `json:"ao" mapstructure:"ao"`

	// PointedAt = which library iTunes itself is currently running against:
	// "original" | "ao". Set by the human when they repoint iTunes.
	PointedAt string `json:"pointed_at" mapstructure:"pointed_at"`

	// ImportSource = which library the importer reads NEW audiobooks from:
	// "original" (legacy one-time import) | "ao" (steady-state re-import).
	ImportSource string `json:"import_source" mapstructure:"import_source"`
}

// Configured reports whether an operator has populated the 4-state model at all.
// When false, the whole subsystem stays on the legacy 2-path behavior.
func (s LibrarySet) Configured() bool {
	return s.AO.ITLPath != "" || s.Original.ITLPath != "" || s.Original.XMLPath != ""
}

// Resolve derives the legacy LibraryReadPath/LibraryWritePath shims from the
// 4-state model so every ambient reader keeps working unchanged (spec §2.3). It is
// a no-op when Libraries is not configured, preserving legacy behavior exactly.
//
//   - LibraryWritePath := Libraries.AO.ITLPath — always (the write target never changes).
//   - LibraryReadPath  := ImportSource=="ao" ? Libraries.AO.ITLPath : Libraries.Original.XMLPath.
func (c *ITunesConfig) Resolve() {
	if c == nil || !c.Libraries.Configured() {
		return
	}
	if c.Libraries.AO.ITLPath != "" {
		c.LibraryWritePath = c.Libraries.AO.ITLPath
	}
	switch c.Libraries.ImportSource {
	case "ao":
		if c.Libraries.AO.ITLPath != "" {
			c.LibraryReadPath = c.Libraries.AO.ITLPath
		}
	default: // "original" or unset
		if c.Libraries.Original.XMLPath != "" {
			c.LibraryReadPath = c.Libraries.Original.XMLPath
		}
	}
}

// booksItunesSegment matches the hands-off Original tree by path segment, so it
// catches the real library regardless of the absolute mount prefix.
const booksItunesSegment = "books/itunes/"

// UnderFrozenITunesTree reports whether a path lives in the hands-off Original
// iTunes tree (books/itunes/**).
//
// That tree is externally managed by iTunes itself and is marked Frozen —
// read-only, never reorganised by us. Callers that PROPOSE structural changes
// (regroup holds, merges, moves) must consult this and skip such paths, because a
// proposal we are not permitted to carry out is noise in a human's queue at best
// and a data-loss invitation at worst.
//
// Exported for producers outside this package; underBooksItunes remains the
// internal spelling used by validation.
func UnderFrozenITunesTree(p string) bool { return underBooksItunes(p) }

func underBooksItunes(p string) bool {
	if p == "" {
		return false
	}
	clean := strings.ReplaceAll(p, "\\", "/")
	return strings.Contains(clean+"/", booksItunesSegment)
}

func pathCoveredByProtected(p string, protected []string) bool {
	if p == "" {
		return true // nothing to protect
	}
	clean := strings.ReplaceAll(p, "\\", "/")
	for _, pref := range protected {
		if pref == "" {
			continue
		}
		if strings.HasPrefix(clean, strings.ReplaceAll(pref, "\\", "/")) {
			return true
		}
	}
	return false
}

// ValidateLibraries returns the fail-closed assertion failures (spec §2.4). Empty
// slice = OK. Inert when Libraries is not configured (so existing deployments are
// unaffected until they adopt the 4-state model). protectedPaths is the effective
// Config.ProtectedPaths.
func (c *ITunesConfig) ValidateLibraries(protectedPaths []string) []string {
	if c == nil || !c.Libraries.Configured() {
		return nil
	}
	var errs []string
	L := c.Libraries

	// 1. The Original tree MUST be covered by ProtectedPaths (both files).
	if L.Original.ITLPath != "" && !pathCoveredByProtected(L.Original.ITLPath, protectedPaths) {
		errs = append(errs, fmt.Sprintf("itunes.libraries.original.itl_path %q is not covered by any protected_paths prefix (the hands-off Original tree must be protected)", L.Original.ITLPath))
	}
	if L.Original.XMLPath != "" && !pathCoveredByProtected(L.Original.XMLPath, protectedPaths) {
		errs = append(errs, fmt.Sprintf("itunes.libraries.original.xml_path %q is not covered by any protected_paths prefix", L.Original.XMLPath))
	}

	// 2. The AO write target must NEVER resolve under books/itunes/**.
	if underBooksItunes(L.AO.ITLPath) {
		errs = append(errs, fmt.Sprintf("itunes.libraries.ao.itl_path %q resolves under books/itunes/** — the writeback target must never point at the Original library", L.AO.ITLPath))
	}

	// 3. The fallback source cannot be silently mutable once AO is authoritative.
	if L.PointedAt == "ao" && L.Original.ITLPath != "" && !L.Original.Frozen {
		errs = append(errs, "itunes.libraries.original.frozen must be true while pointed_at==\"ao\" (the recoverable fallback source cannot be mutable)")
	}

	// 4. No zero-value write target while any sync-cycle op is enabled.
	if (c.SyncEnabled || c.WriteBackEnabled) && L.AO.ITLPath == "" {
		errs = append(errs, "itunes.libraries.ao.itl_path must be set when itunes sync/write-back is enabled (no zero-value write target)")
	}

	return errs
}
