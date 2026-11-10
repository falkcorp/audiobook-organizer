// file: core/secrets/keyring.go
// version: 1.0.1
// guid: 019b2e61-2abe-476e-9ba0-b5fe3f908bb0
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// The keyring file format (05-config §2.3 "Formats", normative). It carries
// key IDs from v1, so data-key rotation, when it lands, changes no format.
const (
	keyringFormat = "mediacore-keyring/1"

	useData   = "data"   // encrypts records
	useDerive = "derive" // feeds DeriveKey and the fingerprint subkey

	statusActive  = "active"
	statusVerify  = "verify"  // a rotated-out derive key: verification only
	statusRetired = "retired" // never used again

	keyBytes = 32

	// The HKDF-SHA256 info strings of the store's own subkeys.
	dataSubkeyInfo        = "mediacore/secrets/aes-256-gcm/v1"
	fingerprintSubkeyInfo = "mediacore/secrets/fingerprint/v1"
	// reservedInfoPrefix is refused by DeriveKey, so no caller can derive
	// the store's own subkeys.
	reservedInfoPrefix = "mediacore/secrets/"

	// defaultKeyFile is core.secrets.key_file's default, relative to the
	// state directory (05-config §2.7).
	defaultKeyFile = "secrets.keyring"

	// maxKeyringBytes bounds what a keyring read accepts.
	maxKeyringBytes = 1 << 20

	// The modes of 05-config §2.3: the keyring file 0600, a directory the
	// store creates for it 0700.
	keyringMode    os.FileMode = 0o600
	keyringDirMode os.FileMode = 0o700
)

// keyMaterial is a key's 32 bytes. It encodes as base64 in the keyring file
// (encoding/json's []byte rule), and every formatting path prints
// [REDACTED], so a key that reaches a log line or an error by mistake shows
// nothing.
type keyMaterial []byte

// String prints [REDACTED].
func (keyMaterial) String() string { return redacted }

// GoString prints [REDACTED].
func (keyMaterial) GoString() string { return "secrets.keyMaterial(" + redacted + ")" }

// Format prints [REDACTED] for every verb.
func (keyMaterial) Format(f fmt.State, _ rune) {
	if _, err := f.Write([]byte(redacted)); err != nil {
		return
	}
}

// LogValue renders as [REDACTED] in slog.
func (keyMaterial) LogValue() slog.Value { return slog.StringValue(redacted) }

// keyringFile is the keyring file: the header (format, install_id) and the
// keys.
type keyringFile struct {
	Format string `json:"format"`
	// InstallID is the install's c/storage/install marker (05-storage
	// §5S.3.4 rule 4): Open writes it when absent, ResetBoot rewrites it,
	// set-install-id sets it back, and export-keyring carries it.
	InstallID string       `json:"install_id,omitempty"`
	Keys      []keyringKey `json:"keys"`
}

// keyringKey is one key of the file.
type keyringKey struct {
	ID       string      `json:"id"`
	Use      string      `json:"use"`
	Created  time.Time   `json:"created"`
	Status   string      `json:"status"`
	Material keyMaterial `json:"material"`
	// RetireAfter is set on a verify-status derive key only: past it,
	// RetireUnused may retire the key for the process.
	RetireAfter *time.Time `json:"retire_after,omitempty"`
}

// The keyring and its keys hold key material in unexported fields, which fmt
// prints raw (it cannot call keyMaterial's methods through them), so every
// formatting path of both prints [REDACTED].

// String prints [REDACTED].
func (*keyring) String() string { return "secrets.keyring(" + redacted + ")" }

// GoString prints [REDACTED].
func (r *keyring) GoString() string { return r.String() }

// Format prints [REDACTED] for every verb.
func (r *keyring) Format(f fmt.State, _ rune) { writeRedacted(f, r.String()) }

// LogValue renders as [REDACTED] in slog.
func (r *keyring) LogValue() slog.Value { return slog.StringValue(r.String()) }

// String prints the key's ID, use and status, never its material.
func (k *ringKey) String() string {
	return "secrets.key(" + k.ID + " " + k.Use + " " + k.Status + " " + redacted + ")"
}

// GoString is String.
func (k *ringKey) GoString() string { return k.String() }

// Format prints String for every verb.
func (k *ringKey) Format(f fmt.State, _ rune) { writeRedacted(f, k.String()) }

// LogValue renders the key without its material.
func (k *ringKey) LogValue() slog.Value { return k.keyringKey.LogValue() }

// writeRedacted writes s to a fmt.State. A short write has nowhere to be
// reported: fmt's Formatter contract returns nothing, and s holds no secret.
func writeRedacted(f fmt.State, s string) {
	if _, err := io.WriteString(f, s); err != nil {
		return
	}
}

// LogValue renders a key without its material: slog's JSON handler would
// otherwise encode the material as base64 through encoding/json.
func (k keyringKey) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", k.ID), slog.String("use", k.Use), slog.String("status", k.Status))
}

// validKeyID reports whether id is [a-z][a-z0-9]{0,31} ("k1", "d2").
func validKeyID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// validInstallID reports whether id can be a keyring header's install_id:
// 1 to 128 printable ASCII characters without spaces (the store writes 32
// hex characters; a restored marker may be any such string).
func validInstallID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	return true
}

// validate checks the file's invariants: the format, unique well-formed key
// IDs, a known use and status, 32-byte material, retire_after on verify keys
// only, and exactly one active key per use.
func (f *keyringFile) validate() error {
	if f.Format != keyringFormat {
		return fmt.Errorf("format %q, want %q", f.Format, keyringFormat)
	}
	if f.InstallID != "" && !validInstallID(f.InstallID) {
		return errors.New("install_id is not 1 to 128 printable characters without spaces")
	}
	seen := map[string]bool{}
	active := map[string]int{}
	for i, k := range f.Keys {
		if !validKeyID(k.ID) {
			return fmt.Errorf("key %d: id %q does not match [a-z][a-z0-9]{0,31}", i, k.ID)
		}
		if seen[k.ID] {
			return fmt.Errorf("key %s appears twice", k.ID)
		}
		seen[k.ID] = true
		switch k.Use {
		case useData, useDerive:
		default:
			return fmt.Errorf("key %s: use %q, want data or derive", k.ID, k.Use)
		}
		switch k.Status {
		case statusActive, statusVerify, statusRetired:
		default:
			return fmt.Errorf("key %s: status %q, want active, verify or retired", k.ID, k.Status)
		}
		if len(k.Material) != keyBytes {
			return fmt.Errorf("key %s: material is %d bytes, want %d", k.ID, len(k.Material), keyBytes)
		}
		if k.Created.IsZero() {
			return fmt.Errorf("key %s: no created time", k.ID)
		}
		switch {
		case k.Status == statusVerify && k.RetireAfter == nil:
			return fmt.Errorf("key %s: a verify key needs retire_after", k.ID)
		case k.Status != statusVerify && k.RetireAfter != nil:
			return fmt.Errorf("key %s: retire_after on a %s key", k.ID, k.Status)
		case k.Status == statusVerify && k.Use != useDerive:
			return fmt.Errorf("key %s: only a derive key can be in verify status", k.ID)
		}
		if k.Status == statusActive {
			active[k.Use]++
		}
	}
	for _, use := range []string{useData, useDerive} {
		if active[use] != 1 {
			return fmt.Errorf("%d active %s keys, want exactly one", active[use], use)
		}
	}
	return nil
}

// decodeKeyring decodes a keyring file strictly (no unknown field, no
// trailing data) and validates it. Its errors never quote key material.
func decodeKeyring(raw []byte) (*keyringFile, error) {
	var f keyringFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode: data after the keyring object")
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// cleanKeyFile is name, or the default when empty; it must be a local path
// (filepath.IsLocal) under the state directory.
func cleanKeyFile(name string) (string, error) {
	if name == "" {
		return defaultKeyFile, nil
	}
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("secrets: key file %q is not a relative path inside the state directory", name)
	}
	return filepath.Clean(name), nil
}

// exportedRecordName is the export record beside the keyring file:
// secrets.keyring.exported for the default key file (05-config §2.4).
func exportedRecordName(keyFile string) string { return keyFile + ".exported" }

// readKeyring reads and decodes the keyring file name under root.
func readKeyring(root *os.Root, name string) (*keyringFile, error) {
	fh, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	raw, rerr := io.ReadAll(io.LimitReader(fh, maxKeyringBytes+1))
	if err := errors.Join(rerr, fh.Close()); err != nil {
		return nil, err
	}
	if len(raw) > maxKeyringBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxKeyringBytes)
	}
	return decodeKeyring(raw)
}

// checkMode returns the keyring file's mode, setting it to 0600 first when
// group or others can reach it (05-config §2.4 step 1); ErrKeyfileInsecure
// when that fails. fixed reports a change, for the caller's log.
func checkMode(root *os.Root, name string) (fixed bool, err error) {
	fi, err := root.Stat(name)
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("secrets: keyring %s is not a regular file", name)
	}
	if fi.Mode().Perm()&0o077 == 0 {
		return false, nil
	}
	if err := root.Chmod(name, keyringMode); err != nil {
		return false, fmt.Errorf("%w: %s is mode %v and could not be set to 0600: %w", ErrKeyfileInsecure, name, fi.Mode().Perm(), err)
	}
	if fi, err = root.Stat(name); err != nil {
		return false, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("%w: %s is still mode %v after chmod 0600", ErrKeyfileInsecure, name, fi.Mode().Perm())
	}
	return true, nil
}

// writeKeyring validates f and writes it to name under root atomically.
func writeKeyring(root *os.Root, name string, f *keyringFile) error {
	if err := f.validate(); err != nil {
		return fmt.Errorf("secrets: refusing to write an invalid keyring: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("secrets: encode the keyring: %w", err)
	}
	return writeFileAtomic(root, name, append(data, '\n'))
}

// writeKeyringNew writes f to name, which must not exist: a first or
// regenerated keyring never replaces one that appeared meanwhile.
func writeKeyringNew(root *os.Root, name string, f *keyringFile) error {
	if err := f.validate(); err != nil {
		return fmt.Errorf("secrets: refusing to write an invalid keyring: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("secrets: encode the keyring: %w", err)
	}
	return writeFile(root, name, append(data, '\n'), true)
}

// writeFileAtomic writes data to name under root, replacing it.
func writeFileAtomic(root *os.Root, name string, data []byte) error {
	return writeFile(root, name, data, false)
}

// writeFile writes data to name under root: a new temporary file created
// 0600 beside it, written and fsynced, then renamed over name (replace) or
// hard-linked to name, which fails when name exists (no replace), then the
// directory fsynced. A directory the path needs is created 0700. Only the
// temporary file this call created is ever removed: when a step before the
// rename fails, and after the link.
func writeFile(root *os.Root, name string, data []byte, noReplace bool) (err error) {
	dir := filepath.Dir(name)
	if dir != "." {
		if err := root.MkdirAll(dir, keyringDirMode); err != nil {
			return fmt.Errorf("secrets: create %s: %w", dir, err)
		}
	}
	suffix, err := randomHex(6)
	if err != nil {
		return err
	}
	tmp := name + ".tmp-" + suffix
	fh, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyringMode)
	if err != nil {
		return fmt.Errorf("secrets: create %s: %w", tmp, err)
	}
	renamed := false
	defer func() {
		if !renamed {
			if rerr := root.Remove(tmp); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				err = errors.Join(err, rerr)
			}
		}
	}()
	if _, err := fh.Write(data); err != nil {
		return errors.Join(fmt.Errorf("secrets: write %s: %w", tmp, err), fh.Close())
	}
	if err := fh.Sync(); err != nil {
		return errors.Join(fmt.Errorf("secrets: fsync %s: %w", tmp, err), fh.Close())
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("secrets: close %s: %w", tmp, err)
	}
	if noReplace {
		lerr := root.Link(tmp, name)
		switch {
		case lerr == nil:
			// The defer removes the temporary name; name keeps the inode.
			return syncDir(root, dir)
		case errors.Is(lerr, fs.ErrExist):
			return fmt.Errorf("secrets: create %s: it appeared meanwhile and is never replaced: %w", name, lerr)
		}
		// A filesystem without hard links: check, then rename. The window
		// is the caller's: Open runs under the store lock.
		if _, serr := root.Lstat(name); !errors.Is(serr, fs.ErrNotExist) {
			return fmt.Errorf("secrets: create %s: it appeared meanwhile and is never replaced (link: %v)", name, lerr)
		}
	}
	if err := root.Rename(tmp, name); err != nil {
		return fmt.Errorf("secrets: rename %s to %s: %w", tmp, name, err)
	}
	renamed = true
	return syncDir(root, dir)
}

// syncDir fsyncs dir under root, so a rename in it is durable.
func syncDir(root *os.Root, dir string) error {
	d, err := root.Open(dir)
	if err != nil {
		return fmt.Errorf("secrets: open %s: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		return errors.Join(fmt.Errorf("secrets: fsync %s: %w", dir, err), d.Close())
	}
	return d.Close()
}

// randomHex is n random bytes in hex.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("secrets: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// newKey is a fresh key with 32 random bytes.
func newKey(id, use string, now time.Time) (keyringKey, error) {
	m := make(keyMaterial, keyBytes)
	if _, err := rand.Read(m); err != nil {
		return keyringKey{}, fmt.Errorf("secrets: random: %w", err)
	}
	return keyringKey{ID: id, Use: use, Created: now, Status: statusActive, Material: m}, nil
}

// newKeyringFile is a first keyring: data key k<n> and derive key d<n>,
// both active, with installID in the header.
func newKeyringFile(n int, installID string, now time.Time) (*keyringFile, error) {
	k, err := newKey("k"+strconv.Itoa(n), useData, now)
	if err != nil {
		return nil, err
	}
	d, err := newKey("d"+strconv.Itoa(n), useDerive, now)
	if err != nil {
		return nil, err
	}
	return &keyringFile{Format: keyringFormat, InstallID: installID, Keys: []keyringKey{k, d}}, nil
}

// maxIDNumber is the highest key number in f (0 for none).
func (f *keyringFile) maxIDNumber() int {
	n := 0
	for _, k := range f.Keys {
		n = max(n, idNumber(k.ID))
	}
	return n
}

// idNumber is the decimal suffix of a key ID ("k12" -> 12), 0 without one.
func idNumber(id string) int {
	i := 0
	for i < len(id) && (id[i] < '0' || id[i] > '9') {
		i++
	}
	n, err := strconv.Atoi(id[i:])
	if err != nil {
		return 0
	}
	return n
}

// subkey derives a 32-byte subkey of material for info (HKDF-SHA256, no
// salt).
func subkey(material []byte, info string) ([]byte, error) {
	k, err := hkdf.Key(sha256.New, material, nil, info, keyBytes)
	if err != nil {
		return nil, fmt.Errorf("secrets: derive a subkey: %w", err)
	}
	return k, nil
}

// ringKey is one loaded key: its file entry and, for a usable data key, the
// AEAD of its data subkey.
type ringKey struct {
	keyringKey
	aead cipher.AEAD
}

// keyring is the loaded keyring: the file as read or written, the keys by
// ID, the active key of each use and the fingerprint subkey of the active
// derive key. It changes only at Open.
type keyring struct {
	file   keyringFile
	byID   map[string]*ringKey
	data   *ringKey
	derive *ringKey
	fp     []byte
}

// buildRing loads f (already validated) into memory.
func buildRing(f *keyringFile) (*keyring, error) {
	r := &keyring{file: *f, byID: map[string]*ringKey{}}
	for _, k := range f.Keys {
		rk := &ringKey{keyringKey: k}
		if k.Use == useData && k.Status != statusRetired {
			sk, err := subkey(k.Material, dataSubkeyInfo)
			if err != nil {
				return nil, err
			}
			block, err := aes.NewCipher(sk)
			if err != nil {
				return nil, fmt.Errorf("secrets: data key %s: %w", k.ID, err)
			}
			if rk.aead, err = cipher.NewGCM(block); err != nil {
				return nil, fmt.Errorf("secrets: data key %s: %w", k.ID, err)
			}
		}
		r.byID[k.ID] = rk
		if k.Status == statusActive {
			switch k.Use {
			case useData:
				r.data = rk
			case useDerive:
				r.derive = rk
			}
		}
	}
	fp, err := subkey(r.derive.Material, fingerprintSubkeyInfo)
	if err != nil {
		return nil, err
	}
	r.fp = fp
	return r, nil
}

// wipe overwrites the loaded key material (Close).
func (r *keyring) wipe() {
	for _, k := range r.byID {
		clear(k.Material)
	}
	clear(r.fp)
}

// ids is every key ID of the file, in file order.
func (r *keyring) ids() []string {
	out := make([]string, 0, len(r.file.Keys))
	for _, k := range r.file.Keys {
		out = append(out, k.ID)
	}
	return out
}
