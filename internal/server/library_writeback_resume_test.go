// file: internal/server/library_writeback_resume_test.go
// version: 1.0.1
// guid: 06cd9d1b-4457-4af0-b3f0-5ac22a64e395
// last-edited: 2026-10-03

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// examinedBooks reads back which books runBulkWriteBack looked at from the
// per-book log lines it writes. The books in these tests do not exist, so every
// one of them lands on the "book <id>: not found" line, which is the cheapest
// path through the worker that still runs the whole per-item loop.
func examinedBooks(lines []string) map[string]bool {
	out := map[string]bool{}
	for _, l := range lines {
		if !strings.HasPrefix(l, "book ") || !strings.HasSuffix(l, ": not found") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(l, "book "), ": not found")
		out[id] = true
	}
	return out
}

// TestBulkWriteBack_ResumeSkipsCheckpointedBooks is the test a ResumeRestart op
// with no usable checkpoint fails. Before this checkpoint existed the only state
// runBulkWriteBack wrote was a v1 blob keyed on a ULID minted per attempt that
// nothing read back, so a restart re-examined every book. This runs the op,
// cancels it partway, then resumes it the way resumeRestart would — with the
// last checkpoint overlaid on the original params — and asserts the resumed run
// examines exactly the books the checkpoint still owed.
func TestBulkWriteBack_ResumeSkipsCheckpointedBooks(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	const n = 60
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("writeback-book-%03d", i)
	}
	params, err := json.Marshal(bulkWriteBackOpParams{BookIDs: ids, Rename: true})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	// ---- first attempt: interrupted partway ----
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &resumeRecorder{opID: "op-writeback-resume-1"}
	// runBulkWriteBack reports progress once per examined book, after the
	// book. Cancel at the midpoint so the run has crossed the checkpoint
	// cadence (25) at least once and still has work left to owe.
	//
	// The cancel point must be a GATE, not just a cancel() call. The
	// recorder appends the progress call under its lock and runs this
	// callback after unlocking, so the worker that drew nth == n/2 can be
	// descheduled before it reaches cancel() while the other workers
	// examine every remaining book ("checkpoint owes 0 of 60" in CI on
	// 2026-09-19; reproduced 8 in 2,400 under parallel load). So every
	// later callback waits until the cancel has landed. A worker checks
	// ctx before each book and its callback returns only after cancel, so
	// each worker examines at most one book past the gate: at least n/2
	// and at most n/2-1+workers books are examined, and workers is capped
	// at maxWriteBackWorkers (8), well under n/2. Blocking here cannot
	// deadlock: neither the recorder nor the worker pool holds a lock
	// across UpdateProgress, and the n/2 callback itself never waits.
	canceled := make(chan struct{})
	first.onProgress = func(nth int) {
		switch {
		case nth == n/2:
			cancel()
			close(canceled)
		case nth > n/2:
			<-canceled
		}
	}
	// runBulkWriteBack reports cancellation through its log and returns nil;
	// the op's Run passes that through. What matters here is the checkpoint.
	_ = s.runBulkWriteBackOp(ctx, params, first)

	var ckpt bulkWriteBackOpParams
	lastCkpt := first.lastState(t, &ckpt)
	if !ckpt.Rename {
		t.Fatal("checkpoint dropped Rename; a resumed run would silently stop renaming files")
	}
	remaining := idSet(ckpt.BookIDs)
	if len(remaining) == 0 || len(remaining) == n {
		t.Fatalf("checkpoint owes %d of %d books; the interrupt did not land partway", len(remaining), n)
	}

	// Every examined book must be OUT of the remaining set and every
	// unexamined one IN it — the done-set must track what the workers actually
	// reached, not a count that out-of-order completion makes meaningless.
	examined := examinedBooks(first.logLines())
	if maxExamined := n/2 - 1 + writeBackWorkers(); len(examined) > maxExamined {
		t.Fatalf("first attempt examined %d books after a cancel at %d (max %d with %d workers); workers ignored the cancel",
			len(examined), n/2, maxExamined, writeBackWorkers())
	}
	for _, id := range ids {
		switch {
		case examined[id] && remaining[id]:
			t.Errorf("book %s was examined but the checkpoint still owes it", id)
		case !examined[id] && !remaining[id]:
			t.Errorf("book %s was never examined but the checkpoint dropped it", id)
		}
	}

	// ---- resumed attempt: checkpoint overlaid on params ----
	resumedParams := overlayCheckpoint(t, params, lastCkpt)
	second := &resumeRecorder{opID: "op-writeback-resume-2"}
	if err := s.runBulkWriteBackOp(context.Background(), resumedParams, second); err != nil {
		t.Fatalf("resumed run: %v", err)
	}

	resumedExamined := examinedBooks(second.logLines())
	for id := range resumedExamined {
		if !remaining[id] {
			t.Errorf("resumed run re-examined %s, which the first attempt had already finished", id)
		}
	}
	for id := range remaining {
		if !resumedExamined[id] {
			t.Errorf("resumed run never examined %s, which the checkpoint owed", id)
		}
	}
	if got := second.lastProgress(t); got.total != len(remaining) || got.current != len(remaining) {
		t.Errorf("resumed run ended its progress at %d/%d, want %d/%d", got.current, got.total, len(remaining), len(remaining))
	}
}

// TestBulkWriteBackOpParams_EmptyRemainingIsExplicit pins the field shape the
// overlay depends on: a finished batch must serialise book_ids as an empty
// list, not omit it, or the base params' original list shows through and the
// resumed run rewrites every file again.
func TestBulkWriteBackOpParams_EmptyRemainingIsExplicit(t *testing.T) {
	data, err := json.Marshal(bulkWriteBackOpParams{BookIDs: []string{}, Rename: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := raw["book_ids"]
	if !ok {
		t.Fatalf("book_ids was omitted from the checkpoint: %s", data)
	}
	if string(got) != "[]" {
		t.Fatalf("book_ids = %s, want []", got)
	}
}
