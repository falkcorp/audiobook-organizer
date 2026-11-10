// file: core/secrets/offline.go
// version: 1.0.1
// guid: ea65355e-8977-4406-a748-63dd510dc8d8
// last-edited: 2026-11-10

package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// The keyring file's offline operations behind the three core commands
// (05-config §2.4): export-keyring, rotate-derive and set-install-id. None
// opens the store: export-keyring takes no lock at all, and the kernel runs
// the other two inside kernel.OpenOffline, which refuses while the server
// holds the store lock.

// exportFormat is the export record's format.
const exportFormat = "mediacore-keyring-export/1"

// exportRecord is <key file>.exported: the key IDs the last export carried
// and when. It holds no key material.
type exportRecord struct {
	Format string    `json:"format"`
	KeyIDs []string  `json:"key_ids"`
	At     time.Time `json:"at"`
}

// readExportRecord reads the export record name under root (fs.ErrNotExist
// when absent).
func readExportRecord(root *os.Root, name string) (*exportRecord, error) {
	fh, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	raw, rerr := io.ReadAll(io.LimitReader(fh, maxKeyringBytes))
	if err := errors.Join(rerr, fh.Close()); err != nil {
		return nil, fmt.Errorf("secrets: read %s: %w", name, err)
	}
	var r exportRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("secrets: %s does not decode: %w", name, err)
	}
	if r.Format != exportFormat {
		return nil, fmt.Errorf("secrets: %s has format %q, want %q", name, r.Format, exportFormat)
	}
	return &r, nil
}

// ExportResult is what ExportKeyring did.
type ExportResult struct {
	Out       string   // the absolute path written
	Record    string   // the export record, relative to the state directory
	KeyIDs    []string // the exported key IDs, sorted
	InstallID string   // the keyring header's install_id ("" when it has none)
}

// ExportKeyring copies the keyring file keyFile ("" = the default) of
// stateDir to out, created 0600 (temporary file, fsync, rename), then writes
// the export record <key file>.exported. It opens no store and takes no
// lock, so it works while the server runs (SEC10-9). It refuses an out that
// exists looser than 0600 or is not a regular file, and one that is the
// keyring or its record. The copy is the keyring file byte for byte, header
// install_id included, so storage restore-offline --keyring can put it back.
func ExportKeyring(stateDir *os.Root, keyFile, out string, now time.Time) (ExportResult, error) {
	if stateDir == nil {
		return ExportResult{}, errors.New("secrets: export-keyring needs the state directory")
	}
	if out == "" {
		return ExportResult{}, errors.New("secrets: export-keyring needs --out")
	}
	keyFile, err := cleanKeyFile(keyFile)
	if err != nil {
		return ExportResult{}, err
	}
	recordName := exportedRecordName(keyFile)
	raw, f, err := readKeyringBytes(stateDir, keyFile)
	if err != nil {
		return ExportResult{}, err
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return ExportResult{}, fmt.Errorf("secrets: --out %s: %w", out, err)
	}
	dir, base := filepath.Split(abs)
	outRoot, err := os.OpenRoot(dir)
	if err != nil {
		return ExportResult{}, fmt.Errorf("secrets: open the directory of --out: %w", err)
	}
	err = checkExportTarget(stateDir, keyFile, recordName, outRoot, base, abs)
	if err == nil {
		err = writeFileAtomic(outRoot, base, raw)
	}
	// A failed close fails the export before its record is written, so the
	// health warning stays raised; a re-run may overwrite the 0600 copy.
	if cerr := outRoot.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("secrets: close the directory of --out: %w", cerr))
	}
	if err != nil {
		return ExportResult{}, err
	}
	ids := make([]string, 0, len(f.Keys))
	for _, k := range f.Keys {
		ids = append(ids, k.ID)
	}
	slices.Sort(ids)
	rec, err := encodeJSON(exportRecord{Format: exportFormat, KeyIDs: ids, At: now.UTC()})
	if err != nil {
		return ExportResult{}, err
	}
	if err := writeFileAtomic(stateDir, recordName, append(rec, '\n')); err != nil {
		return ExportResult{}, fmt.Errorf("secrets: the keyring was exported to %s but the export record was not written: %w", abs, err)
	}
	return ExportResult{Out: abs, Record: recordName, KeyIDs: ids, InstallID: f.InstallID}, nil
}

// readKeyringBytes reads keyFile under root and validates it, returning the
// bytes and the decoded file.
func readKeyringBytes(root *os.Root, keyFile string) ([]byte, *keyringFile, error) {
	fi, err := root.Lstat(keyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("secrets: there is no keyring %s in the state directory: %w", keyFile, err)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("secrets: stat the keyring %s: %w", keyFile, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("secrets: the keyring %s is not a regular file", keyFile)
	}
	fh, err := root.Open(keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("secrets: open the keyring %s: %w", keyFile, err)
	}
	raw, rerr := io.ReadAll(io.LimitReader(fh, maxKeyringBytes+1))
	if err := errors.Join(rerr, fh.Close()); err != nil {
		return nil, nil, fmt.Errorf("secrets: read the keyring %s: %w", keyFile, err)
	}
	if len(raw) > maxKeyringBytes {
		return nil, nil, fmt.Errorf("secrets: the keyring %s is larger than %d bytes", keyFile, maxKeyringBytes)
	}
	f, err := decodeKeyring(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("secrets: the keyring %s is not valid: %w", keyFile, err)
	}
	return raw, f, nil
}

// checkExportTarget refuses an export destination that exists looser than
// 0600 or is not a regular file (a symbolic link included), and one that is
// the keyring or its export record.
func checkExportTarget(stateDir *os.Root, keyFile, recordName string, outRoot *os.Root, base, abs string) error {
	if base == "" {
		return fmt.Errorf("secrets: --out %s names a directory, not a file", abs)
	}
	if stateAbs, err := filepath.Abs(stateDir.Name()); err == nil {
		for _, own := range []string{keyFile, recordName} {
			if filepath.Join(stateAbs, own) == abs {
				return fmt.Errorf("secrets: --out %s is the state directory's %s", abs, own)
			}
		}
	}
	fi, err := outRoot.Lstat(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secrets: stat --out %s: %w", abs, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("secrets: --out %s exists and is not a regular file", abs)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("secrets: --out %s exists with mode %v; export-keyring writes only a file group and others cannot reach (0600)", abs, fi.Mode().Perm())
	}
	for _, own := range []string{keyFile, recordName} {
		ofi, err := stateDir.Stat(own)
		if err == nil && os.SameFile(fi, ofi) {
			return fmt.Errorf("secrets: --out %s is the state directory's %s", abs, own)
		}
	}
	return nil
}

// RotateResult is what RotateDerive did.
type RotateResult struct {
	Old, New    string    // the previous and the new active derive key IDs
	RetireAfter time.Time // the previous key's retire_after
}

// RotateDerive adds d<n+1> as the active derive key of the keyring keyFile
// ("" = the default) and sets the previous one to verify with retire_after
// = now + window (05-config §2.3 "Derive-key rotation", SEC2-24). It
// refuses with no keyring and never creates one. The kernel runs it inside
// kernel.OpenOffline (refused while the server holds the store lock), with
// window the longer of the backup retention and the longest refresh-token
// TTL.
func RotateDerive(stateDir *os.Root, keyFile string, window time.Duration, now time.Time) (RotateResult, error) {
	if window < 0 {
		return RotateResult{}, fmt.Errorf("secrets: rotate-derive window %v is negative", window)
	}
	keyFile, f, err := loadOffline(stateDir, keyFile)
	if err != nil {
		return RotateResult{}, err
	}
	n := 0
	ids := map[string]bool{}
	for _, k := range f.Keys {
		ids[k.ID] = true
		if k.Use == useDerive {
			n = max(n, idNumber(k.ID))
		}
	}
	newID := ""
	for n++; ; n++ {
		if newID = "d" + strconv.Itoa(n); !ids[newID] {
			break
		}
	}
	nk, err := newKey(newID, useDerive, now.UTC())
	if err != nil {
		return RotateResult{}, err
	}
	ra := now.Add(window).UTC()
	old := ""
	for i := range f.Keys {
		if k := &f.Keys[i]; k.Use == useDerive && k.Status == statusActive {
			old = k.ID
			k.Status, k.RetireAfter = statusVerify, &ra
		}
	}
	f.Keys = append(f.Keys, nk)
	if err := writeKeyring(stateDir, keyFile, f); err != nil {
		return RotateResult{}, err
	}
	return RotateResult{Old: old, New: newID, RetireAfter: ra}, nil
}

// SetInstallID rewrites only the keyring header's install_id to id (temp
// file, fsync, rename, 0600) and returns the previous value; the keys are
// untouched, so the export record stays valid (05-config §2.4, DATA8-12).
// It refuses with no keyring. The kernel checks id against the store's
// c/storage/install marker and holds the offline lock.
func SetInstallID(stateDir *os.Root, keyFile, id string) (old string, err error) {
	if !validInstallID(id) {
		return "", errors.New("secrets: --install-id must be 1 to 128 printable characters without spaces")
	}
	keyFile, f, err := loadOffline(stateDir, keyFile)
	if err != nil {
		return "", err
	}
	old = f.InstallID
	if old == id {
		return old, nil
	}
	f.InstallID = id
	if err := writeKeyring(stateDir, keyFile, f); err != nil {
		return "", err
	}
	return old, nil
}

// loadOffline resolves keyFile and reads the keyring for an offline
// rewrite: its mode fixed to 0600 or ErrKeyfileInsecure, a missing keyring
// refused.
func loadOffline(stateDir *os.Root, keyFile string) (string, *keyringFile, error) {
	if stateDir == nil {
		return "", nil, errors.New("secrets: the offline command needs the state directory")
	}
	keyFile, err := cleanKeyFile(keyFile)
	if err != nil {
		return "", nil, err
	}
	if _, err := checkMode(stateDir, keyFile); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("secrets: there is no keyring %s in the state directory: %w", keyFile, err)
		}
		return "", nil, err
	}
	f, err := readKeyring(stateDir, keyFile)
	if err != nil {
		return "", nil, fmt.Errorf("secrets: the keyring %s cannot be read: %w", keyFile, err)
	}
	return keyFile, f, nil
}
