// file: internal/server/writeback_queue_merge_test.go
// version: 1.0.0
// guid: 5e630d3c-5768-4e31-927a-0ab8394c703b
// last-edited: 2026-08-28

package server

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMergeBatchSaveQueuedParams_MergesMatchingModes(t *testing.T) {
	existing, _ := json.Marshal(batchSaveOpParams{
		BookIDs:  []string{"book-a", "book-b"},
		Organize: true,
		Force:    false,
	})
	incoming, _ := json.Marshal(batchSaveOpParams{
		BookIDs:  []string{"book-b", "book-c"},
		Organize: true,
		Force:    false,
	})

	raw, merged, err := mergeBatchSaveQueuedParams(existing, incoming)
	if err != nil {
		t.Fatalf("mergeBatchSaveQueuedParams() error: %v", err)
	}
	if !merged {
		t.Fatal("mergeBatchSaveQueuedParams() merged = false, want true")
	}
	var got batchSaveOpParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode merged params: %v", err)
	}
	want := batchSaveOpParams{BookIDs: []string{"book-a", "book-b", "book-c"}, Organize: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merged params = %#v, want %#v", got, want)
	}
}

func TestMergeBatchSaveQueuedParams_RefusesChangedModes(t *testing.T) {
	existing, _ := json.Marshal(batchSaveOpParams{BookIDs: []string{"book-a"}, Organize: true})
	incoming, _ := json.Marshal(batchSaveOpParams{BookIDs: []string{"book-b"}, Organize: false})

	_, merged, err := mergeBatchSaveQueuedParams(existing, incoming)
	if err != nil {
		t.Fatalf("mergeBatchSaveQueuedParams() error: %v", err)
	}
	if merged {
		t.Fatal("mergeBatchSaveQueuedParams() merged incompatible organize modes")
	}
}

func TestMergeBulkWriteBackQueuedParams_MergesMatchingRenameMode(t *testing.T) {
	existing, _ := json.Marshal(bulkWriteBackOpParams{BookIDs: []string{"book-a", "book-b"}, Rename: true})
	incoming, _ := json.Marshal(bulkWriteBackOpParams{BookIDs: []string{"book-b", "book-c"}, Rename: true})

	raw, merged, err := mergeBulkWriteBackQueuedParams(existing, incoming)
	if err != nil {
		t.Fatalf("mergeBulkWriteBackQueuedParams() error: %v", err)
	}
	if !merged {
		t.Fatal("mergeBulkWriteBackQueuedParams() merged = false, want true")
	}
	var got bulkWriteBackOpParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode merged params: %v", err)
	}
	want := bulkWriteBackOpParams{BookIDs: []string{"book-a", "book-b", "book-c"}, Rename: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merged params = %#v, want %#v", got, want)
	}
}

func TestMergeBulkWriteBackQueuedParams_RefusesChangedRenameMode(t *testing.T) {
	existing, _ := json.Marshal(bulkWriteBackOpParams{BookIDs: []string{"book-a"}, Rename: true})
	incoming, _ := json.Marshal(bulkWriteBackOpParams{BookIDs: []string{"book-b"}, Rename: false})

	_, merged, err := mergeBulkWriteBackQueuedParams(existing, incoming)
	if err != nil {
		t.Fatalf("mergeBulkWriteBackQueuedParams() error: %v", err)
	}
	if merged {
		t.Fatal("mergeBulkWriteBackQueuedParams() merged incompatible rename modes")
	}
}
