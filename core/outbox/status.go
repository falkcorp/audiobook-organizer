// file: core/outbox/status.go
// version: 1.0.0
// guid: 0b7f0c06-1d5b-4e3c-9a64-2f0f9d3a8e64
// last-edited: 2026-11-12

package outbox

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
)

// statCap bounds every count Status makes: a backlog past it is reported as
// "at least", so a status read costs a bounded scan however far behind a
// consumer is.
const statCap = 1000

// Status is the read model of 05-events §5E.11's GET /outbox/consumers: every
// declared consumer, enabled or not, with its lag, pending, dead and backlog
// counts.
type Status struct {
	At        time.Time
	Topics    []TopicStatus
	Consumers []ConsumerStatus
}

// TopicStatus is one durable topic's intake.
type TopicStatus struct {
	Topic         string
	Partitions    int
	High          uint64 // the last seq issued
	IntakePending int    // intake rows not yet sequenced (at most statCap)
	IntakeCapped  bool
	IntakeAge     time.Duration // age of the oldest intake row
}

// ConsumerStatus is one consumer. A consumer that is declared (a manifest or
// the core list) but not registered in this build is a disabled module's: its
// messages are kept for it.
type ConsumerStatus struct {
	Name     string
	Module   string
	Topic    string // "" until the consumer has registered or has a cursor
	Enabled  bool   // registered in this build
	Declared bool   // listed by a manifest or the core list (false: a removed consumer's leftover cursors)

	Pending       int  // messages it has not taken over, retries included (at most statCap per partition)
	PendingCapped bool // some partition had at least statCap
	Retrying      int  // messages under retry
	Lag           time.Duration
	Dead          int
	IntakeBacklog int // intake rows of its topic not yet sequenced
	IntakeCapped  bool
	// RetainedBacklog is the backlog a disabled consumer's absence keeps in the
	// store: its Pending. Zero for an enabled consumer, whose backlog is live.
	RetainedBacklog int
}

// Status reads the outbox's state. Every count is bounded by statCap.
func (o *Outbox) Status(ctx context.Context) (Status, error) {
	st := Status{At: o.now()}
	intake := map[string]TopicStatus{}
	for _, name := range o.sortedTopics() {
		ti, err := o.topic(name)
		if err != nil {
			continue
		}
		ts := TopicStatus{Topic: name, Partitions: ti.partitions}
		hi, _, err := o.store.ReadHigh(ctx, name)
		if err != nil {
			return st, fmt.Errorf("status of %s: %w", name, err)
		}
		ts.High = hi.High
		n, capped, oldest, err := o.store.IntakeStats(ctx, name, statCap)
		if err != nil {
			return st, fmt.Errorf("intake of %s: %w", name, err)
		}
		ts.IntakePending, ts.IntakeCapped = n, capped
		if n > 0 {
			ts.IntakeAge = max(0, st.At.Sub(oldest))
		}
		st.Topics = append(st.Topics, ts)
		intake[name] = ts
	}

	cursors, err := o.store.AllCursors(ctx)
	if err != nil {
		return st, fmt.Errorf("status: cursors: %w", err)
	}
	dead, err := o.store.DeadCounts(ctx)
	if err != nil {
		return st, fmt.Errorf("status: dead letters: %w", err)
	}
	byConsumer := map[string][]outboxstore.CursorRow{}
	for _, c := range cursors {
		byConsumer[c.Pair.Consumer] = append(byConsumer[c.Pair.Consumer], c)
	}

	o.mu.Lock()
	names := map[string]bool{}
	for n := range o.declared {
		names[n] = true
	}
	regs := map[string]*consumerReg{}
	for n, reg := range o.consumer {
		names[n] = true
		regs[n] = reg
	}
	declared := map[string]bool{}
	for n := range o.declared {
		declared[n] = true
	}
	o.mu.Unlock()
	for n := range byConsumer {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	slices.Sort(sorted)

	for _, name := range sorted {
		cs := ConsumerStatus{Name: name, Module: consumerModule(name), Declared: declared[name], Dead: dead[name]}
		if reg := regs[name]; reg != nil {
			cs.Enabled, cs.Topic = true, reg.topic
		}
		rows := byConsumer[name]
		if cs.Topic == "" && len(rows) > 0 {
			cs.Topic = rows[0].Pair.Topic
		}
		var oldest time.Time
		for _, c := range rows {
			if c.Pair.Topic != cs.Topic {
				continue
			}
			n, capped, first, below, retrying, tryOldest, err := o.pairBacklog(ctx, c)
			if err != nil {
				return st, fmt.Errorf("status of %s: %w", name, err)
			}
			cs.Pending += n + below
			cs.PendingCapped = cs.PendingCapped || capped
			cs.Retrying += retrying
			for _, t := range []time.Time{first, tryOldest} {
				if !t.IsZero() && (oldest.IsZero() || t.Before(oldest)) {
					oldest = t
				}
			}
		}
		if !oldest.IsZero() {
			cs.Lag = max(0, st.At.Sub(oldest))
		}
		if ts, ok := intake[cs.Topic]; ok {
			cs.IntakeBacklog, cs.IntakeCapped = ts.IntakePending, ts.IntakeCapped
		}
		if !cs.Enabled {
			cs.RetainedBacklog = cs.Pending
		}
		st.Consumers = append(st.Consumers, cs)
	}
	return st, nil
}

// consumerModule is the module a consumer name belongs to (its first segment).
func consumerModule(name string) string {
	m, _, _ := strings.Cut(name, ".")
	return m
}

// pairBacklog counts what one pair has not taken over: the messages above its
// cursor (n, capped), those under retry at or below it (below: an unordered
// consumer's cursor passes a message that is still being retried), all
// messages under retry (retrying), and the oldest staged time of the first and
// of the retried ones.
func (o *Outbox) pairBacklog(ctx context.Context, c outboxstore.CursorRow) (n int, capped bool, first time.Time, below, retrying int, tryOldest time.Time, err error) {
	n, capped, first, err = o.store.MessageStats(ctx, c.Pair.Topic, c.Pair.PP, c.Cur.Seq, statCap)
	if err != nil {
		return
	}
	var st outboxstore.PairState
	if st, err = o.store.ReadPair(ctx, c.Pair); err != nil {
		return
	}
	retrying = len(st.Try.Entries)
	for _, e := range st.Try.Entries {
		if e.Seq > c.Cur.Seq {
			continue // above the cursor: already counted in n
		}
		below++
		rows, rerr := o.store.MessagesAfter(ctx, c.Pair.Topic, c.Pair.PP, e.Seq-1, 1)
		if rerr != nil {
			err = rerr
			return
		}
		if len(rows) == 1 && rows[0].Seq == e.Seq && (tryOldest.IsZero() || rows[0].Msg.StagedAt.Before(tryOldest)) {
			tryOldest = rows[0].Msg.StagedAt
		}
	}
	return
}
