// file: core/config/bootrecord.go
// version: 1.1.0
// guid: 56fd5d59-8302-4fc6-8fb4-699e2671afa9
// last-edited: 2026-11-19

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
)

// The boot record (05-config §1.3.5): the UI-made values of ReloadBoot keys,
// needed before storage opens, in <state_dir>/config.boot.json, written
// atomically through the StateFiles that Options.BootRecordRoot returns.
// Unknown paths (a module this binary lacks) are retained on every rewrite.
// No other component writes the file.

const (
	bootRecordName   = "config.boot.json"
	bootRecordTemp   = "config.boot.json.tmp"
	bootRecordBad    = "config.boot.json.bad"
	bootRecordFormat = "mediacore-boot/1"

	// The crash-point labels of the record's writes (06 §6.5.1; names coined
	// by F3-07). The quarantine is P0's one write; a rewrite comes from
	// Apply, after readiness, and is labelled all the same.
	labelBootQuarantine = "config.bootrecord.quarantine"
	labelBootWrite      = "config.bootrecord.write"
)

// bootRecordFile is the record's JSON form.
type bootRecordFile struct {
	Format string                   `json:"format"`
	Gen    uint64                   `json:"gen"`
	Values map[Path]json.RawMessage `json:"values"`
}

// bootRecordIO reads and writes the record under one root. renameHook, when
// set, runs between the temp file's fsync and the rename (tests: a crash
// there must leave the previous record intact).
type bootRecordIO struct {
	root       StateFiles
	renameHook func() error
}

// read returns the record's bytes, or nil when it does not exist. An I/O
// error fails closed (05-config §1.5).
func (r bootRecordIO) read() ([]byte, error) {
	b, err := r.root.ReadFile(bootRecordName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: boot record %s: %w", bootRecordName, err)
	}
	return b, nil
}

// parseBootRecord decodes the record strictly.
func parseBootRecord(b []byte) (bootRecordFile, error) {
	var rec bootRecordFile
	if err := strictDecode(b, &rec); err != nil {
		return rec, err
	}
	if rec.Format != bootRecordFormat {
		return rec, fmt.Errorf("format %q, want %q", rec.Format, bootRecordFormat)
	}
	if rec.Values == nil {
		rec.Values = map[Path]json.RawMessage{}
	}
	return rec, nil
}

// quarantine renames an unparsable record to config.boot.json.bad, replacing
// an older one.
func (r bootRecordIO) quarantine() error {
	if err := r.root.Rename(labelBootQuarantine, bootRecordName, bootRecordBad); err != nil {
		return fmt.Errorf("config: quarantine the boot record: %w", err)
	}
	return r.syncDir(labelBootQuarantine)
}

// write replaces the record atomically: a new temp file (0600), fsync,
// rename, directory fsync.
func (r bootRecordIO) write(rec bootRecordFile) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode the boot record: %w", err)
	}
	return r.writeBytes(append(b, '\n'))
}

// writeBytes replaces the record with b atomically. The temp file is always a
// new file: a leftover one is removed first and the open is O_EXCL, so a stale
// temp file's mode (0644) never reaches the record and a symlink planted at
// the temp name is never followed (os.Root follows one that stays inside the
// state directory, which would overwrite the keyring or another state file).
func (r bootRecordIO) writeBytes(b []byte) error {
	if err := r.removeTemp(); err != nil {
		return fmt.Errorf("config: remove a leftover boot record temp file: %w", err)
	}
	if err := r.root.WriteNew(labelBootWrite, bootRecordTemp, b, 0o600); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("config: boot record temp file: %w", err)
		}
		return fmt.Errorf("config: write the boot record: %w", errors.Join(err, r.removeTemp()))
	}
	if r.renameHook != nil {
		if err := r.renameHook(); err != nil {
			return fmt.Errorf("config: write the boot record: %w", err)
		}
	}
	if err := r.root.Rename(labelBootWrite, bootRecordTemp, bootRecordName); err != nil {
		return fmt.Errorf("config: install the boot record: %w", errors.Join(err, r.removeTemp()))
	}
	return r.syncDir(labelBootWrite)
}

// restore puts back the record's previous bytes (nil: it did not exist).
func (r bootRecordIO) restore(prev []byte) error {
	if prev == nil {
		err := r.root.Remove(labelBootWrite, bootRecordName)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("config: remove the boot record: %w", err)
		}
		return r.syncDir(labelBootWrite)
	}
	return r.writeBytes(prev)
}

// removeTemp deletes a leftover temp file.
func (r bootRecordIO) removeTemp() error {
	if err := r.root.Remove(labelBootWrite, bootRecordTemp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// syncDir fsyncs the record's directory so a rename survives a crash.
func (r bootRecordIO) syncDir(label string) error {
	if err := r.root.SyncDir(label, "."); err != nil {
		return fmt.Errorf("config: sync the state directory: %w", err)
	}
	return nil
}
