// file: core/outbox/fault.go
// version: 1.0.0
// guid: 41666c62-b0eb-40fc-ab35-17a0619edae8
// last-edited: 2026-11-12

package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/tasks"
)

// How the relay reports what it cannot do. Nothing it logs about a failure
// carries the failure's text: a decoder's message quotes the value it rejected
// and a row's value is a payload (a handler's error and panic are held to the
// same rule, see errType). A log line names what failed (the consumer, topic,
// partition and, for a row, which row), a closed token for the class of
// failure (faultKind) and the Go type of the error, and that is all.

// faultEvery is how often the relay repeats the same fault in its log; the
// ones in between are counted into the next line.
const faultEvery = time.Minute

// faultKind is the closed token for the class of err.
func faultKind(err error) string {
	switch {
	case errors.Is(err, codec.ErrUnknownVersion):
		return "unknown_version"
	case errors.Is(err, outboxstore.ErrCorruptRow):
		return "corrupt_row"
	case errors.Is(err, outboxstore.ErrPartitionsChanged):
		return "partitions_changed"
	case errors.Is(err, storage.ErrConflict):
		return "conflict"
	case errors.Is(err, tasks.ErrFenced):
		return "fenced"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "error"
}

// errAttrs are the log attributes that describe err without quoting it: its
// class, its Go type and, for a row that did not decode, which row.
func errAttrs(err error) []any {
	attrs := []any{slog.String("kind", faultKind(err)), slog.String("err_type", errType(err))}
	if re, ok := errors.AsType[*outboxstore.RowError](err); ok {
		attrs = append(attrs, slog.String("row", re.Where))
	}
	return attrs
}

// faultNote is what the log holds back about one repeating fault.
type faultNote struct {
	at         time.Time
	suppressed int
}

// faultDue says whether the fault named what should be logged now, and how
// many identical ones were held back since the last line.
func (r *relay) faultDue(what string) (due bool, suppressed int) {
	now := r.o.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.faults[what]
	if ok && now.Sub(n.at) < faultEvery {
		n.suppressed++
		r.faults[what] = n
		return false, 0
	}
	r.faults[what] = faultNote{at: now}
	return true, n.suppressed
}

// fault logs an error that is neither a cancellation nor an expected race,
// once a minute per what.
func (r *relay) fault(what string, err error) {
	if err == nil || r.rc.Context().Err() != nil || r.stopped() {
		return
	}
	due, suppressed := r.faultDue(what)
	if !due {
		return
	}
	attrs := errAttrs(err)
	if suppressed > 0 {
		attrs = append(attrs, slog.Int("repeats_since_last", suppressed))
	}
	r.log.Warn("outbox: "+what+" failed", attrs...)
}

// partitionsOK says whether a topic may be worked on: its messages were
// spread over the partition count the catalog declares now. hi is seq/<topic>
// and found is false for a topic nothing was sequenced on. A mismatch stops the
// topic (delivery, retention, cursor creation and purge all skip it), reports
// core.outbox Degraded and logs once; the stop lifts by itself when the
// declaration matches again.
func (r *relay) partitionsOK(ti *topicInfo, hi outboxstore.High, found bool) bool {
	subject := "topic " + ti.name
	if !found || hi.Partitions == ti.partitions {
		r.mu.Lock()
		was := r.partStop[ti.name]
		if was {
			delete(r.partStop, ti.name)
			delete(r.topicStop, ti.name)
		}
		r.mu.Unlock()
		if was {
			r.o.health.clear(subject)
		}
		return true
	}
	r.mu.Lock()
	r.partStop[ti.name] = true
	r.topicStop[ti.name] = r.o.now()
	r.mu.Unlock()
	if r.o.health.stop(subject, "partitions_changed") {
		r.log.Error("outbox: topic stopped; its partition count changed and its messages are not re-spread",
			slog.String("topic", ti.name), slog.Int("sequenced_over", hi.Partitions), slog.Int("declared", ti.partitions))
	}
	return false
}

// partitionsChanged is the error of a topic whose declared partition count no
// longer matches the one its messages were spread over.
func partitionsChanged(topic string, stamped, declared int) error {
	return fmt.Errorf("outbox: %s was sequenced over %d partitions and now declares %d: %w",
		topic, stamped, declared, outboxstore.ErrPartitionsChanged)
}

// checkPartitions is partitionsOK for an operator call (no relay run): the
// error to refuse with, or nil.
func (o *Outbox) checkPartitions(ctx context.Context, ti *topicInfo) error {
	hi, found, err := o.store.ReadHigh(ctx, ti.name)
	if err != nil {
		return err
	}
	if found && hi.Partitions != ti.partitions {
		return partitionsChanged(ti.name, hi.Partitions, ti.partitions)
	}
	return nil
}
