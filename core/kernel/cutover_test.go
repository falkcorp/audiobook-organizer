// file: core/kernel/cutover_test.go
// version: 1.0.4
// guid: 09c631c4-1c2d-4d66-ae13-7ee32b295c1e
// last-edited: 2026-11-11

package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// The cutover fixture: the module gcpd with a legacy family under the claim
// "case:" (read-write before the cutover, retired by it). case:file/<n> rows
// import into gcpd's keyspace as case/<n>, every fifth one is quarantined
// as undecodable, and every case:vault/<n> row (Sensitive) is quarantined as
// holding a credential. core/audit is among the owners so a purge can be
// audited.

const cutoverPath = "/batcave/store"

func cutoverOwners(mode storage.ClaimMode) []storage.OwnerSpec {
	return []storage.OwnerSpec{
		{Owner: "gcpd", Enabled: true, Spec: storage.Spec{Keyspace: true, Claims: []storage.Claim{
			{Prefix: "case:", Mode: mode, Description: "the GCPD's case files (a synthetic legacy family)"}}}},
		auditOwnerSpec(),
	}
}

// cutoverRows is the fixture's source rows: files 1..files, vaults 1..vaults.
func cutoverRows(files, vaults int) map[string]string {
	rows := map[string]string{}
	for n := 1; n <= files; n++ {
		rows[fmt.Sprintf("case:file/%03d", n)] = fmt.Sprintf("The Long Halloween, part %d", n)
	}
	for n := 1; n <= vaults; n++ {
		rows[fmt.Sprintf("case:vault/%03d", n)] = fmt.Sprintf("Wayne Manor safe combination %d", n)
	}
	return rows
}

// seedCutover writes the pre-cutover store at cutoverPath on fs.
func seedCutover(t *testing.T, fs vfs.FS, rows map[string]string) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(fs), Owners: cutoverOwners(storage.ClaimReadWrite),
		Build: "batcomputer-1", FreeSpaceMargin: 1})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatal(errors.Join(err, o.Close()))
	}
	lr, _ := db.Legacy("gcpd")
	b := lr.NewBatch()
	for k, v := range rows {
		if err := b.Set([]byte(k), []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(b.Commit(ctx, storage.Sync), db.Close()); err != nil {
		t.Fatal(err)
	}
}

// cutoverHandles is what the step reaches during one boot.
type cutoverHandles struct{ ks storage.Keyspace }

// cutoverMigration is the fixture's cutover step.
func cutoverMigration(h *cutoverHandles) datamigrate.DataMigration {
	return datamigrate.DataMigration{
		ID: "gcpd.case-files", Owner: "gcpd", Description: "import the GCPD's case files",
		Source:      []storage.Range{{Start: []byte("case:"), End: []byte("case;")}},
		Parallelism: tasks.Fixed(2, "test: two disjoint shards of the fixture"), Mode: datamigrate.BootBlocking,
		Compat: storage.Breaking, Cutover: true, Sensitive: []string{"case:vault/"},
		Step: func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			k := string(kv.Key)
			if strings.HasPrefix(k, "case:vault/") {
				return w.Quarantine(kv.Key, kv.Value, "a credential the importer does not carry")
			}
			var n int
			if _, err := fmt.Sscanf(k, "case:file/%03d", &n); err != nil {
				return err
			}
			if n%5 == 0 {
				return w.Quarantine(kv.Key, kv.Value, "Joker venom: the row does not decode")
			}
			b := w.Batch().Bind(h.ks)
			if err := b.In(h.ks).Set([]byte(fmt.Sprintf("case/%03d", n)), kv.Value); err != nil {
				return err
			}
			if err := w.Delete(kv.Key); err != nil {
				return err
			}
			w.Count("imported")
			return nil
		},
	}
}

// fakeSealer seals a row into gcpd's keyspace under sealed/<name>, the bytes
// reversed: a stand-in for the keyring's sealed secret record, staged in the
// chunk's batch. It unseals and deletes the same records.
type fakeSealer struct {
	db     *storage.DB
	sealed int
}

func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[len(b)-1-i] = c
	}
	return out
}

func (f *fakeSealer) ks() storage.Keyspace { ks, _ := f.db.Keyspace("gcpd"); return ks }

func (f *fakeSealer) Seal(_ context.Context, b *storage.Batch, name string, raw []byte) error {
	f.sealed++
	ks := f.ks()
	return b.Bind(ks).In(ks).Set([]byte("sealed/"+name), reverse(raw))
}

func (f *fakeSealer) Unseal(ctx context.Context, name string) ([]byte, error) {
	v, err := f.ks().Get(ctx, []byte("sealed/"+name))
	return reverse(v), err
}

func (f *fakeSealer) DeleteSealed(_ context.Context, b *storage.Batch, name string) error {
	ks := f.ks()
	return b.Bind(ks).In(ks).Delete([]byte("sealed/" + name))
}

// bootCutover opens the cutover build over fs with the quarantine writer
// built from sealer (nil = no sealer), runs P4b and returns the open store
// or the boot's error. sf, when set, learns the store before P4b.
func bootCutover(t *testing.T, fs vfs.FS, sealer storage.QuarantineSealer, sf *fakeSealer) (*storage.DB, error) {
	t.Helper()
	h := &cutoverHandles{}
	return bootWith(t, fs, h, cutoverMigration(h), sealer, sf)
}

// bootWith is bootCutover with the step m, whose handles are h.
func bootWith(t *testing.T, fs vfs.FS, h *cutoverHandles, m datamigrate.DataMigration, sealer storage.QuarantineSealer, sf *fakeSealer) (*storage.DB, error) {
	t.Helper()
	ctx := context.Background()
	owners := cutoverOwners(storage.ClaimRetired)
	cfg := DataMigrationsConfig{ChunkItems: 7, Build: "batcomputer-2", Logger: slog.New(slog.DiscardHandler),
		Quarantine: storage.QuarantineWriter(sealer)}
	dms, err := NewDataMigrations(cfg, owners, m)
	if err != nil {
		t.Fatal(err)
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(fs), Owners: owners,
		Build: "batcomputer-2", FreeSpaceMargin: 1, DataSteps: dms.Steps()})
	if err != nil {
		return nil, err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return nil, errors.Join(err, o.Close())
	}
	h.ks, _ = db.Keyspace("gcpd")
	if sf != nil {
		sf.db = db
	}
	if err := db.Migrate(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// levelOwners is cutoverOwners with gcpd at level 1, raised by its level
// step (storage.LevelStep): the store after M7.
func levelOwners(mode storage.ClaimMode) []storage.OwnerSpec {
	owners := cutoverOwners(mode)
	owners[0].Spec.Level = 1
	owners[0].Spec.Migrations = []storage.Migration{storage.LevelStep("gcpd", 1, 1)}
	return owners
}

// bootOwners boots the store on fs with owners and no data steps, and
// closes it.
func bootOwners(t *testing.T, fs vfs.FS, owners []storage.OwnerSpec) (storage.Levels, error) {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(fs), Owners: owners,
		Build: "batcomputer-2", FreeSpaceMargin: 1})
	if err != nil {
		return nil, err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return nil, errors.Join(err, o.Close())
	}
	if err := db.Migrate(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	levels := o.Levels()
	return levels, db.Close()
}

// TestQuarantineCommands is F3-04's quarantine gate (05-storage §5S.3.13
// "Quarantine", §5S.10): with no sealer wired a sensitive row fails the step
// and its source row stays; with one, a sensitive row is stored only as a
// sealed secret record staged in the chunk's batch, its quarantine record
// naming the secret and holding no value; the listing never shows a value;
// the export writes every value, sensitive ones opened through the
// unsealer; the purge deletes one step's records and their sealed secrets
// with its audit event in the same batch; health carries the warning in
// Detail, never Reason, and clears once the quarantine is empty.
func TestQuarantineCommands(t *testing.T) {
	ctx := context.Background()
	mem := vfs.NewMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	rows := cutoverRows(30, 4)
	seedCutover(t, mem, rows)

	// No sealer: the first sensitive row fails the step, nothing of it is
	// quarantined and its source stays.
	if db, err := bootCutover(t, mem, nil, nil); !errors.Is(err, storage.ErrQuarantineNoSealer) {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("boot with no sealer = %v, want ErrQuarantineNoSealer", err)
	}
	if left := legacyKeys(t, mem, "case:vault/"); len(left) != 4 {
		t.Fatalf("after the refused boot the sensitive source rows are %q, want all 4", left)
	}
	sf := &fakeSealer{}
	db, err := bootCutover(t, mem, sf, sf)
	if err != nil {
		t.Fatalf("boot with the sealer: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lr, _ := db.Legacy("gcpd")
	for kv, err := range lr.Scan(ctx, storage.ScanOptions{Start: []byte("case:"), End: []byte("case;"), KeysOnly: true}) {
		if err != nil {
			t.Fatal(err)
		}
		t.Errorf("source row %s survived the cutover", kv.Key)
	}

	entries, err := db.Quarantine(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var sensitive, plain int
	for _, e := range entries {
		r := e.Record
		want, ok := rows[string(r.Key)]
		if !ok || r.Step != "gcpd.case-files" || r.Build != "batcomputer-2" || r.At.IsZero() {
			t.Errorf("record %d = %+v: not a fixture row of the step with build and time", e.N, r)
			continue
		}
		if strings.HasPrefix(string(r.Key), "case:vault/") {
			sensitive++
			if !r.Sensitive || !e.Sealed() || len(r.Value) != 0 || r.Secret != storage.QuarantineSecretName(e.N) {
				t.Errorf("sensitive record %+v: want sealed as %s with no value", r, storage.QuarantineSecretName(e.N))
			}
			if got, err := sf.Unseal(ctx, r.Secret); err != nil || string(got) != want {
				t.Errorf("sealed secret %s = %q, %v; want the row's bytes", r.Secret, got, err)
			}
		} else {
			plain++
			if r.Sensitive || e.Sealed() || string(r.Value) != want {
				t.Errorf("plain record %+v: want its value and no secret", r)
			}
		}
	}
	if sensitive != 4 || plain != 6 || sf.sealed != 4 {
		t.Fatalf("quarantined %d sensitive (sealed %d) and %d plain, want 4 (4) and 6", sensitive, sf.sealed, plain)
	}
	// No plaintext of a sensitive row anywhere under c/.
	sks, _ := db.Keyspace("core/storage")
	for kv, err := range sks.Scan(ctx, storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(kv.Value, []byte("safe combination")) {
			t.Errorf("c/storage/%s holds a sensitive row's plaintext", kv.Key)
		}
	}

	// Health: the warning in Detail only.
	res, err := StorageHealth(ctx, db, map[string]int64{"gcpd.case-files": 2}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != health.StatusUp || res.Reason != "" || !strings.Contains(res.Detail, "step=gcpd.case-files rows=10 sensitive=4") ||
		!strings.Contains(res.Detail, "orphaned_refs=2") {
		t.Errorf("health = %+v, want Up with the quarantine in Detail only", res)
	}

	// Export: every value, the sensitive ones unsealed.
	var out bytes.Buffer
	n, err := db.ExportQuarantine(ctx, "gcpd.case-files", sf, &out)
	if err != nil || n != 10 {
		t.Fatalf("export = %d, %v; want 10", n, err)
	}
	exported := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var r storage.QuarantineRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("export line %q: %v", line, err)
		}
		exported[string(r.Key)] = string(r.Value)
	}
	for k, v := range exported {
		if rows[k] != v {
			t.Errorf("export of %s = %q, want %q", k, v, rows[k])
		}
	}
	if _, err := db.ExportQuarantine(ctx, "gcpd.case-files", nil, &bytes.Buffer{}); err == nil {
		t.Error("export of sealed rows with no unsealer succeeded")
	}

	// Purge: fails closed with no deleter for sealed rows; then deletes the
	// step's records and sealed secrets with the audit event.
	if _, err := PurgeQuarantine(ctx, db, "gcpd.case-files", nil, "alfred", nil); err == nil {
		t.Fatal("purge of sealed rows with no secret deleter succeeded")
	}
	if got, _ := db.Quarantine(ctx, ""); len(got) != 10 {
		t.Fatalf("a refused purge deleted records: %d left", len(got))
	}
	n, err = PurgeQuarantine(ctx, db, "gcpd.case-files", sf, "alfred", nil)
	if err != nil || n != 10 {
		t.Fatalf("purge = %d, %v; want 10", n, err)
	}
	if got, _ := db.Quarantine(ctx, ""); len(got) != 0 {
		t.Errorf("purge left %d records", len(got))
	}
	for _, e := range entries {
		if e.Sealed() {
			if v, err := sf.ks().Get(ctx, []byte("sealed/"+e.Record.Secret)); !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("sealed secret %s survived the purge: %q, %v", e.Record.Secret, v, err)
			}
		}
	}
	as, err := buildAudit(ctx, db, slog.New(slog.DiscardHandler), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var events int
	for rec, err := range as.Query(ctx, audit.Filter{}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Action == "core.storage.quarantine_purge" && rec.Actor.Username == "alfred" {
			events++
		}
	}
	if events != 1 {
		t.Errorf("purge audit events = %d, want 1", events)
	}
	res, err = StorageHealth(ctx, db, nil, time.Now())
	if err != nil || res.Detail != "" {
		t.Errorf("health after the purge = %+v, %v; want the warning cleared", res, err)
	}
}

// legacyKeys opens the store read-only and lists gcpd's legacy keys under
// prefix.
func legacyKeys(t *testing.T, fs vfs.FS, prefix string) []string {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(fs), Owners: cutoverOwners(storage.ClaimRetired),
		Build: "batcomputer-2", FreeSpaceMargin: 1, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	src, err := o.PreflightSource("gcpd")
	if err != nil {
		t.Fatal(err)
	}
	end := []byte(prefix)
	end[len(end)-1]++
	var out []string
	for kv, err := range src.Scan(ctx, storage.ScanOptions{Start: []byte(prefix), End: end, KeysOnly: true}) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(kv.Key))
	}
	return out
}

// blockedMigration is the fixture's cutover step with a Preflight that
// reads one source row and reports it as a blocker.
func blockedMigration(h *cutoverHandles) datamigrate.DataMigration {
	m := cutoverMigration(h)
	m.Preflight = func(ctx context.Context, r storage.Reader) ([]datamigrate.Blocker, error) {
		if _, err := r.Get(ctx, []byte("case:vault/001")); err != nil {
			return nil, err
		}
		return []datamigrate.Blocker{{Key: []byte("case:vault/001"), Family: "gcpd.vault",
			Reason: "Bane's account merge in progress", Fix: "finish or undo the merge on the pre-freeze release"}}, nil
	}
	return m
}

// preflightOnce opens the store writable with owners and runs the P4a
// preflight of m through DataMigrations, as the boot does before Prepare.
func preflightOnce(t *testing.T, fs vfs.FS, owners []storage.OwnerSpec, m datamigrate.DataMigration, log *slog.Logger) error {
	t.Helper()
	ctx := context.Background()
	dms, err := NewDataMigrations(DataMigrationsConfig{Build: "batcomputer-2", Logger: slog.New(slog.DiscardHandler),
		Preflight: CutoverPreflight(log)}, owners, m)
	if err != nil {
		t.Fatal(err)
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(fs), Owners: owners,
		Build: "batcomputer-2", FreeSpaceMargin: 1, DataSteps: dms.Steps()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	return dms.Preflight(ctx, o)
}

// dmRecord reports whether the store holds step's c/storage/dm/ record.
func dmRecord(t *testing.T, fs vfs.FS, step string) bool {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(fs), Owners: cutoverOwners(storage.ClaimRetired),
		Build: "batcomputer-2", FreeSpaceMargin: 1, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	db, err := o.Offline()
	if err != nil {
		t.Fatal(err)
	}
	state, err := db.RewriteRecord(ctx, step)
	if err != nil {
		t.Fatal(err)
	}
	return state != ""
}

// TestPreflight_RunsOnlyBeforeCutoverStarts: a pending cutover step whose
// Preflight returns a blocker refuses the boot with ErrCutoverBlocked (exit
// 78), logs the report naming storage cutover-blockers, never a value, and
// writes nothing (no record, every source row in place, and the pre-freeze
// build still opens the store); the same store with the step's c/storage/
// dm/ record, or with the owner's level at the step's target, skips the
// preflight.
func TestPreflight_RunsOnlyBeforeCutoverStarts(t *testing.T) {
	mem := vfs.NewMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	seedCutover(t, mem, cutoverRows(30, 4))
	var logged bytes.Buffer
	err := preflightOnce(t, mem, cutoverOwners(storage.ClaimRetired), blockedMigration(&cutoverHandles{}), slog.New(slog.NewTextHandler(&logged, nil)))
	if !errors.Is(err, storage.ErrCutoverBlocked) || ExitCode(err) != ExitConfig || ExitConfig != 78 {
		t.Fatalf("preflight = %v (exit %d), want ErrCutoverBlocked and exit 78", err, ExitCode(err))
	}
	if be, ok := errors.AsType[*storage.CutoverBlockedError](err); !ok || len(be.Blockers) != 1 || be.Blockers[0].Step != "gcpd.case-files" {
		t.Fatalf("refusal = %#v, want one blocker of gcpd.case-files", err)
	}
	if out := logged.String(); !strings.Contains(out, "storage cutover-blockers") || !strings.Contains(out, "Bane's account merge") ||
		strings.Contains(out, "safe combination") {
		t.Errorf("preflight log = %q, want the report naming storage cutover-blockers and no value", out)
	}
	if dmRecord(t, mem, "gcpd.case-files") {
		t.Error("the refusal wrote the step's record")
	}
	if got := legacyKeys(t, mem, "case:"); len(got) != 34 {
		t.Errorf("after the refusal %d source rows, want 34", len(got))
	}
	seedCutover(t, mem, nil) // the pre-freeze build opens the store

	// An error inside the step leaves its record: the preflight is skipped.
	h := &cutoverHandles{}
	failing := cutoverMigration(h)
	inner := failing.Step
	failing.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		if string(kv.Key) == "case:file/020" {
			return errors.New("scarecrow's toxin: the importer fails")
		}
		return inner(ctx, kv, w)
	}
	sf := &fakeSealer{}
	if db, err := bootWith(t, mem, h, failing, sf, sf); err == nil {
		_ = db.Close()
		t.Fatal("the failing cutover boot succeeded")
	}
	if !dmRecord(t, mem, "gcpd.case-files") {
		t.Fatal("the failed cutover left no record; the case proves nothing")
	}
	if err := preflightOnce(t, mem, cutoverOwners(storage.ClaimRetired), blockedMigration(&cutoverHandles{}), nil); err != nil {
		t.Errorf("preflight with the step's record = %v, want skipped", err)
	}

	// A raised level skips it too: a store whose owner is already at the
	// cutover step's level (M7 committed), with no record.
	lvl := vfs.NewMem()
	lvl.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	seedCutover(t, lvl, cutoverRows(3, 1))
	raised := levelOwners(storage.ClaimRetired)
	m := blockedMigration(&cutoverHandles{})
	m.RaisesLevel = 1
	if err := preflightOnce(t, lvl, raised, m, nil); !errors.Is(err, storage.ErrCutoverBlocked) {
		t.Fatalf("control: preflight below the level = %v, want ErrCutoverBlocked", err)
	}
	if levels, err := bootOwners(t, lvl, raised); err != nil || levels["gcpd"] != 1 {
		t.Fatalf("the level step boot = %v, %v; want gcpd at level 1", levels, err)
	}
	if err := preflightOnce(t, lvl, raised, m, nil); err != nil {
		t.Errorf("preflight at the raised level = %v, want skipped", err)
	}
	if err := preflightOnce(t, mem, levelOwners(storage.ClaimRetired), m, nil); err != nil {
		t.Errorf("preflight with the record (level not raised) = %v, want skipped", err)
	}
}

// TestPreflight_RunsBeforePrepare (05-storage §5S.4 step 5a, 06 §6.5.1 P4
// (a)): the cutover preflight is P4a's first act, between Open and Prepare.
// Over a pre-core build's store (legacy rows only, no c/ key) a blocker
// refuses the boot with nothing written: no checkpoint, no pointer, no
// install marker or any other c/ key. After Prepare, which takes the
// checkpoint and writes the marker (the control), the preflight is refused,
// so a boot that wires it in the wrong order fails instead of refusing with
// the checkpoint taken (the order F2-16 wires: Open, DataMigrations.
// Preflight, Prepare, Migrate).
func TestPreflight_RunsBeforePrepare(t *testing.T) {
	ctx := context.Background()
	cpArea := filepath.Join(filepath.Dir(cutoverPath), ".mediacore-checkpoints")
	pointer := cutoverPath + ".migration-checkpoint"
	open := func(t *testing.T) (*vfs.MemFS, *DataMigrations, *storage.Opened) {
		mem := vfs.NewMem()
		mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
		raw, err := cpebble.Open(cutoverPath, &cpebble.Options{FS: mem, Logger: quietPebble{}})
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range cutoverRows(5, 2) {
			if err := raw.Set([]byte(k), []byte(v), cpebble.Sync); err != nil {
				t.Fatal(err)
			}
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		owners := cutoverOwners(storage.ClaimRetired)
		dms, err := NewDataMigrations(DataMigrationsConfig{Build: "batcomputer-2", Logger: slog.New(slog.DiscardHandler),
			Preflight: CutoverPreflight(slog.New(slog.DiscardHandler))}, owners, blockedMigration(&cutoverHandles{}))
		if err != nil {
			t.Fatal(err)
		}
		o, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(mem), Owners: owners,
			Build: "batcomputer-2", FreeSpaceMargin: 1, DataSteps: dms.Steps()})
		if err != nil {
			t.Fatal(err)
		}
		return mem, dms, o
	}
	// written is the checkpoint area's entries, whether the pointer exists
	// and the store's c/ keys.
	written := func(t *testing.T, mem *vfs.MemFS) (cps []string, ptr bool, core []string) {
		t.Helper()
		cps, _ = mem.List(cpArea)
		_, err := mem.Stat(pointer)
		raw, rerr := cpebble.Open(cutoverPath, &cpebble.Options{FS: mem, ReadOnly: true, Logger: quietPebble{}})
		if rerr != nil {
			t.Fatal(rerr)
		}
		defer func() { _ = raw.Close() }()
		it, ierr := raw.NewIter(&cpebble.IterOptions{LowerBound: []byte("c/"), UpperBound: []byte("c0")})
		if ierr != nil {
			t.Fatal(ierr)
		}
		for it.First(); it.Valid(); it.Next() {
			core = append(core, string(it.Key()))
		}
		if cerr := it.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		return cps, err == nil, core
	}

	t.Run("a blocker refuses before anything is written", func(t *testing.T) {
		mem, dms, o := open(t)
		err := dms.Preflight(ctx, o)
		if cerr := o.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		if !errors.Is(err, storage.ErrCutoverBlocked) {
			t.Fatalf("preflight = %v, want ErrCutoverBlocked", err)
		}
		if cps, ptr, core := written(t, mem); len(cps) != 0 || ptr || len(core) != 0 {
			t.Errorf("the refusal wrote checkpoints %v, pointer %t, core keys %v; want nothing", cps, ptr, core)
		}
	})

	t.Run("after Prepare it is refused", func(t *testing.T) {
		mem, dms, o := open(t)
		db, err := o.Prepare(ctx)
		if err != nil {
			t.Fatal(errors.Join(err, o.Close()))
		}
		err = dms.Preflight(ctx, o)
		if cerr := db.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		if err == nil || errors.Is(err, storage.ErrCutoverBlocked) || !strings.Contains(err.Error(), "before Prepare") {
			t.Errorf("preflight after Prepare = %v, want it refused as out of order", err)
		}
		if cps, ptr, core := written(t, mem); len(cps) == 0 || !ptr || len(core) == 0 {
			t.Errorf("control: Prepare wrote checkpoints %v, pointer %t, core keys %v; the first case proves nothing", cps, ptr, core)
		}
	})
}

// p4bOrderFixture is the cutover build of TestP4bOrder_LevelStepBeforeCutover:
// gcpd at level 1 with a versioned step (v1) and its level step (v2), the
// cutover step raising level 1 and M8 after it, each noting itself in order
// when it runs. failAt makes the cutover step fail on that source key.
//
// The cutover step runs as two shards and a failing shard cancels its
// sibling, whose open chunk then commits nothing, so the failing row first
// waits for passed: closed when the step reaches case:file/008, the first
// row of shard 0's second chunk, which the runner reads only after the first
// chunk (case:file/001..007) committed. Without the wait, whether 001's
// chunk is committed before the failure is down to scheduling.
type p4bOrderFixture struct {
	mu      sync.Mutex
	order   []string
	failAt  string
	visits  map[string]int
	enabled bool
	passed  chan struct{}
	once    sync.Once
}

// newP4bOrderFixture is a fixture failing at failAt ("" never fails).
func newP4bOrderFixture(failAt string, enabled bool) *p4bOrderFixture {
	return &p4bOrderFixture{failAt: failAt, visits: map[string]int{}, enabled: enabled, passed: make(chan struct{})}
}

func (f *p4bOrderFixture) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) == 0 || f.order[len(f.order)-1] != s {
		f.order = append(f.order, s)
	}
}

func (f *p4bOrderFixture) owners() []storage.OwnerSpec {
	owners := cutoverOwners(storage.ClaimRetired)
	lvl := storage.LevelStep("gcpd", 2, 1)
	up := lvl.Up
	lvl.Up = func(ctx context.Context, tx *storage.MigrationTx) error { f.note("level"); return up(ctx, tx) }
	owners[0].Enabled = f.enabled
	owners[0].Spec.Level = 1
	owners[0].Spec.Migrations = []storage.Migration{
		{Version: 1, Name: "bat-signal", Compat: storage.Additive, Up: func(context.Context, *storage.MigrationTx) error { f.note("versioned"); return nil }},
		lvl,
	}
	return owners
}

// boot runs the cutover build's P4b over fs and returns the open store or
// the boot's error.
func (f *p4bOrderFixture) boot(t *testing.T, fs vfs.FS) (*storage.DB, error) {
	t.Helper()
	ctx := context.Background()
	h, sf := &cutoverHandles{}, &fakeSealer{}
	m := cutoverMigration(h)
	m.RaisesLevel = 1
	inner := m.Step
	m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		f.note("cutover")
		f.mu.Lock()
		f.visits[string(kv.Key)]++
		f.mu.Unlock()
		if string(kv.Key) == "case:file/008" {
			f.once.Do(func() { close(f.passed) })
		}
		if string(kv.Key) == f.failAt {
			select {
			case <-f.passed:
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-time.After(30 * time.Second):
				return errors.New("p4bOrderFixture: shard 0 never passed its first chunk")
			}
			return errors.New("scarecrow's toxin: the importer fails")
		}
		return inner(ctx, kv, w)
	}
	rw := storage.NewRewriteStep("gcpd", "gcpd.rewrite")
	owners := f.owners()
	dms, err := NewDataMigrations(DataMigrationsConfig{ChunkItems: 7, Build: "batcomputer-2", Logger: slog.New(slog.DiscardHandler),
		Quarantine: storage.QuarantineWriter(sf), LevelStep: LevelStep, Rewrite: &rw}, owners, m)
	if err != nil {
		t.Fatal(err)
	}
	dms.rewriteRun = func(ctx context.Context, db *storage.DB) error {
		f.note("compaction")
		return db.SetRewriteRecord(ctx, rw.ID, storage.DataMigrationDone)
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(fs), Owners: owners,
		Build: "batcomputer-2", FreeSpaceMargin: 1, DataSteps: dms.Steps()})
	if err != nil {
		return nil, err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return nil, errors.Join(err, o.Close())
	}
	h.ks, _ = db.Keyspace("gcpd")
	sf.db = db
	if err := db.Migrate(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// TestP4bOrder_LevelStepBeforeCutover (06 §6.10, D10, 03 §3.8): over a
// module with a legacy claim, a cutover step with an importer and M8, P4b
// runs the versioned step, then the level step, then the cutover step, then
// M8's compaction. An error inside the cutover step, after the level step
// committed, leaves a store the older build refuses; the next boot resumes
// from the cursor and every row ends imported or quarantined exactly once,
// the undecodable ones under c/storage/quarantine/ and in health. The
// cutover runs with the module disabled; a fresh install stamps the cutover
// step and M8 done and runs neither.
func TestP4bOrder_LevelStepBeforeCutover(t *testing.T) {
	ctx := context.Background()
	newMem := func() *vfs.MemFS {
		m := vfs.NewMem()
		m.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
		return m
	}
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			mem := newMem()
			seedCutover(t, mem, cutoverRows(30, 2))
			older := cutoverOwners(storage.ClaimReadWrite)
			want := []string{"versioned", "level", "cutover"}
			if !enabled {
				// The module's own steps up to the cutover build ran while it
				// was enabled; only the level step and the cutover are left.
				older[0].Spec.Migrations = []storage.Migration{{Version: 1, Name: "bat-signal", Compat: storage.Additive,
					Up: func(context.Context, *storage.MigrationTx) error { return nil }}}
				if _, err := bootOwners(t, mem, older); err != nil {
					t.Fatal(err)
				}
				want = []string{"level", "cutover"}
			}
			f := newP4bOrderFixture("case:file/024", enabled)
			if db, err := f.boot(t, mem); err == nil {
				_ = db.Close()
				t.Fatal("the failing cutover boot succeeded")
			}
			if !slices.Equal(f.order, want) {
				t.Fatalf("P4b order before the failure = %q, want %q", f.order, want)
			}
			if _, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(mem), Owners: older,
				Build: "batcomputer-1", FreeSpaceMargin: 1}); !isLevelTooNew(err) {
				t.Fatalf("the older build over the raised store = %v, want *storage.LevelTooNewError", err)
			}
			f.failAt = ""
			db, err := f.boot(t, mem)
			if err != nil {
				t.Fatalf("the resuming boot: %v", err)
			}
			defer func() { _ = db.Close() }()
			if want := append(want, "compaction"); !slices.Equal(f.order, want) {
				t.Errorf("P4b order = %q, want %q", f.order, want)
			}
			ks, _ := db.Keyspace("gcpd")
			imported := map[string]bool{}
			for kv, err := range ks.Scan(ctx, storage.ScanOptions{Start: []byte("case/"), End: []byte("case0"), KeysOnly: true}) {
				if err != nil {
					t.Fatal(err)
				}
				imported[string(kv.Key)] = true
			}
			q, err := db.Quarantine(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]int{}
			for _, e := range q {
				seen[string(e.Record.Key)]++
			}
			for k, n := range seen {
				if n != 1 {
					t.Errorf("row %s quarantined %d times", k, n)
				}
			}
			if len(imported) != 24 || len(seen) != 8 {
				t.Errorf("imported %d and quarantined %d rows, want 24 and 8 (6 undecodable, 2 sensitive)", len(imported), len(seen))
			}
			if left := legacyKeysOf(t, db); len(left) != 0 {
				t.Errorf("source rows left: %q", left)
			}
			if f.visits["case:file/001"] != 1 {
				t.Errorf("case:file/001, in the first committed chunk, was stepped %d times; the resume must start at the cursor", f.visits["case:file/001"])
			}
			res, err := StorageHealth(ctx, db, nil, time.Now())
			if err != nil || !strings.Contains(res.Detail, "step=gcpd.case-files rows=8") || res.Reason != "" {
				t.Errorf("health = %+v, %v; want the quarantine in Detail", res, err)
			}
		})
	}

	t.Run("fresh install", func(t *testing.T) {
		f := newP4bOrderFixture("", true)
		db, err := f.boot(t, newMem())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if slices.Contains(f.order, "cutover") || slices.Contains(f.order, "compaction") {
			t.Errorf("a fresh install ran %q", f.order)
		}
		for _, id := range []string{"gcpd.case-files", "gcpd.rewrite"} {
			if state, err := db.RewriteRecord(ctx, id); err != nil || state != storage.DataMigrationDone {
				t.Errorf("fresh install: %s's record = %q, %v; want done", id, state, err)
			}
		}
	})
}

// labelProbe is a bootseam.Labeler that keeps the open region's label the
// way the crash-matrix kit does (the outer label wins), for a test to read
// which writer a call runs as.
type labelProbe struct {
	mu    sync.Mutex
	label string
	depth int
}

func (p *labelProbe) Enter(label string) func() {
	p.mu.Lock()
	if p.depth == 0 {
		p.label = label
	}
	p.depth++
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if p.depth--; p.depth == 0 {
				p.label = ""
			}
			p.mu.Unlock()
		})
	}
}

func (p *labelProbe) current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.label
}

// TestQuarantineRunsAsTheChunksWrite (06 §6.5.1 "Boot writers"): the runner
// calls the quarantine hook inside the chunk's crash-matrix region, so the
// quarantine sequence's lease refill, its one write outside the chunk's
// batch, is the chunk writer's (storagemigrate.LabelChunk). A label of its
// own would be counted as whichever region a concurrent shard had open,
// since a region is the boot's and not a goroutine's. One shard here, so a
// hook outside the region would see no label at all.
func TestQuarantineRunsAsTheChunksWrite(t *testing.T) {
	mem := vfs.NewMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	seedCutover(t, mem, cutoverRows(30, 2))
	probe := &labelProbe{}
	ctx := bootseam.With(context.Background(), &bootseam.Seams{FS: mem, Labels: probe})
	h, sf := &cutoverHandles{}, &fakeSealer{}
	m := cutoverMigration(h)
	m.Parallelism = tasks.Sequential("test: one shard, so no other region is open")
	var mu sync.Mutex
	var labels []string
	writer := storage.QuarantineWriter(sf)
	quarantine := func(ctx context.Context, b *storage.Batch, step string, key, raw []byte, reason string, sensitive bool) error {
		mu.Lock()
		labels = append(labels, probe.current())
		mu.Unlock()
		return writer(ctx, b, step, key, raw, reason, sensitive)
	}
	owners := cutoverOwners(storage.ClaimRetired)
	dms, err := NewDataMigrations(DataMigrationsConfig{ChunkItems: 7, Build: "batcomputer-2", Logger: slog.New(slog.DiscardHandler),
		Quarantine: quarantine}, owners, m)
	if err != nil {
		t.Fatal(err)
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: cutoverPath, FS: storage.FSOf(mem), Owners: owners,
		Build: "batcomputer-2", FreeSpaceMargin: 1, DataSteps: dms.Steps()})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		t.Fatal(errors.Join(err, o.Close()))
	}
	h.ks, _ = db.Keyspace("gcpd")
	sf.db = db
	if err := errors.Join(db.Migrate(ctx), db.Close()); err != nil {
		t.Fatal(err)
	}
	if len(labels) != 8 {
		t.Fatalf("the hook ran %d times, want 8 (6 undecodable rows, 2 sensitive)", len(labels))
	}
	for i, l := range labels {
		if l != storagemigrate.LabelChunk {
			t.Errorf("quarantine %d ran under %q, want the chunk's region %q", i+1, l, storagemigrate.LabelChunk)
		}
	}
	if inChunkRegion(nil) != nil {
		t.Error("inChunkRegion(nil) is not nil: a build with no quarantine writer must keep failing the chunk")
	}
}

func isLevelTooNew(err error) bool {
	_, ok := errors.AsType[*storage.LevelTooNewError](err)
	return ok
}

// legacyKeysOf lists gcpd's legacy keys on an open store.
func legacyKeysOf(t *testing.T, db *storage.DB) []string {
	t.Helper()
	lr, _ := db.Legacy("gcpd")
	var out []string
	for kv, err := range lr.Scan(context.Background(), storage.ScanOptions{Start: []byte("case:"), End: []byte("case;"), KeysOnly: true}) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(kv.Key))
	}
	return out
}

// TestExitCode: a blocked cutover, a store rewrite swapped back twice and a
// store too new for the build exit 78 (EX_CONFIG: a restart cannot help),
// wrapped or not; any other error exits 1, and nil 0.
func TestExitCode(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want int
	}{
		"nil":            {nil, 0},
		"blocked":        {fmt.Errorf("kernel: P4a: %w", &storage.CutoverBlockedError{}), 78},
		"blocked plain":  {storage.ErrCutoverBlocked, 78},
		"swapped back":   {fmt.Errorf("storage: M8: %w", storage.ErrRewriteSwappedBack), 78},
		"level too new":  {fmt.Errorf("kernel: P2: %w", &storage.LevelTooNewError{Owner: "gcpd", Store: 3, Supported: 2, Source: "stamp"}), 78},
		"anything else":  {errors.New("the Riddler's question"), 1},
		"quarantine nil": {storage.ErrQuarantineNoSealer, 1},
	} {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("%s: ExitCode(%v) = %d, want %d", name, c.err, got, c.want)
		}
	}
}

// TestQuarantineGauge: <ns>_storage_quarantine_rows{step,sensitive}
// records per step the sealed rows and the others; after a purge both fall
// to 0 rather than keeping the step's last value.
func TestQuarantineGauge(t *testing.T) {
	ctx := context.Background()
	mem := vfs.NewMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	seedCutover(t, mem, cutoverRows(30, 4))
	sf := &fakeSealer{}
	db, err := bootCutover(t, mem, sf, sf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(ctx) })
	scope, err := p.Core("storage", "step", "sensitive")
	if err != nil {
		t.Fatal(err)
	}
	gauge := func(sensitive string) float64 {
		t.Helper()
		mfs, err := p.Gatherer().Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() != "mediacore_storage_quarantine_rows" {
				continue
			}
			for _, m := range mf.GetMetric() {
				if matches(m, map[string]string{"step": "gcpd.case-files", "sensitive": sensitive}) {
					return m.GetGauge().GetValue()
				}
			}
		}
		t.Fatalf("no quarantine_rows series for sensitive=%s", sensitive)
		return 0
	}
	qg, err := NewQuarantineGauge(scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := qg.Record(ctx, db); err != nil {
		t.Fatal(err)
	}
	if s, o := gauge("true"), gauge("false"); s != 4 || o != 6 {
		t.Errorf("quarantine_rows = %v sensitive, %v other; want 4 and 6", s, o)
	}
	if _, err := db.PurgeQuarantine(ctx, "gcpd.case-files", sf, nil); err == nil || !strings.Contains(err.Error(), "audit") {
		t.Fatalf("a purge with no audit event = %v, want refused", err)
	}
	if err := qg.Record(ctx, db); err != nil {
		t.Fatal(err)
	}
	if s, o := gauge("true"), gauge("false"); s != 4 || o != 6 {
		t.Fatalf("after the refused purge quarantine_rows = %v sensitive, %v other; want 4 and 6 kept", s, o)
	}
	if _, err := db.PurgeQuarantine(ctx, "gcpd.case-files", sf, func(*storage.Batch, int) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := qg.Record(ctx, db); err != nil {
		t.Fatal(err)
	}
	if s, o := gauge("true"), gauge("false"); s != 0 || o != 0 {
		t.Errorf("quarantine_rows after the purge = %v and %v, want 0 and 0", s, o)
	}
}
