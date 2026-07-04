// file: internal/itunes/itl_identity.go
// version: 1.1.0
// guid: 4f8a2b1c-9d3e-4c7a-b5f6-1e2d3c4b5a69
// last-edited: 2026-07-03
//
// Library identity fingerprinting for the ITLSafetyContract (SPEC 3 / K13–K14).
//
// The June 2026 contract (T003) certifies that a payload is a well-formed
// iTunes library, but nothing certifies that it is *our* library: a fresh
// library authored by iTunes itself (same path, same Library Persistent ID,
// disjoint track PIDs — the July 2026 "374-track cloud stub") passes all
// eight structural guards. This module supplies the external truth anchor:
// a persisted fingerprint of the last successfully-written library, checked
// before the next mutation is allowed to proceed.
//
// The fingerprint is stored in a JSON sidecar next to the library file
// (<library>.identity.json) and carries the Library Persistent ID, the track
// and playlist counts, and an evenly-spaced sample of track Persistent IDs.
// Continuity is asserted by requiring (a) an unchanged Library PID and (b) a
// minimum overlap between the sampled PIDs and the candidate payload's PID
// set. Adopting a genuinely new library is an explicit operator action
// (ContractConfig.AdoptLibrary), never an inference.

package itunes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// identitySidecarSuffix is appended to the library path to form the sidecar
// path: "iTunes Library.itl" → "iTunes Library.itl.identity.json".
const identitySidecarSuffix = ".identity.json"

// identitySampleMax bounds the number of track PIDs persisted in the sidecar.
// 1024 evenly-spaced PIDs detects wholesale replacement with high confidence
// while keeping the sidecar a few tens of KB even for 100k-track libraries.
const identitySampleMax = 1024

// identitySchemaVersion is bumped on any incompatible sidecar layout change;
// readers reject sidecars with a newer schema than they understand.
const identitySchemaVersion = 1

// libraryPIDFileOffset is the absolute file offset of the 8-byte Library
// Persistent ID inside the hdfm header (empirical: matches the Album Artwork
// cache directory name and is stable across golden/live/stub libraries).
// Like the count fields at 0x44..0x54 it lives inside headerRemainder at
// remainder offset (0x34 - (17 + len(version))).
const libraryPIDFileOffset = 0x34

// LibraryIdentity is the persisted fingerprint of a known-good library state.
type LibraryIdentity struct {
	SchemaVersion int       `json:"schema_version"`
	LibraryPID    string    `json:"library_pid"` // 16 lowercase hex chars
	TrackCount    int       `json:"track_count"`
	PlaylistCount int       `json:"playlist_count"`
	SampleStride  int       `json:"sample_stride"`
	PIDSample     []string  `json:"pid_sample"` // evenly-spaced track PIDs, payload order
	// FileSHA256 is the hex SHA-256 of the exact ON-DISK bytes our last
	// successful write produced (Tier 4 / K17): a mismatch on the next read
	// means an external writer (iTunes) touched the file in between, so the
	// content guards must vet a foreign state, not our own last output.
	FileSHA256 string    `json:"file_sha256,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// FileSHA256Hex returns the hex SHA-256 of raw library file bytes, the value
// stored in LibraryIdentity.FileSHA256.
func FileSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MatchesFileSHA reports whether data hashes to the recorded FileSHA256.
// Returns true when no checksum is recorded (nothing to contradict).
func (id *LibraryIdentity) MatchesFileSHA(data []byte) bool {
	if id == nil || id.FileSHA256 == "" {
		return true
	}
	return FileSHA256Hex(data) == id.FileSHA256
}

// ExtractLibraryPIDHex returns the Library Persistent ID from an hdfm header
// as 16 lowercase hex chars (MSB-first, matching the Album Artwork cache dir
// name), or "" when the header is nil or too short to carry the field.
func ExtractLibraryPIDHex(hdr *hdfmHeader) string {
	if hdr == nil {
		return ""
	}
	relOff := libraryPIDFileOffset - (headerFixedPrefix + len(hdr.version))
	if relOff < 0 || relOff+8 > len(hdr.headerRemainder) {
		return ""
	}
	var pid [8]byte
	copy(pid[:], hdr.headerRemainder[relOff:relOff+8])
	if pid == ([8]byte{}) {
		return ""
	}
	return fmt.Sprintf("%02x%02x%02x%02x%02x%02x%02x%02x",
		pid[0], pid[1], pid[2], pid[3], pid[4], pid[5], pid[6], pid[7])
}

// ComputeLibraryIdentity fingerprints a decoded (decrypted+inflated) LE
// payload and its header. Returns an error when the master track list cannot
// be located — an unfingerprintable library must not silently produce an
// empty identity that future writes would then "match".
func ComputeLibraryIdentity(payload []byte, hdr *hdfmHeader) (*LibraryIdentity, error) {
	_, pids := collectMithTidsPids(payload)
	if pids == nil {
		return nil, errors.New("ComputeLibraryIdentity: master track list not locatable")
	}
	playlists, _ := countPlaylistsAndCheckMiph(payload)

	stride := 1
	if len(pids) > identitySampleMax {
		stride = len(pids) / identitySampleMax
	}
	sample := make([]string, 0, identitySampleMax+1)
	for i := 0; i < len(pids); i += stride {
		sample = append(sample, pids[i])
	}

	return &LibraryIdentity{
		SchemaVersion: identitySchemaVersion,
		LibraryPID:    ExtractLibraryPIDHex(hdr),
		TrackCount:    len(pids),
		PlaylistCount: playlists,
		SampleStride:  stride,
		PIDSample:     sample,
		UpdatedAt:     time.Now().UTC(),
	}, nil
}

// IdentitySidecarPath returns the sidecar path for a library path.
func IdentitySidecarPath(libraryPath string) string {
	return libraryPath + identitySidecarSuffix
}

// LoadLibraryIdentity reads the identity sidecar for libraryPath. A missing
// sidecar returns (nil, nil) — absence is a legitimate first-run state, not
// an error. A present-but-unreadable or newer-schema sidecar is an error:
// fail closed rather than treat a corrupt anchor as "no anchor".
func LoadLibraryIdentity(libraryPath string) (*LibraryIdentity, error) {
	data, err := os.ReadFile(IdentitySidecarPath(libraryPath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("LoadLibraryIdentity: %w", err)
	}
	var id LibraryIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("LoadLibraryIdentity: unmarshal %s: %w", IdentitySidecarPath(libraryPath), err)
	}
	if id.SchemaVersion > identitySchemaVersion {
		return nil, fmt.Errorf("LoadLibraryIdentity: sidecar schema %d newer than supported %d", id.SchemaVersion, identitySchemaVersion)
	}
	return &id, nil
}

// SaveLibraryIdentity persists the identity sidecar atomically (temp file +
// rename in the same directory).
func SaveLibraryIdentity(libraryPath string, id *LibraryIdentity) error {
	if id == nil {
		return errors.New("SaveLibraryIdentity: nil identity")
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("SaveLibraryIdentity: marshal: %w", err)
	}
	target := IdentitySidecarPath(libraryPath)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("SaveLibraryIdentity: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("SaveLibraryIdentity: rename: %w", err)
	}
	return nil
}

// SampleOverlapPct returns the percentage (0–100) of the identity's sampled
// PIDs present in the given payload's master track list. Returns -1 when the
// sample is empty or the payload's master list is unlocatable (callers must
// treat -1 as "cannot assess", not as 0%).
func (id *LibraryIdentity) SampleOverlapPct(payload []byte) int {
	if id == nil || len(id.PIDSample) == 0 {
		return -1
	}
	_, pids := collectMithTidsPids(payload)
	if pids == nil {
		return -1
	}
	present := make(map[string]struct{}, len(pids))
	for _, p := range pids {
		present[p] = struct{}{}
	}
	hits := 0
	for _, s := range id.PIDSample {
		if _, ok := present[s]; ok {
			hits++
		}
	}
	return hits * 100 / len(id.PIDSample)
}
