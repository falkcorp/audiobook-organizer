// file: core/audit/helpers_test.go
// version: 1.0.0
// guid: 3b6d2f0e-4a1c-4d7b-9e58-6c0f1a2b7d93
// last-edited: 2026-11-10

package audit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
)

// auditOwner is the owner of the c/audit/ keyspace the kernel registers.
const auditOwner storage.Owner = "core/audit"

// env is a booted store holding the audit keyspace and its seq sequence. It
// boots as coretest.Store does (storage.Open, Prepare, Migrate); coretest
// cannot be imported here, it reaches the kernel, which will import audit.
type env struct {
	db  *storage.DB
	ks  storage.Keyspace
	seq *storage.Sequence
}

// bootEnv opens path on fs with a sequence lease of lease (0 = storage's
// default) and closes the store at cleanup.
func bootEnv(t testing.TB, fs vfs.FS, path string, lease uint64) *env {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            path,
		Owners:          []storage.OwnerSpec{{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true}},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(fs),
		SequenceLease:   lease,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace(auditOwner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", auditOwner)
	}
	return &env{db: db, ks: ks, seq: ks.Sequence("seq")}
}

// newEnv boots a store in a fresh in-memory file system.
func newEnv(t testing.TB) *env {
	t.Helper()
	return bootEnv(t, vfs.NewMem(), filepath.Join(t.TempDir(), "main.pebble"), 0)
}

// deps is what New needs over e.
func (e *env) deps() Deps { return Deps{Keyspace: e.ks, Seq: e.seq} }

// newStoreOn is New over e with opts, fatal on error.
func newStoreOn(t testing.TB, e *env, opts Options, d Deps) *Store {
	t.Helper()
	d.Keyspace, d.Seq = e.ks, e.seq
	s, err := New(context.Background(), opts, d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// all returns every record of s, newest first.
func all(t testing.TB, s *Store, f Filter) []Record {
	t.Helper()
	f.Limit = MaxQueryLimit
	var out []Record
	for rec, err := range s.Query(context.Background(), f) {
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

// seqs is the Seq of each record.
func seqs(recs []Record) []uint64 {
	out := make([]uint64, len(recs))
	for i, r := range recs {
		out[i] = r.Seq
	}
	return out
}

// actions is the Action of each record.
func actions(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Action
	}
	return out
}

// sortedDesc reports whether xs is strictly descending.
func sortedDesc(xs []uint64) bool {
	return slices.IsSortedFunc(xs, func(a, b uint64) int {
		switch {
		case a > b:
			return -1
		case a < b:
			return 1
		}
		return 0
	}) && len(slices.Compact(slices.Clone(xs))) == len(xs)
}

// memSink is an in-memory sink: the writer's batches, in order. It never
// touches Pebble, so a test can run the writer inside a testing/synctest
// bubble. fail makes the next write return an error.
type memSink struct {
	mu      sync.Mutex
	batches [][]Record
	fail    error
	next    uint64
}

func (m *memSink) write(_ context.Context, recs []Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		err := m.fail
		m.fail = nil
		return err
	}
	out := slices.Clone(recs)
	for i := range out {
		m.next++
		out[i].Seq = m.next
	}
	m.batches = append(m.batches, out)
	return nil
}

func (m *memSink) records() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, b := range m.batches {
		out = append(out, b...)
	}
	return out
}

// gate is a sink whose write blocks until release is closed.
type gate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate() *gate { return &gate{entered: make(chan struct{}), release: make(chan struct{})} }

func (g *gate) write(ctx context.Context, _ []Record) error {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// bruce is the fixture actor.
func bruce() Actor {
	return Actor{UserID: "bruce_wayne", Username: "Bruce Wayne", Method: "session", CredentialID: "sess_batcave", ClientAddr: "192.0.2.7", UserAgent: "Batmobile/1.0"}
}

// jsonLen is the length of v's JSON.
func jsonLen(v any) (int, error) {
	raw, err := json.Marshal(v)
	return len(raw), err
}
