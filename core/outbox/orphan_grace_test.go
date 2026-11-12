// file: core/outbox/orphan_grace_test.go
// version: 1.0.0
// guid: 19edff98-8d09-411b-a3f8-bfd20c69dc37
// last-edited: 2026-11-12

package outbox_test

import (
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
)

// orphanRig is the build that mentions neither registration nor declaration of
// consumerN3, over the store of a build that had it.
func orphanRig(t *testing.T, cp *crashPoints, clk *skew, rc *recorder) *rig {
	t.Helper()
	return newRig(t, rigOpts{fs: cp.fs, now: clk.now, sweep: sweepFast, cfg: orphanCfg,
		core: []string{consumerN, consumerN2}, register: consumeInto(rc, consumerN)})
}

func orphanCfg(c *outbox.Config) { c.RelayRotate = 24 * time.Hour; c.OrphanRetention = time.Hour }

// A consumer that was idle for longer than orphan_retention is not dropped the
// moment a build without it boots: the grace period runs from the first sweep
// that saw it missing, not from the cursor's last touch, so a rollback or a
// manifest typo does not delete a fresh message the idle consumer still needs.
// The message and the cursors survive until a full grace period of absence has
// passed, then both go.
func TestOutbox_OrphanGraceRunsFromTheFirstSweepThatSawItAbsent(t *testing.T) {
	clk := &skew{}
	cp := newCrashPoints()
	rn, rg := &recorder{}, &recorder{}
	r1 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, sweep: sweepFast, cfg: orphanCfg, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, rn.handler(consumerN)),
			outbox.Consume(notice(), consumerN3, rg.handler(consumerN3)),
		}}}
	}})
	r1.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "both consumers to take the Joker", func() bool { return rn.count() == 1 && rg.count() == 1 })
	eventually(t, "the topic to empty", func() bool { return r1.messages(noticeName) == 0 })
	r1.close()

	// The mirror sat idle for two hours (a quiet topic); then a build that no
	// longer mentions it boots and a message arrives.
	clk.advance(2 * time.Hour)
	rn2 := &recorder{}
	r2 := orphanRig(t, cp, clk, rn2)
	r2.stage("wayne-manor", Dispatch{Villain: "Penguin"})
	eventually(t, "the remaining consumer to take the Penguin", func() bool { return rn2.count() == 1 })
	eventually(t, "the sweep to mark the removed consumer absent", func() bool { return r2.absent(consumerN3) == 2 })

	keep := func() bool { return r2.messages(noticeName) == 1 && r2.cursors(consumerN3) == 2 }
	stays(t, "the fresh message and the idle consumer's cursors kept", 500*time.Millisecond, keep)
	clk.advance(30 * time.Minute)
	stays(t, "everything kept half way through the grace period", 500*time.Millisecond, keep)

	// Something else rewrites the removed consumer's cursors half way through
	// (an operator's purge does): the grace period is measured from when the
	// consumer went missing, not from the cursor's last write, so it does not
	// start over.
	for pp := range 2 {
		rewritePair(t, r2, outboxstore.Pair{Consumer: consumerN3, Topic: noticeName, PP: pp}, clk.now(), func(*outboxstore.PairState) {})
	}
	clk.advance(31 * time.Minute)
	eventually(t, "the cursors to be dropped once the grace period ran out", func() bool { return r2.cursors(consumerN3) == 0 })
	eventually(t, "the message to follow", func() bool { return r2.messages(noticeName) == 0 })
}

// A consumer that comes back loses its absence mark, so the next time it is
// missing the whole grace period starts over, however long ago the first
// absence began.
func TestOutbox_OrphanGraceRestartsWhenTheConsumerComesBack(t *testing.T) {
	clk := &skew{}
	cp := newCrashPoints()
	both := func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, (&recorder{}).handler(consumerN)),
			outbox.Consume(notice(), consumerN3, (&recorder{}).handler(consumerN3)),
		}}}
	}
	r1 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, sweep: sweepFast, cfg: orphanCfg, register: both})
	eventually(t, "both consumers' cursors", func() bool { return r1.cursors(consumerN) == 2 && r1.cursors(consumerN3) == 2 })
	r1.close()

	r2 := orphanRig(t, cp, clk, &recorder{})
	eventually(t, "the sweep to mark the mirror absent", func() bool { return r2.absent(consumerN3) == 2 })
	r2.close()

	// Back again: the mark goes.
	r3 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, sweep: sweepFast, cfg: orphanCfg, register: both})
	eventually(t, "the mark to clear", func() bool { return r3.absent(consumerN3) == 0 })
	r3.close()

	// Away again two hours later: a fresh mark, not the stale one, so the
	// cursors are still there after sweeps.
	clk.advance(2 * time.Hour)
	r4 := orphanRig(t, cp, clk, &recorder{})
	eventually(t, "the sweep to mark the mirror absent again", func() bool { return r4.absent(consumerN3) == 2 })
	stays(t, "the cursors kept: its grace period only just began", 500*time.Millisecond, func() bool { return r4.cursors(consumerN3) == 2 })
}
