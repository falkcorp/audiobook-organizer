// file: core/backup/manifest.go
// version: 1.0.3
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
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/backuparchive"
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
	if m.InstallID == "" && m.Provenance.MAC != "" {
		return nil, fmt.Errorf("%w: no install ID", ErrManifest)
	}
	if err := m.validateText(); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(m.Entries))
	var total int64
	for _, e := range m.Entries {
		// The path is judged before it is quoted: a foreign manifest may carry a
		// path as long as the manifest itself.
		if len(e.Path) > backuparchive.MaxNameBytes {
			return nil, fmt.Errorf("%w: an entry path is over %d bytes", ErrManifest, backuparchive.MaxNameBytes)
		}
		if _, err := backuparchive.CleanName(e.Path, false); err != nil {
			return nil, fmt.Errorf("%w: entry %q is not a clean relative path", ErrManifest, e.Path)
		}
		if e.Size < 0 || len(e.SHA256) != 64 || seen[e.Path] || e.Path == ManifestName {
			return nil, fmt.Errorf("%w: entry %q", ErrManifest, e.Path)
		}
		if _, err := hex.DecodeString(e.SHA256); err != nil {
			return nil, fmt.Errorf("%w: entry %q digest", ErrManifest, e.Path)
		}
		if e.Size > math.MaxInt64-total {
			return nil, fmt.Errorf("%w: the entries' sizes overflow", ErrManifest)
		}
		total += e.Size
		seen[e.Path] = true
	}
	return &m, nil
}

// Bounds on the text of a manifest. A manifest that arrives through an import is
// a stranger's: every string in it is bounded and free of control characters
// before anything quotes it into a log, a warning or a catalogue row, and a map
// is bounded in size.
const (
	maxManifestText = 256
	maxManifestKeys = 4096
)

// MaxReasonBytes is the longest reason an archive's manifest can carry. It is the
// bound ParseManifest applies to every free string of a manifest, in bytes, not
// characters: a text of 100 CJK characters is 300 bytes.
const MaxReasonBytes = maxManifestText

// ValidReason is nil when reason can be written into a manifest and read back
// out of it: valid UTF-8, at most MaxReasonBytes bytes, no control character (a
// CR or LF in it would split a log line). It is the test ParseManifest applies
// to the same member, so a reason that passes here is one the archive's own
// catalogue accepts. Create refuses with it before any work, and the HTTP route
// answers its 400 with it, so neither keeps a copy of the rule. The error names
// no value: a reason is free text.
func ValidReason(reason string) error {
	if !validText(reason, MaxReasonBytes) {
		return fmt.Errorf("%w: it must be valid text of at most %d bytes with no control character", ErrInvalidReason, MaxReasonBytes)
	}
	return nil
}

// validText reports whether s is a valid UTF-8 string of at most limit bytes
// with no control character (a CR or LF in it would split a log line).
func validText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// validateText checks the manifest's ID and every free string and map key of it.
// The errors name the member, never its value.
func (m *Manifest) validateText() error {
	bad := func(what string) error {
		return fmt.Errorf("%w: %s is not valid text of at most %d bytes", ErrManifest, what, maxManifestText)
	}
	if !isULID(string(m.ArchiveID)) {
		return fmt.Errorf("%w: the archive ID is not a ULID", ErrManifest)
	}
	for _, f := range []struct{ name, v string }{
		{"app", m.App}, {"app_version", m.AppVersion}, {"core_version", m.CoreVersion},
		{"install_id", m.InstallID}, {"reason", m.Reason},
		{"provenance.alg", m.Provenance.Alg}, {"provenance.key_id", m.Provenance.KeyID}, {"provenance.mac", m.Provenance.MAC},
	} {
		if !validText(f.v, maxManifestText) {
			return bad(f.name)
		}
	}
	if m.ServedBoot != nil && !validText(m.ServedBoot.Nonce, maxManifestText) {
		return bad("served_boot.nonce")
	}
	if len(m.Levels) > maxManifestKeys || len(m.Keyspaces) > maxManifestKeys || len(m.Modules) > maxManifestKeys || len(m.Derived) > maxManifestKeys {
		return fmt.Errorf("%w: a member lists more than %d names", ErrManifest, maxManifestKeys)
	}
	for k := range m.Levels {
		if !validText(k, maxManifestText) {
			return bad("a levels key")
		}
	}
	for k := range m.Keyspaces {
		if !validText(k, maxManifestText) {
			return bad("a keyspaces key")
		}
	}
	for k, v := range m.Modules {
		if !validText(k, maxManifestText) || !validText(v, maxManifestText) {
			return bad("a modules member")
		}
	}
	for k, v := range m.Derived {
		if !validText(k, maxManifestText) || !validText(v.Generation, maxManifestText) {
			return bad("a derived member")
		}
	}
	return nil
}

// isULID reports whether s is an ID NewArchiveID could have issued: 26 Crockford
// base32 characters whose first is at most 7 (128 bits). A "legacy-" ID is not
// one: a legacy archive has no manifest, so no manifest carries one.
func isULID(s string) bool {
	if len(s) != 26 || s[0] > '7' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(crockford, s[i]) < 0 {
			return false
		}
	}
	return true
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
