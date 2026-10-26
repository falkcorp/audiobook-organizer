// file: internal/activity/batcher.go
// version: 1.3.0
// guid: 7f3c1a2e-8b4d-4e9f-a5c6-2d0e3b7f9a1c
// last-edited: 2026-10-25

package activity

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// BatchItem is one sub-entry within a batched ActivityEntry.
type BatchItem struct {
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Detail string `json:"detail,omitempty"`
}

// BatchKey identifies a group of related entries to merge.
//
// UserData is part of the key so a group of lines that name a user (the
// TagUserData lines Writer withholds from callers without users.manage) never
// shares a batch with lines that do not: the flushed entry carries one tag set,
// so a mixed batch would either leak the user lines or hide the plain ones.
type BatchKey struct {
	Type        string
	Source      string
	OperationID string
	UserData    bool
}

// BatchDetails is stored in ActivityEntry.Details for batched entries.
type BatchDetails struct {
	Batched       bool        `json:"batched"`
	BatchKeyStr   string      `json:"batch_key"`
	Items         []BatchItem `json:"items"`
	WindowStart   time.Time   `json:"window_start"`
	WindowEnd     time.Time   `json:"window_end"`
	OriginalCount int         `json:"original_count"`
	Truncated     bool        `json:"truncated,omitempty"`
}

const (
	maxBatchItems    = 200
	batchWindow      = 15 * time.Second
	batchAbsoluteCap = 60 * time.Second
)

type pendingBatch struct {
	key         BatchKey
	items       []BatchItem
	firstSeen   time.Time
	lastSeen    time.Time
	overflowCnt int
	timer       *time.Timer
}

// ActivityBatcher accumulates batchable entries for up to 15s,
// then emits one merged ActivityEntry per BatchKey.
type ActivityBatcher struct {
	mu      sync.Mutex
	pending map[BatchKey]*pendingBatch
	out     chan<- database.ActivityEntry
	done    chan struct{}
	window  time.Duration // defaults to batchWindow
}

// NewActivityBatcher creates a new ActivityBatcher that drains to out.
func NewActivityBatcher(out chan<- database.ActivityEntry) *ActivityBatcher {
	return newActivityBatcherWithWindow(out, batchWindow)
}

// newActivityBatcherWithWindow creates a batcher with a custom flush window.
// Used in tests to avoid 15-second waits.
func newActivityBatcherWithWindow(out chan<- database.ActivityEntry, window time.Duration) *ActivityBatcher {
	return &ActivityBatcher{
		pending: make(map[BatchKey]*pendingBatch),
		out:     out,
		done:    make(chan struct{}),
		window:  window,
	}
}

// Submit accumulates item under the given key. If this is the first item for
// the key a timer is started; on expiry the batch is flushed automatically.
// Early flush occurs when total items >= 500 or the absolute cap is reached.
func (b *ActivityBatcher) Submit(key BatchKey, item BatchItem) {
	b.mu.Lock()

	pb, exists := b.pending[key]
	if !exists {
		pb = &pendingBatch{
			key:       key,
			firstSeen: time.Now(),
		}
		pb.timer = time.AfterFunc(b.window, func() { b.flushKey(key) })
		b.pending[key] = pb
	}

	pb.lastSeen = time.Now()

	if len(pb.items) < maxBatchItems {
		pb.items = append(pb.items, item)
	} else {
		pb.overflowCnt++
	}

	total := len(pb.items) + pb.overflowCnt
	earlyFlush := total >= 500 || time.Since(pb.firstSeen) >= batchAbsoluteCap

	b.mu.Unlock()

	if earlyFlush {
		b.mu.Lock()
		if existing, ok := b.pending[key]; ok {
			existing.timer.Stop()
		}
		b.mu.Unlock()
		go b.flushKey(key)
	}
}

// FlushAll synchronously flushes all pending batches.
func (b *ActivityBatcher) FlushAll() {
	b.mu.Lock()
	keys := make([]BatchKey, 0, len(b.pending))
	for k := range b.pending {
		keys = append(keys, k)
	}
	b.mu.Unlock()

	for _, k := range keys {
		b.flushKey(k)
	}
}

// flushKey snapshots the pending batch for key, removes it from the map,
// builds an ActivityEntry, and sends it to the output channel.
func (b *ActivityBatcher) flushKey(key BatchKey) {
	b.mu.Lock()
	pb, ok := b.pending[key]
	if !ok {
		b.mu.Unlock()
		return
	}
	pb.timer.Stop()
	delete(b.pending, key)
	b.mu.Unlock()

	if len(pb.items) == 0 && pb.overflowCnt == 0 {
		return
	}

	originalCount := len(pb.items) + pb.overflowCnt
	truncated := pb.overflowCnt > 0

	bd := BatchDetails{
		Batched:       true,
		BatchKeyStr:   fmt.Sprintf("%s|%s|%s", key.Type, key.Source, key.OperationID),
		Items:         pb.items,
		WindowStart:   pb.firstSeen,
		WindowEnd:     pb.lastSeen,
		OriginalCount: originalCount,
		Truncated:     truncated,
	}

	raw, err := json.Marshal(bd)
	if err != nil {
		return
	}
	var details map[string]any
	if err := json.Unmarshal(raw, &details); err != nil {
		return
	}

	noun := batchNoun(key.Type)
	summary := fmt.Sprintf("Batched %d %s", originalCount, noun)

	entry := database.ActivityEntry{
		Timestamp:   pb.firstSeen,
		Tier:        "batch",
		Type:        key.Type,
		Level:       "info",
		Source:      key.Source,
		OperationID: key.OperationID,
		Summary:     summary,
		Details:     details,
	}
	if key.UserData {
		// Details.Items holds the lines themselves, so the entry is user data
		// even though the summary ("Batched N ...") names nobody.
		entry.Tags = append(entry.Tags, TagUserData)
	}

	select {
	case b.out <- entry:
	case <-b.done:
	}
}

// Close flushes all pending batches and closes the done channel.
func (b *ActivityBatcher) Close() {
	b.FlushAll()
	close(b.done)
}

// batchNoun returns a human-readable plural noun for a batch type.
func batchNoun(batchType string) string {
	switch batchType {
	case "embedded-tag-load":
		return "embedded tag loads"
	case "tag-scan":
		return "tag scans"
	case "scan-file-processed":
		return "files scanned"
	case "metadata-apply":
		return "metadata applies"
	case "path-repair":
		return "path repairs"
	case "isbn-enrich":
		return "books enriched with ISBN"
	case "temp-file-cleanup":
		return "orphaned temp files removed"
	case "missing-file-repair":
		return "missing files repaired"
	case "purge-deleted":
		return "purge errors"
	default:
		return batchType + " operations"
	}
}
