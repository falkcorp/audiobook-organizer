// file: internal/books/cutover/m5/step.go
// version: 1.0.1
// guid: 4e8b2d70-a1c5-4963-8f3e-7d0c9b5a2e16
// last-edited: 2026-11-13

// Package m5 is books' cutover step M5, Log (03 §3.8 row M5; 05-logging §5;
// F5-15): HEAD's system log rows, syslog:<time>:<source>, into core's native
// journal through log.Importer, one journal record per row, and the rows gone.
//
// The step is dark until F5-18 registers it: nothing here is reached from
// internal/books/cutover's step list or from main. It takes the Importer as a
// parameter and imports neither core/internal/logjournal (an app module cannot)
// nor internal/books/cutover (which imports this package once the step list
// exists, so the step ID comes in as a parameter too).
package m5

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// Prefix is HEAD's system log family: syslog:<RFC3339Nano>:<source>, written by
// PebbleStore.AddSystemActivityLog (internal/books/store/pebble_store_activity.go).
const Prefix = "syslog:"

// Module is the journal record's Module for every imported row: the system log
// was books' own, written by its housekeeping goroutines.
const Module = "books"

// UserIDAttr is the attribute that carries a row's user_id when it has one.
const UserIDAttr = "user_id"

// Outcomes the step counts beside the runner's "quarantined".
const (
	OutcomeImported = "imported" // a row imported into the journal and deleted
)

// Quarantine reasons: fixed classes, never the decoder's text (a JSON error
// quotes the value it rejected).
const (
	reasonUndecodable = "the syslog row does not decode"
	reasonLevel       = "the syslog row's level is not one HEAD wrote (info, warn or error)"
	reasonTime        = "the syslog row has no usable time"
)

// systemActivityLog is HEAD's row (store.SystemActivityLog), frozen here: this
// package cannot import the store whose rows it retires, and HEAD's row never
// changes again.
type systemActivityLog struct {
	ID        int       `json:"id"`
	UserID    string    `json:"user_id,omitempty"`
	Source    string    `json:"source"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// SourceRanges is the step's Source: the retired claim of the syslog: family.
func SourceRanges() []storage.Range {
	start := []byte(Prefix)
	end := bytes.Clone(start)
	end[len(end)-1]++
	return []storage.Range{{Start: start, End: end}}
}

// Config is what the step needs besides its declaration.
type Config struct {
	// Importer is the native journal's importer (module.Importers' log member).
	Importer log.Importer
	// Logger receives one line per quarantined row (its key, a class, never its
	// message). nil discards.
	Logger *slog.Logger
}

// Migration is the step's state: the importer and the step's own accounting.
// It holds no per-chunk state, because the runner steps its shards
// concurrently: every Step call is independent.
type Migration struct {
	imp    log.Importer
	logger *slog.Logger

	read, imported, quarantined atomic.Int64
}

// New returns the data migration for the step with ID id.
//
// raisesLevel and after are the flip's: F3's books level and the StepRefs of
// the steps that must run first (books' level step), the same two M2 and M3
// take. The caller passes them because books' level constants live in package
// cutover, which imports this one.
//
// Parallelism is Fixed(8): the work is one synced journal commit per row, so it
// is bound by the disk's fsync rate, not by the CPU, and concurrent commits from
// several shards share an fsync (group commit). Eight shards raise throughput
// well past one without a core's worth of work each; more would only queue on
// the same disk. tasks.CPU() would take executor CPU slots for work that waits
// on I/O. The shards are disjoint key ranges of the retired syslog: claim, so no
// two workers step one row, and a row's import is keyed by its own key
// (idempotent), so no row is journaled twice however the work is split or
// resumed.
func New(cfg Config, id string, raisesLevel int, after []storage.StepRef) (datamigrate.DataMigration, error) {
	switch {
	case cfg.Importer == nil:
		return datamigrate.DataMigration{}, errors.New("m5: Config.Importer is required")
	case id == "":
		return datamigrate.DataMigration{}, errors.New("m5: the step ID is required")
	case raisesLevel <= 0:
		return datamigrate.DataMigration{}, errors.New("m5: raisesLevel is the flip's books level, above 0")
	case len(after) == 0:
		return datamigrate.DataMigration{}, errors.New("m5: after is books' level step")
	}
	m := &Migration{imp: cfg.Importer, logger: cfg.Logger}
	if m.logger == nil {
		m.logger = slog.New(slog.DiscardHandler)
	}
	return datamigrate.DataMigration{
		ID:          id,
		Owner:       "books",
		Description: "the system log (syslog: rows) into core's journal",
		Source:      SourceRanges(),
		Parallelism: tasks.Fixed(8, "one synced journal commit per row: disk-bound, and concurrent commits share an fsync"),
		Mode:        datamigrate.BootBlocking,
		Compat:      storage.Breaking,
		RaisesLevel: raisesLevel,
		After:       append([]storage.StepRef(nil), after...),
		Cutover:     true,
		Step:        m.Step,
		Verify:      m.Verify,
	}, nil
}

// Step imports one row and deletes it. The import is one Importer.Import call
// of one record, returned before the delete is staged, so by the time the chunk
// batch holding the delete commits the record is already durable in the
// journal: a crash between the two leaves the row to be imported again, onto
// the same key (the importer's key is a function of the record's time and the
// row's key), which writes nothing new. The step buffers nothing across calls.
//
// A row that does not decode, whose level HEAD never wrote, or that has no
// usable time is quarantined with its raw bytes (Quarantine deletes the source
// key itself). Anything the importer returns fails the chunk: a refusal of a
// well-formed record is the importer's defect or a full disk, and the boot
// stops with the checkpoint open rather than losing rows.
func (m *Migration) Step(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	m.read.Add(1)
	key := string(kv.Key)
	rec, reason := decode(key, kv.Value)
	if reason != "" {
		m.logger.Warn("m5: a system log row is quarantined", "key", key, "reason", reason)
		if err := w.Quarantine(kv.Key, kv.Value, reason); err != nil {
			return err
		}
		m.quarantined.Add(1)
		return nil
	}
	if err := m.imp.Import(ctx, []log.ImportedRecord{{Record: rec, SourceID: key}}); err != nil {
		return fmt.Errorf("m5: import the system log row %q: %w", key, err)
	}
	if err := w.Delete(kv.Key); err != nil {
		return err
	}
	m.imported.Add(1)
	w.Count(OutcomeImported)
	return nil
}

// decode is the journal record of a row, or the quarantine reason.
func decode(key string, raw []byte) (log.Record, string) {
	var row systemActivityLog
	if err := json.Unmarshal(raw, &row); err != nil {
		return log.Record{}, reasonUndecodable
	}
	lv, ok := level(row.Level)
	if !ok {
		return log.Record{}, reasonLevel
	}
	// Judged in UTC, the zone the record is stored in: a created_at of
	// 9999-12-31T23:59:59-05:00 is year 10000 there, which the journal's Import
	// refuses (it would fail the BootBlocking chunk on every boot), so the check
	// is the importer's own.
	t := row.CreatedAt.UTC()
	keySource := ""
	if kt, ks, ok := parseKey(key); ok {
		keySource = ks
		if !usable(t) {
			// A row stored before created_at was written, or with a zero value:
			// the key carries the time the row was added.
			t = kt.UTC()
		}
	}
	if !usable(t) {
		return log.Record{}, reasonTime
	}
	sub := row.Source
	if sub == "" {
		sub = keySource // the key carries the source the row was added under
	}
	rec := log.Record{Time: t, Level: lv, Module: Module, Subsystem: sub, Message: row.Message}
	if row.UserID != "" {
		rec.Attrs = []slog.Attr{slog.String(UserIDAttr, row.UserID)}
	}
	return rec, ""
}

// level is the slog level of a level string HEAD wrote. AddSystemActivityLog is
// called with Level.String() for levels at or above info only
// (internal/logger/standard.go), so these three are the whole set.
func level(s string) (slog.Level, bool) {
	switch s {
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return 0, false
}

// usable is a time the journal can key: not zero, 1970 through year 9999.
func usable(t time.Time) bool {
	return !t.IsZero() && t.UnixMilli() >= 0 && t.Year() <= 9999
}

// parseKey splits syslog:<RFC3339Nano>:<source> into its time and source. The
// time itself holds colons, so it is cut by its shape, not by the delimiter:
// "T", then a zone that is "Z" or a signed hh:mm, then ":" and the source.
func parseKey(key string) (t time.Time, source string, ok bool) {
	rest, found := strings.CutPrefix(key, Prefix)
	if !found {
		return time.Time{}, "", false
	}
	tIdx := strings.IndexByte(rest, 'T')
	if tIdx < 0 {
		return time.Time{}, "", false
	}
	end := -1
	for i := tIdx + 1; i < len(rest); i++ {
		switch rest[i] {
		case 'Z':
			end = i + 1
		case '+', '-':
			end = i + 6 // the sign and hh:mm
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 || end >= len(rest) || rest[end] != ':' {
		return time.Time{}, "", false
	}
	t, err := time.Parse(time.RFC3339Nano, rest[:end])
	if err != nil {
		return time.Time{}, "", false
	}
	return t, rest[end+1:], true
}

// Verify is the step's invariant gate: no syslog: row is left, and this boot's
// own accounting balances (every row Step read was imported or quarantined,
// never neither). The counts in the report are this boot's: a boot that resumed
// after a crash reports only what it stepped.
func (m *Migration) Verify(ctx context.Context, r storage.Reader) (datamigrate.VerifyReport, error) {
	rep := datamigrate.VerifyReport{Read: m.read.Load(), Written: m.imported.Load(), Quarantined: m.quarantined.Load()}
	for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: SourceRanges()[0].Start, End: SourceRanges()[0].End, KeysOnly: true, Limit: 1}) {
		if err != nil {
			return rep, err
		}
		rep.Failures = append(rep.Failures, fmt.Sprintf("a syslog: row is still present (%s)", kv.Key))
	}
	if rep.Read != rep.Written+rep.Quarantined {
		rep.Failures = append(rep.Failures, fmt.Sprintf("read %d rows but imported %d and quarantined %d", rep.Read, rep.Written, rep.Quarantined))
	}
	return rep, nil
}
