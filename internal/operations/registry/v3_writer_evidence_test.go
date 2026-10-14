// file: internal/operations/registry/v3_writer_evidence_test.go
// version: 1.0.0
// guid: 3ebbf9a7-49e9-4226-b8b5-535c23956b47
// last-edited: 2026-10-14

package registry

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// noEvidenceStore is a v3-capable store whose two history reads are
// shadowed by fields of the same name, so neither opswriter.JournalReader
// nor opswriter.BookHistoryReader resolves from it.
type noEvidenceStore struct {
	*database.PebbleStore
	GetOperationChanges  struct{}
	GetBookChangeHistory struct{}
}

// 05-PR5b: the Writer newV3Writer builds reads the run's own history out of
// a real store -- its metadata history batches ("op-<opID>-<ulid>") and its
// operation journal rows -- and nobody else's. A store without those reads
// still gets a Live Writer (no error); it just never finds evidence, so a
// resumed Fixer skips a row as changed since plan instead of claiming it.
func TestV3WriterReadsThisRunsHistory(t *testing.T) {
	p := newPebbleV3(t)
	ctx := context.Background()
	earlier := time.Now().Add(-time.Hour)
	record := func(bookID, batchID string) {
		t.Helper()
		v := `"x"`
		if err := p.RecordMetadataChange(&database.MetadataChangeRecord{BookID: bookID, Field: "title",
			PreviousValue: &v, NewValue: &v, ChangeType: historyChangeTypeV3, Source: "test.fixer",
			ChangedAt: earlier, BatchID: batchID}); err != nil {
			t.Fatal(err)
		}
	}
	journal := func(opID, bookID, field string) {
		t.Helper()
		if err := p.CreateOperationChange(&database.OperationChange{OperationID: opID, BookID: bookID,
			ChangeType: database.ChangeTypeBookFileRepoint, FieldName: field}); err != nil {
			t.Fatal(err)
		}
	}
	// The earlier attempt of op-a, and another op's rows.
	record("book-001", "op-op-a-"+ulid.Make().String())
	journal("op-a", "book-002", "book_file:file-002")
	record("book-003", "op-op-b-"+ulid.Make().String())
	journal("op-b", "book-004", "book_file:file-004")
	// The store stamps journal rows with its own clock; the resumed
	// attempt's Writer must start strictly after them.
	time.Sleep(5 * time.Millisecond)

	r := NewWithOptions(p, slog.Default(), 1, Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
	w, err := r.newV3Writer(ctx, "op-a", "test.fixer", ops.Live)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		subjects []ops.Subject
		want     bool
	}{
		{ops.Books("book-001"), true},
		{ops.Books("book-002"), true},
		{ops.BookFiles("file-002"), true},
		{ops.Books("book-003"), false},
		{ops.Books("book-004"), false},
		{ops.BookFiles("file-004"), false},
	} {
		if got, err := w.Wrote(ctx, c.subjects); err != nil || got != c.want {
			t.Errorf("Wrote(%v) = %v, %v; want %v", c.subjects, got, err, c.want)
		}
	}

	bare := NewWithOptions(noEvidenceStore{PebbleStore: p}, slog.Default(), 1,
		Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
	w2, err := bare.newV3Writer(ctx, "op-a", "test.fixer", ops.Live)
	if err != nil {
		t.Fatalf("a store without the history reads must still back a Live Writer: %v", err)
	}
	if got, err := w2.Wrote(ctx, ops.Books("book-001")); err != nil || got {
		t.Fatalf("Wrote without the reads = %v, %v; want false, nil", got, err)
	}
}
