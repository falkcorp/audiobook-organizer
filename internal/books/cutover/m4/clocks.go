// file: internal/books/cutover/m4/clocks.go
// version: 1.0.0
// guid: 0f6b3a92-d417-4e85-9c2a-7b18e5d03f64
// last-edited: 2026-11-18

package m4

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// Quarantine reasons of the clock, marker and bucket families (fixed classes).
const (
	reasonSetting    = "the setting row does not decode"
	reasonSched      = "the schedule state row does not decode or names no kind"
	reasonTickTask   = "the clock names a scheduler task the scheduler never had"
	reasonTickTime   = "the clock holds no RFC3339 time"
	reasonWindowDate = "the maintenance window clock holds no YYYY-MM-DD date"
	reasonPauseValue = "the pause marker does not decode"
	reasonStandDown  = "the stand-down marker does not decode"
	reasonBatchKey   = "the debounce row's key or entry does not decode"
	reasonBatchKind  = "the debounce row's operation names no usable kind"
)

// Outcomes of the families above.
const (
	OutcomeClock         = "clock-imported"
	OutcomeClockDropped  = "clock-dropped"  // a clock of a task that fires nothing, or of a retired kind
	OutcomePause         = "pause-imported" // a set marker
	OutcomeMarkerCleared = "marker-cleared" // HEAD's cleared sentinel: a blank value or one that reads as absent
	OutcomeStandDown     = "standdown-imported"
	OutcomeBucket        = "debounce-imported"
)

// settingRow is the setting: row envelope (store.Setting): the value is inside.
type settingRow struct {
	Value string `json:"value"`
}

// schedRow is opv3:sched: (store.SchedState).
type schedRow struct {
	DefID        string     `json:"def_id"`
	LastFire     *time.Time `json:"last_fire"`
	Missed       int64      `json:"missed"`
	GateClosedAt *time.Time `json:"gate_closed_at"`
}

// pauseValue is the operator pause marker (registry.pauseMarker).
type pauseValue struct {
	Paused   bool   `json:"paused"`
	Reason   string `json:"reason"`
	By       string `json:"by"`
	SinceUTC int64  `json:"since_unix_nano"`
}

// standDownValue is the scan stand-down marker (registry.scanStandDownMarker).
type standDownValue struct {
	HolderOpID     string `json:"holder_op_id"`
	ScanOpID       string `json:"scan_op_id"`
	LeaseExpiryUTC int64  `json:"lease_expiry_unix_nano"`
}

// batchEntry is op:batch: (store.BatchBucketEntry).
type batchEntry struct {
	Sub struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"sub"`
	AddedAt int64 `json:"added_at"`
}

// settingValue reads the value out of a setting: row.
func settingValue(raw []byte) (string, bool) {
	var s settingRow
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s.Value, true
}

// clockSource is one source row of a kind's clock.
type clockSource struct {
	kv   storage.KV
	kind tasks.Kind
	// due, fire and run are the times the row contributes (zero: none).
	due, fire, run time.Time
	missed         int64
	gate           time.Time
}

// clockClass is what a clock source row turned out to be.
type clockClass int

const (
	clockGood      clockClass = iota // contributes to the clock of kind
	clockDrop                        // fires nothing: deleted, no clock
	clockBad                         // quarantined by itself, with reason
	clockNotSource                   // not a clock row
)

// classifyClock reads one clock source row.
func (m *Migration) classifyClock(kv storage.KV) (clockSource, clockClass, string) {
	key := string(kv.Key)
	src := clockSource{kv: kv}
	switch {
	case strings.HasPrefix(key, prefixSched):
		var s schedRow
		if json.Unmarshal(kv.Value, &s) != nil {
			return src, clockBad, reasonSched
		}
		tg, err := kindrename.Resolve(s.DefID)
		if err != nil || tg.Kind == "" {
			return src, clockBad, reasonSched
		}
		if tg.Rule == kindrename.RuleRetired {
			return src, clockDrop, ""
		}
		src.kind, src.missed = tg.Kind, s.Missed
		if s.LastFire != nil {
			// HEAD's SchedState carries LastFire, not the LastDue and LastRun
			// 5T.8.3 lists: its schedule fires at the due time, so the fire time
			// is all three.
			src.due, src.fire, src.run = s.LastFire.UTC(), s.LastFire.UTC(), s.LastFire.UTC()
		}
		if s.GateClosedAt != nil {
			src.gate = s.GateClosedAt.UTC()
		}
		return src, clockGood, ""
	case strings.HasPrefix(key, settingTick):
		task := strings.TrimPrefix(key, settingTick)
		target, ok := kindrename.SchedulerKind(task)
		if !ok {
			return src, clockBad, reasonTickTask
		}
		v, ok := settingValue(kv.Value)
		at, err := time.Parse(time.RFC3339, v)
		if !ok || err != nil {
			return src, clockBad, reasonTickTime
		}
		if target.Dropped {
			return src, clockDrop, ""
		}
		src.kind, src.due = target.Kind, at.UTC()
		return src, clockGood, ""
	case key == keyFullSweep:
		v, ok := settingValue(kv.Value)
		at, err := time.Parse(time.RFC3339, v)
		if !ok || err != nil {
			return src, clockBad, reasonTickTime
		}
		src.kind, src.due = kindrename.FullSweepKind, at.UTC()
		return src, clockGood, ""
	case key == keyWindow:
		v, ok := settingValue(kv.Value)
		// The date HEAD stores is the local calendar day the window ran: its
		// clock is that day's local midnight, so a window that ran on day D is
		// not run again on D west of UTC (MOD7-3, MOD8-9).
		at, err := time.ParseInLocation("2006-01-02", v, m.loc)
		if !ok || err != nil {
			return src, clockBad, reasonWindowDate
		}
		tg, err := kindrename.Resolve("maintenance.window")
		if err != nil {
			return src, clockBad, reasonWindowDate
		}
		src.kind, src.due, src.run = tg.Kind, at.UTC(), at.UTC()
		return src, clockGood, ""
	}
	return src, clockNotSource, ""
}

// clockRows is every clock source row, read live: the sched and interval-clock
// families by scan and the two exact keys by Get.
func (m *Migration) clockRows(ctx context.Context) ([]storage.KV, error) {
	r := m.legacy()
	var out []storage.KV
	for _, p := range []string{prefixSched, settingTick} {
		rows, err := scanRows(ctx, r, p)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	for _, k := range []string{keyFullSweep, keyWindow} {
		raw, err := r.Get(ctx, []byte(k))
		switch {
		case errors.Is(err, storage.ErrNotFound):
		case err != nil:
			return nil, err
		default:
			out = append(out, storage.KV{Key: []byte(k), Value: raw})
		}
	}
	return out, nil
}

// stepClock handles one clock source row. A kind's clock may come from several
// rows (a def's v3 schedule state, the scheduler's interval clock of the task
// that fires it, the full sweep's and the window's own clocks), and Importer.Clock
// replaces what it finds, so one row owns each kind: the lowest key among the
// kind's rows, found by reading the (small) clock families live. That row
// merges the group, imports one clock and deletes every row of the group in its
// own batch; the others leave their rows to it. A row that is no longer among
// the live rows was taken by its group's batch.
func (m *Migration) stepClock(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	rows, err := m.clockRows(ctx)
	if err != nil {
		return err
	}
	mine := -1
	for i, row := range rows {
		if string(row.Key) == string(kv.Key) {
			mine = i
		}
	}
	if mine < 0 {
		m.deferred.Add(1)
		w.Count(OutcomeDeferred)
		return nil // taken by the group's batch since the runner read it
	}
	self, class, reason := m.classifyClock(rows[mine])
	switch class {
	case clockBad:
		m.logger.Warn("m4: a clock row is quarantined", "key", string(kv.Key), "reason", reason)
		m.rowsQuarantined.Add(1)
		return w.Quarantine(rows[mine].Key, rows[mine].Value, reason)
	case clockDrop:
		m.dropped.Add(1)
		w.Count(OutcomeClockDropped)
		return w.Delete(rows[mine].Key)
	case clockGood:
	default:
		return errors.New("m4: a row that is no clock source reached the clock step")
	}
	var group []clockSource
	for _, row := range rows {
		src, c, _ := m.classifyClock(row)
		if c == clockGood && src.kind == self.kind {
			group = append(group, src)
		}
	}
	slices.SortFunc(group, func(a, b clockSource) int { return strings.Compare(string(a.kv.Key), string(b.kv.Key)) })
	if string(group[0].kv.Key) != string(kv.Key) {
		m.deferred.Add(1)
		w.Count(OutcomeDeferred)
		return nil // the group's lowest row imports it
	}
	// The reads above are several (a scan per family, a Get per exact key), not
	// one snapshot: the group's driver may have committed between them, taking
	// every row of the group, and a later read would then see a group missing
	// its driver's rows. Its commit is atomic, so my own row still being here
	// after the last read proves no such commit came first.
	if _, err := m.legacy().Get(ctx, kv.Key); errors.Is(err, storage.ErrNotFound) {
		m.deferred.Add(1)
		w.Count(OutcomeDeferred)
		return nil
	} else if err != nil {
		return err
	}
	var c tasks.Clock
	for _, g := range group {
		c.LastDue = later(c.LastDue, g.due)
		c.LastFire = later(c.LastFire, g.fire)
		c.LastRun = later(c.LastRun, g.run)
		c.GateClosedAt = later(c.GateClosedAt, g.gate)
		c.Missed = int(max(int64(c.Missed), g.missed))
	}
	if err := m.imp.Clock(ctx, w.Batch(), self.kind, c); err != nil {
		return err
	}
	for _, g := range group {
		if err := w.Delete(g.kv.Key); err != nil {
			return err
		}
	}
	m.clocks.Add(1)
	w.Count(OutcomeClock)
	return nil
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
