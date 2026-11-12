// file: core/outbox/crash_test.go
// version: 1.0.0
// guid: b4f7a8d1-2c6a-4f0e-9f35-1a3e5c7d9b21
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// crashPoints takes crash-consistent clones of a file system the first time
// each named point is reached. Each point yields two states a crash could
// leave: exactly what was synced, and that plus a random half of what was not.
type crashPoints struct {
	fs     *vfs.MemFS
	mu     sync.Mutex
	clones map[string][]*vfs.MemFS
}

func newCrashPoints() *crashPoints {
	return &crashPoints{fs: vfs.NewCrashableMem(), clones: map[string][]*vfs.MemFS{}}
}

func (p *crashPoints) hit(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, done := p.clones[name]; done {
		return
	}
	p.clones[name] = []*vfs.MemFS{
		p.fs.CrashClone(vfs.CrashCloneCfg{}),
		p.fs.CrashClone(vfs.CrashCloneCfg{UnsyncedDataPercent: 50, RNG: rand.New(rand.NewPCG(7, 11))}),
	}
}

func (p *crashPoints) at(t testing.TB, name string) []*vfs.MemFS {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.clones[name]
	if len(c) == 0 {
		t.Fatalf("crash point %q was never reached", name)
	}
	return c
}

func (p *crashPoints) reached(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clones[name]) > 0
}

func (p *crashPoints) hooks() *outbox.TestHooks {
	return &outbox.TestHooks{
		BeforeIntakeCommit: func(string) { p.hit("intake-before-commit") },
		AfterIntakeCommit:  func(string) { p.hit("intake-after-commit") },
		AfterHandler:       func(string, uint64) { p.hit("handler-returned") },
		BeforeAckCommit:    func(string, uint64) { p.hit("ack-before-commit") },
		AfterAckCommit:     func(string, uint64) { p.hit("ack-after-commit") },
	}
}

// A crash at any point between the handler running and the ack committing
// redelivers the message, with the same IdempotencyKey; a crash after the ack
// committed does not (at-least-once, never lost).
func TestOutbox_RedeliveryAfterCrashBeforeAck(t *testing.T) {
	cp := newCrashPoints()
	rc1 := &recorder{}
	r1 := newRig(t, rigOpts{fs: cp.fs, hooks: cp.hooks(), register: consumeInto(rc1, consumerN)})
	r1.stage("batmobile", Dispatch{Villain: "Two-Face", Block: 3})
	eventually(t, "the first delivery and its ack", func() bool { return rc1.count() == 1 && cp.reached("ack-after-commit") })
	first := rc1.all()[0]

	for _, tc := range []struct {
		point     string
		redeliver bool
	}{
		{"handler-returned", true}, {"ack-before-commit", true}, {"ack-after-commit", false},
	} {
		for i, clone := range cp.at(t, tc.point) {
			t.Run(tc.point+"/"+string(rune('a'+i)), func(t *testing.T) {
				rc2 := &recorder{}
				_ = newRig(t, rigOpts{fs: clone, register: consumeInto(rc2, consumerN)})
				if tc.redeliver {
					eventually(t, "the redelivery", func() bool { return rc2.count() >= 1 })
					stays(t, "exactly one redelivery", 100*time.Millisecond, func() bool { return rc2.count() == 1 })
					got := rc2.all()[0]
					if got.ID != first.ID || got.Seq != first.Seq || got.Villain != "Two-Face" {
						t.Fatalf("redelivered %+v, first was %+v", got, first)
					}
					return
				}
				stays(t, "no redelivery of an acked message", 150*time.Millisecond, func() bool { return rc2.count() == 0 })
			})
		}
	}
}

// A crash at either end of the intake batch neither loses the message nor
// numbers it twice: the clone before the commit still has the intake row and
// no seq, the clone after has the seq and no intake row, and both deliver
// seq 1 once and number the next message 2.
func TestOutbox_IntakeCrashNoReissue(t *testing.T) {
	cp := newCrashPoints()
	rc1 := &recorder{}
	r1 := newRig(t, rigOpts{fs: cp.fs, hooks: cp.hooks(), register: consumeInto(rc1, consumerN)})
	r1.stage("batmobile", Dispatch{Villain: "Bane"})
	eventually(t, "the first message", func() bool { return rc1.count() == 1 && cp.reached("intake-after-commit") })

	for _, point := range []string{"intake-before-commit", "intake-after-commit"} {
		for i, clone := range cp.at(t, point) {
			t.Run(point+"/"+string(rune('a'+i)), func(t *testing.T) {
				rc2 := &recorder{}
				r2 := newRig(t, rigOpts{fs: clone, register: consumeInto(rc2, consumerN), noRelay: true})
				st := outboxstore.New(r2.outKS)
				hi, _, err := st.ReadHigh(context.Background(), noticeName)
				must(t, err)
				intake := r2.count(outboxstore.InPrefix(noticeName))
				msgs := r2.count(outboxstore.MsgTopicPrefix(noticeName))
				switch point {
				case "intake-before-commit":
					if hi.High != 0 || intake != 1 || msgs != 0 {
						t.Fatalf("before the commit: high=%d intake=%d messages=%d", hi.High, intake, msgs)
					}
				default:
					if hi.High != 1 || intake != 0 || msgs != 1 {
						t.Fatalf("after the commit: high=%d intake=%d messages=%d", hi.High, intake, msgs)
					}
				}
				r2.startRelay()
				eventually(t, "the delivery", func() bool { return rc2.count() == 1 })
				if got := rc2.all()[0]; got.Seq != 1 || got.Villain != "Bane" {
					t.Fatalf("delivery = %+v", got)
				}
				r2.stage("batmobile", Dispatch{Villain: "Ra's"})
				eventually(t, "the next message", func() bool { return rc2.count() == 2 })
				if got := rc2.all()[1]; got.Seq != 2 {
					t.Fatalf("the next message was numbered %d, want 2: a seq was reissued", got.Seq)
				}
				stays(t, "no duplicate", 60*time.Millisecond, func() bool { return rc2.count() == 2 })
			})
		}
	}
}

// A relay whose read of seq/<topic> went stale cannot number a message twice:
// a competing writer commits the same rows between the relay's read and its
// commit, the relay's commit fails, and each message has exactly one seq.
func TestOutbox_ZombieRelayCannotDoubleSequence(t *testing.T) {
	rc := &recorder{}
	var once sync.Once
	var r *rig
	hooks := &outbox.TestHooks{BeforeIntakeCommit: func(topic string) {
		once.Do(func() {
			// The zombie: another writer reads the same intake rows and commits first.
			st := outboxstore.New(r.outKS)
			rows, _, err := st.ScanIntake(context.Background(), topic, time.Now(), 256, 1<<20)
			must(t, err)
			b := st.OpenBatch()
			defer b.Close()
			_, err = st.StageSequence(context.Background(), b, topic, 2, rows)
			must(t, err)
			must(t, b.Commit(context.Background(), storage.Sync))
		})
	}}
	r = newRig(t, rigOpts{partitions: 2, hooks: hooks, register: consumeInto(rc, consumerN)})
	r.stage("batmobile", Dispatch{Villain: "Killer Croc", Block: 1})
	eventually(t, "the delivery", func() bool { return rc.count() >= 1 })
	stays(t, "exactly one delivery", 100*time.Millisecond, func() bool { return rc.count() == 1 })
	if got := rc.all()[0]; got.Seq != 1 {
		t.Fatalf("the message was numbered %d, want 1", got.Seq)
	}
	hi, _, err := outboxstore.New(r.outKS).ReadHigh(context.Background(), noticeName)
	must(t, err)
	if hi.High != 1 {
		t.Fatalf("high-water mark = %d, want 1", hi.High)
	}
}

// A ConsumeTx handler's write and the ack are one batch: a crash before the
// commit leaves neither, so the redelivery applies the effect once; a crash
// after leaves both, so nothing is redelivered. A handler that fails after
// staging leaves nothing.
func TestOutbox_TxHandlerEffectivelyOnce(t *testing.T) {
	var counter sync.Mutex
	runs := 0
	register := func(fail *bool) func(testing.TB, *rig) []registration {
		return func(_ testing.TB, r *rig) []registration {
			h := outbox.ConsumeTx(notice(), consumerN, func(_ context.Context, tx *outbox.Tx, m outbox.Message[Dispatch]) error {
				counter.Lock()
				runs++
				n := runs
				counter.Unlock()
				w := tx.Batch().Bind(r.gothamKS).In(r.gothamKS)
				if err := w.Set([]byte("effect/"+m.IdempotencyKey+"/"+string(rune('0'+n))), []byte(m.Payload.Villain)); err != nil {
					return err
				}
				if fail != nil && *fail {
					return outbox.ErrPermanent
				}
				return nil
			})
			return []registration{{owner: "core", hs: []outbox.Handler{h}}}
		}
	}
	effects := func(r *rig) int {
		n := 0
		for _, err := range r.gothamKS.Scan(context.Background(), storage.ScanOptions{Start: []byte("effect/"), End: []byte("effect0"), KeysOnly: true}) {
			must(t, err)
			n++
		}
		return n
	}

	cp := newCrashPoints()
	r1 := newRig(t, rigOpts{fs: cp.fs, hooks: cp.hooks(), register: register(nil)})
	r1.stage("batmobile", Dispatch{Villain: "Mad Hatter"})
	eventually(t, "the effect and the ack", func() bool { return cp.reached("ack-after-commit") })
	eventually(t, "one effect", func() bool { return effects(r1) == 1 })

	t.Run("crash before the commit", func(t *testing.T) {
		for _, clone := range cp.at(t, "ack-before-commit") {
			r2 := newRig(t, rigOpts{fs: clone, register: register(nil), noRelay: true})
			if n := effects(r2); n != 0 {
				t.Fatalf("the effect of an uncommitted batch is visible (%d)", n)
			}
			r2.startRelay()
			eventually(t, "the redelivery's effect", func() bool { return effects(r2) == 1 })
			stays(t, "one effect only", 100*time.Millisecond, func() bool { return effects(r2) == 1 })
		}
	})
	t.Run("crash after the commit", func(t *testing.T) {
		for _, clone := range cp.at(t, "ack-after-commit") {
			r2 := newRig(t, rigOpts{fs: clone, register: register(nil), noRelay: true})
			if n := effects(r2); n != 1 {
				t.Fatalf("the committed effect is missing (%d)", n)
			}
			r2.startRelay()
			stays(t, "no second effect", 150*time.Millisecond, func() bool { return effects(r2) == 1 })
		}
	})
	t.Run("a failing handler leaves nothing and dead-letters when permanent", func(t *testing.T) {
		fail := true
		r := newRig(t, rigOpts{register: register(&fail)})
		r.stage("batmobile", Dispatch{Villain: "Clayface"})
		eventually(t, "the dead letter", func() bool {
			l, _, err := r.ob.ListDead(context.Background(), "", 10)
			must(t, err)
			return len(l) == 1
		})
		if n := effects(r); n != 0 {
			t.Fatalf("a failed handler's staged write was committed (%d)", n)
		}
	})
}

// A ConsumeIntoTask consumer's run and cursor advance are one batch: a crash
// before the commit leaves neither a queued run nor an ack, so the redelivery
// starts exactly one run; after the commit it starts none; and no idempotency
// record is written (the cursor is the dedup).
func TestOutbox_ConsumeIntoTaskStartsOnce(t *testing.T) {
	runsOf := func(r *rig) int { return len(r.runsOf(runKind)) }
	register := func(testing.TB, *rig) []registration {
		h := outbox.ConsumeIntoTask(signal(), consumerC, runKind, func(m outbox.Message[Dispatch]) (any, error) {
			return respondParams{Villain: m.Payload.Villain, Block: m.Payload.Block}, nil
		}, outbox.Live())
		return []registration{{owner: "gotham", hs: []outbox.Handler{h}}}
	}
	rs := &responder{}
	cp := newCrashPoints()
	r1 := newRig(t, rigOpts{fs: cp.fs, hooks: cp.hooks(), register: register, defs: []tasks.Definition{rs.def()}})
	r1.stageGotham("batmobile", Dispatch{Villain: "Poison Ivy", Block: 9})
	eventually(t, "one run", func() bool { return len(rs.all()) == 1 && cp.reached("ack-after-commit") })
	if got := rs.all()[0]; got.Villain != "Poison Ivy" || got.Block != 9 {
		t.Fatalf("run params = %+v", got)
	}
	if n := r1.countIn(r1.taskKS, taskstore.IdemPrefix("")); n != 0 {
		t.Fatalf("%d idempotency records written by an outbox-triggered start", n)
	}

	for _, point := range []string{"ack-before-commit", "ack-after-commit"} {
		for i, clone := range cp.at(t, point) {
			t.Run(point+"/"+string(rune('a'+i)), func(t *testing.T) {
				rs2 := &responder{}
				r2 := newRig(t, rigOpts{fs: clone, register: register, defs: []tasks.Definition{rs2.def()}, noRelay: true})
				before := runsOf(r2)
				if (point == "ack-before-commit" && before != 0) || (point == "ack-after-commit" && before != 1) {
					t.Fatalf("queued runs in the %s clone = %d", point, before)
				}
				r2.startRelay()
				stays(t, "one run in total", 300*time.Millisecond, func() bool { return runsOf(r2) <= 1 })
				eventually(t, "the run", func() bool { return runsOf(r2) == 1 })
				if n := r2.countIn(r2.taskKS, taskstore.IdemPrefix("")); n != 0 {
					t.Fatalf("%d idempotency records", n)
				}
			})
		}
	}
}
