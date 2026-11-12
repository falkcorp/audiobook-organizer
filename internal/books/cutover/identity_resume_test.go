// file: internal/books/cutover/identity_resume_test.go
// version: 1.0.0
// guid: b181040b-01e9-4d4d-9a27-4a9464b816a4
// last-edited: 2026-11-12

package cutover

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

var errKilled = errors.New("the test killed the process")

// killAt wraps dm's Step so the n-th call (1-based, across the boot) fails as
// a killed process would: the chunk in progress does not commit, every chunk
// before it did.
func killAt(dm datamigrate.DataMigration, n int64) datamigrate.DataMigration {
	var calls atomic.Int64
	step := dm.Step
	dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		if calls.Add(1) == n {
			return errKilled
		}
		return step(ctx, kv, w)
	}
	return dm
}

// sameIdentityOutcome fails for any difference between two migrated stores
// that M3 owns: every native user, key, role, identity and allow-list entry
// with its content, the quarantine records (by key, reason and value, not by
// number), the audit events and the journals left in books' range.
func sameIdentityOutcome(t testing.TB, got, want *idSnap) {
	t.Helper()
	if got.State != "done" {
		t.Errorf("the step's state is %q, want done", got.State)
	}
	if !slices.Equal(mapKeys(got.Users), mapKeys(want.Users)) {
		t.Errorf("users %v, want %v", mapKeys(got.Users), mapKeys(want.Users))
	}
	for id, w := range want.Users {
		if g, ok := got.Users[id]; ok && (g.Hash != w.Hash || g.User.Status != w.User.Status || !slices.Equal(g.User.Roles, w.User.Roles) || g.User.Username != w.User.Username) {
			t.Errorf("user %s differs: %+v vs %+v", id, g.User, w.User)
		}
	}
	if !slices.Equal(mapKeys(got.Keys), mapKeys(want.Keys)) {
		t.Errorf("keys %v, want %v", mapKeys(got.Keys), mapKeys(want.Keys))
	}
	for id, w := range want.Keys {
		if g, ok := got.Keys[id]; ok && (g.Hash != w.Hash || g.Key.Status != w.Key.Status || !g.Key.ExpiresAt.Equal(w.Key.ExpiresAt) ||
			!slices.Equal(scopeStrings(g.Key.Scopes), scopeStrings(w.Key.Scopes))) {
			t.Errorf("key %s differs", id)
		}
	}
	if !slices.Equal(mapKeys(got.Roles), mapKeys(want.Roles)) {
		t.Errorf("roles %v, want %v", mapKeys(got.Roles), mapKeys(want.Roles))
	}
	for name, w := range want.Roles {
		if g, ok := got.Roles[name]; ok && !slices.Equal(scopeStrings(g.Added), scopeStrings(w.Added)) {
			t.Errorf("role %s differs", name)
		}
	}
	if !slices.Equal(mapKeys(got.Idents), mapKeys(want.Idents)) {
		t.Errorf("identities %v, want %v", mapKeys(got.Idents), mapKeys(want.Idents))
	}
	if !slices.Equal(got.Allow, want.Allow) {
		t.Errorf("allow-list %v, want %v", got.Allow, want.Allow)
	}
	if !slices.Equal(got.Events, want.Events) {
		t.Errorf("audit events %v, want %v", got.Events, want.Events)
	}
	flat := func(s *idSnap) []string {
		var out []string
		for _, q := range s.Quarantine {
			out = append(out, q.Key+"|"+q.Reason+"|"+q.Value)
		}
		slices.Sort(out)
		return out
	}
	if g, w := flat(got), flat(want); !slices.Equal(g, w) {
		t.Errorf("quarantine records differ: %d vs %d", len(g), len(w))
	}
	for k, v := range want.Legacy {
		if strings.HasPrefix(k, "usermerge:journal:") && string(got.Legacy[k]) != string(v) {
			t.Errorf("journal %s differs", k)
		}
	}
	for _, p := range identityFamilies {
		for k := range got.Legacy {
			if strings.HasPrefix(k, p) {
				t.Errorf("legacy row %q is left behind", k)
			}
		}
	}
}

// TestIdentityCutover_Resumable (05-storage §5S.3.6, §5S.3.13): M3 killed after
// each chunk commit, and inside a chunk, and booted again, ends in the store
// an uninterrupted run leaves, with its done record: nothing doubled (the
// importers' records carry an absent-Expect, so a second import of a record
// fails the boot) and nothing lost.
func TestIdentityCutover_Resumable(t *testing.T) {
	const chunk = 7
	want, err := newIDRig(t, "overlay", func(r *idRig) { r.chunkItems = chunk }).run(t)
	if err != nil {
		t.Fatalf("uninterrupted M3: %v", err)
	}
	// Calls of Step in an uninterrupted boot: the first of every chunk is a
	// chunk boundary and the others are inside it. Every fifth call is killed.
	var total int64
	{
		r := newIDRig(t, "overlay", func(r *idRig) { r.chunkItems = chunk })
		dm := r.migration()
		var calls atomic.Int64
		step := dm.Step
		dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			calls.Add(1)
			return step(ctx, kv, w)
		}
		if err := r.boot(context.Background(), r.newMem(t), []datamigrate.DataMigration{dm}, nil); err != nil {
			t.Fatal(err)
		}
		total = calls.Load()
	}
	if total < 20 {
		t.Fatalf("the corpus gives %d Step calls: too few cells to mean anything", total)
	}
	for n := int64(1); n <= total; n += 5 {
		r := newIDRig(t, "overlay", func(r *idRig) { r.chunkItems = chunk })
		mem := r.newMem(t)
		err := r.boot(context.Background(), mem, []datamigrate.DataMigration{killAt(r.migration(), n)}, nil)
		if !errors.Is(err, errKilled) {
			t.Fatalf("kill at call %d of %d: the boot returned %v", n, total, err)
		}
		got, err := r.runOn(t, mem)
		if err != nil {
			t.Fatalf("kill at call %d: the boot after it failed: %v\n%s", n, err, r.logs.String())
		}
		sameIdentityOutcome(t, got, want)
		if t.Failed() {
			t.Fatalf("the cell killed at call %d of %d differs", n, total)
		}
	}
}

// TestIdentityCutover_Resumable_SkipsPreflightAfterTheUserChunk (F4-06): the
// first chunk commits every role and user and deletes their HEAD rows, so a
// kill before the key chunk leaves keys whose owner is no longer a u: row. A
// preflight run on that store would name every one of them; the next boot
// skips it (the cutover has started) and reports no key, and the keys are
// imported against the owners the first chunk committed.
func TestIdentityCutover_Resumable_SkipsPreflightAfterTheUserChunk(t *testing.T) {
	r := newIDRig(t, "overlay")
	mem := r.newMem(t)
	// The first Step call is the first chunk (one item per chunk): it pulls
	// every role and user forward. The second is killed.
	if err := r.boot(context.Background(), mem, []datamigrate.DataMigration{killAt(r.migration(), 2)}, nil); !errors.Is(err, errKilled) {
		t.Fatalf("the first boot returned %v, want the kill", err)
	}
	dm := r.migration()
	var hazard []datamigrate.Blocker
	r.preMigrate = func(ctx context.Context, e *idEnv) error {
		lr, _ := e.db.Legacy(BooksOwner)
		var err error
		hazard, err = dm.Preflight(ctx, lr)
		return err
	}
	mark := len(r.logs.String())
	got, err := r.runOn(t, mem)
	if err != nil {
		t.Fatalf("the boot after the kill: %v\n%s", err, r.logs.String())
	}
	if len(hazard) == 0 {
		t.Errorf("the half-migrated store passes the preflight: the kill left no key without its owner, so this cell proves nothing")
	}
	for _, b := range hazard {
		if b.Family != "apikey:" && b.Family != "oauthid:" {
			t.Errorf("an unexpected blocker on the half-migrated store: %+v", b)
		}
	}
	logs := r.logs.String()[mark:]
	if !strings.Contains(logs, "cutover preflight skipped") || strings.Contains(logs, "blocker:") {
		t.Errorf("the boot after the user chunk did not skip the preflight cleanly:\n%s", logs)
	}
	want, err := newIDRig(t, "overlay").run(t)
	if err != nil {
		t.Fatal(err)
	}
	sameIdentityOutcome(t, got, want)
}

// TestIdentityCutover_CrashAtEveryChunk is M3's cells of the chunk-boundary
// crash matrix (05-storage §5S.3.13 TestCutoverCrashAtEveryChunk): a crash
// before every kill point of the plan, chunk and state writers, at every
// chunk's first item and inside every chunk, and a boot of each clone ends in
// the uninterrupted store with the step done.
func TestIdentityCutover_CrashAtEveryChunk(t *testing.T) {
	for _, c := range []struct {
		name, corpus string
		chunk        int
	}{
		{"stage1", "stage1", 4},
		{"overlay", "overlay", 12},
	} {
		t.Run(c.name, func(t *testing.T) {
			opt := func(r *idRig) { r.chunkItems = c.chunk }
			want, err := newIDRig(t, c.corpus, opt).run(t)
			if err != nil {
				t.Fatalf("uninterrupted M3: %v", err)
			}
			r := newIDRig(t, c.corpus, opt)
			crashmatrix.RunChunks(t, crashmatrix.ChunkScenario{
				Path:       rigStore,
				Seed:       func(t testing.TB, fs vfs.FS) { r.seed(t, fs) },
				Migrations: []datamigrate.DataMigration{r.migration()},
				Boot: func(ctx context.Context, fs vfs.FS, migs []datamigrate.DataMigration) error {
					return r.boot(ctx, fs, migs, nil)
				},
				Check: func(t testing.TB, fs vfs.FS) {
					got, err := r.runOn(t, fs)
					if err != nil {
						t.Fatalf("reading the migrated store: %v", err)
					}
					sameIdentityOutcome(t, got, want)
				},
				Inside: true,
			})
		})
	}
}
