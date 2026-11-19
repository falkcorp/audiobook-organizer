// file: core/outbox/purge_committed_test.go
// version: 1.0.1
// guid: 0d6a4f0e-2b67-4a35-9f3e-6c1f8b7a42d1
// last-edited: 2026-11-19

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
)

// purgeFixture is a rig with no relay, consumerN's two cursors at 0 and n
// sequenced messages spread over both partitions of the notice topic.
func purgeFixture(t *testing.T, hooks *outbox.TestHooks, n int) *rig {
	t.Helper()
	ctx := context.Background()
	r := newRig(t, rigOpts{noExec: true, hooks: hooks, register: consumeInto(&recorder{}, consumerN)})
	st := outboxstore.New(r.outKS)
	rows := make([]outboxstore.IntakeRow, 0, n)
	for i := range n {
		id := fmt.Sprintf("01J0000000000000000SEQ%03d", i)
		rows = append(rows, outboxstore.IntakeRow{ID: id, Intake: outboxstore.Intake{Msg: outboxstore.Msg{
			ID: id, Key: fmt.Sprintf("gotham-%d", i), Payload: rawJSON(t, Dispatch{Villain: "Penguin", Block: i}),
			StagedAt: time.Now(), Producer: "core"}}})
	}
	b := st.OpenBatch()
	_, err := st.StageSequence(ctx, b, noticeName, 2, rows)
	must(t, err)
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()
	cb := st.OpenBatch()
	_, err = st.StageCursorInit(ctx, cb, consumerN, noticeName, 2, true, time.Now())
	must(t, err)
	must(t, cb.Commit(ctx, storage.Sync))
	cb.Close()
	return r
}

// pending is how many sequenced messages consumerN has not taken over.
func (r *rig) pendingOf(consumer string) int {
	r.t.Helper()
	st, err := r.ob.Status(context.Background())
	must(r.t, err)
	for _, c := range st.Consumers {
		if c.Name == consumer {
			return c.Pending
		}
	}
	r.t.Fatalf("no status for %s", consumer)
	return 0
}

// A purge reports what it committed. A partition whose commit does not happen
// (here, a request canceled between the partition's staging and its commit) has
// skipped nothing, so the count of a failed purge is the partitions that did
// advance, and the cursors agree with it.
func TestOutbox_PurgeBacklogReportsOnlyCommittedPartitions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cancelAt int // the partition whose commit the request is canceled before
	}{{"first partition", 0}, {"second partition", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hooks := &outbox.TestHooks{BeforePurgeCommit: func(_ string, pp int) {
				if pp == tc.cancelAt {
					cancel()
				}
			}}
			r := purgeFixture(t, hooks, 10)
			before := r.pendingOf(consumerN)
			if before != 10 {
				t.Fatalf("pending before the purge = %d, want 10", before)
			}
			skipped, err := r.ob.PurgeBacklog(ctx, consumerN)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("PurgeBacklog = %d, %v; want the cancellation", skipped, err)
			}
			moved := before - r.pendingOf(consumerN)
			if skipped != moved {
				t.Fatalf("PurgeBacklog reported %d skipped, the cursors moved past %d", skipped, moved)
			}
			if tc.cancelAt == 0 && skipped != 0 {
				t.Fatalf("no partition committed, yet %d reported as skipped", skipped)
			}
			if tc.cancelAt == 1 && skipped == 0 {
				t.Fatal("the first partition committed and the failed purge reports nothing; the fixture put no message on it")
			}
		})
	}
}

// An uncanceled purge reports what the cursors moved past.
func TestOutbox_PurgeBacklogReportsEverythingItCommitted(t *testing.T) {
	r := purgeFixture(t, nil, 10)
	skipped, err := r.ob.PurgeBacklog(context.Background(), consumerN)
	must(t, err)
	if skipped != 10 || r.pendingOf(consumerN) != 0 {
		t.Fatalf("skipped = %d, pending after = %d; want 10 and 0", skipped, r.pendingOf(consumerN))
	}
}

// A retry entry of a message the cursor already passed is still something the
// consumer has not finished, so the purge counts it as skipped, the same way the
// status read counts it as pending; the count and the cursors agree.
func TestOutbox_PurgeBacklogCountsRetriesLikeStatus(t *testing.T) {
	ctx := context.Background()
	r := purgeFixture(t, nil, 10)
	st := outboxstore.New(r.outKS)
	hi, _, err := st.ReadHigh(ctx, noticeName)
	must(t, err)
	p := outboxstore.Pair{Consumer: consumerN, Topic: noticeName, PP: 0}
	b := st.OpenBatch()
	ps, err := st.LoadPair(ctx, b, p)
	must(t, err)
	ps.Cur.Seq = hi.High // partition 0 has taken over everything, but one message is waiting to be retried
	ps.Try.Entries = []outboxstore.TryEntry{{Seq: 1, Attempt: 1, NextAt: time.Now().Add(time.Hour), Token: "error"}}
	must(t, st.StagePair(b, p, ps, time.Now()))
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()

	before := r.pendingOf(consumerN)
	if before == 0 {
		t.Fatal("the fixture left nothing pending")
	}
	skipped, err := r.ob.PurgeBacklog(ctx, consumerN)
	must(t, err)
	if skipped != before || r.pendingOf(consumerN) != 0 {
		t.Fatalf("skipped = %d, pending before = %d, after = %d; want the two counts equal and 0 after", skipped, before, r.pendingOf(consumerN))
	}
}
