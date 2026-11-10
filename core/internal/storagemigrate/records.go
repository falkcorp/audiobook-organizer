// file: core/internal/storagemigrate/records.go
// version: 1.0.0
// guid: e8ee65f7-200a-40bf-8fad-a457eed5916a
// last-edited: 2026-11-10

package storagemigrate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
)

// storageOwner is storage's own keyspace c/storage/, where every record of
// a data migration lives (§5S.3.6).
const storageOwner storage.Owner = "core/storage"

// The records of migration id, relative to c/storage/ (notation §5S.3.3):
// the state record storage.DataMigrationKey(id), and beneath it the plan,
// one cursor per shard, one record per held item and Verify's report.
func stateKey(id string) []byte  { return storage.DataMigrationKey(id) }
func planKey(id string) []byte   { return sub(id, keys.Str("plan")) }
func reportKey(id string) []byte { return sub(id, keys.Str("report")) }
func shardKey(id string, n int) []byte {
	return sub(id, keys.Str("shard"), keys.U64(uint64(n)))
}
func heldKey(id string, key []byte) []byte { return sub(id, keys.Str("held"), keys.Bytes(key)) }

func sub(id string, segs ...keys.Seg) []byte {
	return append(stateKey(id), keys.Join(segs...)...)
}

// planRecord is the persisted shard plan {n, boundaries} (DATA-12): shard i
// is [Boundaries[i-1], Boundaries[i]) intersected with the source, the
// first open below and the last open above. Workers is the count it was
// planned for (diagnostics only: a resume reuses the plan whatever the
// count is now).
type planRecord struct {
	Shards     int       `json:"shards"`
	Boundaries [][]byte  `json:"boundaries"`
	Workers    int       `json:"workers"`
	At         time.Time `json:"at"`
	Build      string    `json:"build"`
}

// cursorRecord is a shard's cursor: the last key processed, relative to the
// source handle, committed in the batch of the chunk that processed it.
type cursorRecord struct {
	Last []byte `json:"last"`
}

// heldRecord is one held item.
type heldRecord struct {
	Reason string    `json:"reason"`
	Shard  int       `json:"shard"`
	At     time.Time `json:"at"`
}

// reportRecord is Verify's last report, kept beside the state record.
type reportRecord struct {
	State  string                   `json:"state"`
	Report datamigrate.VerifyReport `json:"report"`
	Error  string                   `json:"error,omitempty"`
	At     time.Time                `json:"at"`
}

func planCodec() codec.Codec[planRecord]     { return codec.JSON[planRecord](1) }
func cursorCodec() codec.Codec[cursorRecord] { return codec.JSON[cursorRecord](1) }
func heldCodec() codec.Codec[heldRecord]     { return codec.JSON[heldRecord](1) }
func reportCodec() codec.Codec[reportRecord] { return codec.JSON[reportRecord](1) }

// ErrBookkeepingUnreadable: a record of a data migration does not decode.
// The boot fails: a guessed plan or cursor could process an item twice or
// skip one. Name coined by F2-06.
var ErrBookkeepingUnreadable = errors.New("storagemigrate: data migration record unreadable")

// read decodes the record at key; found is false when it is absent.
func read[T any](ctx context.Context, ks storage.Keyspace, key []byte, c codec.Codec[T]) (v T, found bool, err error) {
	raw, err := ks.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	v, err = c.Decode(raw)
	if err != nil {
		return v, false, fmt.Errorf("%w: c/storage/%q: %w", ErrBookkeepingUnreadable, key, err)
	}
	return v, true, nil
}

// stage encodes v into b at key through ks.
func stage[T any](b *storage.Batch, ks storage.Keyspace, key []byte, c codec.Codec[T], v T) error {
	raw, err := c.Encode(v)
	if err != nil {
		return err
	}
	return b.In(ks).Set(key, raw)
}

// stats are one migration's counters for the metrics (§5S.8): the items per
// outcome this boot, the state it ended in, the time it took.
type stats struct {
	mu       sync.Mutex
	counts   map[string]int64
	state    string
	ran      bool
	took     time.Duration
	chunks   int64
	resumed  bool
	shards   int
	finished time.Time
}

func newStats() *stats { return &stats{counts: map[string]int64{}} }

func (s *stats) add(counts map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range counts {
		s.counts[k] += v
	}
	s.chunks++
}

// Stats is one migration's outcome of this boot, for the kernel's metrics
// (<ns>_storage_data_migration_items_total{migration,outcome},
// <ns>_storage_data_migration_state{migration,state}, and
// <ns>_kernel_boot_phase_seconds{phase="P4b",step=<id>}). Name coined by
// F2-06.
type Stats struct {
	ID string
	// State is the state the migration ended this boot in: done, failed,
	// running (a boot that failed or stopped mid-way), or, for one that did
	// not run, its stored state (pending when absent; held when storage
	// deferred it).
	State string
	// Ran: the boot called the runner for it. Took is then its duration.
	Ran  bool
	Took time.Duration
	// Items counts the items of this boot per outcome.
	Items map[string]int64
	// Chunks is the chunks committed this boot; Shards the plan's count.
	Chunks int64
	Shards int
}

func (s *stats) snapshot(id string) Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{ID: id, State: s.state, Ran: s.ran, Took: s.took, Items: maps.Clone(s.counts), Chunks: s.chunks, Shards: s.shards}
}

// Report is every registered migration's Stats after the boot's DAG ran:
// the ones the boot ran as they ended, the rest from their stored state
// (held when storage deferred them this boot, db.Plan's deferred list).
func (r *Runner) Report(ctx context.Context, db *storage.DB) ([]Stats, error) {
	sks, ok := db.Keyspace(storageOwner)
	if !ok {
		return nil, fmt.Errorf("storagemigrate: the store has no %s keyspace", storageOwner)
	}
	_, deferred := db.Plan()
	held := map[string]bool{}
	for _, d := range deferred {
		if d.DataMigration != "" {
			held[d.DataMigration] = true
		}
	}
	out := make([]Stats, 0, len(r.migs))
	for _, mg := range r.migs {
		st := mg.stats.snapshot(mg.m.ID)
		if !st.Ran {
			rec, found, err := read(ctx, sks, stateKey(mg.m.ID), storage.DataMigrationCodec())
			switch {
			case err != nil:
				return nil, err
			case found:
				st.State = rec.State
			default:
				st.State = datamigrate.StatePending
			}
			if held[mg.m.ID] && st.State != datamigrate.StateDone {
				st.State = datamigrate.StateHeld
			}
		}
		out = append(out, st)
	}
	return out, nil
}
