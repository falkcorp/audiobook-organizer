// file: core/outbox/status_stopped_test.go
// version: 1.0.0
// guid: 3d9f1a62-5e48-4b07-a1c3-82e6f0b7d415
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
)

// stopped reports whether st lists want.
func stoppedHas(st outbox.Status, want outbox.StoppedStatus) bool {
	return slices.Contains(st.Stopped, want)
}

// The operator's status read survives exactly when it is needed: a row that
// does not decode (a bad one, or one a newer build wrote) does not fail it. The
// row is left out, named in Stopped by its pair or topic and a closed cause, and
// the counts it touches say "at least". Only a failure of the store itself is an
// error.
func TestOutbox_StatusSurvivesAnUnreadableRowAndNamesIt(t *testing.T) {
	ctx := context.Background()
	seqRow := func(t *testing.T, r *rig) {
		st := outboxstore.New(r.outKS)
		b := st.OpenBatch()
		_, err := st.StageSequence(ctx, b, noticeName, 2, []outboxstore.IntakeRow{{ID: "X", Intake: outboxstore.Intake{
			ID: "X", Key: "wayne-manor", Payload: []byte(`{}`), StagedAt: time.Now(), Producer: "core"}}})
		must(t, err)
		must(t, b.Commit(ctx, storage.Sync))
		b.Close()
	}
	pp := outboxstore.Partition("wayne-manor", 2)

	for _, tc := range []struct {
		name  string
		row   []byte
		cause string
	}{
		{"a row of a newer build", v2(t, outboxstore.Msg{ID: "X", Key: "wayne-manor", Payload: []byte(`{}`), StagedAt: time.Now(), Producer: "core"}), "unknown_version"},
		{"a corrupt row", []byte(`j{"v":1,"id":`), "corrupt_row"},
	} {
		t.Run("message row, "+tc.name, func(t *testing.T) {
			r := newRig(t, rigOpts{register: consumeInto(&recorder{}, consumerN)})
			eventually(t, "the consumer's cursors", func() bool { return r.cursors(consumerN) == 2 })
			seqRow(t, r)
			r.putRaw(outboxstore.MsgKey(noticeName, pp, 1), tc.row)
			st, err := r.ob.Status(ctx)
			if err != nil {
				t.Fatalf("Status failed on one unreadable message row: %v", err)
			}
			want := outbox.StoppedStatus{Topic: noticeName, Consumer: consumerN, Partition: pp, Cause: tc.cause}
			if !stoppedHas(st, want) {
				t.Fatalf("Stopped = %+v, want it to list %+v", st.Stopped, want)
			}
			var cs *outbox.ConsumerStatus
			for i := range st.Consumers {
				if st.Consumers[i].Name == consumerN {
					cs = &st.Consumers[i]
				}
			}
			if cs == nil || !cs.PendingCapped {
				t.Fatalf("the consumer is missing from the status, or its backlog is not marked as a floor: %+v", cs)
			}
		})
	}

	t.Run("cursor rows", func(t *testing.T) {
		r := newRig(t, rigOpts{register: consumeInto(&recorder{}, consumerN)})
		eventually(t, "the consumer's cursors", func() bool { return r.cursors(consumerN) == 2 })
		// Both partitions' rows: the read must go on past the first unreadable one.
		for pp := range 2 {
			r.putRaw(outboxstore.CurKey(consumerN, noticeName, pp), v2(t, outboxstore.Cursor{Seq: 1, Rev: 9, At: time.Now()}))
		}
		st, err := r.ob.Status(ctx)
		if err != nil {
			t.Fatalf("Status failed on unreadable cursor rows: %v", err)
		}
		for pp := range 2 {
			if want := (outbox.StoppedStatus{Topic: noticeName, Consumer: consumerN, Partition: pp, Cause: "unknown_version"}); !stoppedHas(st, want) {
				t.Fatalf("Stopped = %+v, want it to list %+v", st.Stopped, want)
			}
		}
		found := false
		for _, c := range st.Consumers {
			found = found || (c.Name == consumerN && c.Enabled)
		}
		if !found {
			t.Fatalf("the consumer with unreadable cursors is missing from the status: %+v", st.Consumers)
		}
	})

	t.Run("intake row", func(t *testing.T) {
		r := newRig(t, rigOpts{register: consumeInto(&recorder{}, consumerN)})
		eventually(t, "the consumer's cursors", func() bool { return r.cursors(consumerN) == 2 })
		b := r.outKS.NewBatch()
		must(t, b.Set(outboxstore.InKey(noticeName, "00000000000000000000000000"), []byte(`zNEWER-BUILD-ENCODING`)))
		must(t, b.Commit(ctx, storage.Sync))
		b.Close()
		st, err := r.ob.Status(ctx)
		if err != nil {
			t.Fatalf("Status failed on an unreadable intake row: %v", err)
		}
		if want := (outbox.StoppedStatus{Topic: noticeName, Cause: "corrupt_row"}); !stoppedHas(st, want) {
			t.Fatalf("Stopped = %+v, want it to list %+v", st.Stopped, want)
		}
	})

	t.Run("seq row", func(t *testing.T) {
		r := newRig(t, rigOpts{register: consumeInto(&recorder{}, consumerN)})
		eventually(t, "the consumer's cursors", func() bool { return r.cursors(consumerN) == 2 })
		r.putRaw(outboxstore.SeqKey(noticeName), []byte(`j{"v":1,"high":`))
		st, err := r.ob.Status(ctx)
		if err != nil {
			t.Fatalf("Status failed on an unreadable seq row: %v", err)
		}
		if want := (outbox.StoppedStatus{Topic: noticeName, Cause: "corrupt_row"}); !stoppedHas(st, want) {
			t.Fatalf("Stopped = %+v, want it to list %+v", st.Stopped, want)
		}
	})

	t.Run("a topic whose partition count changed", func(t *testing.T) {
		cp, _ := strandedFour(t)
		r := newRig(t, rigOpts{fs: cp.fs, partitions: 2, register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.ConsumeRaw(signalName, consumerN, (&payloads{}).handler(consumerN))}}}
		}})
		st, err := r.ob.Status(ctx)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if want := (outbox.StoppedStatus{Topic: signalName, Cause: "partitions_changed"}); !stoppedHas(st, want) {
			t.Fatalf("Stopped = %+v, want it to list %+v", st.Stopped, want)
		}
	})

	t.Run("a healthy store lists nothing", func(t *testing.T) {
		r := newRig(t, rigOpts{register: consumeInto(&recorder{}, consumerN)})
		eventually(t, "the consumer's cursors", func() bool { return r.cursors(consumerN) == 2 })
		st, err := r.ob.Status(ctx)
		must(t, err)
		if len(st.Stopped) != 0 {
			t.Fatalf("Stopped = %+v on a healthy store", st.Stopped)
		}
	})
}
