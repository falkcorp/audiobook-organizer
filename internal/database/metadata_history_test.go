// file: internal/database/metadata_history_test.go
// version: 1.2.0
// guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-10-14

package database

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// setupTestDBWithMigrations creates a temp SQLite DB and runs migrations.
func setupTestDBWithMigrations(t *testing.T) (Store, func()) {
	t.Helper()
	store, cleanup := setupTestDB(t)
	if err := RunMigrations(store); err != nil {
		cleanup()
		t.Fatalf("RunMigrations failed: %v", err)
	}
	return store, cleanup
}

func TestRecordMetadataChange_Basic(t *testing.T) {
	store, cleanup := setupTestDBWithMigrations(t)
	defer cleanup()

	prev := `"Old Title"`
	next := `"New Title"`
	now := time.Now()

	record := &MetadataChangeRecord{
		BookID:        "book-abc-123",
		Field:         "title",
		PreviousValue: &prev,
		NewValue:      &next,
		ChangeType:    "override",
		Source:        "manual",
		ChangedAt:     now,
	}

	err := store.RecordMetadataChange(record)
	if err != nil {
		t.Fatalf("RecordMetadataChange failed: %v", err)
	}

	records, err := store.GetMetadataChangeHistory("book-abc-123", "title", 10)
	if err != nil {
		t.Fatalf("GetMetadataChangeHistory failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	r := records[0]
	if r.BookID != "book-abc-123" {
		t.Errorf("expected book_id 'book-abc-123', got %q", r.BookID)
	}
	if r.Field != "title" {
		t.Errorf("expected field 'title', got %q", r.Field)
	}
	if r.PreviousValue == nil || *r.PreviousValue != prev {
		t.Errorf("expected previous_value %q, got %v", prev, r.PreviousValue)
	}
	if r.NewValue == nil || *r.NewValue != next {
		t.Errorf("expected new_value %q, got %v", next, r.NewValue)
	}
	if r.ChangeType != "override" {
		t.Errorf("expected change_type 'override', got %q", r.ChangeType)
	}
	if r.Source != "manual" {
		t.Errorf("expected source 'manual', got %q", r.Source)
	}
	if r.ID == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestGetMetadataChangeHistory_OrderedByNewest(t *testing.T) {
	store, cleanup := setupTestDBWithMigrations(t)
	defer cleanup()

	base := time.Now().Add(-3 * time.Hour)
	for i := range 3 {
		val := `"value` + string(rune('A'+i)) + `"`
		record := &MetadataChangeRecord{
			BookID:     "book-order",
			Field:      "title",
			NewValue:   &val,
			ChangeType: "override",
			ChangedAt:  base.Add(time.Duration(i) * time.Hour),
		}
		if err := store.RecordMetadataChange(record); err != nil {
			t.Fatalf("RecordMetadataChange #%d failed: %v", i, err)
		}
	}

	records, err := store.GetMetadataChangeHistory("book-order", "title", 10)
	if err != nil {
		t.Fatalf("GetMetadataChangeHistory failed: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}

	// Newest first: records[0] should have the latest changed_at
	for i := 1; i < len(records); i++ {
		if records[i].ChangedAt.After(records[i-1].ChangedAt) {
			t.Errorf("records not ordered newest first: record[%d] (%v) is after record[%d] (%v)",
				i, records[i].ChangedAt, i-1, records[i-1].ChangedAt)
		}
	}
}

func TestGetMetadataChangeHistory_FiltersByField(t *testing.T) {
	store, cleanup := setupTestDBWithMigrations(t)
	defer cleanup()

	now := time.Now()
	fields := []string{"title", "author_name", "title"}
	for i, field := range fields {
		val := `"val` + string(rune('0'+i)) + `"`
		record := &MetadataChangeRecord{
			BookID:     "book-filter",
			Field:      field,
			NewValue:   &val,
			ChangeType: "override",
			ChangedAt:  now.Add(time.Duration(i) * time.Second),
		}
		if err := store.RecordMetadataChange(record); err != nil {
			t.Fatalf("RecordMetadataChange #%d failed: %v", i, err)
		}
	}

	titleRecords, err := store.GetMetadataChangeHistory("book-filter", "title", 10)
	if err != nil {
		t.Fatalf("GetMetadataChangeHistory(title) failed: %v", err)
	}
	if len(titleRecords) != 2 {
		t.Errorf("expected 2 title records, got %d", len(titleRecords))
	}
	for _, r := range titleRecords {
		if r.Field != "title" {
			t.Errorf("expected field 'title', got %q", r.Field)
		}
	}

	authorRecords, err := store.GetMetadataChangeHistory("book-filter", "author_name", 10)
	if err != nil {
		t.Fatalf("GetMetadataChangeHistory(author_name) failed: %v", err)
	}
	if len(authorRecords) != 1 {
		t.Errorf("expected 1 author_name record, got %d", len(authorRecords))
	}
}

func TestGetBookChangeHistory_AllFields(t *testing.T) {
	store, cleanup := setupTestDBWithMigrations(t)
	defer cleanup()

	now := time.Now()
	fields := []string{"title", "author_name", "narrator"}
	for i, field := range fields {
		val := `"val` + string(rune('0'+i)) + `"`
		record := &MetadataChangeRecord{
			BookID:     "book-all",
			Field:      field,
			NewValue:   &val,
			ChangeType: "fetched",
			ChangedAt:  now.Add(time.Duration(i) * time.Second),
		}
		if err := store.RecordMetadataChange(record); err != nil {
			t.Fatalf("RecordMetadataChange #%d failed: %v", i, err)
		}
	}

	records, err := store.GetBookChangeHistory("book-all", 100)
	if err != nil {
		t.Fatalf("GetBookChangeHistory failed: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records across all fields, got %d", len(records))
	}

	foundFields := map[string]bool{}
	for _, r := range records {
		foundFields[r.Field] = true
	}
	for _, f := range fields {
		if !foundFields[f] {
			t.Errorf("expected field %q in results", f)
		}
	}
}

func TestGetBookChangeHistory_LimitRespected(t *testing.T) {
	store, cleanup := setupTestDBWithMigrations(t)
	defer cleanup()

	now := time.Now()
	for i := range 10 {
		val := `"val` + string(rune('0'+i)) + `"`
		record := &MetadataChangeRecord{
			BookID:     "book-limit",
			Field:      "title",
			NewValue:   &val,
			ChangeType: "override",
			ChangedAt:  now.Add(time.Duration(i) * time.Second),
		}
		if err := store.RecordMetadataChange(record); err != nil {
			t.Fatalf("RecordMetadataChange #%d failed: %v", i, err)
		}
	}

	records, err := store.GetBookChangeHistory("book-limit", 3)
	if err != nil {
		t.Fatalf("GetBookChangeHistory failed: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records with limit=3, got %d", len(records))
	}
}

// 05-PR5b: the Fixer resume evidence check (opswriter.Writer.RowApplied)
// reads a book's WHOLE metadata history through ReadBookChangeHistory. It
// must return every row, however many there are and however their fields
// sort (the "author_id" row sorts before every "title" one), where a capped
// GetBookChangeHistory keeps only limit rows; which ones it keeps is not
// pinned here. A row that does not decode is counted, not returned, so the
// caller can refuse to judge a partial history; GetBookChangeHistory still
// drops it silently.
func TestReadBookChangeHistory_CompleteAndCountsUndecodable(t *testing.T) {
	store, cleanup := setupTestDBWithMigrations(t)
	defer cleanup()
	p, ok := store.(*PebbleStore)
	if !ok {
		t.Fatalf("store is %T, not *PebbleStore", store)
	}

	base := time.Now().Add(-time.Hour)
	const titles = 149
	for i := range titles {
		val := `"t"`
		if err := store.RecordMetadataChange(&MetadataChangeRecord{BookID: "book-many", Field: "title",
			NewValue: &val, ChangeType: "override", BatchID: "other-batch",
			ChangedAt: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("RecordMetadataChange #%d: %v", i, err)
		}
	}
	val := `"7"`
	if err := store.RecordMetadataChange(&MetadataChangeRecord{BookID: "book-many", Field: "author_id",
		NewValue: &val, ChangeType: "ops_v3", BatchID: "op-run-batch",
		ChangedAt: base.Add(time.Duration(titles) * time.Second)}); err != nil {
		t.Fatal(err)
	}
	hasRunBatch := func(rows []MetadataChangeRecord) bool {
		return slices.ContainsFunc(rows, func(r MetadataChangeRecord) bool { return r.BatchID == "op-run-batch" })
	}

	capped, err := store.GetBookChangeHistory("book-many", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != 100 {
		t.Fatalf("limit 100 returned %d rows", len(capped))
	}
	all, bad, err := p.ReadBookChangeHistory("book-many")
	if err != nil || bad != 0 {
		t.Fatalf("ReadBookChangeHistory: %d undecodable, %v", bad, err)
	}
	if len(all) != titles+1 || !hasRunBatch(all) {
		t.Fatalf("ReadBookChangeHistory: %d rows (want %d), run batch present=%v", len(all), titles+1, hasRunBatch(all))
	}

	// One undecodable row of the book, and one of another book.
	for _, k := range []string{"metadata_change:book-many:publisher:1", "metadata_change:book-other:title:1"} {
		if err := p.db.Set([]byte(k), []byte("{not json"), pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	all, bad, err = p.ReadBookChangeHistory("book-many")
	if err != nil || bad != 1 || len(all) != titles+1 {
		t.Fatalf("with a bad row: %d rows, %d undecodable, %v; want %d, 1, nil", len(all), bad, err, titles+1)
	}
	if got, err := store.GetBookChangeHistory("book-many", math.MaxInt); err != nil || len(got) != titles+1 {
		t.Fatalf("GetBookChangeHistory with a bad row: %d rows, %v; want %d (dropped silently)", len(got), err, titles+1)
	}
}

func TestGetMetadataChangeHistory_EmptyResult(t *testing.T) {
	store, cleanup := setupTestDBWithMigrations(t)
	defer cleanup()

	records, err := store.GetMetadataChangeHistory("nonexistent-book", "title", 10)
	if err != nil {
		t.Fatalf("GetMetadataChangeHistory failed: %v", err)
	}
	if records == nil {
		// nil is acceptable as empty
		records = []MetadataChangeRecord{}
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records for nonexistent book, got %d", len(records))
	}
}
