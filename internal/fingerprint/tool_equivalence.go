// file: internal/fingerprint/tool_equivalence.go
// version: 1.0.0
// guid: 2777d0a1-f26f-48e4-88fd-4795787a94b5
// last-edited: 2026-09-19

package fingerprint

import (
	"slices"
	"strings"
	"sync/atomic"
)

// Tool-pair equivalence: the ONE place that decides whether windows cut by
// two (fpcalc, ffmpeg) builds are interchangeable. Every reader that compares
// tool versions goes through ToolsEquivalent: acoustid.window-backfill's
// "is this window current" check and WindowSetSimilarity's provenance check.
// Two readers with two rules is how a remote worker's windows could count as
// current (never recomputed) yet be refused by similarity forever.
//
// The equivalence class is the server's own pair plus the configured
// fingerprint_worker_tool_versions allowlist: an allowlisted pair has passed a
// worker's parity gate (byte-identical windows to the server's), so it is
// interchangeable with the server's pair and with every other allowlisted
// pair. Any pair is always equivalent to itself.
//
// The class is set by SetToolEquivalence, which acoustid.window-backfill calls
// when it resolves its tools. Until then only the configured pairs are in it
// (the server's pair joins once resolved), and a mismatch fails closed: two
// prints refused as incomparable, never compared wrongly.

type toolClass struct {
	pairs []ToolVersionInfo
}

var toolEquivalence atomic.Pointer[toolClass]

// ParseToolPairs parses "<fpcalc>/<ffmpeg>" entries, skipping malformed ones.
func ParseToolPairs(entries []string) []ToolVersionInfo {
	var out []ToolVersionInfo
	for _, e := range entries {
		fp, ff, ok := strings.Cut(strings.TrimSpace(e), "/")
		fp, ff = strings.TrimSpace(fp), strings.TrimSpace(ff)
		if ok && fp != "" && ff != "" {
			out = append(out, ToolVersionInfo{Fpcalc: fp, FFmpeg: ff})
		}
	}
	return out
}

// SetToolEquivalence replaces the equivalence class: server (when non-empty)
// plus the allowlisted pairs.
func SetToolEquivalence(server ToolVersionInfo, allowlisted []ToolVersionInfo) {
	c := &toolClass{}
	if server.Fpcalc != "" && server.FFmpeg != "" {
		c.pairs = append(c.pairs, server)
	}
	for _, p := range allowlisted {
		if p.Fpcalc != "" && p.FFmpeg != "" && !slices.Contains(c.pairs, p) {
			c.pairs = append(c.pairs, p)
		}
	}
	toolEquivalence.Store(c)
}

// EquivalentToolPairs returns the current class (a copy).
func EquivalentToolPairs() []ToolVersionInfo {
	if c := toolEquivalence.Load(); c != nil {
		return slices.Clone(c.pairs)
	}
	return nil
}

// ToolsEquivalent reports whether windows cut by pair a and pair b may be
// compared with, or stand in for, each other.
func ToolsEquivalent(a, b ToolVersionInfo) bool {
	if a == b {
		return true
	}
	c := toolEquivalence.Load()
	if c == nil {
		return false
	}
	return slices.Contains(c.pairs, a) && slices.Contains(c.pairs, b)
}
