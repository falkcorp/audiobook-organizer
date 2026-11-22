// file: internal/books/cutover/m5/step_nonfatal_internal_test.go
// version: 0.1.0
// guid: d9e5faf8-4036-44d1-bd7e-33927916f934
// last-edited: 2026-11-22

package m5

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// M5 never blocks the boot (owner ruling, 2026-11-22). These tests run Step
// over a fake chunk, so the split between "a failure about one row" (quarantined,
// the step goes on) and "the database is unhealthy" (returned, the chunk fails)
// is pinned at the call, one case per error the step can meet. The real runner's
// side of the same split is the boot cells in cutover_test.go.

// fakeChunk is a chunk's Sink and CutoverSink: it records what Step stages and
// returns the errors a test injects.
type fakeChunk struct {
	mu          sync.Mutex
	deleted     []string
	quarantined []fakeQuarantine
	counts      map[string]int
	deleteErr   error
	quarErr     error
}

type fakeQuarantine struct{ key, raw, reason string }

var (
	_ datamigrate.Sink        = (*fakeChunk)(nil)
	_ datamigrate.CutoverSink = (*fakeChunk)(nil)
)

func (c *fakeChunk) Delete(key []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deleteErr != nil {
		return c.deleteErr
	}
	c.deleted = append(c.deleted, string(key))
	return nil
}

func (c *fakeChunk) Quarantine(key, raw []byte, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quarErr != nil {
		return c.quarErr
	}
	c.quarantined = append(c.quarantined, fakeQuarantine{string(key), string(raw), reason})
	return nil
}

func (c *fakeChunk) Count(outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	c.counts[outcome]++
}

func (c *fakeChunk) Set(_, _ []byte) error         { return errors.New("unexpected Set") }
func (c *fakeChunk) SetIfAbsent(_, _ []byte) error { return errors.New("unexpected SetIfAbsent") }
func (c *fakeChunk) SetIfUnchanged(_, _, _ []byte) error {
	return errors.New("unexpected SetIfUnchanged")
}
func (c *fakeChunk) DeleteIfUnchanged(_, _ []byte) error {
	return errors.New("unexpected DeleteIfUnchanged")
}
func (c *fakeChunk) Hold(_ []byte, _ string)         {}
func (c *fakeChunk) Batch() *storage.Batch           { return nil }
func (c *fakeChunk) writer() *datamigrate.StepWriter { return datamigrate.NewStepWriter(c, c) }

// fakeImporter returns a configured error for a SourceID and records every call.
type fakeImporter struct {
	mu    sync.Mutex
	fail  map[string]error
	calls []string
}

func (f *fakeImporter) Import(_ context.Context, recs []log.ImportedRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range recs {
		f.calls = append(f.calls, r.SourceID)
		if err := f.fail[r.SourceID]; err != nil {
			return err
		}
	}
	return nil
}

// goodRow is a well-formed HEAD row at a key of the shape HEAD writes.
func goodRow(t *testing.T, at time.Time, source string) (key string, raw []byte) {
	t.Helper()
	b, err := json.Marshal(systemActivityLog{ID: 1, Source: source, Level: "info", Message: "Year One indexed", CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return Prefix + at.Format(time.RFC3339Nano) + ":" + source, b
}

func newTestMigration(t *testing.T, imp log.Importer) (*Migration, *strings.Builder) {
	t.Helper()
	var logs strings.Builder
	m := &Migration{imp: imp, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	return m, &logs
}

// TestStep_AnImportFailureQuarantinesTheRowAndTheStepGoesOn: the journal
// refuses the first row, the step quarantines it (raw bytes, a fixed reason that
// does not carry the importer's text) and returns nil; the next row imports.
func TestStep_AnImportFailureQuarantinesTheRowAndTheStepGoesOn(t *testing.T) {
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	bad, badRaw := goodRow(t, at, "scanner")
	next, nextRaw := goodRow(t, at.Add(time.Second), "organizer")
	refuse := fmt.Errorf("record 0: %w: the journal refused the record 0xBADC0DE", log.ErrImportRejected)
	imp := &fakeImporter{fail: map[string]error{bad: refuse}}
	m, logs := newTestMigration(t, imp)
	c := &fakeChunk{}
	w := c.writer()

	if err := m.Step(context.Background(), storage.KV{Key: []byte(bad), Value: badRaw}, w); err != nil {
		t.Fatalf("Step over a row the journal refused returned %v, want nil: M5 never blocks", err)
	}
	if len(c.quarantined) != 1 || c.quarantined[0].key != bad || c.quarantined[0].raw != string(badRaw) || c.quarantined[0].reason != reasonImport {
		t.Fatalf("quarantined = %+v, want the row's key and raw bytes under %q", c.quarantined, reasonImport)
	}
	if strings.Contains(c.quarantined[0].reason, "0xBADC0DE") {
		t.Errorf("the quarantine reason carries the importer's text: %q", c.quarantined[0].reason)
	}
	if len(c.deleted) != 0 || c.counts[OutcomeImported] != 0 {
		t.Errorf("the refused row was also deleted or counted as imported: deleted %v counts %v", c.deleted, c.counts)
	}
	if err := m.Step(context.Background(), storage.KV{Key: []byte(next), Value: nextRaw}, w); err != nil {
		t.Fatalf("the row after it: %v", err)
	}
	if len(c.deleted) != 1 || c.deleted[0] != next || c.counts[OutcomeImported] != 1 {
		t.Errorf("the next row was not imported and deleted: deleted %v counts %v", c.deleted, c.counts)
	}
	if got := fmt.Sprint(imp.calls); got != fmt.Sprint([]string{bad, next}) {
		t.Errorf("Import calls = %s, want both rows once", got)
	}
	if m.read.Load() != 2 || m.imported.Load() != 1 || m.quarantined.Load() != 1 {
		t.Errorf("counts read %d imported %d quarantined %d, want 2, 1, 1", m.read.Load(), m.imported.Load(), m.quarantined.Load())
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, `msg="m5: a system log row is quarantined because the journal did not accept it"`) || !strings.Contains(out, bad) {
		t.Errorf("no m5: WARN naming the row: %s", out)
	}
	if strings.Contains(out, "Year One") {
		t.Errorf("the log quotes a row's message: %s", out)
	}
}

// TestStep_TheStoreBeingUnhealthyStaysFatal: every error that is the database's
// is returned and nothing is quarantined for it. A swallowed one would let a boot
// finish over a store that is failing. Only an Import error that matches
// log.ErrImportRejected is a verdict on the row: every other Import error is the
// store's or the context's, including the ones a store defect repeats for every
// row (a read-only or misbound handle, a batch limit, a commit failure).
func TestStep_TheStoreBeingUnhealthyStaysFatal(t *testing.T) {
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	key, raw := goodRow(t, at, "scanner")
	boom := errors.New("the store failed")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()

	cases := []struct {
		name       string
		ctx        context.Context
		importErr  error
		quarErr    error
		deleteErr  error
		row        []byte
		wantErr    error
		wantQuar   int
		wantImport bool
	}{
		{name: "the_quarantine_of_a_refused_row_fails", ctx: live, importErr: fmt.Errorf("record 0: %w", log.ErrImportRejected), quarErr: boom, wantErr: boom, wantImport: true},
		{name: "the_quarantine_of_an_undecodable_row_fails", ctx: live, row: []byte("not json"), quarErr: boom, wantErr: boom},
		{name: "the_delete_of_an_imported_row_fails", ctx: live, deleteErr: boom, wantErr: boom, wantImport: true},
		{name: "the_batch_was_ended_under_the_delete", ctx: live, deleteErr: storage.ErrBatchDone, wantErr: storage.ErrBatchDone, wantImport: true},
		{name: "an_import_store_error_that_is_not_a_refusal", ctx: live, importErr: boom, wantErr: boom, wantImport: true},
		{name: "the_import_handle_is_read_only", ctx: live, importErr: fmt.Errorf("journal batch: %w", storage.ErrReadOnly), wantErr: storage.ErrReadOnly, wantImport: true},
		{name: "the_import_batch_is_too_large", ctx: live, importErr: fmt.Errorf("commit: %w", storage.ErrBatchTooLarge), wantErr: storage.ErrBatchTooLarge, wantImport: true},
		{name: "the_import_met_an_ended_batch", ctx: live, importErr: fmt.Errorf("commit: %w", storage.ErrBatchDone), wantErr: storage.ErrBatchDone, wantImport: true},
		{name: "the_boot_is_cancelled", ctx: canceled, importErr: errors.New("the importer stopped"), wantImport: true}, // the cause is the runner's to report: any error will do
		{name: "the_importer_returns_a_cancel", ctx: live, importErr: context.Canceled, wantErr: context.Canceled, wantImport: true},
		{name: "the_importer_returns_a_deadline", ctx: live, importErr: context.DeadlineExceeded, wantErr: context.DeadlineExceeded, wantImport: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rowRaw := raw
			if c.row != nil {
				rowRaw = c.row
			}
			imp := &fakeImporter{fail: map[string]error{key: c.importErr}}
			m, _ := newTestMigration(t, imp)
			ch := &fakeChunk{quarErr: c.quarErr, deleteErr: c.deleteErr}
			err := m.Step(c.ctx, storage.KV{Key: []byte(key), Value: rowRaw}, ch.writer())
			if err == nil {
				t.Fatal("Step returned nil, want the store's error")
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Errorf("Step error = %v, want %v", err, c.wantErr)
			}
			if len(ch.quarantined) != c.wantQuar {
				t.Errorf("quarantined %v for a store error, want %d", ch.quarantined, c.wantQuar)
			}
			if m.quarantined.Load() != 0 {
				t.Errorf("counted %d quarantined for a store error", m.quarantined.Load())
			}
			if (len(imp.calls) > 0) != c.wantImport {
				t.Errorf("Import calls %v, want called: %v", imp.calls, c.wantImport)
			}
		})
	}
}
