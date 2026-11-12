// file: core/backup/manifest.go
// version: 1.0.1
// guid: 2b0f3a62-7c1e-4a6e-9a0b-4f7d6b3c1e55
// last-edited: 2026-11-12

package backup

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
)

// ManifestName is the archive's first entry; ManifestSidecar is the suffix of
// the same bytes beside the archive (§5B.3.3).
const (
	ManifestName    = "mediacore-manifest.json"
	ManifestSidecar = ".manifest.json"
	VerifySidecar   = ".verify.json"
	ChecksumSidecar = ".sha256"

	// MaxManifestBytes bounds a manifest read from an archive or a sidecar: the
	// manifest lists every entry, so it grows with the store, but never past
	// this.
	MaxManifestBytes = 16 << 20

	provAlg  = "hmac-sha256"
	provInfo = "core.backup/manifest/v1"
)

// MarshalManifest is the manifest's one byte form: encoding/json over the
// struct (field order fixed, map keys sorted), so a decoded manifest marshals
// to the bytes that were signed. It is what the sidecar holds and what the MAC
// covers once Provenance.MAC is empty.
func MarshalManifest(m *Manifest) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("backup: marshal manifest: %w", err)
	}
	return b, nil
}

// ParseManifest decodes b strictly: an unknown member (a newer additive field
// such as `encrypted`, which would change how the archive must be read) is
// refused rather than ignored, and so is anything after the one JSON value.
// Every failure is ErrManifest. It does not check provenance.
func ParseManifest(b []byte) (*Manifest, error) {
	if len(b) > MaxManifestBytes {
		return nil, fmt.Errorf("%w: %d bytes is over the %d byte cap", ErrManifest, len(b), MaxManifestBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data after the manifest", ErrManifest)
	}
	if m.Format != FormatMediacore {
		return nil, fmt.Errorf("%w: format %q", ErrManifest, m.Format)
	}
	if m.ArchiveID == "" || m.InstallID == "" && m.Provenance.MAC != "" {
		return nil, fmt.Errorf("%w: no archive or install ID", ErrManifest)
	}
	seen := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		if e.Path == "" || e.Size < 0 || len(e.SHA256) != 64 || seen[e.Path] || e.Path == ManifestName {
			return nil, fmt.Errorf("%w: entry %q", ErrManifest, e.Path)
		}
		if _, err := hex.DecodeString(e.SHA256); err != nil {
			return nil, fmt.Errorf("%w: entry %q digest", ErrManifest, e.Path)
		}
		seen[e.Path] = true
	}
	return &m, nil
}

// canonical is the MAC'd bytes: the manifest with Provenance.MAC empty.
func canonical(m *Manifest) ([]byte, error) {
	c := *m
	c.Provenance.MAC = ""
	return MarshalManifest(&c)
}

// SumManifest is the SHA-256 the restore intent records (manifest_sha256).
func SumManifest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Sign fills m.Provenance under the active derivation key. It is the last step
// of building a manifest: nothing may change m afterwards.
func Sign(m *Manifest, keys journal.Keys) error {
	kid := keys.ActiveDeriveID()
	m.Provenance = Provenance{Alg: provAlg, KeyID: kid}
	mac, err := macOf(m, keys)
	if err != nil {
		return err
	}
	m.Provenance.MAC = mac
	return nil
}

func macOf(m *Manifest, keys journal.Keys) (string, error) {
	key, err := keys.DeriveKeyFor(m.Provenance.KeyID, provInfo)
	if err != nil {
		return "", err
	}
	b, err := canonical(m)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, key)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Classify is B5: Own only when the MAC verifies under this install's
// derivation key Provenance.KeyID and the manifest's InstallID is the live
// store's (liveInstall "" means there is no live store: the host-loss case,
// where the keyring export alone anchors the archive). Everything else, a
// missing or wrong MAC, a retired or unknown key, another install, an unknown
// algorithm, is Foreign. It never returns an error: a failure to verify is the
// answer.
func Classify(m *Manifest, keys journal.Keys, liveInstall string) ProvenanceClass {
	if m == nil || keys == nil || m.Provenance.Alg != provAlg || m.Provenance.MAC == "" {
		return Foreign
	}
	got, err := hex.DecodeString(m.Provenance.MAC)
	if err != nil {
		return Foreign
	}
	want, err := macOf(m, keys)
	if err != nil {
		return Foreign
	}
	wantB, err := hex.DecodeString(want)
	if err != nil {
		return Foreign
	}
	if !hmac.Equal(got, wantB) {
		return Foreign
	}
	if liveInstall != "" && m.InstallID != liveInstall {
		return Foreign
	}
	return Own
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewArchiveID is a ULID: 48 bits of milliseconds and 80 random bits, 26
// Crockford base32 characters, so IDs sort by creation time. Core has no ULID
// dependency; this is the whole encoding.
func NewArchiveID(now time.Time) (ArchiveID, error) {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := range 6 {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	if _, err := rand.Read(b[6:]); err != nil {
		return "", fmt.Errorf("backup: archive ID: %w", err)
	}
	var out [26]byte
	// 128 bits as 26 groups of 5: two leading zero bits make 130, so the first
	// character is at most '7', as the ULID spec has it.
	var acc uint16
	bits, n := 2, 0
	for i := range 16 {
		acc = acc<<8 | uint16(b[i])
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[n] = crockford[(acc>>uint(bits))&31]
			n++
		}
	}
	return ArchiveID(out[:]), nil
}

// ValidArchiveID reports whether s has the shape of an ID this package issues
// (a ULID, or a "legacy-" name): no separator, no dot-dot, bounded.
func ValidArchiveID(s string) bool {
	if len(s) == 26 {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
				return false
			}
		}
		return true
	}
	if len(s) > len(LegacyIDPrefix) && len(s) <= 200 && s[:len(LegacyIDPrefix)] == LegacyIDPrefix {
		for i := len(LegacyIDPrefix); i < len(s); i++ {
			c := s[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
		return true
	}
	return false
}
