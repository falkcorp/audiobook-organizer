// file: internal/books/cutover/m4/markers.go
// version: 1.0.0
// guid: b4e81d27-6c3f-4a90-8d52-1f07a9c6e3b5
// last-edited: 2026-11-18

package m4

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// stepPause imports the operator pause marker. HEAD clears nothing: a resumed
// gate writes a marker whose Paused is false (a blank value is also read as
// absent), and neither carries a pause.
func (m *Migration) stepPause(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	v, ok := settingValue(kv.Value)
	if !ok {
		return m.quarantineOne(kv, w, reasonSetting)
	}
	if strings.TrimSpace(v) != "" {
		var p pauseValue
		if json.Unmarshal([]byte(v), &p) != nil {
			return m.quarantineOne(kv, w, reasonPauseValue)
		}
		if p.Paused {
			var at time.Time
			if p.SinceUTC != 0 {
				at = time.Unix(0, p.SinceUTC).UTC()
			}
			if err := m.imp.Pause(ctx, w.Batch(), tasks.Pause{Reason: p.Reason, By: tasks.Actor{UserID: p.By}, At: at}); err != nil {
				return err
			}
			m.markers.Add(1)
			w.Count(OutcomePause)
			return w.Delete(kv.Key)
		}
	}
	w.Count(OutcomeMarkerCleared)
	return w.Delete(kv.Key)
}

// stepStandDown imports the scan stand-down marker for the converted scan
// kind's key. A blank value is HEAD's cleared sentinel, and a marker with no
// scan op reads as absent (HEAD's readScanStandDownMarker): neither imports.
func (m *Migration) stepStandDown(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	v, ok := settingValue(kv.Value)
	if !ok {
		return m.quarantineOne(kv, w, reasonSetting)
	}
	if strings.TrimSpace(v) != "" {
		var s standDownValue
		if json.Unmarshal([]byte(v), &s) != nil {
			return m.quarantineOne(kv, w, reasonStandDown)
		}
		if s.ScanOpID != "" {
			var exp time.Time
			if s.LeaseExpiryUTC != 0 {
				exp = time.Unix(0, s.LeaseExpiryUTC).UTC()
			}
			hold := tasks.StandDown{Key: m.scanKey, Holder: tasks.RunID(s.HolderOpID), Quiesced: []tasks.RunID{tasks.RunID(s.ScanOpID)}, Expires: exp}
			if err := m.imp.StandDown(ctx, w.Batch(), hold); err != nil {
				return err
			}
			m.markers.Add(1)
			w.Count(OutcomeStandDown)
			return w.Delete(kv.Key)
		}
	}
	w.Count(OutcomeMarkerCleared)
	return w.Delete(kv.Key)
}

func (m *Migration) quarantineOne(kv storage.KV, w *datamigrate.StepWriter, reason string) error {
	m.logger.Warn("m4: a row is quarantined", "key", string(kv.Key), "reason", reason)
	m.rowsQuarantined.Add(1)
	return w.Quarantine(kv.Key, kv.Value, reason)
}

// stepBucket handles one op:batch: row. HEAD keeps a debounce bucket as one row
// per pending subject under op:batch:<op type>:; the native bucket is one record
// per kind, so the lowest row of the op type owns it, as in stepClock: it reads
// every row of the op type, imports one bucket and deletes them all in one batch.
//
// Every row under the op type's prefix belongs to the bucket, including one
// whose key does not decode as a debounce row (a stray key): the lowest row
// owns the bucket whatever its shape, and a stray key anywhere in it sets the
// whole bucket aside. A stray row is never set aside by its own worker. It
// once was, while the others deferred to the lowest row: when the stray row
// was the lowest, nothing owned the bucket, and whether its rows were imported
// or left behind (failing Verify) hung on whether the stray row's quarantine
// had committed when the others were stepped. Only a key that names no op
// type, which no owner scans, is set aside by itself.
func (m *Migration) stepBucket(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	opType, ok := batchBucket(string(kv.Key))
	if !ok {
		return m.quarantineOne(kv, w, reasonBatchKey)
	}
	prefix := prefixBatch + opType + ":"
	rows, err := scanRows(ctx, m.legacy(), prefix)
	if err != nil {
		return err
	}
	if len(rows) == 0 || string(rows[0].Key) != string(kv.Key) {
		// Either a lower row owns the bucket, or the owner's batch took this row
		// since the runner read it.
		m.deferred.Add(1)
		w.Count(OutcomeDeferred)
		return nil
	}
	if slices.ContainsFunc(rows, func(row storage.KV) bool {
		_, ok := batchOpType(string(row.Key))
		return !ok
	}) {
		// Before the kind: rows that cannot be read are not deleted unread, even
		// under a retired kind.
		for _, row := range rows {
			why := reasonBatchStray
			if _, ok := batchOpType(string(row.Key)); !ok {
				why = reasonBatchKey
			}
			if err := m.quarantineOne(row, w, why); err != nil {
				return err
			}
		}
		return nil
	}
	tg, err := kindrename.Resolve(opType)
	switch {
	case err != nil || tg.Kind == "":
		return m.quarantineBucket(rows, w, reasonBatchKind)
	case tg.Rule == kindrename.RuleRetired:
		// A retired kind's pending starts would start nothing.
		for _, row := range rows {
			if err := w.Delete(row.Key); err != nil {
				return err
			}
		}
		m.dropped.Add(1)
		w.Count(OutcomeClockDropped)
		return nil
	}
	d := tasks.DebounceBucket{Kind: tg.Kind}
	var good []storage.KV
	for _, row := range rows {
		var e batchEntry // every key decodes: a stray set the bucket aside above
		if json.Unmarshal(row.Value, &e) != nil || e.Sub.ID == "" {
			if err := m.quarantineOne(row, w, reasonBatchKey); err != nil {
				return err
			}
			continue
		}
		at := time.Unix(0, e.AddedAt).UTC()
		if d.First.IsZero() || at.Before(d.First) {
			d.First = at
		}
		if at.After(d.Last) {
			d.Last = at
		}
		d.Subjects = append(d.Subjects, subjectRef(e.Sub.Type+":"+e.Sub.ID))
		good = append(good, row)
	}
	if len(good) == 0 {
		return nil
	}
	if err := m.imp.Debounce(ctx, w.Batch(), d); err != nil {
		return err
	}
	for _, row := range good {
		if err := w.Delete(row.Key); err != nil {
			return err
		}
	}
	m.buckets.Add(1)
	w.Count(OutcomeBucket)
	return nil
}

func (m *Migration) quarantineBucket(rows []storage.KV, w *datamigrate.StepWriter, reason string) error {
	for _, row := range rows {
		if err := m.quarantineOne(row, w, reason); err != nil {
			return err
		}
	}
	return nil
}

// batchBucket is the op type of an op:batch: key that lies under an op type's
// prefix (op:batch:<op type>:), whatever the rest of the key holds: every such
// row is in the bucket owner's scan. ok is false for a key that names no op
// type (no owner ever scans it).
func batchBucket(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, prefixBatch)
	if !ok {
		return "", false
	}
	opType, _, ok := strings.Cut(rest, ":")
	return opType, ok && opType != ""
}

// batchOpType is the op type of an op:batch:<op type>:<subject type>:<subject
// id> key.
func batchOpType(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, prefixBatch)
	if !ok {
		return "", false
	}
	opType, tail, ok := strings.Cut(rest, ":")
	if !ok || opType == "" || !strings.Contains(tail, ":") {
		return "", false
	}
	return opType, true
}
