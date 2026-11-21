// file: core/backup/journal/unapplied_rows.go
// version: 0.1.0
// guid: 7b3e9d52-4a18-4c6f-8e07-d1f2a95c6b43
// last-edited: 2026-11-21

package journal

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/storage"
)

// UnappliedRow is one stored entry of the unapplied queue as the operator's
// tool (storage journal-queue) sees it. ReadUnapplied cannot serve that tool:
// it fails on the first entry that does not decode, and an entry that does not
// decode is exactly what the tool exists to remove (it fails every boot).
type UnappliedRow struct {
	// Key is the stored key, journal_unapplied/<seq>.
	Key string
	// Seq is the seq the key names; 0 when it names none (Malformed).
	Seq uint64
	// Malformed is true when the key's suffix is not the 20-digit seq the
	// queue writes. Such a row can be listed but not named, so not cleared.
	Malformed bool
	// Entry is the decoded entry; nil when Err is set.
	Entry *Unapplied
	// Err is the decode failure of a poison entry (never the value: the
	// decoder's text can quote it, so a caller prints the key and the fact).
	Err error
}

// ScanUnapplied lists every entry of the queue in key order, decodable or not.
// A row that does not decode is returned with Err, not as a failure of the scan;
// a failure of the scan itself is an error.
func ScanUnapplied(ctx context.Context, ks storage.Reader) ([]UnappliedRow, error) {
	var out []UnappliedRow
	for kv, err := range ks.Scan(ctx, storage.ScanOptions{Start: []byte(unappliedPrefix), End: []byte(unappliedPrefix + "\xff")}) {
		if err != nil {
			return nil, fmt.Errorf("journal: scan %s: %w", unappliedPrefix, err)
		}
		row := UnappliedRow{Key: string(kv.Key)}
		seq, perr := strconv.ParseUint(strings.TrimPrefix(row.Key, unappliedPrefix), 10, 64)
		if perr != nil || string(unappliedKey(seq)) != row.Key {
			row.Malformed = true
		} else {
			row.Seq = seq
		}
		if u, derr := decodeUnapplied(kv.Key, kv.Value); derr != nil {
			row.Err = derr
		} else {
			row.Entry = &u
		}
		out = append(out, row)
	}
	return out, nil
}

// StageUnappliedRowDelete stages the removal of the stored row r into b, by the
// key the row has. It is for the operator's tool only: a row that does not
// decode, or whose key and entry name different seqs, cannot be removed by
// StageUnappliedDelete (which builds the key from a seq), and such a row is
// what fails every boot. A row whose key is not under the queue's prefix is
// refused, so the tool can never name another row.
func StageUnappliedRowDelete(b *storage.Batch, ks storage.Keyspace, r UnappliedRow) error {
	if !strings.HasPrefix(r.Key, unappliedPrefix) || len(r.Key) == len(unappliedPrefix) {
		return fmt.Errorf("journal: %q is not a key of the unapplied queue", r.Key)
	}
	if err := bind(b, ks); err != nil {
		return err
	}
	return b.In(ks).Delete([]byte(r.Key))
}
