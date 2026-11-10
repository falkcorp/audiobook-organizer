// file: core/config/bootrecord.go
// version: 1.0.0
// guid: 56fd5d59-8302-4fc6-8fb4-699e2671afa9
// last-edited: 2026-11-10

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// The boot record (05-config §1.3.5): the UI-made values of ReloadBoot keys,
// needed before storage opens, in <state_dir>/config.boot.json, written
// atomically through the os.Root that Options.BootRecordRoot returns. Unknown
// paths (a module this binary lacks) are retained on every rewrite. No other
// component writes the file.

const (
	bootRecordName   = "config.boot.json"
	bootRecordTemp   = "config.boot.json.tmp"
	bootRecordBad    = "config.boot.json.bad"
	bootRecordFormat = "mediacore-boot/1"
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
	root       *os.Root
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
	if err := r.root.Rename(bootRecordName, bootRecordBad); err != nil {
		return fmt.Errorf("config: quarantine the boot record: %w", err)
	}
	return r.syncDir()
}

// write replaces the record atomically: temp file (0600), fsync, rename,
// directory fsync.
func (r bootRecordIO) write(rec bootRecordFile) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode the boot record: %w", err)
	}
	return r.writeBytes(append(b, '\n'))
}

// writeBytes replaces the record with b atomically.
func (r bootRecordIO) writeBytes(b []byte) error {
	f, err := r.root.OpenFile(bootRecordTemp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("config: boot record temp file: %w", err)
	}
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("config: write the boot record: %w", errors.Join(werr, r.removeTemp()))
	}
	if r.renameHook != nil {
		if err := r.renameHook(); err != nil {
			return fmt.Errorf("config: write the boot record: %w", err)
		}
	}
	if err := r.root.Rename(bootRecordTemp, bootRecordName); err != nil {
		return fmt.Errorf("config: install the boot record: %w", errors.Join(err, r.removeTemp()))
	}
	return r.syncDir()
}

// restore puts back the record's previous bytes (nil: it did not exist).
func (r bootRecordIO) restore(prev []byte) error {
	if prev == nil {
		err := r.root.Remove(bootRecordName)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("config: remove the boot record: %w", err)
		}
		return r.syncDir()
	}
	return r.writeBytes(prev)
}

// removeTemp deletes a leftover temp file.
func (r bootRecordIO) removeTemp() error {
	if err := r.root.Remove(bootRecordTemp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// syncDir fsyncs the record's directory so a rename survives a crash.
func (r bootRecordIO) syncDir() error {
	d, err := r.root.Open(".")
	if err != nil {
		return fmt.Errorf("config: open the state directory: %w", err)
	}
	serr := d.Sync()
	if cerr := d.Close(); serr == nil {
		serr = cerr
	}
	if serr != nil {
		return fmt.Errorf("config: sync the state directory: %w", serr)
	}
	return nil
}
