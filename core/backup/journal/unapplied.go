// file: core/backup/journal/unapplied.go
// version: 1.0.0
// guid: 5382e06d-ca9d-4f14-bb33-01b6760604b6
// last-edited: 2026-11-12

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// The queue of unapplied revocation lines, c/backup/journal_unapplied/<seq>.
//
// A revocation line of a kind this build does not know was written by a newer
// build. This build cannot apply it, but it must not stop: holding the store's
// head below that line would make every later boot re-apply the lines after it
// (undoing an admin's re-enable, a new password, a re-enabled key), refuse the
// journal for good and fail closed again each time. So the head passes the line,
// and the line is recorded here first, durably, so that nothing is lost by
// that: the first boot of a build that knows its kind applies it and deletes
// the entry in the same batch (the kernel's drain). An entry is a pointer into
// the journal, not a copy: (seq, mac) says which line, and a drain applies
// only a line the verified chain holds at that seq with that mac.

// Unapplied is one entry of the queue.
type Unapplied struct {
	// Seq and MAC name the journal line this entry stands for.
	Seq uint64 `json:"seq"`
	MAC string `json:"mac"`
	// Kind is the kind this build could not apply, for the operator reading the
	// queue; the drain re-reads it from the line.
	Kind Kind `json:"kind"`
	// At is the line's own timestamp.
	At time.Time `json:"at"`
	// Closed is true once the boot that queued the line failed closed for it:
	// the store may lack a revocation it cannot name, and one fail-closed pass
	// per entry (not per boot) is what answers that.
	Closed bool `json:"closed"`
}

// UnappliedOf is the entry for the line l.
func UnappliedOf(l Line, closed bool) Unapplied {
	return Unapplied{Seq: l.Seq, MAC: l.MAC, Kind: l.Kind, At: l.At.UTC(), Closed: closed}
}

// unappliedKey is c/backup/journal_unapplied/<seq> with the seq in fixed width,
// so a scan lists the entries in journal order.
func unappliedKey(seq uint64) []byte { return []byte(fmt.Sprintf("%s%020d", unappliedPrefix, seq)) }

func decodeUnapplied(key, raw []byte) (Unapplied, error) {
	var u Unapplied
	if err := json.Unmarshal(raw, &u); err != nil {
		return Unapplied{}, fmt.Errorf("journal: decode %s: %w", key, err)
	}
	return u, nil
}

// StageUnapplied stages u into b. An entry the store already holds for the same
// line (seq and mac) is left as it is, whatever u says: a boot that crashed
// after queueing a line and is queueing it again must not take back an earlier
// boot's Closed. An entry for another line at that seq (a journal that was
// replaced) is replaced by u. The committed entry is read outside b: only the
// boot writes entries, one at a time.
func StageUnapplied(ctx context.Context, b *storage.Batch, ks storage.Keyspace, u Unapplied) error {
	if err := bind(b, ks); err != nil {
		return err
	}
	key := unappliedKey(u.Seq)
	switch raw, err := ks.Get(ctx, key); {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return fmt.Errorf("journal: read %s: %w", key, err)
	default:
		held, err := decodeUnapplied(key, raw)
		if err != nil {
			return err
		}
		if held.MAC == u.MAC {
			return nil
		}
	}
	val, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return b.In(ks).Set(key, val)
}

// StageUnappliedDelete stages the removal of the entry for seq into b: the
// drain stages it in the batch that applies the line, so the line is applied and
// its entry gone together or neither.
func StageUnappliedDelete(b *storage.Batch, ks storage.Keyspace, seq uint64) error {
	if err := bind(b, ks); err != nil {
		return err
	}
	return b.In(ks).Delete(unappliedKey(seq))
}

// ReadUnapplied lists the queue in journal order. An entry that does not decode
// is an error, like any other c/backup/ record: it names a revocation nobody
// can identify, which a boot must not step over.
func ReadUnapplied(ctx context.Context, ks storage.Reader) ([]Unapplied, error) {
	var out []Unapplied
	for kv, err := range ks.Scan(ctx, storage.ScanOptions{Start: []byte(unappliedPrefix), End: []byte(unappliedPrefix + "\xff")}) {
		if err != nil {
			return nil, fmt.Errorf("journal: scan %s: %w", unappliedPrefix, err)
		}
		u, err := decodeUnapplied(kv.Key, kv.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// MarkUnappliedClosed records, synchronously, that the boot failed closed for
// the entries of lines seqs: it sets Closed on each entry still there with the
// same mac as the one the caller read (an entry that is gone, or replaced by
// another line's, is left alone). The entries are plain writes, not Serialized,
// and only the boot writes them, so there is no conflict to retry. Not atomic
// with the fail-closed pass it follows, on purpose: a crash between them repeats
// the pass at the next boot (at least once), which narrows and is idempotent.
func MarkUnappliedClosed(ctx context.Context, ks storage.Keyspace, entries []Unapplied) (err error) {
	if len(entries) == 0 {
		return nil
	}
	b := ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	n := 0
	for _, e := range entries {
		key := unappliedKey(e.Seq)
		raw, err := ks.Get(ctx, key)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("journal: read %s: %w", key, err)
		}
		held, err := decodeUnapplied(key, raw)
		if err != nil {
			return err
		}
		if held.MAC != e.MAC || held.Closed {
			continue
		}
		held.Closed = true
		val, err := json.Marshal(held)
		if err != nil {
			return err
		}
		if err := b.In(ks).Set(key, val); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return nil
	}
	return b.Commit(ctx, storage.Sync)
}
