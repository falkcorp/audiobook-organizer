// file: core/internal/pebble/engine.go
// version: 1.0.0
// guid: 1f0b30a9-2400-473a-be39-654bb7480460
// last-edited: 2026-11-07

// Package pebble is core storage's engine (05-storage §5S.3, §5S.9.3 rows
// 1-2): the Pebble open without a format version and the ratchet to the one
// pinned format, the lease gate every engine call holds (the successor of
// HEAD's GatedDB), the atomic file writer the sidecars and checkpoint records
// use, and the store guard's comparison rules. It is re-implemented from
// HEAD's pebble_gate.go, storage_format.go and open path, not moved: ABO keeps
// its own copies until R1-23 re-homes them.
//
// Only package storage imports it (L1, the layer of the package it
// implements).
package pebble

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// Format is the one pinned Pebble format of the main and every auxiliary DB
// (05-storage §5S.3.7 "Pebble format pin"): value separation, HEAD's
// PebbleFormatMajorVersion. The engine opens without a format version, so an
// existing store keeps its format until Ratchet, which the boot runs only
// after the pre-migration checkpoint (§5S.4 step 7): a ratchet ahead of the
// checkpoint would make the checkpoint unopenable by the previous build.
const Format = cpebble.FormatValueSeparation

// Options configures one engine.
type Options struct {
	// FS is the filesystem the engine, its sidecars and its checkpoints use;
	// nil = vfs.Default. Tests pass vfs.NewMem or vfs.NewCrashableMem.
	FS vfs.FS
	// CacheBytes and MemtableBytes are core.storage.cache_bytes and
	// core.storage.memtable_bytes; 0 = Pebble's default.
	CacheBytes    int64
	MemtableBytes int64
	// ReadOnly opens Pebble ReadOnly: no WAL flush, no ratchet, no OPTIONS
	// file (05-storage §5S.3.10).
	ReadOnly bool
	// ErrorIfNotExists refuses a directory that holds no store.
	ErrorIfNotExists bool
	// Logger receives Pebble's own log lines at debug level, and the
	// engine's; nil discards them.
	Logger *slog.Logger
}

// Engine is one open Pebble store and its lease gate.
type Engine struct {
	db       *cpebble.DB
	gate     *gate
	fs       vfs.FS
	path     string
	readOnly bool
}

// Open opens (or creates) the store at path without a format version: a new
// store starts at Pebble's minimum supported format and an existing one keeps
// its own, so a refused open has changed nothing a previous build cannot read
// (05-storage §5S.4 step 2).
func Open(path string, o Options) (*Engine, error) {
	fs := o.FS
	if fs == nil {
		fs = vfs.Default
	}
	opts := &cpebble.Options{
		FS:               fs,
		ReadOnly:         o.ReadOnly,
		ErrorIfNotExists: o.ErrorIfNotExists,
		Logger:           pebbleLogger{l: o.Logger},
	}
	if o.MemtableBytes > 0 {
		opts.MemTableSize = uint64(o.MemtableBytes)
	}
	var cache *cpebble.Cache
	if o.CacheBytes > 0 {
		cache = cpebble.NewCache(o.CacheBytes)
		opts.Cache = cache
	}
	db, err := cpebble.Open(path, opts)
	if cache != nil {
		// The engine holds its own reference; this one is the constructor's.
		cache.Unref()
	}
	if err != nil {
		return nil, fmt.Errorf("pebble: open %s: %w", path, err)
	}
	e := &Engine{db: db, fs: fs, path: path, readOnly: o.ReadOnly}
	e.gate = newGate(walSyncer{db: db}, !o.ReadOnly)
	return e, nil
}

// Path is the store's directory.
func (e *Engine) Path() string { return e.path }

// FS is the engine's filesystem.
func (e *Engine) FS() vfs.FS { return e.fs }

// ReadOnly reports a read-only open.
func (e *Engine) ReadOnly() bool { return e.readOnly }

// Lease takes one gate lease for the duration of an engine call, or refuses
// with ErrClosing once Close began. The caller runs its engine calls through
// DB and calls the returned release exactly once when they are done (an
// iterator or snapshot keeps its lease until it is closed).
func (e *Engine) Lease() (release func(), err error) {
	if err := e.gate.acquire(); err != nil {
		return nil, err
	}
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		e.gate.release()
	}, nil
}

// DB is the engine, for a caller holding a lease.
func (e *Engine) DB() *cpebble.DB { return e.db }

// FormatMajorVersion is the store's current Pebble format.
func (e *Engine) FormatMajorVersion() cpebble.FormatMajorVersion { return e.db.FormatMajorVersion() }

// NeedsRatchet reports whether the store is below the pinned format.
func (e *Engine) NeedsRatchet() bool { return !e.readOnly && e.db.FormatMajorVersion() < Format }

// Ratchet moves the store to the pinned format (05-storage §5S.4 step 7).
// Only after the boot's checkpoint, or on a fresh store; a read-only engine
// refuses.
func (e *Engine) Ratchet() error {
	if e.readOnly {
		return errors.New("pebble: ratchet of a read-only engine")
	}
	release, err := e.Lease()
	if err != nil {
		return err
	}
	defer release()
	if err := e.db.RatchetFormatMajorVersion(Format); err != nil {
		return fmt.Errorf("pebble: ratchet %s to format %d: %w", e.path, Format, err)
	}
	return nil
}

// Close drains the gate up to timeout (core.storage.close_timeout), syncs the
// WAL and closes the engine. On timeout it returns an error and leaves the
// engine open; it may be called again.
func (e *Engine) Close(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return e.gate.close(timeout)
}

// walSyncer adapts *pebble.DB to the gate's WAL barrier: an empty LogData
// with Sync (HEAD's LogData(nil, Sync), ident_state.go:1360).
type walSyncer struct{ db *cpebble.DB }

func (w walSyncer) SyncWAL() error { return w.db.LogData(nil, cpebble.Sync) }
func (w walSyncer) Close() error   { return w.db.Close() }

// pebbleLogger routes Pebble's own lines to the engine logger at debug, and
// its errors at error. Fatalf panics, as Pebble's default logger exits: a
// fatal engine error must not be swallowed.
type pebbleLogger struct{ l *slog.Logger }

func (p pebbleLogger) Infof(format string, args ...any) {
	if p.l != nil {
		p.l.Debug("pebble", "line", fmt.Sprintf(format, args...))
	}
}

func (p pebbleLogger) Errorf(format string, args ...any) {
	if p.l != nil {
		p.l.Error("pebble", "line", fmt.Sprintf(format, args...))
	}
}

func (p pebbleLogger) Fatalf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.l != nil {
		p.l.Error("pebble: fatal", "line", msg)
	}
	panic("pebble: fatal: " + msg)
}
