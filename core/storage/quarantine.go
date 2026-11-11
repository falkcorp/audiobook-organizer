// file: core/storage/quarantine.go
// version: 1.0.2
// guid: f747e96f-3ac0-4178-ad12-b685be746c02
// last-edited: 2026-11-11

package storage

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// The quarantine of 05-storage §5S.3.13: a cutover step's chunk that meets a
// source row it cannot convert (one that does not decode, or that an importer
// refused) copies the row into a QuarantineRecord under
// c/storage/quarantine/<step-id>/<n> and deletes the source key, both in the
// chunk's batch. A row under one of the step's Sensitive prefixes is never
// kept raw: its bytes are sealed as the secret record
// core.storage.quarantine.row-<n> through the QuarantineSealer seam, in the
// same batch, and the record names it in Secret with an empty Value.

// QuarantineRecord is one quarantined row (05-storage §5S.3.13). Build is
// the binary ID (app.BinaryID), never a UI build ID.
//
// Reason is the error the step or its importer returned, kept as text in
// the record and printed by storage quarantine list and the dry run's
// report. It must never carry a value: an importer's refusal names the
// field and the rule it broke, never the field's content, because a
// sensitive row's Value is sealed and its Reason is not (F3-06's importer
// contract).
type QuarantineRecord struct {
	Key       []byte    `json:"key"`
	Value     []byte    `json:"value"`
	Secret    string    `json:"secret"`
	Reason    string    `json:"reason"` // never a value; see the type's doc
	Step      string    `json:"step"`
	Sensitive bool      `json:"sensitive"`
	Build     string    `json:"build"`
	At        time.Time `json:"at"`
}

// The cutover's errors (05-storage §5S.3.13). ErrImportConflict lives in
// import_errors.go, where F3-03 declared it first.
var (
	ErrImportRejected     = errors.New("storage: the importer refused the record")
	ErrCutoverBlocked     = errors.New("storage: the cutover is blocked; see storage cutover-blockers")
	ErrRewriteSwappedBack = errors.New("storage: the store rewrite swapped back twice; restore the cutover checkpoint (storage restore-offline, make rollback-restore)")
)

// ErrQuarantineNoSealer: a row under one of a cutover step's Sensitive
// prefixes was quarantined with no QuarantineSealer wired. The chunk fails
// and its source row stays: a sensitive row is never kept raw and never
// dropped.
var ErrQuarantineNoSealer = errors.New("storage: a sensitive row was quarantined with no sealer wired")

// quarantineSequence names the quarantine's Sequence in storage's keyspace.
const quarantineSequence = "quarantine"

// QuarantineKey is record n of step's quarantine, relative to storage's own
// keyspace c/storage/: quarantine/<step>/<n>, n big-endian so a step's
// records list in the order they were numbered.
func QuarantineKey(step string, n uint64) []byte {
	return keys.Join(keys.Str("quarantine"), keys.Str(step), keys.U64(n))
}

// quarantinePrefix is every record of step, or of every step when step is "".
func quarantinePrefix(step string) []byte {
	if step == "" {
		return keys.Prefix(keys.Str("quarantine"))
	}
	return keys.Prefix(keys.Str("quarantine"), keys.Str(step))
}

// QuarantineSecretName is the secret record a sensitive row n is sealed as:
// the secrets.Name string core.storage.quarantine.row-<n>.
func QuarantineSecretName(n uint64) string {
	return "core.storage.quarantine.row-" + strconv.FormatUint(n, 10)
}

// QuarantineSealer seals a sensitive row's raw bytes as the secret record
// name, staging the sealed record into b, the chunk's batch, so the record
// and the source row's delete commit together or not at all. It has the
// shape of secrets.Importer.ImportSecret; storage (L1) cannot import
// secrets (L2), so F3-07 binds it at boot through an adapter over the
// keyring's importer. Type name coined by F3-04.
type QuarantineSealer interface {
	Seal(ctx context.Context, b *Batch, name string, raw []byte) error
}

// QuarantineUnsealer opens a sealed row for `storage quarantine export`.
// Type name coined by F3-04.
type QuarantineUnsealer interface {
	Unseal(ctx context.Context, name string) ([]byte, error)
}

// QuarantineSecretDeleter stages the deletion of a sealed row's secret
// record into b, for `storage quarantine purge`. Type name coined by F3-04.
type QuarantineSecretDeleter interface {
	DeleteSealed(ctx context.Context, b *Batch, name string) error
}

func quarantineCodec() codec.Codec[QuarantineRecord] { return codec.JSON[QuarantineRecord](1) }

// QuarantineWriter is the data-migration runner's quarantine writer
// (internal/storagemigrate QuarantineWriter): for the source row key (an
// absolute key) holding raw, it stages the record of data migration step
// into b, the chunk's batch, under the next number of the quarantine
// sequence; a sensitive row is sealed through sealer into b and its record
// carries only the secret's name. With a nil sealer a sensitive row fails
// (ErrQuarantineNoSealer) before anything is staged. It is safe for
// concurrent use by the runner's shard workers, each with its own batch.
//
// Its one durable write outside b is the quarantine sequence's lease refill
// (Sequence.NextIn commits a new lease on its own first, so a crash only
// skips record numbers, never reuses one). That write is the chunk's: the
// kernel runs this writer inside the chunk's crash-matrix region
// (kernel.DataMigrationsConfig.Quarantine), because the shards commit their
// chunks concurrently and a region is the boot's, not a goroutine's, so a
// lease under a label of its own would be counted as whichever region
// another shard had open. Name coined by F3-04.
func QuarantineWriter(sealer QuarantineSealer) func(ctx context.Context, b *Batch, step string, key, raw []byte, reason string, sensitive bool) error {
	return func(ctx context.Context, b *Batch, step string, key, raw []byte, reason string, sensitive bool) error {
		if b == nil || b.db == nil {
			return errors.New("storage: quarantine into a batch with no store")
		}
		if sensitive && sealer == nil {
			return fmt.Errorf("%w: %s's row %q", ErrQuarantineNoSealer, step, key)
		}
		db := b.db
		ks := db.storageKS()
		n, err := ks.Sequence(quarantineSequence).NextIn(ctx, b)
		if err != nil {
			return fmt.Errorf("storage: quarantine sequence: %w", err)
		}
		rec := QuarantineRecord{Key: slices.Clone(key), Reason: reason, Step: step, Sensitive: sensitive, Build: db.buildID(), At: db.clock().UTC()}
		if sensitive {
			rec.Secret = QuarantineSecretName(n)
			if err := sealer.Seal(ctx, b, rec.Secret, raw); err != nil {
				return fmt.Errorf("storage: seal quarantined row %q as %s: %w", key, rec.Secret, err)
			}
		} else {
			rec.Value = slices.Clone(raw)
		}
		v, err := quarantineCodec().Encode(rec)
		if err != nil {
			return err
		}
		b.Bind(ks)
		return b.In(ks).Set(QuarantineKey(step, n), v)
	}
}

// buildID is the binary ID the boot opened the store with; "" outside a
// boot.
func (db *DB) buildID() string {
	if db.boot == nil {
		return ""
	}
	return db.boot.opts.Build
}

// QuarantineEntry is one record of the quarantine and its number n.
type QuarantineEntry struct {
	N      uint64
	Record QuarantineRecord
}

// Sealed reports whether the row's value is a sealed secret record rather
// than kept in the record.
func (e QuarantineEntry) Sealed() bool { return e.Record.Secret != "" }

// Quarantine lists step's records (every step's when step is ""), in key
// order: by step, then by number. The records hold the raw value of every
// row that is not sealed: a caller that lists for an operator prints keys,
// reasons and sizes, never Value (`storage quarantine list`).
func (db *DB) Quarantine(ctx context.Context, step string) ([]QuarantineEntry, error) {
	var out []QuarantineEntry
	prefix := quarantinePrefix(step)
	for kv, err := range db.storageKS().ScanPrefix(ctx, prefix) {
		if err != nil {
			return nil, err
		}
		segs, err := keys.Split(kv.Key)
		if err != nil || len(segs) != 3 || len(segs[2]) != 8 {
			return nil, fmt.Errorf("storage: quarantine key %q is not quarantine/<step>/<n>", kv.Key)
		}
		rec, err := quarantineCodec().Decode(kv.Value)
		if err != nil {
			return nil, fmt.Errorf("storage: quarantine record %q: %w", kv.Key, err)
		}
		out = append(out, QuarantineEntry{N: binary.BigEndian.Uint64(segs[2]), Record: rec})
	}
	return out, nil
}

// QuarantineCount is one step's quarantine: its records and how many of
// them are sealed sensitive rows.
type QuarantineCount struct {
	Rows      int
	Sensitive int
}

// QuarantineCounts is every step's QuarantineCount, the health token
// storage_quarantine's counts and mediacore_storage_quarantine_rows' value
// (§5S.8); an empty map when nothing is quarantined.
func (db *DB) QuarantineCounts(ctx context.Context) (map[string]QuarantineCount, error) {
	entries, err := db.Quarantine(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[string]QuarantineCount{}
	for _, e := range entries {
		c := out[e.Record.Step]
		c.Rows++
		if e.Record.Sensitive {
			c.Sensitive++
		}
		out[e.Record.Step] = c
	}
	return out, nil
}

// ExportQuarantine writes step's records (every step's when step is "") to
// w as JSON Lines, one QuarantineRecord per line with its value: a sealed
// row's bytes are opened through unseal. With a nil unseal a sealed row is
// refused, so an export never silently omits one. It returns the records
// written.
func (db *DB) ExportQuarantine(ctx context.Context, step string, unseal QuarantineUnsealer, w io.Writer) (int, error) {
	entries, err := db.Quarantine(ctx, step)
	if err != nil {
		return 0, err
	}
	enc := json.NewEncoder(w)
	for i, e := range entries {
		rec := e.Record
		if e.Sealed() {
			if unseal == nil {
				return i, fmt.Errorf("storage: quarantined row %q is sealed as %s and no unsealer is wired", rec.Key, rec.Secret)
			}
			raw, err := unseal.Unseal(ctx, rec.Secret)
			if err != nil {
				return i, fmt.Errorf("storage: unseal %s: %w", rec.Secret, err)
			}
			rec.Value = raw
		}
		if err := enc.Encode(rec); err != nil {
			return i, err
		}
	}
	return len(entries), nil
}

// PurgeQuarantine deletes every record of step and the sealed secret record
// of each sensitive one, in one batch, with audit's event staged into the
// same batch (core.storage.quarantine_purge), and commits it Sync. It
// returns the records deleted; a step with none commits nothing. step must
// name one step, and audit is required: a purge deletes rows the operator
// can never recover, so it never commits without its audit event. A
// read-only store refuses.
func (db *DB) PurgeQuarantine(ctx context.Context, step string, del QuarantineSecretDeleter, audit func(b *Batch, rows int) error) (n int, err error) {
	if step == "" {
		return 0, errors.New("storage: purge names no step")
	}
	if audit == nil {
		return 0, fmt.Errorf("storage: purge of %s's quarantine stages no audit event (core.storage.quarantine_purge)", step)
	}
	if db.readOnly {
		return 0, fmt.Errorf("storage: purge of %s's quarantine on a read-only store", step)
	}
	entries, err := db.Quarantine(ctx, step)
	if err != nil || len(entries) == 0 {
		return 0, err
	}
	ks := db.storageKS()
	b := ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	for _, e := range entries {
		if e.Sealed() {
			if del == nil {
				return 0, fmt.Errorf("storage: quarantined row %q is sealed as %s and no secret deleter is wired", e.Record.Key, e.Record.Secret)
			}
			if err := del.DeleteSealed(ctx, b, e.Record.Secret); err != nil {
				return 0, fmt.Errorf("storage: delete sealed %s: %w", e.Record.Secret, err)
			}
		}
		if err := b.In(ks).Delete(QuarantineKey(step, e.N)); err != nil {
			return 0, err
		}
	}
	if err := audit(b, len(entries)); err != nil {
		return 0, err
	}
	if err := b.Commit(ctx, Sync); err != nil {
		return 0, err
	}
	return len(entries), nil
}

// QuarantineSteps is the steps with records in counts, sorted.
func QuarantineSteps(counts map[string]QuarantineCount) []string {
	out := make([]string, 0, len(counts))
	for s := range counts {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
