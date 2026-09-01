// file: internal/audioext/audioext.go
// version: 1.1.0
// guid: 1a53bbfc-4b40-45a6-9c37-135163864a11
// last-edited: 2026-09-01

// Package audioext owns the ONE list of file extensions that decide whether a
// file on disk is part of this library.
//
// # Why this package exists
//
// `supported_extensions` is a user-configurable setting. Before this package
// existed, fifteen downstream code paths ignored it and hardcoded their own
// narrower list instead: the filesystem watcher knew 8 extensions, the
// iTunes plist filter knew 7, the relink/repair maintenance jobs knew 6 each.
// A user who adds an extension gets those files scanned and imported — the
// ingest path does read the config — and then never watched, never relinked,
// never repaired, never provenance-captured. Nothing errors; the work is
// simply not done, and the count of files that "need" it reads as zero.
//
// So the canonical list lives here, in a leaf package with no internal
// dependencies, and every predicate that asks "is this file part of the
// library?" resolves against it.
//
// # What this package is NOT for
//
// Three different questions were tangled together in those hardcoded lists.
// Only the first belongs here:
//
//  1. "Should the library track this file?" — this package, config-driven.
//  2. "Can tool X decode/transcode/tag this?" — a capability list bound to a
//     specific tool. It stays narrow and stays where the tool is:
//     internal/fingerprint (fpcalc), internal/plugins/acoustid (fpcalc, and it
//     knows .alac/.ape/.wv which are not library extensions),
//     internal/remux and internal/transcode (ffmpeg containers),
//     internal/tagger (TagLib), intro_transcribe (whisper). Widening those
//     from config would be a regression, not a de-duplication: .aax and .aaxc
//     are in the canonical list below and are DRM-protected — internal/audioutil
//     .DetectDRM documents that this application cannot decode them at all.
//     A capability list that excludes them is CORRECT.
//  3. "What MIME type / DRM scheme does this extension imply?" — a mapping
//     table, not a predicate. internal/server/handlers/abs/mapper.go and
//     internal/audioutil/drm.go. Widening a DRM test would make it claim every
//     .mp3 is DRM-shaped.
//
// # .mp4
//
// .mp4 is deliberately NOT in Default. It is a valid MPEG-4 audio container,
// but it is overwhelmingly a VIDEO container, and Default feeds the ingest
// scanner: adding it means a trailer or a bonus-feature video sitting in a
// library folder gets imported as an audiobook. The cost of that is higher
// than the cost of the two callers that recognise .mp4 today keeping their own
// list. Those two — internal/linkintegrity/classify.go (a directory-shape
// classifier working on files already inside a known book folder) and
// internal/plugins/maintenance/junk_title_derive.go (strips a known extension
// off a filename) — are therefore NOT resolved against this package. Routing
// them through it would silently REMOVE .mp4 recognition, which is a
// regression wearing a refactor's clothes.
package audioext

import (
	"path/filepath"
	"sort"
	"strings"
)

// defaultExtensions is the compiled-in canonical list. It is the value
// internal/config seeds `supported_extensions` with, and the value every
// config-aware lookup falls back to when the configured list is empty.
//
// It is unexported and copied by Default() so that no caller can mutate the
// one list the whole application agrees on.
var defaultExtensions = []string{
	".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma",
	".opus", ".oga", ".wav", ".aiff", ".aif", ".mka", ".aax", ".aaxc",
}

// Default returns a fresh copy of the compiled-in canonical extension list.
func Default() []string {
	out := make([]string, len(defaultExtensions))
	copy(out, defaultExtensions)
	return out
}

// Set is a lookup set of normalized (lowercase, dot-prefixed) extensions.
//
// The underlying type is map[string]bool rather than map[string]struct{} on
// purpose: the call sites this package replaced all indexed a
// map[string]bool, and an unnamed underlying type keeps a Set assignable to a
// map[string]bool parameter, so helpers that take one do not need to change.
type Set map[string]bool

// Normalize lowercases an extension and gives it a leading dot, so that a
// config written as `MP3` or `mp3` behaves the same as `.mp3`. An empty
// string stays empty — "" is not the extension ".".
func Normalize(ext string) string {
	e := strings.ToLower(strings.TrimSpace(ext))
	if e == "" {
		return ""
	}
	if !strings.HasPrefix(e, ".") {
		e = "." + e
	}
	return e
}

// NewSet builds a Set from exts. Entries are normalized; blanks are dropped.
// A nil or empty input yields an EMPTY set, not the default one — callers who
// want the fail-open behaviour must call Resolve.
func NewSet(exts []string) Set {
	s := make(Set, len(exts))
	for _, e := range exts {
		if n := Normalize(e); n != "" {
			s[n] = true
		}
	}
	return s
}

// DefaultSet returns a Set of the compiled-in canonical list.
func DefaultSet() Set { return NewSet(defaultExtensions) }

// Resolve returns the configured extension set, FALLING BACK to the
// compiled-in default when configured is nil or empty.
//
// 🔴 The fallback is the whole point. config.AppConfig is a package-level zero
// value, so AppConfig.SupportedExtensions is nil in any binary that has not
// run InitConfig — every test binary that does not, and any code path that
// runs before it. A user can also write `supported_extensions: []` into
// config.yaml. A predicate that resolved against an empty set would answer
// "not audio" for EVERY file, and the watcher, the relink jobs and the
// provenance capture would all do zero work while logging success. That is the
// exact shape of the chapter_consolidation_threshold_min = 0 incident, where
// one zero-valued setting silently disabled a whole subsystem. Fail open to
// the compiled default instead: doing the default amount of work is always
// recoverable; silently doing none is not.
func Resolve(configured []string) Set {
	s := NewSet(configured)
	if len(s) == 0 {
		return DefaultSet()
	}
	return s
}

// Has reports whether ext (with or without a leading dot, any case) is in s.
func (s Set) Has(ext string) bool { return s[Normalize(ext)] }

// MatchPath reports whether path carries an extension in s. A path with no
// extension — a directory, typically — is not a match.
func (s Set) MatchPath(path string) bool {
	if path == "" {
		return false
	}
	return s[strings.ToLower(filepath.Ext(path))]
}

// Sorted returns the set's extensions in lexical order, for logging and for
// op parameters that surface the effective list to a user.
func (s Set) Sorted() []string {
	out := make([]string, 0, len(s))
	for e := range s {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}
