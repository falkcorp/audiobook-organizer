// file: core/storage/quarantine_test.go
// version: 1.0.0
// guid: 52fb9c5e-d23f-4e22-ae1e-8b98932e1188
// last-edited: 2026-11-11

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/keys"
)

// vault is a fake sealer, unsealer and secret deleter in one: it stages a
// sealed record into the chunk's batch (reversed bytes under sealed/<name>)
// and remembers what it was asked, so a test sees what a real keyring
// adapter would receive.
type vault struct {
	mu        sync.Mutex
	sealed    map[string][]byte
	deleted   []string
	sealErr   error
	unsealErr error
	deleteErr error
}

func newVault() *vault { return &vault{sealed: map[string][]byte{}} }

func (v *vault) Seal(_ context.Context, b *Batch, name string, raw []byte) error {
	if v.sealErr != nil {
		return v.sealErr
	}
	v.mu.Lock()
	v.sealed[name] = slices.Clone(raw)
	v.mu.Unlock()
	rev := slices.Clone(raw)
	slices.Reverse(rev)
	return b.Set([]byte("sealed/"+name), rev)
}

func (v *vault) Unseal(_ context.Context, name string) ([]byte, error) {
	if v.unsealErr != nil {
		return nil, v.unsealErr
	}
	raw, ok := v.sealed[name]
	if !ok {
		return nil, errors.New("no sealed secret " + name)
	}
	return slices.Clone(raw), nil
}

func (v *vault) DeleteSealed(_ context.Context, b *Batch, name string) error {
	if v.deleteErr != nil {
		return v.deleteErr
	}
	v.deleted = append(v.deleted, name)
	return b.Delete([]byte("sealed/" + name))
}

// quarantineRow commits one quarantined row through QuarantineWriter.
func quarantineRow(t *testing.T, db *DB, sealer QuarantineSealer, step string, key, raw []byte, reason string, sensitive bool) error {
	t.Helper()
	ctx := context.Background()
	b := db.storageKS().NewBatch()
	defer func() { _ = b.Close() }()
	if err := QuarantineWriter(sealer)(ctx, b, step, key, raw, reason, sensitive); err != nil {
		return err
	}
	return b.Commit(ctx, Sync)
}

// quarantineDB is a booted store holding Batman-themed quarantined rows: two
// of step "gotham.import" (the second sensitive and sealed) and one of
// "arkham.import".
func quarantineDB(t *testing.T) (*DB, *vault) {
	t.Helper()
	db := newEnv(t, cpOwners()...).open()
	v := newVault()
	for _, r := range []struct {
		step, key, raw, reason string
		sensitive              bool
	}{
		{"gotham.import", "joker:1", "ha", "bad json", false},
		{"gotham.import", "joker:secret", "batcave-door-code", "field password does not decode", true},
		{"arkham.import", "arkham:7", "two-face", "unknown mode", false},
	} {
		if err := quarantineRow(t, db, v, r.step, []byte(r.key), []byte(r.raw), r.reason, r.sensitive); err != nil {
			t.Fatal(err)
		}
	}
	return db, v
}

// TestQuarantineWriterRecordsListsAndCounts (05-storage §5S.3.13): rows
// quarantined through the writer list in step then number order with their
// reason and build; a plain row keeps its value, a sensitive row keeps none
// and names the secret core.storage.quarantine.row-<n> its bytes were sealed
// as, in the same batch; the counts are per step and QuarantineSteps sorts
// them.
func TestQuarantineWriterRecordsListsAndCounts(t *testing.T) {
	ctx := context.Background()
	db, v := quarantineDB(t)
	all, err := db.Quarantine(ctx, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("Quarantine(all) = %d entries, %v; want 3", len(all), err)
	}
	// arkham sorts before gotham; numbers follow the order the rows were written.
	if all[0].Record.Step != "arkham.import" || all[1].Record.Step != "gotham.import" || all[2].Record.Step != "gotham.import" {
		t.Fatalf("steps in order = %s, %s, %s", all[0].Record.Step, all[1].Record.Step, all[2].Record.Step)
	}
	if all[1].N >= all[2].N {
		t.Errorf("gotham's numbers %d, %d are not ascending", all[1].N, all[2].N)
	}
	plain, sensitive := all[1], all[2]
	if plain.Sealed() || string(plain.Record.Value) != "ha" || string(plain.Record.Key) != "joker:1" || plain.Record.Reason != "bad json" || plain.Record.Build != "batcomputer-1" {
		t.Errorf("plain row = %+v", plain)
	}
	wantSecret := QuarantineSecretName(sensitive.N)
	if wantSecret != "core.storage.quarantine.row-"+itoa(sensitive.N) {
		t.Errorf("QuarantineSecretName(%d) = %q", sensitive.N, wantSecret)
	}
	if !sensitive.Sealed() || sensitive.Record.Secret != wantSecret || len(sensitive.Record.Value) != 0 || !sensitive.Record.Sensitive {
		t.Errorf("sensitive row = %+v, want sealed as %s with no value", sensitive, wantSecret)
	}
	if string(v.sealed[wantSecret]) != "batcave-door-code" {
		t.Errorf("the sealer received %q for %s", v.sealed[wantSecret], wantSecret)
	}
	if got, err := db.storageKS().Get(ctx, []byte("sealed/"+wantSecret)); err != nil || string(got) != "edoc-rood-evactab" {
		t.Errorf("the sealed record staged in the chunk's batch = %q, %v", got, err)
	}
	gotham, err := db.Quarantine(ctx, "gotham.import")
	if err != nil || len(gotham) != 2 {
		t.Errorf("Quarantine(gotham.import) = %d entries, %v; want 2", len(gotham), err)
	}
	counts, err := db.QuarantineCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["gotham.import"] != (QuarantineCount{Rows: 2, Sensitive: 1}) || counts["arkham.import"] != (QuarantineCount{Rows: 1}) || len(counts) != 2 {
		t.Errorf("counts = %+v", counts)
	}
	if got := QuarantineSteps(counts); !slices.Equal(got, []string{"arkham.import", "gotham.import"}) {
		t.Errorf("QuarantineSteps = %v", got)
	}
	if got := QuarantineSteps(nil); len(got) != 0 {
		t.Errorf("QuarantineSteps(nil) = %v, want none", got)
	}
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }

// TestQuarantineWriterRefusals: a batch with no store, a sensitive row with
// no sealer (the chunk fails and nothing is staged: a sensitive row is never
// kept raw) and a sealer that fails (the cause is wrapped, nothing commits)
// all leave the quarantine empty.
func TestQuarantineWriterRefusals(t *testing.T) {
	ctx := context.Background()
	db := newEnv(t, cpOwners()...).open()
	if err := QuarantineWriter(nil)(ctx, nil, "gotham.import", []byte("k"), []byte("v"), "r", false); err == nil || !strings.Contains(err.Error(), "no store") {
		t.Errorf("nil batch = %v, want the no-store refusal", err)
	}
	if err := QuarantineWriter(nil)(ctx, &Batch{}, "gotham.import", []byte("k"), []byte("v"), "r", false); err == nil || !strings.Contains(err.Error(), "no store") {
		t.Errorf("zero batch = %v, want the no-store refusal", err)
	}
	err := quarantineRow(t, db, nil, "gotham.import", []byte("joker:secret"), []byte("raw"), "r", true)
	mustErrIs(t, err, ErrQuarantineNoSealer)
	if !strings.Contains(err.Error(), "gotham.import") || strings.Contains(err.Error(), "raw") {
		t.Errorf("no-sealer error %q should name the step and never the row's bytes", err)
	}
	v := newVault()
	v.sealErr = errors.New("keyring locked")
	err = quarantineRow(t, db, v, "gotham.import", []byte("joker:secret"), []byte("raw"), "r", true)
	mustErrIs(t, err, v.sealErr)
	if !strings.Contains(err.Error(), "core.storage.quarantine.row-") {
		t.Errorf("seal error %q does not name the secret", err)
	}
	if got, err := db.Quarantine(ctx, ""); err != nil || len(got) != 0 {
		t.Errorf("after refused writes the quarantine holds %d entries, %v; want none", len(got), err)
	}
}

// TestQuarantineListRefusesMalformedRows: a key under quarantine/ that is not
// <step>/<n>, and a record that does not decode, are errors naming the key,
// never skipped.
func TestQuarantineListRefusesMalformedRows(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		key  []byte
		want string
	}{
		"short key":      {keys.Join(keys.Str("quarantine"), keys.Str("gotham.import")), "is not quarantine/<step>/<n>"},
		"undecodable":    {QuarantineKey("gotham.import", 1), "quarantine record"},
		"bad number len": {keys.Join(keys.Str("quarantine"), keys.Str("gotham.import"), keys.Str("one")), "is not quarantine/<step>/<n>"},
	} {
		db := newEnv(t, cpOwners()...).open()
		if err := db.storageKS().Set(ctx, c.key, []byte("not json")); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Quarantine(ctx, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Quarantine = %v, want %q", name, err, c.want)
		}
		if _, err := db.QuarantineCounts(ctx); err == nil {
			t.Errorf("%s: QuarantineCounts swallowed the malformed row", name)
		}
	}
}

// TestExportQuarantineWritesJSONLines: an export is one JSON line per record
// with its value (a sealed row's bytes opened through the unsealer), step
// filtered; a sealed row with no unsealer, or one the unsealer cannot open,
// stops the export with the number written so far rather than omit it.
func TestExportQuarantineWritesJSONLines(t *testing.T) {
	ctx := context.Background()
	db, v := quarantineDB(t)
	var out bytes.Buffer
	n, err := db.ExportQuarantine(ctx, "", v, &out)
	if err != nil || n != 3 {
		t.Fatalf("export = %d, %v; want 3", n, err)
	}
	var recs []QuarantineRecord
	dec := json.NewDecoder(&out)
	for dec.More() {
		var r QuarantineRecord
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 3 || string(recs[0].Value) != "two-face" || string(recs[1].Value) != "ha" || string(recs[2].Value) != "batcave-door-code" {
		t.Fatalf("exported values = %v", recs)
	}
	out.Reset()
	if n, err := db.ExportQuarantine(ctx, "arkham.import", nil, &out); err != nil || n != 1 || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("single-step export = %d, %v, %q; want 1 line without needing an unsealer", n, err, out.String())
	}
	out.Reset()
	n, err = db.ExportQuarantine(ctx, "gotham.import", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "no unsealer") || n != 1 {
		t.Errorf("sealed row without an unsealer = %d, %v; want 1 written then a refusal", n, err)
	}
	v.unsealErr = errors.New("keyring locked")
	n, err = db.ExportQuarantine(ctx, "gotham.import", v, &out)
	mustErrIs(t, err, v.unsealErr)
	if n != 1 {
		t.Errorf("failed unseal returned %d written, want 1", n)
	}
	if _, err := db.ExportQuarantine(ctx, "gotham.import", v, failingWriter{}); err == nil {
		t.Error("an export whose writer fails returned no error")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// TestPurgeQuarantine: a purge deletes one step's records and the sealed
// secret of each sensitive one together with its audit event, in one
// commit, and leaves other steps alone; it refuses an empty step, a missing
// audit (the rows are unrecoverable), a read-only store, and a sealed row
// with no deleter or a failing one or a failing audit, each deleting nothing.
func TestPurgeQuarantine(t *testing.T) {
	ctx := context.Background()
	db, v := quarantineDB(t)
	audit := func(rows *int) func(b *Batch, n int) error {
		return func(b *Batch, n int) error {
			*rows = n
			return b.Set([]byte("audit/quarantine_purge"), []byte(itoa(uint64(n))))
		}
	}
	var rows int
	remaining := func(step string) int {
		t.Helper()
		got, err := db.Quarantine(ctx, step)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}

	if _, err := db.PurgeQuarantine(ctx, "", v, audit(&rows)); err == nil || !strings.Contains(err.Error(), "no step") {
		t.Errorf("purge of no step = %v", err)
	}
	if _, err := db.PurgeQuarantine(ctx, "gotham.import", v, nil); err == nil || !strings.Contains(err.Error(), "no audit event") {
		t.Errorf("purge with no audit = %v", err)
	}
	if n, err := db.PurgeQuarantine(ctx, "gotham.import", nil, audit(&rows)); n != 0 || err == nil || !strings.Contains(err.Error(), "no secret deleter") {
		t.Errorf("purge of a sealed row with no deleter = %d, %v", n, err)
	}
	v.deleteErr = errors.New("keyring locked")
	if n, err := db.PurgeQuarantine(ctx, "gotham.import", v, audit(&rows)); n != 0 || !errors.Is(err, v.deleteErr) {
		t.Errorf("purge with a failing deleter = %d, %v", n, err)
	}
	v.deleteErr = nil
	auditErr := errors.New("audit sink down")
	if n, err := db.PurgeQuarantine(ctx, "gotham.import", v, func(*Batch, int) error { return auditErr }); n != 0 || !errors.Is(err, auditErr) {
		t.Errorf("purge with a failing audit = %d, %v", n, err)
	}
	if got := remaining("gotham.import"); got != 2 {
		t.Fatalf("refused purges left %d gotham records, want all 2", got)
	}

	if n, err := db.PurgeQuarantine(ctx, "robin.import", v, audit(&rows)); n != 0 || err != nil || rows != 0 {
		t.Errorf("purge of an empty step = %d, %v, audit rows %d; want nothing committed", n, err, rows)
	}
	v.deleted = nil // the refused attempts staged deletes that never committed
	n, err := db.PurgeQuarantine(ctx, "gotham.import", v, audit(&rows))
	if err != nil || n != 2 || rows != 2 {
		t.Fatalf("purge = %d, %v, audit rows %d; want 2 and 2", n, err, rows)
	}
	if remaining("gotham.import") != 0 || remaining("arkham.import") != 1 {
		t.Errorf("after the purge gotham has %d, arkham %d records; want 0 and 1", remaining("gotham.import"), remaining("arkham.import"))
	}
	if len(v.deleted) != 1 || !strings.HasPrefix(v.deleted[0], "core.storage.quarantine.row-") {
		t.Errorf("deleted sealed secrets = %v, want exactly the sensitive row's", v.deleted)
	}
	if _, err := db.storageKS().Get(ctx, []byte("sealed/"+v.deleted[0])); !errors.Is(err, ErrNotFound) {
		t.Errorf("the sealed record survived the purge: %v", err)
	}
	if got, err := db.storageKS().Get(ctx, []byte("audit/quarantine_purge")); err != nil || string(got) != "2" {
		t.Errorf("the audit event committed with the purge = %q, %v", got, err)
	}
}

// TestPurgeQuarantineReadOnly: a read-only store refuses a purge and keeps
// its records.
func TestPurgeQuarantineReadOnly(t *testing.T) {
	ctx := context.Background()
	e := newEnvFS(t, vfs.NewMem(), cpOwners()...)
	db := e.open()
	if err := quarantineRow(t, db, nil, "gotham.import", []byte("joker:1"), []byte("ha"), "bad json", false); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ro := e.opts
	ro.ReadOnly, ro.MustExist = true, true
	o, err := Open(ctx, ro)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	n, err := o.db.PurgeQuarantine(ctx, "gotham.import", nil, func(*Batch, int) error { return nil })
	if n != 0 || err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only purge = %d, %v", n, err)
	}
	if got, err := o.db.Quarantine(ctx, ""); err != nil || len(got) != 1 {
		t.Errorf("the read-only store lists %d records, %v; want 1", len(got), err)
	}
}
