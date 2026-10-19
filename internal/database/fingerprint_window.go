// file: internal/database/fingerprint_window.go
// version: 1.4.1
// guid: c23e6967-5192-4584-a48c-2b5d89c2e887
// last-edited: 2026-10-19

package database

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
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
// The kind and ref types live here, not in internal/fingerprint, so the stored
// format does not depend on the package that computes prints. The one
// exception is the window-band index below, whose keys are the bands
// fingerprint.WindowBands computes (a pure function of the stored Raw bytes);
// the store imports that package for them, as the head-print LSH index does.

// FingerprintWindowKind is what part of the file a window covers.
type FingerprintWindowKind string

const (
	// WindowKindHead is a print starting at offset 0. The legacy 120 s print on
	// BookFile.AcoustIDFingerprint is exposed as a virtual row of this kind.
	WindowKindHead FingerprintWindowKind = "head"
	// WindowKindWindow is a print cut at a fraction of the duration (SlotBP).
	// Slot 0 is legal and distinct from head: a short file's single window
	// covering the whole file is window:0 (CoversWhole), not head:0.
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
//	fpwinidx:<ver:1B><band:1B><hash:8B BE>:<file_id>          window-band index row (empty value)
//	fpwinidx_meta:<file_id>                                   1B ver + N x (1B band + 8B hash BE)
//
// The two fpwinidx families are the inverted index over window prints (02-PR8,
// pebble_store_fpwinidx.go): one row per distinct (band, hash) of a tracked
// file's banded windows, plus the file's member list, which is what lets a
// replaced or deleted window remove its rows without recomputing the old
// print. Only f: refs are indexed, only kind "window" rows are banded, and the
// rows are written in the SAME batch as the window write that changes them.
//
// NOTE for a rollback wipe: "fpwin_fail:", "fpwinidx:" and "fpwinidx_meta:"
// each sort OUTSIDE ["fpwin:", "fpwin;"), so a DeleteRange over the window
// prefix leaves the tombstones and the whole index behind. Wipe all four
// ranges (and clear system:flag:fpwinidx_v1_done with the index).
const (
	fpwinKeyPrefix        = "fpwin:"
	fpwinFailKeyPrefix    = "fpwin_fail:"
	fpwinIdxKeyPrefix     = "fpwinidx:"
	fpwinIdxMetaKeyPrefix = "fpwinidx_meta:"
)

// fpwinIdxFileIDOffset is where the file ID starts in an fpwinidx: key:
// prefix 9 + version 1 + band 1 + hash 8 + ':' 1.
const fpwinIdxFileIDOffset = len(fpwinIdxKeyPrefix) + 1 + 1 + 8 + 1

// fpwinIdxBandPrefix is the key prefix of every index row of one (band,
// hash), including the trailing ':' so a probe's range covers exactly them.
func fpwinIdxBandPrefix(b fingerprint.WindowBand) []byte {
	out := make([]byte, 0, fpwinIdxFileIDOffset)
	out = append(out, fpwinIdxKeyPrefix...)
	out = append(out, fingerprint.WindowBandVersion, b.Band)
	out = binary.BigEndian.AppendUint64(out, b.Hash)
	return append(out, ':')
}

// fpwinIdxKey is the index row of one (band, hash) for fileID.
func fpwinIdxKey(b fingerprint.WindowBand, fileID string) []byte {
	return append(fpwinIdxBandPrefix(b), fileID...)
}

// fpwinIdxMetaKey is fileID's member-list row.
func fpwinIdxMetaKey(fileID string) []byte {
	return []byte(fpwinIdxMetaKeyPrefix + fileID)
}

// encodeWindowIndexMeta packs the version byte and the file's bands.
func encodeWindowIndexMeta(bands []fingerprint.WindowBand) []byte {
	out := make([]byte, 1, 1+9*len(bands))
	out[0] = fingerprint.WindowBandVersion
	for _, b := range bands {
		out = append(out, b.Band)
		out = binary.BigEndian.AppendUint64(out, b.Hash)
	}
	return out
}

// decodeWindowIndexMeta unpacks a member list. ok is false for a row of
// another band-scheme version or a malformed length: the caller treats the
// file as not indexed under this version.
func decodeWindowIndexMeta(v []byte) (bands []fingerprint.WindowBand, ok bool) {
	if len(v) == 0 || v[0] != fingerprint.WindowBandVersion || (len(v)-1)%9 != 0 {
		return nil, false
	}
	body := v[1:]
	bands = make([]fingerprint.WindowBand, 0, len(body)/9)
	for off := 0; off < len(body); off += 9 {
		bands = append(bands, fingerprint.WindowBand{Band: body[off], Hash: binary.BigEndian.Uint64(body[off+1 : off+9])})
	}
	return bands, true
}

// ErrFingerprintWindowRowGone is wrapped by every window write refused because
// the f: ref's book_file row does not exist (or was deleted while the write
// waited). The row is resolved by file ID, so a row moved to another book is
// NOT gone.
var ErrFingerprintWindowRowGone = errors.New("fingerprint window: book_file row does not exist")

// FingerprintWindowRef names what a set of windows describes: "f:<file_id>" for
// a tracked book_file row, or "p:<sha256 hex>" for an untracked file identified
// by its library-relative path.
type FingerprintWindowRef string

// FileWindowRef is the ref for the book_file row with this ID.
func FileWindowRef(fileID string) FingerprintWindowRef {
	return FingerprintWindowRef("f:" + fileID)
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
// Pipeline and every tool/version field are deliberately EMPTY. The legacy print
// came from fpcalc reading the container directly, not from the ffmpeg PCM
// pipe, and similarity code pairs windows on Pipeline: stamping any pipeline ID
// here would let it compare incompatible prints and still return a number.
// Pinned by TestFpwin_WindowsForFileAddsTheLegacyHead.
//
// Fields the legacy write path never recorded are likewise left zero rather
// than guessed: LengthSec (the analysis length, config-dependent and not
// persisted), DecodedSec, Algorithm, source size/mtime and ComputedAt.
// Consumers identify this row by Virtual.
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
		Virtual:         true,
	}, true
}

// FingerprintWindowFailure is the fpwin_fail:<ref> tombstone: the window
// pipeline failed on this file in a way that retrying the same bytes with the
// same tools will not fix (a non-zero ffmpeg/fpcalc exit, unparseable output,
// too few frames, audio shorter than the planned window).
//
// It is durable only for what it names. A backfill treats it as current only
// while the file's size and mtime, the pipeline, the window set and both tool
// versions all still match; any change means the failure no longer describes
// the file and the file is planned again. Transient conditions (cancellation,
// a timeout, an unknown duration) are never recorded here.
type FingerprintWindowFailure struct {
	SchemaVersion   int                  `json:"v"`
	Ref             FingerprintWindowRef `json:"ref"`
	Reason          string               `json:"reason"`
	Detail          string               `json:"detail,omitempty"`
	SlotBP          int                  `json:"slot_bp"`
	WindowSet       string               `json:"window_set"`
	Pipeline        string               `json:"pipeline"`
	FpcalcVersion   string               `json:"fpcalc_version"`
	FFmpegVersion   string               `json:"ffmpeg_version"`
	SourceSize      int64                `json:"source_size"`
	SourceMtimeUnix int64                `json:"source_mtime_unix"`
	// DurationUsedSec/DurationSource are the duration the failing cut was
	// planned from (possibly a fresh ffprobe after a retry).
	DurationUsedSec float64 `json:"duration_used_sec"`
	DurationSource  string  `json:"duration_source,omitempty"`
	// PlannedDurationSec is the duration the book_file ROW gave when the
	// file was planned (0 = unknown). A tombstone is current only while the
	// row still gives the same value, so correcting a wrong duration re-plans.
	PlannedDurationSec float64   `json:"planned_duration_sec"`
	FailedAt           time.Time `json:"failed_at"`
	Host               string    `json:"host"`
}

// validate checks the invariants the tombstone key depends on.
func (f *FingerprintWindowFailure) validate() error {
	if f == nil {
		return fmt.Errorf("fingerprint window failure: nil")
	}
	if err := f.Ref.validate(); err != nil {
		return err
	}
	if f.Reason == "" {
		return fmt.Errorf("fingerprint window failure %s: empty reason", f.Ref)
	}
	return nil
}
