// file: internal/database/fingerprint_window.go
// version: 1.0.0
// guid: c23e6967-5192-4584-a48c-2b5d89c2e887
// last-edited: 2026-09-19

package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Windowed AcoustID fingerprints: the stored format.
//
// Design: .claude/notes/windowed-fingerprint-design-2026-09-12.md, section (c).
// Every print on BookFile.AcoustIDFingerprint is the first 120 s of the file,
// which for most audiobooks is the shared Audible intro. A window is a print cut
// from somewhere else in the file (10/50/90% of its duration), stored one row
// per window in a sidecar keyspace so a later whole-file print is just another
// row of kind "whole", with no schema change.
//
// The kind and ref types live here, not in internal/fingerprint, so the store
// has no dependency on the package that computes prints.

// FingerprintWindowKind is what part of the file a window covers.
type FingerprintWindowKind string

const (
	// WindowKindHead is a print starting at offset 0. The legacy 120 s print on
	// BookFile.AcoustIDFingerprint is exposed as a virtual row of this kind.
	WindowKindHead FingerprintWindowKind = "head"
	// WindowKindWindow is a print cut at a fraction of the duration (SlotBP).
	WindowKindWindow FingerprintWindowKind = "window"
	// WindowKindWhole is a print over the entire file.
	WindowKindWhole FingerprintWindowKind = "whole"
)

// Valid reports whether k is one of the three stored kinds.
func (k FingerprintWindowKind) Valid() bool {
	switch k {
	case WindowKindHead, WindowKindWindow, WindowKindWhole:
		return true
	}
	return false
}

// FingerprintWindowSchemaVersion is FingerprintWindow.SchemaVersion for rows
// written by this code.
const FingerprintWindowSchemaVersion = 1

// LegacyHeadPipeline is FingerprintWindow.Pipeline on the virtual head row
// synthesized from BookFile.AcoustIDFingerprint. That print came from fpcalc
// reading the container directly, not from the ffmpeg PCM pipe, so it must never
// pair with a window of a different pipeline. Consumers pair on Pipeline.
const LegacyHeadPipeline = "fpcalc-direct-head/legacy"

// MaxWindowSlotBP is the largest SlotBP: 100% of the duration, in basis points.
const MaxWindowSlotBP = 10000

// Key scheme. Everything is outside "book_file:" and "book:" so the memdb warmup
// (memdb_warmup.go, which scans byte ranges anchored on those prefixes) never
// reads a window — the same reasoning as bookSigKeyPrefix. Pinned by
// TestFpwin_WarmupNeverReadsTheWindowPrefix.
//
//	fpwin:f:<file_id>:<kind>:<slot_bp>                        tracked BookFile rows
//	fpwin:p:<sha256(library-relative path)>:<kind>:<slot_bp>  untracked on-disk candidates
//	fpwin_fail:<ref>                                          window failure tombstone
//
// NOTE for a rollback wipe: "fpwin_fail:" sorts OUTSIDE ["fpwin:", "fpwin;"),
// so a DeleteRange over the window prefix leaves the tombstones behind. Wipe
// both ranges.
const (
	fpwinKeyPrefix     = "fpwin:"
	fpwinFailKeyPrefix = "fpwin_fail:"
)

// FingerprintWindowRef names what a set of windows describes: "f:<file_id>" for
// a tracked book_file row, or "p:<sha256 hex>" for an untracked file identified
// by its library-relative path.
type FingerprintWindowRef string

// FileWindowRef is the ref for the book_file row with this ID.
func FileWindowRef(fileID string) FingerprintWindowRef {
	return FingerprintWindowRef("f:" + fileID)
}

// PathWindowRef is the ref for an untracked file, keyed by the SHA-256 of its
// library-relative path. The caller normalizes the path (Unicode form, slash
// direction) before calling; this hashes exactly the bytes it is given.
func PathWindowRef(libraryRelPath string) FingerprintWindowRef {
	sum := sha256.Sum256([]byte(libraryRelPath))
	return FingerprintWindowRef("p:" + hex.EncodeToString(sum[:]))
}

// validate rejects a ref that would build an ambiguous key. The ID part must not
// contain ':' — the key is split on it, and "f:a:b" would read back as file "a".
func (r FingerprintWindowRef) validate() error {
	s := string(r)
	if len(s) < 3 || (s[:2] != "f:" && s[:2] != "p:") {
		return fmt.Errorf("fingerprint window ref %q: want f:<file_id> or p:<hash>", s)
	}
	if strings.ContainsAny(s[2:], ":;") {
		return fmt.Errorf("fingerprint window ref %q: id must not contain ':' or ';'", s)
	}
	return nil
}

// FingerprintWindow is one stored print over one span of one file.
//
// Raw is the same encoding as BookFile.AcoustIDFingerprint: the raw uint32
// stream as little-endian bytes.
type FingerprintWindow struct {
	SchemaVersion   int                   `json:"v"`
	Ref             FingerprintWindowRef  `json:"ref"`  // "f:<file_id>" | "p:<hash>"
	Kind            FingerprintWindowKind `json:"kind"` // head|window|whole
	SlotBP          int                   `json:"slot_bp"`
	WindowSet       string                `json:"window_set"` // "ws1"
	OffsetSec       float64               `json:"offset_sec"`
	LengthSec       float64               `json:"length_sec"` // 0 = whole
	DecodedSec      float64               `json:"decoded_sec"`
	CoversWhole     bool                  `json:"covers_whole,omitempty"`
	DurationUsedSec float64               `json:"duration_used_sec"`
	DurationSource  string                `json:"duration_source"`
	Frames          int                   `json:"frames"`
	Raw             []byte                `json:"raw"` // LE uint32, same as AcoustIDFingerprint
	Algorithm       int                   `json:"algorithm"`
	Pipeline        string                `json:"pipeline"`
	FpcalcVersion   string                `json:"fpcalc_version"`
	FFmpegVersion   string                `json:"ffmpeg_version"`
	SourceSize      int64                 `json:"source_size"`
	SourceMtimeUnix int64                 `json:"source_mtime_unix"`
	ComputedAt      time.Time             `json:"computed_at"`
	Host            string                `json:"host"`
	LeaseID         string                `json:"lease_id,omitempty"`

	// Virtual marks the synthesized legacy head row. It is never stored:
	// PutFingerprintWindow refuses a virtual row, and the json tag keeps it off
	// the wire if one is ever marshalled by hand.
	Virtual bool `json:"-"`
}

// validate checks the invariants the key scheme depends on.
func (w *FingerprintWindow) validate() error {
	if w == nil {
		return fmt.Errorf("fingerprint window: nil")
	}
	if w.Virtual {
		return fmt.Errorf("fingerprint window %s: the virtual legacy head row is synthesized on read and is never stored", w.Ref)
	}
	if err := w.Ref.validate(); err != nil {
		return err
	}
	if !w.Kind.Valid() {
		return fmt.Errorf("fingerprint window %s: invalid kind %q", w.Ref, w.Kind)
	}
	if w.SlotBP < 0 || w.SlotBP > MaxWindowSlotBP {
		return fmt.Errorf("fingerprint window %s: slot_bp %d outside [0, %d]", w.Ref, w.SlotBP, MaxWindowSlotBP)
	}
	if w.Kind != WindowKindWindow && w.SlotBP != 0 {
		return fmt.Errorf("fingerprint window %s: kind %s must use slot_bp 0, got %d", w.Ref, w.Kind, w.SlotBP)
	}
	if len(w.Raw) == 0 || len(w.Raw)%4 != 0 {
		return fmt.Errorf("fingerprint window %s: raw print is %d bytes, want a non-empty multiple of 4", w.Ref, len(w.Raw))
	}
	return nil
}

// fpwinRefPrefix is the key prefix holding every window of one ref, including
// the trailing ':' so "f:ab" never matches "f:abc".
func fpwinRefPrefix(ref FingerprintWindowRef) []byte {
	return []byte(fpwinKeyPrefix + string(ref) + ":")
}

// fpwinKey is the one place a window's key is derived, from the row's own Ref,
// Kind and SlotBP, so the stored Ref and the key cannot disagree.
func fpwinKey(w *FingerprintWindow) []byte {
	return []byte(fpwinKeyPrefix + string(w.Ref) + ":" + string(w.Kind) + ":" + strconv.Itoa(w.SlotBP))
}

// fpwinFailKey is the failure tombstone key for a ref.
func fpwinFailKey(ref FingerprintWindowRef) []byte {
	return []byte(fpwinFailKeyPrefix + string(ref))
}

// legacyHeadWindow synthesizes the virtual kind=head row from a book_file row's
// legacy print. ok is false when the row carries no print.
//
// Fields the legacy write path never recorded are left zero rather than guessed:
// LengthSec (the analysis length, config-dependent and not persisted),
// DecodedSec, Algorithm, tool versions, source size/mtime and ComputedAt.
// Consumers identify this row by Virtual or by Pipeline == LegacyHeadPipeline.
func legacyHeadWindow(f *BookFile) (FingerprintWindow, bool) {
	if f == nil || len(f.AcoustIDFingerprint) == 0 {
		return FingerprintWindow{}, false
	}
	return FingerprintWindow{
		SchemaVersion:   FingerprintWindowSchemaVersion,
		Ref:             FileWindowRef(f.ID),
		Kind:            WindowKindHead,
		SlotBP:          0,
		OffsetSec:       0,
		DurationUsedSec: f.AcoustIDFingerprintDurationSec,
		DurationSource:  "acoustid_fingerprint_duration_sec",
		Frames:          len(f.AcoustIDFingerprint) / 4,
		Raw:             f.AcoustIDFingerprint,
		Pipeline:        LegacyHeadPipeline,
		Virtual:         true,
	}, true
}
