// file: internal/activity/api_test.go
// version: 1.1.1
// guid: b2e7f4a1-6c9d-4e3b-8f0a-1d5c7e2b9f4a

package activity

import (
	"strings"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// newTestWriter builds a Writer that writes to an in-memory channel only
// (no real ActivityStore needed). The batcher drains into the same channel.
func newTestWriter(chanSize int) *Writer {
	ch := make(chan database.ActivityEntry, chanSize)
	return &Writer{
		ch:      ch,
		batcher: NewActivityBatcher(ch),
		// stdout and store intentionally nil for unit tests — no I/O needed.
	}
}

// TestLogBatch_NilWriter verifies that passing a nil Writer does not panic.
func TestLogBatch_NilWriter(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("LogBatch(nil, ...) panicked: %v", r)
		}
	}()
	LogBatch(nil, "op1", "tag-scan", "scanner", BatchItem{Name: "book.m4b", Count: 1})
}

// TestLogBatch_EmptyOperationID verifies that an empty operationID causes the
// item to fall through to a plain debug ActivityEntry on the channel (not batched).
func TestLogBatch_EmptyOperationID(t *testing.T) {
	w := newTestWriter(16)

	LogBatch(w, "", "tag-scan", "scanner", BatchItem{Name: "book.m4b", Count: 1})

	select {
	case e := <-w.ch:
		// Should be a plain debug entry, NOT a batch entry — Details must be nil.
		if e.Details != nil {
			t.Errorf("expected nil Details for plain fallback entry, got %v", e.Details)
		}
		if e.Tier != "debug" {
			t.Errorf("expected Tier=debug, got %q", e.Tier)
		}
		if e.Summary != "book.m4b" {
			t.Errorf("expected Summary=book.m4b, got %q", e.Summary)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected a plain debug entry on the channel, got none")
	}
}

// TestLogBatch_UnregisteredType verifies that an unregistered batch type also
// falls through to a plain debug ActivityEntry on the channel.
func TestLogBatch_UnregisteredType(t *testing.T) {
	w := newTestWriter(16)

	LogBatch(w, "op1", "unknown-type", "scanner", BatchItem{Name: "book.m4b", Count: 1})

	select {
	case e := <-w.ch:
		if e.Details != nil {
			t.Errorf("expected nil Details for plain fallback entry, got %v", e.Details)
		}
		if e.Type != "unknown-type" {
			t.Errorf("expected Type=unknown-type, got %q", e.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected a plain debug entry on the channel, got none")
	}
}

// TestLogBatch_ValidBatch verifies that a valid operationID + registered type
// routes through the batcher and produces a batched entry with Details["batched"]==true.
func TestLogBatch_ValidBatch(t *testing.T) {
	w := newTestWriter(16)

	LogBatch(w, "op-valid", "tag-scan", "scanner", BatchItem{Name: "book.m4b", Count: 1})

	// After Submit the item should be pending, not yet on the channel.
	w.batcher.mu.Lock()
	key := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op-valid"}
	_, ok := w.batcher.pending[key]
	w.batcher.mu.Unlock()
	if !ok {
		t.Fatal("expected 1 pending batch entry after LogBatch, found none")
	}

	// FlushAll should send the merged entry to the channel.
	w.batcher.FlushAll()

	select {
	case e := <-w.ch:
		if e.Details == nil {
			t.Fatal("expected non-nil Details after flush")
		}
		batched, ok := e.Details["batched"]
		if !ok {
			t.Fatal("expected 'batched' key in Details")
		}
		if batched != true {
			t.Errorf("expected batched=true, got %v", batched)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected a batched entry on the channel after FlushAll, got none")
	}
}

// TestFlushOperation_NilWriter verifies that passing a nil Writer does not panic.
func TestFlushOperation_NilWriter(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("FlushOperation(nil, ...) panicked: %v", r)
		}
	}()
	FlushOperation(nil, "op1")
}

// TestFlushOperation_OnlyFlushesMatchingOp verifies that FlushOperation only
// flushes batches whose OperationID matches, leaving other ops pending.
func TestFlushOperation_OnlyFlushesMatchingOp(t *testing.T) {
	w := newTestWriter(16)

	// Submit one item for op1 and one for op2.
	LogBatch(w, "op1", "tag-scan", "scanner", BatchItem{Name: "book-a.m4b", Count: 1})
	LogBatch(w, "op2", "tag-scan", "scanner", BatchItem{Name: "book-b.m4b", Count: 1})

	// Flush only op1.
	FlushOperation(w, "op1")

	// Channel should have exactly 1 entry (for op1).
	entries := drainAll(w.ch, 5, 100*time.Millisecond)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after FlushOperation(op1), got %d", len(entries))
	}
	if entries[0].OperationID != "op1" {
		t.Errorf("expected flushed entry OperationID=op1, got %q", entries[0].OperationID)
	}

	// op2 should still be pending.
	w.batcher.mu.Lock()
	key2 := BatchKey{Type: "tag-scan", Source: "scanner", OperationID: "op2"}
	_, stillPending := w.batcher.pending[key2]
	w.batcher.mu.Unlock()
	if !stillPending {
		t.Error("expected op2 to remain pending after FlushOperation(op1)")
	}
}

// TestEnrichTags verifies that EnrichTags correctly derives and appends tags.
func TestEnrichTags(t *testing.T) {
	tests := []struct {
		name     string
		entry    database.ActivityEntry
		wantTags []string
	}{
		{
			name: "op tag from OperationID",
			entry: database.ActivityEntry{
				OperationID: "op-123",
				Level:       "info",
				Source:      "scanner",
				Type:        "scan",
			},
			wantTags: []string{"op:op-123", "outcome:ok", "source:scanner", "action:scan", "component:scanner"},
		},
		{
			name: "book and scope tags from BookID",
			entry: database.ActivityEntry{
				BookID: "book-456",
				Level:  "info",
				Source: "metafetch",
				Type:   "metadata-apply",
			},
			wantTags: []string{"book:book-456", "scope:book", "outcome:ok", "source:metafetch", "action:metadata-apply"},
		},
		{
			name: "outcome:warn from warning level",
			entry: database.ActivityEntry{
				Level:  "warning",
				Source: "itunes",
				Type:   "itunes_sync",
			},
			wantTags: []string{"outcome:warn", "source:itunes", "action:import", "component:itunes_sync"},
		},
		{
			name: "outcome:error from error level",
			entry: database.ActivityEntry{
				Level:  "error",
				Source: "dedup",
				Type:   "dedup",
			},
			wantTags: []string{"outcome:error", "source:dedup", "action:dedup", "component:dedup"},
		},
		{
			name: "idempotency: existing tags not duplicated",
			entry: database.ActivityEntry{
				OperationID: "op-789",
				Level:       "info",
				Source:      "scanner",
				Type:        "scan",
				Tags:        []string{"op:op-789"}, // Already has this tag
			},
			wantTags: []string{"op:op-789", "outcome:ok", "source:scanner", "action:scan", "component:scanner"},
		},
		{
			name: "all fields populated",
			entry: database.ActivityEntry{
				OperationID: "op-full",
				BookID:      "book-789",
				Level:       "error",
				Source:      "maintenance-window",
				Type:        "maintenance-window",
			},
			wantTags: []string{"op:op-full", "book:book-789", "scope:book", "outcome:error", "source:maintenance-window", "action:maintenance"},
		},
		{
			name: "unknown type maps to no action tag",
			entry: database.ActivityEntry{
				Level:  "info",
				Source: "unknown",
				Type:   "unknown_type",
			},
			wantTags: []string{"outcome:ok", "source:unknown"},
		},
		{
			name: "system type with startup lifecycle keyword",
			entry: database.ActivityEntry{
				Level:   "info",
				Source:  "server",
				Type:    "system",
				Summary: "Activity log service initialized and recording",
			},
			wantTags: []string{"outcome:ok", "lifecycle:startup"},
		},
		{
			name: "system type with shutdown lifecycle keyword",
			entry: database.ActivityEntry{
				Level:   "warning",
				Source:  "server",
				Type:    "system",
				Summary: "HTTP server forced shutdown",
			},
			wantTags: []string{"outcome:warn", "lifecycle:shutdown"},
		},
		{
			name: "system type with connection keyword",
			entry: database.ActivityEntry{
				Level:   "info",
				Source:  "server",
				Type:    "system",
				Summary: "Client connection closed",
			},
			// "closed" wins over "connection" since shutdown is checked first
			// — both interpretations are valid for this message.
			wantTags: []string{"outcome:ok", "lifecycle:shutdown"},
		},
		{
			name: "system type with no lifecycle keyword",
			entry: database.ActivityEntry{
				Level:   "info",
				Source:  "server",
				Type:    "system",
				Summary: "Embedding backfill already complete (), skipping",
			},
			wantTags: []string{"outcome:ok"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := tt.entry
			EnrichTags(&entry)

			// Verify all expected tags are present
			tagMap := make(map[string]bool)
			for _, tag := range entry.Tags {
				tagMap[tag] = true
			}

			for _, wantTag := range tt.wantTags {
				if !tagMap[wantTag] {
					t.Errorf("expected tag %q not found in %v", wantTag, entry.Tags)
				}
			}

			// Verify no unexpected tags (exact match)
			if len(entry.Tags) != len(tt.wantTags) {
				t.Errorf("expected %d tags, got %d: %v", len(tt.wantTags), len(entry.Tags), entry.Tags)
			}
		})
	}
}

// TestEnrichTags_NilEntry verifies that EnrichTags handles nil gracefully.
func TestEnrichTags_NilEntry(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("EnrichTags(nil) panicked: %v", r)
		}
	}()
	EnrichTags(nil)
}

// TestEnrichTags_ComponentFromDetails verifies that a "component" key in
// Details produces a component: tag without disturbing other tags.
func TestEnrichTags_ComponentFromDetails(t *testing.T) {
	e := database.ActivityEntry{
		Level:   "info",
		Source:  "server",
		Type:    "system",
		Details: map[string]any{"component": "library_counts_cache"},
	}
	EnrichTags(&e)

	found := false
	for _, tag := range e.Tags {
		if tag == "component:library_counts_cache" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected component:library_counts_cache in %v", e.Tags)
	}
}

// TestEnrichTags_ComponentFromSourceMapping verifies that a well-known source
// value produces a component: tag via the sourceToComponent mapping.
func TestEnrichTags_ComponentFromSourceMapping(t *testing.T) {
	e := database.ActivityEntry{
		Level:  "info",
		Source: "scanner",
		Type:   "system",
	}
	EnrichTags(&e)

	found := false
	for _, tag := range e.Tags {
		if tag == "component:scanner" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected component:scanner in %v", e.Tags)
	}
}

// TestEnrichTags_NoComponentForGenericSource verifies that a source with no
// mapping does not produce a component: tag.
func TestEnrichTags_NoComponentForGenericSource(t *testing.T) {
	e := database.ActivityEntry{
		Level:   "info",
		Source:  "server",
		Type:    "system",
		Summary: "generic message",
	}
	EnrichTags(&e)

	for _, tag := range e.Tags {
		if strings.HasPrefix(tag, "component:") {
			t.Errorf("unexpected component tag on generic server entry: %q in %v", tag, e.Tags)
		}
	}
}

// TestEnrichTags_OpIDFromOperationID verifies that a non-empty OperationID
// produces an op: tag (this was already supported; guard against regression).
func TestEnrichTags_OpIDFromOperationID(t *testing.T) {
	e := database.ActivityEntry{
		Level:       "info",
		Source:      "scanner",
		Type:        "scan",
		OperationID: "01JTEST",
	}
	EnrichTags(&e)

	found := false
	for _, tag := range e.Tags {
		if tag == "op:01JTEST" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected op:01JTEST in %v", e.Tags)
	}
}

// TestParseLogLineFull_SlogOpID verifies that ParseLogLineFull extracts op_id
// from a slog text-format line and returns it in ParsedLogLine.OpID.
func TestParseLogLineFull_SlogOpID(t *testing.T) {
	line := `time=2026-05-20T00:00:00Z level=INFO msg="scan started" op_id=01JTEST component=acoustid`
	p := ParseLogLineFull(line)
	if p.OpID != "01JTEST" {
		t.Errorf("expected OpID=01JTEST, got %q (full: %+v)", p.OpID, p)
	}
}

// TestParseLogLineFull_SlogComponent verifies that ParseLogLineFull extracts
// the component attr from a slog text-format line.
func TestParseLogLineFull_SlogComponent(t *testing.T) {
	line := `time=2026-05-20T00:00:00Z level=INFO msg="cache hit" component=library_counts_cache total_books=100`
	p := ParseLogLineFull(line)
	if p.Component != "library_counts_cache" {
		t.Errorf("expected Component=library_counts_cache, got %q (full: %+v)", p.Component, p)
	}
}

// TestParseLogLineFull_NoOpNoComponent verifies that a plain non-slog line
// produces empty OpID and Component (no regression on existing behaviour).
func TestParseLogLineFull_NoOpNoComponent(t *testing.T) {
	line := `2026/05/20 00:00:00 main.go:42: [INFO] server: HTTP server started`
	p := ParseLogLineFull(line)
	if p.OpID != "" {
		t.Errorf("expected empty OpID for non-slog line, got %q", p.OpID)
	}
	if p.Component != "" {
		t.Errorf("expected empty Component for non-slog line, got %q", p.Component)
	}
	if p.Source != "server" {
		t.Errorf("expected source=server, got %q", p.Source)
	}
}
