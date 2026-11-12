// file: core/storage/cutover_blockers_test.go
// version: 1.0.0
// guid: 5d8c3b72-9e41-4a06-b1f7-0c62e9a4d835
// last-edited: 2026-11-12

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// The cutover-blockers seam (cutover_blockers.go): gotham's "joker:retired:"
// family is a retired claim inside the read-write "joker:" one, so it is the
// step's Source in these tests, "joker:" proper is a key a Set would reach by
// the claim's own mode, and "arkham:" is read-only.

var blockerSource = []Range{{Start: []byte("joker:retired:"), End: []byte("joker:retired;")}}

var blockerSteps = []CutoverStep{{Owner: "gotham", ID: "gotham.import", RaisesLevel: 2}}

// blockerStore boots the head store once (the install marker and the claim
// ledger, as the pre-freeze release leaves them), seeds rows and opens it
// for BeginCutoverBlockers: Open only, nothing prepared.
func blockerStore(t *testing.T, mut func(*OpenOptions)) (*Opened, *env) {
	t.Helper()
	e := newEnv(t, cpOwners()...)
	e.seedRaw("joker:retired:1", "ha", "joker:retired:2", "haha", "joker:3", "hahaha")
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	if mut != nil {
		mut(&e.opts)
	}
	o, err := Open(context.Background(), e.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	return o, e
}

// dumpStore is every key and value of db's engine: the byte-identical check.
func dumpStore(t *testing.T, db *DB) map[string]string {
	t.Helper()
	it, err := db.engine().DB().NewIter(&cpebble.IterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	out := map[string]string{}
	for ok := it.First(); ok; ok = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	if err := it.Error(); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameDump(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Errorf("the store changed: %d keys before, %d after", len(before), len(after))
	}
	for k, v := range before {
		if got, ok := after[k]; !ok || got != v {
			t.Errorf("the store changed at %q", k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("the store gained %q", k)
		}
	}
}

// edit is a CutoverBlockerEdit over blockerSource whose Stage sets key to
// value (a fix) and whose Audit stages a marker row in storage's keyspace.
func (e *env) fixEdit(db *DB, key, value string, recheck func(context.Context, Reader) error) CutoverBlockerEdit {
	return CutoverBlockerEdit{
		Step: "gotham.import", Owner: "gotham", Source: blockerSource, Key: []byte(key), Action: CutoverBlockerFix,
		Reason: "ha is not a laugh", Build: "batcomputer-2", At: time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC),
		Stage: func(_ context.Context, b *Batch) ([]string, error) { return nil, b.Set([]byte(key), []byte(value)) },
		Recheck: func(ctx context.Context, r Reader) error {
			if recheck == nil {
				return nil
			}
			return recheck(ctx, r)
		},
		Audit: func(b *Batch, _ []string) error {
			ks := db.storageKS()
			b.Bind(ks)
			return b.In(ks).Set([]byte("audit-marker"), []byte("fix"))
		},
	}
}

// TestBeginCutoverBlockers_Refusals: the open refuses an empty store, a store
// prepared before, a store whose cutover started (naming make
// rollback-restore), a read-write open of a marker-less store and a second
// call; once it returns, Prepare and Offline refuse and PreflightSource still
// reads.
func TestBeginCutoverBlockers_Refusals(t *testing.T) {
	ctx := context.Background()

	// An empty store.
	fresh := newEnv(t, cpOwners()...)
	o, err := Open(ctx, fresh.opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.BeginCutoverBlockers(ctx, blockerSteps); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("an empty store = %v, want the empty-store refusal", err)
	}
	_ = o.Close()

	// A marker-less head store: refused read-write, listed read-only.
	head := headStore(t, vfs.NewMem(), cpOwners()...)
	ho := preUpgradeOpened(t, head)
	if _, err := ho.BeginCutoverBlockers(ctx, blockerSteps); err == nil || !strings.Contains(err.Error(), "install marker") {
		t.Errorf("a marker-less read-write open = %v, want the marker refusal", err)
	}
	_ = ho.Close()
	head.opts.ReadOnly, head.opts.MustExist = true, true
	rho, err := Open(ctx, head.opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rho.BeginCutoverBlockers(ctx, blockerSteps); err != nil {
		t.Errorf("a marker-less read-only open = %v, want it to pass", err)
	}
	_ = rho.Close()

	// Started: a dm record, then a level at the target.
	o, _ = blockerStore(t, nil)
	if err := markDone(ctx, o.db, "gotham.import"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.BeginCutoverBlockers(ctx, blockerSteps); !errors.Is(err, ErrCutoverStarted) || !strings.Contains(err.Error(), "make rollback-restore") {
		t.Errorf("a started cutover (dm record) = %v, want ErrCutoverStarted naming make rollback-restore", err)
	}
	o, _ = blockerStore(t, nil)
	o.db.boot.stamps = Levels{"gotham": 2}
	if _, err := o.BeginCutoverBlockers(ctx, blockerSteps); !errors.Is(err, ErrCutoverStarted) {
		t.Errorf("a started cutover (level at target) = %v, want ErrCutoverStarted", err)
	}

	// A prepared store, and the state after a successful open.
	o, _ = blockerStore(t, nil)
	db, err := o.BeginCutoverBlockers(ctx, blockerSteps)
	if err != nil || db == nil {
		t.Fatalf("Begin on a pre-cutover store = %v", err)
	}
	if _, err := o.BeginCutoverBlockers(ctx, blockerSteps); err == nil {
		t.Error("a second BeginCutoverBlockers passed")
	}
	if _, err := o.Prepare(ctx); err == nil || !strings.Contains(err.Error(), "BeginCutoverBlockers") {
		t.Errorf("Prepare after Begin = %v, want a refusal", err)
	}
	if _, err := o.Offline(); err == nil || !strings.Contains(err.Error(), "BeginCutoverBlockers") {
		t.Errorf("Offline after Begin = %v, want a refusal", err)
	}
	if err := db.Migrate(ctx); err == nil {
		t.Error("Migrate after Begin ran")
	}
	src, err := o.PreflightSource("gotham")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := src.Get(ctx, []byte("joker:retired:1")); err != nil || string(v) != "ha" {
		t.Errorf("the preflight source reads %q, %v", v, err)
	}

	pe := newEnv(t, cpOwners()...)
	pe.seedRaw("joker:1", "ha")
	if err := pe.open().Close(); err != nil {
		t.Fatal(err)
	}
	po, err := Open(ctx, pe.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = po.Close() })
	pdb, err := po.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := po.BeginCutoverBlockers(ctx, blockerSteps); err == nil || !strings.Contains(err.Error(), "after Prepare") {
		t.Errorf("Begin after Prepare = %v, want a refusal", err)
	}
	if _, err := pdb.EditCutoverBlocker(ctx, pe.fixEdit(pdb, "joker:retired:1", "x", nil)); err == nil || !strings.Contains(err.Error(), "BeginCutoverBlockers") {
		t.Errorf("an edit on a prepared store = %v, want a refusal", err)
	}
}

// TestCutoverBlockerSeam: the seam admits Set and Delete inside the step's
// Source and nothing else; the ordinary LegacyRange still refuses Set on a
// retired claim; no edit works on a store that was not opened with Begin.
func TestCutoverBlockerSeam(t *testing.T) {
	ctx := context.Background()
	o, e := blockerStore(t, nil)
	db, err := o.BeginCutoverBlockers(ctx, blockerSteps)
	if err != nil {
		t.Fatal(err)
	}

	// The ordinary handle: Set on the retired claim is refused.
	lr, _ := db.Legacy("gotham")
	if err := lr.Set(ctx, []byte("joker:retired:9"), []byte("x")); !errors.Is(err, ErrClaimMode) {
		t.Fatalf("LegacyRange.Set on a retired claim = %v, want ErrClaimMode", err)
	}

	before := dumpStore(t, db)
	for name, set := range map[string]string{
		"a read-write key outside Source": "joker:3",
		"a read-only claim":               "arkham:1",
		"a key no claim holds":            "penguin:1",
		"storage's own keyspace":          "c/storage/dm/x",
	} {
		ed := e.fixEdit(db, set, "x", nil)
		if n, err := db.EditCutoverBlocker(ctx, ed); err == nil || !errors.Is(err, ErrOutsideKeyspace) {
			t.Errorf("Set of %s = %d, %v; want ErrOutsideKeyspace", name, n, err)
		}
	}
	del := e.fixEdit(db, "joker:3", "", nil)
	del.Action = CutoverBlockerDelete
	del.Stage = func(_ context.Context, b *Batch) ([]string, error) { return nil, b.Delete([]byte("joker:3")) }
	if _, err := db.EditCutoverBlocker(ctx, del); !errors.Is(err, ErrOutsideKeyspace) {
		t.Errorf("Delete outside Source = %v, want ErrOutsideKeyspace", err)
	}
	rng := e.fixEdit(db, "joker:retired:1", "", nil)
	rng.Action = CutoverBlockerDelete
	rng.Stage = func(_ context.Context, b *Batch) ([]string, error) {
		return nil, b.DeleteRange([]byte("joker:retired:"), []byte("joker:retired;"))
	}
	if _, err := db.EditCutoverBlocker(ctx, rng); !errors.Is(err, ErrOutsideKeyspace) {
		t.Errorf("DeleteRange = %v, want it refused", err)
	}
	sameDump(t, before, dumpStore(t, db))

	// Inside Source: a fix sets the retired key, a delete removes it.
	if n, err := db.EditCutoverBlocker(ctx, e.fixEdit(db, "joker:retired:1", "HA", nil)); err != nil || n != 1 {
		t.Fatalf("fix inside Source = %d, %v", n, err)
	}
	if v, err := lr.Get(ctx, []byte("joker:retired:1")); err != nil || string(v) != "HA" {
		t.Errorf("after the fix the row is %q, %v", v, err)
	}
	del = e.fixEdit(db, "joker:retired:2", "", nil)
	del.Action = CutoverBlockerDelete
	del.Stage = func(_ context.Context, b *Batch) ([]string, error) {
		return []string{"two"}, b.Delete([]byte("joker:retired:2"))
	}
	if n, err := db.EditCutoverBlocker(ctx, del); err != nil || n != 2 {
		t.Fatalf("delete inside Source = %d, %v", n, err)
	}
	if _, err := lr.Get(ctx, []byte("joker:retired:2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("after the delete the row is there: %v", err)
	}
	// The ordinary handle is unchanged by all of it.
	if err := lr.Set(ctx, []byte("joker:retired:9"), []byte("x")); !errors.Is(err, ErrClaimMode) {
		t.Errorf("LegacyRange.Set on a retired claim afterwards = %v, want ErrClaimMode", err)
	}
}

// TestCutoverBlockerSeam_ShadowDialNeverAdmits: with the emergency shadow
// dial on, a key inside the Source range that no claim holds is still
// refused by the seam (the dial lets an ordinary handle through).
func TestCutoverBlockerSeam_ShadowDialNeverAdmits(t *testing.T) {
	ctx := context.Background()
	o, e := blockerStore(t, func(o *OpenOptions) { o.ClaimsShadow = true })
	db, err := o.BeginCutoverBlockers(ctx, blockerSteps)
	if err != nil {
		t.Fatal(err)
	}
	ed := e.fixEdit(db, "penguin:1", "x", nil)
	ed.Source = []Range{{Start: []byte("penguin:"), End: []byte("penguin;")}}
	before := dumpStore(t, db)
	if _, err := db.EditCutoverBlocker(ctx, ed); !errors.Is(err, ErrOutsideKeyspace) {
		t.Errorf("an unclaimed key under the shadow dial = %v, want ErrOutsideKeyspace", err)
	}
	sameDump(t, before, dumpStore(t, db))
}

// TestEditCutoverBlocker_RefusedWritesNothing: a Recheck that refuses, a
// Stage that errors, a change that is not the one named and an audit that
// fails each leave the store byte-identical, durable lease rows included.
func TestEditCutoverBlocker_RefusedWritesNothing(t *testing.T) {
	ctx := context.Background()
	o, e := blockerStore(t, nil)
	db, err := o.BeginCutoverBlockers(ctx, blockerSteps)
	if err != nil {
		t.Fatal(err)
	}
	before := dumpStore(t, db)

	bad := e.fixEdit(db, "joker:retired:1", "Joker venom", func(_ context.Context, r Reader) error {
		v, err := r.Get(context.Background(), []byte("joker:retired:1"))
		if err != nil || string(v) != "Joker venom" {
			t.Errorf("the overlay reads %q, %v; want the staged value", v, err)
		}
		return errors.New("the row still blocks")
	})
	if _, err := db.EditCutoverBlocker(ctx, bad); err == nil || !strings.Contains(err.Error(), "still blocks") {
		t.Errorf("a refused recheck = %v", err)
	}
	stageErr := e.fixEdit(db, "joker:retired:1", "x", nil)
	stageErr.Stage = func(context.Context, *Batch) ([]string, error) { return nil, errors.New("bane broke it") }
	if _, err := db.EditCutoverBlocker(ctx, stageErr); err == nil || !strings.Contains(err.Error(), "bane broke it") {
		t.Errorf("a failed stage = %v", err)
	}
	empty := e.fixEdit(db, "joker:retired:1", "x", nil)
	empty.Stage = func(context.Context, *Batch) ([]string, error) { return nil, nil }
	if _, err := db.EditCutoverBlocker(ctx, empty); err == nil || !strings.Contains(err.Error(), "staged no change") {
		t.Errorf("a stage that changes nothing = %v", err)
	}
	wrongDelete := e.fixEdit(db, "joker:retired:1", "x", nil) // a fix that deletes the row
	wrongDelete.Stage = func(_ context.Context, b *Batch) ([]string, error) { return nil, b.Delete([]byte("joker:retired:1")) }
	if _, err := db.EditCutoverBlocker(ctx, wrongDelete); err == nil || !strings.Contains(err.Error(), "in place") {
		t.Errorf("a fix that deletes the row = %v", err)
	}
	wrongFix := e.fixEdit(db, "joker:retired:1", "x", nil) // a delete that leaves the row
	wrongFix.Action = CutoverBlockerDelete
	if _, err := db.EditCutoverBlocker(ctx, wrongFix); err == nil || !strings.Contains(err.Error(), "did not remove") {
		t.Errorf("a delete that leaves the row = %v", err)
	}
	auditErr := e.fixEdit(db, "joker:retired:1", "x", nil)
	auditErr.Audit = func(*Batch, []string) error { return errors.New("the audit log is gone") }
	if _, err := db.EditCutoverBlocker(ctx, auditErr); err == nil || !strings.Contains(err.Error(), "audit") {
		t.Errorf("a failed audit = %v", err)
	}
	sameDump(t, before, dumpStore(t, db))
}

// TestEditCutoverBlocker_Record: each edit commits the record under
// c/storage/cutover/blockers/<step>/<n> with the change and the audit stage,
// n counting from 1 per step; the value is the JSON of the spec, ids never
// null and no row value in it; a record another writer made first (the
// precondition) fails the commit and leaves the change out.
func TestEditCutoverBlocker_Record(t *testing.T) {
	ctx := context.Background()
	o, e := blockerStore(t, nil)
	db, err := o.BeginCutoverBlockers(ctx, blockerSteps)
	if err != nil {
		t.Fatal(err)
	}
	ks := db.storageKS()
	readRec := func(step string, n uint64) CutoverBlockerRecord {
		t.Helper()
		raw, err := ks.Get(ctx, CutoverBlockerKey(step, n))
		if err != nil {
			t.Fatalf("record %s/%d: %v", step, n, err)
		}
		var r CutoverBlockerRecord
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("record %s/%d is not the spec's JSON: %v (%s)", step, n, err, raw)
		}
		return r
	}

	if n, err := db.EditCutoverBlocker(ctx, e.fixEdit(db, "joker:retired:1", "SECRETVALUE", nil)); err != nil || n != 1 {
		t.Fatalf("first edit = %d, %v", n, err)
	}
	r := readRec("gotham.import", 1)
	if r.Key != `"joker:retired:1"` || r.Action != "fix" || r.Reason != "ha is not a laugh" || r.Build != "batcomputer-2" || r.IDs == nil || len(r.IDs) != 0 {
		t.Errorf("record 1 = %+v", r)
	}
	if !strings.HasSuffix(r.At, "Z") || len(r.At) != len("2006-01-02T15:04:05Z") {
		t.Errorf("record 1's at = %q, want RFC 3339 UTC", r.At)
	}
	raw, _ := ks.Get(ctx, CutoverBlockerKey("gotham.import", 1))
	if strings.Contains(string(raw), "SECRETVALUE") {
		t.Error("the record carries the row's value")
	}
	if v, err := ks.Get(ctx, []byte("audit-marker")); err != nil || string(v) != "fix" {
		t.Errorf("the audit stage did not commit with the edit: %q, %v", v, err)
	}

	del := e.fixEdit(db, "joker:retired:2", "", nil)
	del.Action = CutoverBlockerDelete
	del.Stage = func(_ context.Context, b *Batch) ([]string, error) {
		return []string{"robin", "nightwing"}, b.Delete([]byte("joker:retired:2"))
	}
	if n, err := db.EditCutoverBlocker(ctx, del); err != nil || n != 2 {
		t.Fatalf("second edit = %d, %v", n, err)
	}
	if r := readRec("gotham.import", 2); r.Action != "delete" || len(r.IDs) != 2 || r.IDs[1] != "nightwing" {
		t.Errorf("record 2 = %+v", r)
	}
	other := e.fixEdit(db, "joker:retired:1", "HAHA", nil)
	other.Step = "gotham.other"
	if n, err := db.EditCutoverBlocker(ctx, other); err != nil || n != 1 {
		t.Fatalf("another step's first edit = %d, %v; want its own counter", n, err)
	}

	// A competing writer takes record 3 between the choice of n and the commit.
	before := dumpStore(t, db)
	race := e.fixEdit(db, "joker:retired:1", "HAHAHA", nil)
	race.Audit = func(b *Batch, _ []string) error {
		return ks.Set(ctx, CutoverBlockerKey("gotham.import", 3), []byte("{}"))
	}
	if _, err := db.EditCutoverBlocker(ctx, race); !errors.Is(err, ErrConflict) {
		t.Errorf("a record taken first = %v, want ErrConflict", err)
	}
	after := dumpStore(t, db)
	competing := string(append(slices.Clone(ks.prefix), CutoverBlockerKey("gotham.import", 3)...))
	if after[competing] != "{}" {
		t.Error("the competing record is gone")
	}
	delete(after, competing)
	sameDump(t, before, after) // the change, the audit stage and the record are all out
}

// TestEditCutoverBlocker_ReadOnly: an edit on a read-only open is refused
// whatever the rest says.
func TestEditCutoverBlocker_ReadOnly(t *testing.T) {
	ctx := context.Background()
	o, e := blockerStore(t, func(o *OpenOptions) { o.ReadOnly, o.MustExist = true, true })
	db, err := o.BeginCutoverBlockers(ctx, blockerSteps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.EditCutoverBlocker(ctx, e.fixEdit(db, "joker:retired:1", "x", nil)); !errors.Is(err, ErrReadOnly) {
		t.Errorf("an edit on a read-only open = %v, want ErrReadOnly", err)
	}
}
