// file: core/outbox/version_test.go
// version: 1.0.1
// guid: 7c1e9b44-2a85-4f6d-93a0-b5d8e2f17c39
// last-edited: 2026-11-12

package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
)

// A record of a version this build does not know (written by a newer build) is
// never skipped, never marked done and never deleted: what it blocks stops,
// loudly, and everything it does not block keeps flowing. The tests below
// plant a version-2 row of each kind and check the same four things: the row
// is byte-for-byte unchanged, what is behind it was not delivered, the
// component is Degraded with delivery_stopped, and once the row is readable
// again (a rollback, or the upgrade) delivery resumes in order.

const futureVersion = 2

// putRaw overwrites key in the outbox keyspace with raw, reading it with
// GetForUpdate first as the Serialized prefixes (seq/, cur/, dead/) require.
func (r *rig) putRaw(key, raw []byte) {
	r.t.Helper()
	ctx := context.Background()
	b := r.outKS.NewIndexedBatch()
	defer b.Close()
	r.guard(ctx, b, key)
	must(r.t, b.Set(key, raw))
	must(r.t, b.Commit(ctx, storage.Sync))
}

// guard reads key with GetForUpdate when it lies in a Serialized family (seq/,
// cur/, dead/), which refuses blind writes; the other families take plain writes.
func (r *rig) guard(ctx context.Context, b *storage.Batch, key []byte) {
	r.t.Helper()
	if !bytes.HasPrefix(key, seqRoot()) && !bytes.HasPrefix(key, outboxstore.CurRoot()) && !bytes.HasPrefix(key, outboxstore.DeadRoot()) {
		return
	}
	if _, err := b.GetForUpdate(ctx, r.outKS, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		r.t.Fatal(err)
	}
}

func (r *rig) rawOf(key []byte) []byte {
	r.t.Helper()
	raw, err := r.outKS.Get(context.Background(), key)
	must(r.t, err)
	return raw
}

func (r *rig) deleteRaw(key []byte) {
	r.t.Helper()
	ctx := context.Background()
	b := r.outKS.NewIndexedBatch()
	defer b.Close()
	r.guard(ctx, b, key)
	must(r.t, b.Delete(key))
	must(r.t, b.Commit(ctx, storage.Sync))
}

func (r *rig) waitStopped() {
	r.t.Helper()
	eventually(r.t, "core.outbox to report delivery_stopped", func() bool {
		h := r.healthOf()
		return h.Status == health.StatusDegraded && h.Reason == "delivery_stopped"
	})
}

func (r *rig) waitHealthy() {
	r.t.Helper()
	eventually(r.t, "core.outbox to recover", func() bool { return r.healthOf().Status == health.StatusUp })
}

func v2[T any](t testing.TB, v T) []byte {
	t.Helper()
	raw, err := codec.JSON[T](futureVersion).Encode(v)
	must(t, err)
	return raw
}

// futureCfg keeps the relay running for the whole test on a clock the test moves.
func futureCfg(c *outbox.Config) { c.RelayRotate = 24 * time.Hour }

// An intake row of a newer build stops its topic's intake: nothing behind it is
// sequenced (commit order is never reshuffled), the row stays, another topic
// is unaffected, and the topic resumes when the row is gone.
func TestOutbox_UnknownVersionIntakeRowStopsTopic(t *testing.T) {
	clk := &skew{}
	rn := &recorder{}
	rs := &responder{}
	r := newRig(t, rigOpts{now: clk.now, cfg: futureCfg, defs: nil, noRelay: true, register: func(testing.TB, *rig) []registration {
		return []registration{
			{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN, rn.handler(consumerN))}},
			{owner: "gotham", hs: []outbox.Handler{outbox.ConsumeIntoTask(signal(), consumerC, runKind, okParams, outbox.Live())}},
		}
	}})
	_ = rs
	bad := outboxstore.InKey(noticeName, "00000000000000000000000001")
	r.putRaw(bad, v2(t, outboxstore.Intake{ID: "00000000000000000000000001", Key: "wayne-manor", Payload: rawJSON(t, Dispatch{Villain: "Ra's"}), Producer: "core"}))
	badRaw := bytes.Clone(r.rawOf(bad))
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	r.startRelay()

	r.waitStopped()
	stays(t, "nothing sequenced or delivered behind the unreadable row", 200*time.Millisecond, func() bool {
		return rn.count() == 0 && r.messages(noticeName) == 0 && r.count(outboxstore.InPrefix(noticeName)) == 2
	})
	if !bytes.Equal(r.rawOf(bad), badRaw) {
		t.Fatal("the unreadable intake row was rewritten")
	}
	if _, err := r.outKS.Get(context.Background(), outboxstore.SeqKey(noticeName)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("the topic's sequence advanced past an unreadable row: %v", err)
	}

	r.deleteRaw(bad)
	clk.advance(time.Minute)
	eventually(t, "the held message after the row is gone", func() bool { return rn.count() == 1 })
	r.waitHealthy()
}

// A seq/ row of a newer build stops its topic: intake rows stay where they are
// and the row is untouched.
func TestOutbox_UnknownVersionSeqRowStopsTopic(t *testing.T) {
	clk := &skew{}
	rn := &recorder{}
	r := newRig(t, rigOpts{now: clk.now, cfg: futureCfg, noRelay: true, register: consumeInto(rn, consumerN)})
	r.putRaw(outboxstore.SeqKey(noticeName), v2(t, outboxstore.High{High: 41, Partitions: 2}))
	want := bytes.Clone(r.rawOf(outboxstore.SeqKey(noticeName)))
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	r.startRelay()

	r.waitStopped()
	stays(t, "the message held in intake", 200*time.Millisecond, func() bool {
		return rn.count() == 0 && r.messages(noticeName) == 0 && r.count(outboxstore.InPrefix(noticeName)) == 1
	})
	if !bytes.Equal(r.rawOf(outboxstore.SeqKey(noticeName)), want) {
		t.Fatal("the unreadable sequence row was rewritten")
	}
}

// A message row of a newer build stops the (consumer, partition) it sits in:
// the messages after it on that partition are not delivered and nothing is
// deleted, while the topic's other partition flows; once the row is readable,
// delivery resumes with the stopped message first.
func TestOutbox_UnknownVersionMessageRowStopsPair(t *testing.T) {
	clk := &skew{}
	cp := newCrashPoints()
	ka, kb := keysOnDifferentPartitions(t)
	// Boot 1 has no consumer of the notice topic, so its messages are sequenced
	// and kept.
	r1 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, cfg: futureCfg, register: func(testing.TB, *rig) []registration { return nil }})
	r1.stage(ka, Dispatch{Villain: "Joker", Block: 1})
	r1.stage(ka, Dispatch{Villain: "Penguin", Block: 2})
	r1.stage(ka, Dispatch{Villain: "Riddler", Block: 3})
	r1.stage(kb, Dispatch{Villain: "Bane", Block: 4})
	eventually(t, "four sequenced messages", func() bool { return r1.messages(noticeName) == 4 })
	r1.close()

	rn := &recorder{}
	r2 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, cfg: futureCfg, noRelay: true,
		register: consumeInto(rn, consumerN, outbox.FromBeginning())})
	pp := outboxstore.Partition(ka, 2)
	key := outboxstore.MsgKey(noticeName, pp, 2)
	good := bytes.Clone(r2.rawOf(key))
	bad := v2(t, outboxstore.Msg{ID: "x", Key: ka, Payload: rawJSON(t, Dispatch{Villain: "Penguin"}), Producer: "core"})
	r2.putRaw(key, bad)
	r2.startRelay()

	eventually(t, "the readable messages", func() bool { return rn.count() == 2 })
	if got := rn.villains(); !(got[0] == "Joker" && got[1] == "Bane") && !(got[0] == "Bane" && got[1] == "Joker") {
		t.Fatalf("delivered %v, want the Joker (before the row) and Bane (other partition)", got)
	}
	r2.waitStopped()
	behind := outboxstore.MsgKey(noticeName, pp, 3)
	stays(t, "the pair stopped at the row, keeping it and the message behind it", 200*time.Millisecond, func() bool {
		_, errBehind := r2.outKS.Get(context.Background(), behind)
		return rn.count() == 2 && errBehind == nil && bytes.Equal(r2.rawOf(key), bad)
	})
	if !bytes.Equal(r2.rawOf(key), bad) {
		t.Fatal("the unreadable message row was rewritten")
	}

	r2.putRaw(key, good)
	clk.advance(time.Minute)
	eventually(t, "the stopped pair to resume", func() bool { return rn.count() == 4 })
	var onKa []string
	for _, d := range rn.all() {
		if d.Key == ka {
			onKa = append(onKa, d.Villain)
		}
	}
	if len(onKa) != 3 || onKa[0] != "Joker" || onKa[1] != "Penguin" || onKa[2] != "Riddler" {
		t.Fatalf("partition delivered %v after the repair, want Joker, Penguin, Riddler in order", onKa)
	}
	r2.waitHealthy()
}

// A cursor row of a newer build stops its pair (it is not recreated at the
// head, which would redeliver or skip) while a second consumer of the same
// topic is unaffected; the messages stay for the stopped one.
func TestOutbox_UnknownVersionCursorRowStopsPair(t *testing.T) {
	clk := &skew{}
	rn, rn2 := &recorder{}, &recorder{}
	r := newRig(t, rigOpts{now: clk.now, cfg: futureCfg, sweep: sweepFast, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, rn.handler(consumerN)),
			outbox.Consume(notice(), consumerN2, rn2.handler(consumerN2)),
		}}}
	}})
	r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: 1})
	eventually(t, "the first message to both", func() bool { return rn.count() == 1 && rn2.count() == 1 })
	eventually(t, "retention to empty the topic", func() bool { return r.messages(noticeName) == 0 })
	pp := outboxstore.Partition("wayne-manor", 2)
	key := outboxstore.CurKey(consumerN, noticeName, pp)
	r.putRaw(key, v2(t, outboxstore.Cursor{Seq: 1, Rev: 9, At: time.Now()}))
	want := bytes.Clone(r.rawOf(key))

	r.stage("wayne-manor", Dispatch{Villain: "Penguin", Block: 2})
	r.stage("wayne-manor", Dispatch{Villain: "Riddler", Block: 3})
	eventually(t, "the unaffected consumer", func() bool { return rn2.count() == 3 })
	r.waitStopped()
	stays(t, "the stopped consumer neither delivers nor loses messages", 250*time.Millisecond, func() bool {
		return rn.count() == 1 && r.messages(noticeName) == 2
	})
	if !bytes.Equal(r.rawOf(key), want) {
		t.Fatal("the unreadable cursor row was rewritten")
	}
}

// A try row of a newer build stops its pair at the retry: the failed message
// is not redelivered, not dead-lettered and not skipped.
func TestOutbox_UnknownVersionTryRowStopsPair(t *testing.T) {
	clk := &skew{}
	g := &gate{}
	rc := &recorder{}
	r := newRig(t, rigOpts{now: clk.now, sweep: sweepFast,
		cfg: func(c *outbox.Config) {
			futureCfg(c)
			c.MaxAttempts = 100
			c.BackoffMin, c.BackoffMax = time.Hour, time.Hour
		},
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN, g.handler(rc, consumerN))}}}
		}})
	r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: 1})
	r.stage("wayne-manor", Dispatch{Villain: "Penguin", Block: 2})
	eventually(t, "the first attempt and its retry record", func() bool {
		return g.calls.Load() == 1 && r.count(tryRoot()) == 1
	})
	pp := outboxstore.Partition("wayne-manor", 2)
	key := outboxstore.TryKey(consumerN, noticeName, pp)
	good := bytes.Clone(r.rawOf(key))
	bad := v2(t, outboxstore.Try{Entries: []outboxstore.TryEntry{{Seq: 1, Attempt: 1, NextAt: time.Now(), Token: "error"}}})
	r.putRaw(key, bad)

	clk.advance(2 * time.Hour) // the retry falls due
	r.waitStopped()
	stays(t, "no redelivery past an unreadable retry record", 200*time.Millisecond, func() bool {
		l, _, _ := r.ob.ListDead(context.Background(), "", 5)
		return g.calls.Load() == 1 && rc.count() == 0 && len(l) == 0
	})
	if !bytes.Equal(r.rawOf(key), bad) {
		t.Fatal("the unreadable retry record was rewritten")
	}

	r.putRaw(key, good)
	g.open.Store(true)
	clk.advance(time.Minute)
	eventually(t, "the retry and the message behind it", func() bool { return rc.count() == 2 })
	if got := rc.villains(); got[0] != "Joker" || got[1] != "Penguin" {
		t.Fatalf("delivered %v", got)
	}
	r.waitHealthy()
}

// The codec refuses a row with no version or the wrong one: the same guarantee
// at the store's seam, for every record kind.
func TestOutboxStore_UnknownVersionRefused(t *testing.T) {
	r := newRig(t, rigOpts{noExec: true})
	ctx := context.Background()
	s := outboxstore.New(r.outKS)

	r.putRaw(outboxstore.SeqKey("gotham.signal.raised.v1"), v2(t, outboxstore.High{High: 1, Partitions: 4}))
	if _, _, err := s.ReadHigh(ctx, "gotham.signal.raised.v1"); !errors.Is(err, codec.ErrUnknownVersion) {
		t.Fatalf("ReadHigh = %v, want ErrUnknownVersion", err)
	}
	p := outboxstore.Pair{Consumer: consumerC, Topic: signalName, PP: 1}
	r.putRaw(outboxstore.CurKey(p.Consumer, p.Topic, p.PP), v2(t, outboxstore.Cursor{Seq: 3, Rev: 1}))
	if _, err := s.ReadPair(ctx, p); !errors.Is(err, codec.ErrUnknownVersion) {
		t.Fatalf("ReadPair = %v, want ErrUnknownVersion", err)
	}
	r.putRaw(outboxstore.MsgKey(signalName, 1, 7), v2(t, outboxstore.Msg{ID: "x"}))
	if _, err := s.MessagesAfter(ctx, signalName, 1, 0, 10); !errors.Is(err, codec.ErrUnknownVersion) {
		t.Fatalf("MessagesAfter = %v, want ErrUnknownVersion", err)
	}
	r.putRaw(outboxstore.DeadKey(consumerC, signalName, 9), v2(t, outboxstore.Dead{Part: 1}))
	if _, _, err := s.GetDead(ctx, consumerC, signalName, 9); !errors.Is(err, codec.ErrUnknownVersion) {
		t.Fatalf("GetDead = %v, want ErrUnknownVersion", err)
	}
	// A value in the JSON envelope with no version is as unreadable.
	r.putRaw(outboxstore.CurKey("gotham.dispatch.relay", signalName, 0), []byte(`j{"seq":1}`))
	if _, err := s.ReadPair(ctx, outboxstore.Pair{Consumer: "gotham.dispatch.relay", Topic: signalName, PP: 0}); !errors.Is(err, codec.ErrUnknownVersion) {
		t.Fatalf("an unversioned cursor row = %v, want ErrUnknownVersion", err)
	}
}

// A topic whose declared partition count changed since its messages were
// sequenced stops: keys would map to different partitions, and per-key order
// would break. Intake rows stay, the sequenced messages stay, and the
// component says so.
func TestOutbox_PartitionsChangedStopsTopic(t *testing.T) {
	cp := newCrashPoints()
	r1 := newRig(t, rigOpts{fs: cp.fs, partitions: 4, register: func(testing.TB, *rig) []registration { return nil }})
	r1.stageGotham("batcave", Dispatch{Villain: "Joker"})
	eventually(t, "the first message to be sequenced", func() bool { return r1.messages(signalName) == 1 })
	r1.close()

	r2 := newRig(t, rigOpts{fs: cp.fs, partitions: 8, register: func(testing.TB, *rig) []registration { return nil }})
	r2.stageGotham("batcave", Dispatch{Villain: "Penguin"})
	r2.waitStopped()
	stays(t, "the new message held in intake", 200*time.Millisecond, func() bool {
		return r2.messages(signalName) == 1 && r2.count(outboxstore.InPrefix(signalName)) == 1
	})
}
