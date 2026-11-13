// file: internal/books/cutover/m5/cutover_test.go
// version: 1.0.0
// guid: 5c0e93a8-d641-4b7f-8a25-f3b1d7e60c94
// last-edited: 2026-11-13

package m5_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m5"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// TestCutover_SyslogImported is M5's acceptance over the R2 corpus (F2-12's
// stage 1, F2-13's overlay with its corrupt syslog row) and over synthetic rows
// for each field mapping, quarantine class and failure: the step leaves no
// syslog: row, journals exactly one record per row with HEAD's time, level and
// source as Subsystem, message and user_id, quarantines what it cannot map, and
// resumes after a crash without journaling anything twice.
func TestCutover_SyslogImported(t *testing.T) {
	// legacyOf decodes a seeded store's rows with HEAD's own shape: the test's
	// oracle for what each journal record must carry.
	legacyOf := func(t *testing.T, rows map[string][]byte) map[string]headRow {
		t.Helper()
		out := map[string]headRow{}
		for k, v := range rows {
			var row headRow
			if err := json.Unmarshal(v, &row); err == nil {
				out[k] = row
			}
		}
		return out
	}
	levelOf := map[string]slog.Level{"info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}

	t.Run("corpus_stage1", func(t *testing.T) {
		r := newRig(t, "stage1")
		mem := r.seed(t)
		src := r.before(t, mem)
		if len(src) == 0 {
			t.Fatal("the corpus's stage 1 store holds no syslog: row: the test would compare nothing with nothing")
		}
		want := legacyOf(t, src)
		s, err := r.runOn(t, mem)
		if err != nil {
			t.Fatalf("boot: %v\n%s", err, r.logs)
		}
		if len(s.Left) != 0 {
			t.Errorf("%d syslog: rows left, want 0", len(s.Left))
		}
		if len(s.Quarantine) != 0 {
			t.Errorf("quarantined %+v, want none: stage 1's rows are well-formed", s.Quarantine)
		}
		if s.State != "done" {
			t.Errorf("state = %q, want done", s.State)
		}
		if len(s.Journal) != len(src) {
			t.Fatalf("%d journal records for %d rows", len(s.Journal), len(src))
		}
		checkJournal(t, s, want, levelOf)
		if v := s.Verify; v.Read != int64(len(src)) || v.Written != int64(len(src)) || v.Quarantined != 0 || len(v.Failures) != 0 {
			t.Errorf("verify = %+v, want read=written=%d, nothing quarantined, no failures", v, len(src))
		}
		t.Logf("stage1: %d syslog row(s) imported, 0 quarantined", len(src))
	})

	t.Run("corpus_overlay_quarantines_the_corrupt_row", func(t *testing.T) {
		const corrupt = "syslog:2026-11-10T15:37:13.179512786Z:r2corpus-overlay"
		r := newRig(t, "overlay")
		mem := r.seed(t)
		src := r.before(t, mem)
		raw, ok := src[corrupt]
		if !ok {
			t.Fatalf("the corpus's overlay store has no %s row", corrupt)
		}
		want := legacyOf(t, src)
		if _, decodes := want[corrupt]; decodes {
			t.Fatalf("the overlay's corrupt row decodes as HEAD's row: it is no longer corrupt")
		}
		s, err := r.runOn(t, mem)
		if err != nil {
			t.Fatalf("boot: %v\n%s", err, r.logs)
		}
		if len(s.Left) != 0 {
			t.Errorf("%d syslog: rows left, want 0 (the quarantined row is moved, not kept)", len(s.Left))
		}
		if len(s.Quarantine) != 1 {
			t.Fatalf("quarantined %d rows, want exactly the corrupt one: %+v", len(s.Quarantine), s.Quarantine)
		}
		q := s.Quarantine[0]
		if q.Key != corrupt || q.Step != cutover.LogCutoverID || q.Value != string(raw) {
			t.Errorf("quarantine = %+v, want key %s, step %s and the row's own bytes", q, corrupt, cutover.LogCutoverID)
		}
		if q.Reason == "" || strings.Contains(q.Reason, "Arkham") {
			t.Errorf("quarantine reason %q must be a fixed class that never quotes the row", q.Reason)
		}
		if got, want := len(s.Journal), len(src)-1; got != want {
			t.Errorf("%d journal records, want %d (every row but the corrupt one)", got, want)
		}
		checkJournal(t, s, want, levelOf)
		if v := s.Verify; v.Read != int64(len(src)) || v.Written != int64(len(src)-1) || v.Quarantined != 1 || len(v.Failures) != 0 {
			t.Errorf("verify = %+v, want read=%d written=%d quarantined=1", v, len(src), len(src)-1)
		}
		logs := r.logs.String()
		if !strings.Contains(logs, corrupt) {
			t.Errorf("the quarantine line does not name the row's key:\n%s", logs)
		}
		if strings.Contains(logs, "Arkham") {
			t.Errorf("the quarantine line quotes the row's message:\n%s", logs)
		}
		t.Logf("overlay: %d syslog rows read, %d imported, 1 quarantined", len(src), len(src)-1)
	})

	// The mapping table: each case is one row and what it must become.
	t.Run("field_mapping", func(t *testing.T) {
		t0 := time.Date(2026, 11, 9, 3, 4, 5, 123456789, time.UTC)
		est := time.FixedZone("EST", -5*3600)
		type tc struct {
			name string
			key  string
			val  []byte
			// want is the journal record; reason is the quarantine class instead.
			want   *journaled
			reason string
		}
		row := func(level string, mod func(*headRow)) []byte {
			h := headRow{ID: 7, Source: "organizer", Level: level, Message: "Year One indexed", CreatedAt: t0}
			if mod != nil {
				mod(&h)
			}
			raw, err := json.Marshal(h)
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}
		cases := []tc{
			{name: "info", key: syslogKey(t0, "organizer"), val: row("info", nil),
				want: &journaled{Time: t0, Level: int(slog.LevelInfo), Module: m5.Module, Subsystem: "organizer", Message: "Year One indexed"}},
			{name: "warn", key: syslogKey(t0.Add(time.Second), "organizer"), val: row("warn", func(h *headRow) { h.CreatedAt = t0.Add(time.Second) }),
				want: &journaled{Time: t0.Add(time.Second), Level: int(slog.LevelWarn), Module: m5.Module, Subsystem: "organizer", Message: "Year One indexed"}},
			{name: "error", key: syslogKey(t0.Add(2*time.Second), "organizer"), val: row("error", func(h *headRow) { h.CreatedAt = t0.Add(2 * time.Second) }),
				want: &journaled{Time: t0.Add(2 * time.Second), Level: int(slog.LevelError), Module: m5.Module, Subsystem: "organizer", Message: "Year One indexed"}},
			{name: "user_id_becomes_an_attribute", key: syslogKey(t0.Add(3*time.Second), "dedup"),
				val:  row("info", func(h *headRow) { h.CreatedAt = t0.Add(3 * time.Second); h.Source = "dedup"; h.UserID = "bruce.wayne" }),
				want: &journaled{Time: t0.Add(3 * time.Second), Level: int(slog.LevelInfo), Module: m5.Module, Subsystem: "dedup", Message: "Year One indexed", UserID: "bruce.wayne"}},
			{name: "source_with_a_colon", key: syslogKey(t0.Add(4*time.Second), "dedup:v2"),
				val:  row("info", func(h *headRow) { h.CreatedAt = t0.Add(4 * time.Second); h.Source = "dedup:v2" }),
				want: &journaled{Time: t0.Add(4 * time.Second), Level: int(slog.LevelInfo), Module: m5.Module, Subsystem: "dedup:v2", Message: "Year One indexed"}},
			{name: "created_at_in_another_zone_is_the_same_instant", key: syslogKey(t0.Add(5*time.Second).In(est), "organizer"),
				val:  row("info", func(h *headRow) { h.CreatedAt = t0.Add(5 * time.Second).In(est) }),
				want: &journaled{Time: t0.Add(5 * time.Second), Level: int(slog.LevelInfo), Module: m5.Module, Subsystem: "organizer", Message: "Year One indexed"}},
			{name: "zero_created_at_falls_back_to_the_key_time_utc", key: syslogKey(t0.Add(6*time.Second), "scanner"),
				val:  row("info", func(h *headRow) { h.CreatedAt = time.Time{}; h.Source = "scanner" }),
				want: &journaled{Time: t0.Add(6 * time.Second), Level: int(slog.LevelInfo), Module: m5.Module, Subsystem: "scanner", Message: "Year One indexed"}},
			{name: "zero_created_at_falls_back_to_a_local_zone_key", key: syslogKey(t0.Add(7*time.Second).In(est), "scanner:v2"),
				val:  row("info", func(h *headRow) { h.CreatedAt = time.Time{}; h.Source = "scanner:v2" }),
				want: &journaled{Time: t0.Add(7 * time.Second), Level: int(slog.LevelInfo), Module: m5.Module, Subsystem: "scanner:v2", Message: "Year One indexed"}},
			{name: "pre_1970_created_at_falls_back_to_the_key_time", key: syslogKey(t0.Add(8*time.Second), "scanner"),
				val:  row("info", func(h *headRow) { h.CreatedAt = time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC); h.Source = "scanner" }),
				want: &journaled{Time: t0.Add(8 * time.Second), Level: int(slog.LevelInfo), Module: m5.Module, Subsystem: "scanner", Message: "Year One indexed"}},
			{name: "no_usable_time_anywhere", key: "syslog:not-a-time:scanner",
				val: row("info", func(h *headRow) { h.CreatedAt = time.Time{} }), reason: "has no usable time"},
			{name: "key_without_a_source_separator", key: "syslog:" + t0.Add(9*time.Second).Format(time.RFC3339Nano),
				val: row("info", func(h *headRow) { h.CreatedAt = time.Time{} }), reason: "has no usable time"},
			{name: "level_head_never_wrote", key: syslogKey(t0.Add(10*time.Second), "organizer"),
				val: row("debug", func(h *headRow) { h.CreatedAt = t0.Add(10 * time.Second) }), reason: "level"},
			{name: "empty_level", key: syslogKey(t0.Add(11*time.Second), "organizer"),
				val: row("", func(h *headRow) { h.CreatedAt = t0.Add(11 * time.Second) }), reason: "level"},
			{name: "upper_case_level", key: syslogKey(t0.Add(12*time.Second), "organizer"),
				val: row("INFO", func(h *headRow) { h.CreatedAt = t0.Add(12 * time.Second) }), reason: "level"},
			{name: "not_json", key: syslogKey(t0.Add(13*time.Second), "organizer"), val: []byte("Knightfall\x00"), reason: "does not decode"},
			{name: "wrong_json_type", key: syslogKey(t0.Add(14*time.Second), "organizer"), val: []byte(`{"level":["info"]}`), reason: "does not decode"},
		}
		rows := map[string][]byte{}
		for _, c := range cases {
			rows[c.key] = c.val
		}
		if len(rows) != len(cases) {
			t.Fatal("two cases share a key")
		}
		r := newRig(t, "", withRows(rows))
		s, err := r.run(t)
		if err != nil {
			t.Fatalf("boot: %v\n%s", err, r.logs)
		}
		if len(s.Left) != 0 {
			t.Errorf("%d syslog: rows left", len(s.Left))
		}
		byID := map[string]journaled{}
		for _, j := range s.Journal {
			if _, dup := byID[j.SourceID]; dup {
				t.Errorf("%s is journaled twice", j.SourceID)
			}
			byID[j.SourceID] = j
		}
		quar := map[string]quarantined{}
		for _, q := range s.Quarantine {
			quar[q.Key] = q
		}
		wantJournal, wantQuarantine := 0, 0
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				j, imported := byID[c.key]
				q, quarantinedRow := quar[c.key]
				if c.want != nil {
					wantJournal++
					if !imported || quarantinedRow {
						t.Fatalf("imported=%v quarantined=%v, want it imported", imported, quarantinedRow)
					}
					got := j
					got.SourceID = ""
					wantRec := *c.want
					if !got.Time.Equal(wantRec.Time) {
						t.Errorf("time = %s, want %s", got.Time, wantRec.Time)
					}
					got.Time, wantRec.Time = time.Time{}, time.Time{}
					if got != wantRec {
						t.Errorf("record = %+v, want %+v", got, wantRec)
					}
					return
				}
				wantQuarantine++
				if imported || !quarantinedRow {
					t.Fatalf("imported=%v quarantined=%v, want it quarantined", imported, quarantinedRow)
				}
				if !strings.Contains(q.Reason, c.reason) {
					t.Errorf("reason %q, want it to contain %q", q.Reason, c.reason)
				}
				if q.Value != string(c.val) {
					t.Errorf("the quarantined bytes are not the row's own")
				}
			})
		}
		if len(s.Journal) != wantJournal || len(s.Quarantine) != wantQuarantine {
			t.Errorf("journal %d quarantine %d, want %d and %d", len(s.Journal), len(s.Quarantine), wantJournal, wantQuarantine)
		}
		if v := s.Verify; v.Read != int64(len(cases)) || v.Written != int64(wantJournal) || v.Quarantined != int64(wantQuarantine) || len(v.Failures) != 0 {
			t.Errorf("verify = %+v, want read=%d written=%d quarantined=%d", v, len(cases), wantJournal, wantQuarantine)
		}
	})

	t.Run("one_record_per_row", func(t *testing.T) {
		const n = 300
		rows := synth(t, n, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
		r := newRig(t, "", withRows(rows))
		s, err := r.run(t)
		if err != nil {
			t.Fatalf("boot: %v\n%s", err, r.logs)
		}
		if len(s.Left) != 0 || len(s.Quarantine) != 0 {
			t.Fatalf("left %d, quarantined %d, want none", len(s.Left), len(s.Quarantine))
		}
		if len(s.Journal) != n {
			t.Fatalf("%d journal records, want %d", len(s.Journal), n)
		}
		seen := map[string]int{}
		for _, j := range s.Journal {
			seen[j.SourceID]++
		}
		for k := range rows {
			if seen[k] != 1 {
				t.Errorf("%s journaled %d times, want once", k, seen[k])
			}
			if got := r.j.callsFor(k); got != 1 {
				t.Errorf("%s imported %d times in one clean boot, want once", k, got)
			}
		}
		if v := s.Verify; v.Read != n || v.Written != n || v.Quarantined != 0 {
			t.Errorf("verify = %+v, want %d read and written", v, n)
		}
	})

	t.Run("an_empty_log_is_a_clean_no_op", func(t *testing.T) {
		r := newRig(t, "")
		s, err := r.run(t)
		if err != nil {
			t.Fatalf("boot: %v\n%s", err, r.logs)
		}
		if s.State != "done" || len(s.Journal) != 0 || len(s.Left) != 0 || r.j.totalCalls() != 0 {
			t.Errorf("state %q, journal %d, left %d, import calls %d", s.State, len(s.Journal), len(s.Left), r.j.totalCalls())
		}
	})

	t.Run("an_importer_error_stops_the_boot_and_keeps_the_rows", func(t *testing.T) {
		rows := synth(t, 6, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
		keys := make([]string, 0, len(rows))
		for k := range rows {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		poisoned := keys[3]
		refuse := errors.New("the journal refused the record")
		r := newRig(t, "", withRows(rows), func(r *rig) {
			r.j.failFor = map[string]error{poisoned: refuse}
			r.parallelism = func(dm *datamigrate.DataMigration) { dm.Parallelism = fixedOne() }
		})
		mem := r.seed(t)
		_, err := r.runOn(t, mem)
		if !errors.Is(err, refuse) {
			t.Fatalf("boot error = %v, want the importer's", err)
		}
		if strings.Contains(err.Error(), "Year One") {
			t.Errorf("the error quotes a row's message: %v", err)
		}
		// The boot stopped with the poisoned row (and everything after it) still
		// in the store; the importer recovers, and the next boot finishes the job.
		r.j.mu.Lock()
		r.j.failFor = nil
		r.j.mu.Unlock()
		s, err := r.runOn(t, mem)
		if err != nil {
			t.Fatalf("second boot: %v\n%s", err, r.logs)
		}
		if len(s.Left) != 0 || len(s.Journal) != len(rows) || len(s.Quarantine) != 0 || s.State != "done" {
			t.Errorf("left %d journal %d quarantine %d state %q, want 0, %d, 0, done", len(s.Left), len(s.Journal), len(s.Quarantine), s.State, len(rows))
		}
	})

	t.Run("resume_after_a_crash_between_the_import_and_the_delete", func(t *testing.T) {
		rows := synth(t, 8, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC))
		keys := make([]string, 0, len(rows))
		for k := range rows {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		r := newRig(t, "", withRows(rows), func(r *rig) {
			r.parallelism = func(dm *datamigrate.DataMigration) { dm.Parallelism = fixedOne() }
		})
		mem := r.seed(t)
		dm := r.migration()
		inner := dm.Step
		var calls atomic.Int64
		dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			err := inner(ctx, kv, w)
			if err == nil && calls.Add(1) == 4 {
				return errKilled // the 4th row is imported; its delete never commits
			}
			return err
		}
		if _, err := r.runWith(t, mem, dm); !errors.Is(err, errKilled) {
			t.Fatalf("first boot error = %v, want the injected kill", err)
		}
		if got := r.j.callsFor(keys[3]); got != 1 {
			t.Fatalf("the killed row was imported %d times before the crash, want 1", got)
		}
		s, err := r.runOn(t, mem)
		if err != nil {
			t.Fatalf("resumed boot: %v\n%s", err, r.logs)
		}
		if len(s.Left) != 0 || len(s.Journal) != len(rows) || s.State != "done" {
			t.Errorf("left %d journal %d state %q, want 0, %d, done", len(s.Left), len(s.Journal), s.State, len(rows))
		}
		for _, k := range keys {
			want := 1
			if k == keys[3] {
				want = 2 // imported before the crash and again after it, onto the same key
			}
			if got := r.j.callsFor(k); got != want {
				t.Errorf("%s: %d Import calls, want %d", k, got, want)
			}
		}
		seen := map[string]int{}
		for _, j := range s.Journal {
			seen[j.SourceID]++
		}
		for _, k := range keys {
			if seen[k] != 1 {
				t.Errorf("%s has %d journal records, want 1", k, seen[k])
			}
		}
	})

	t.Run("verify_names_a_row_left_behind", func(t *testing.T) {
		var report datamigrate.VerifyReport
		var verr error
		r := newRig(t, "", func(r *rig) {
			r.mutate = func(ctx context.Context, t testing.TB, lr storage.LegacyRange) {
				if err := lr.Set(ctx, []byte(syslogKey(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), "scanner")), []byte("{}")); err != nil {
					t.Fatal(err)
				}
				report, verr = r.migration().Verify(ctx, lr)
			}
		})
		r.seed(t)
		if verr != nil {
			t.Fatal(verr)
		}
		if len(report.Failures) == 0 || !strings.Contains(strings.Join(report.Failures, "\n"), "still present") {
			t.Errorf("verify over a store that still holds a syslog: row reported %+v, want a failure naming it", report)
		}
	})

	t.Run("declaration", func(t *testing.T) {
		dm := newRig(t, "").migration()
		if dm.ID != cutover.LogCutoverID || dm.Owner != cutover.BooksOwner {
			t.Errorf("id %q owner %q, want %q and %q", dm.ID, dm.Owner, cutover.LogCutoverID, cutover.BooksOwner)
		}
		if !dm.Cutover || dm.Mode != datamigrate.BootBlocking || dm.Compat != storage.Breaking || dm.RaisesLevel != cutover.BooksCutoverLevel {
			t.Errorf("declaration = cutover %v mode %v compat %v level %d", dm.Cutover, dm.Mode, dm.Compat, dm.RaisesLevel)
		}
		if len(dm.Source) != 1 || string(dm.Source[0].Start) != m5.Prefix || string(dm.Source[0].End) != "syslog;" {
			t.Errorf("source = %q, want exactly the syslog: prefix range", dm.Source)
		}
		if _, err := m5.New(m5.Config{}, cutover.LogCutoverID, 1, []storage.StepRef{cutover.BooksLevelStepRef(1)}); err == nil {
			t.Error("New without an Importer succeeded")
		}
	})
}

// checkJournal compares every journal record with the HEAD row it came from.
func checkJournal(t *testing.T, s *snap, want map[string]headRow, levelOf map[string]slog.Level) {
	t.Helper()
	got := map[string]journaled{}
	for _, j := range s.Journal {
		got[j.SourceID] = j
	}
	for key, row := range want {
		j, ok := got[key]
		if !ok {
			t.Errorf("row %s was not journaled", key)
			continue
		}
		if !j.Time.Equal(row.CreatedAt) {
			t.Errorf("%s: time = %s, want created_at %s", key, j.Time, row.CreatedAt)
		}
		if j.Level != int(levelOf[row.Level]) {
			t.Errorf("%s: level = %d, want %q", key, j.Level, row.Level)
		}
		if j.Module != m5.Module || j.Subsystem != row.Source || j.Message != row.Message || j.UserID != row.UserID {
			t.Errorf("%s: record = %+v, want module %s, subsystem %q, message %q, user_id %q", key, j, m5.Module, row.Source, row.Message, row.UserID)
		}
	}
}
