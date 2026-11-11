// file: core/integrations/download/harness_test.go
// version: 1.0.0
// guid: e73fd165-bbea-420c-b1e0-e9822c051be7
// last-edited: 2026-11-11

package download_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The harness: a real Registry over a booted in-memory store with the real
// secrets and audit stores (the shape of integrations' own env_test.go, which
// an external package cannot import), the integrationstest outbound fake, and
// download adapters whose instances are coretest.FakeDownloadClients the test
// hands out by instance ID. The Pool under test is the real one.
//
// It cannot use coretest.NewStore: that is for tests whose packages the
// kernel graph does not reach, and the Registry needs only the three keyspaces
// opened here.

const (
	secretsOwner storage.Owner = "core/secrets"
	auditOwner   storage.Owner = "core/audit"
)

// The test adapters (origin "hello", Batman-named).
const (
	crateAdapter integrations.AdapterID = "hello.batcrate" // torrent clients
	nzbAdapter   integrations.AdapterID = "hello.nzbcrate" // a second adapter, to tell protocols apart
)

// The instances.
var (
	seedbox = integrations.Ref{Kind: integrations.KindDownload, Instance: "seedbox"}
	rpiBox  = integrations.Ref{Kind: integrations.KindDownload, Instance: "rpi-box"}
	wayne   = integrations.Ref{Kind: integrations.KindDownload, Instance: "wayne-nzb"}
)

// frozen is the harness clock's start.
var frozen = time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	t        testing.TB
	reg      *integrations.Registry
	out      *integrationstest.Outbound
	clock    *clock
	disabled atomic.Bool

	mu      sync.Mutex
	clients map[integrations.InstanceID]download.Client
	builds  map[integrations.InstanceID]int
	fakes   map[integrations.InstanceID]*coretest.FakeDownloadClient
}

// newHarness boots the store and a Registry with the two test adapters and,
// when extra is not nil, the descriptors it returns (registered by origin
// "core").
func newHarness(t testing.TB, extra ...integrations.Descriptor) *harness {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: instancestore.Owner, Spec: instancestore.StorageSpec(), Enabled: true},
			{Owner: secretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
			{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
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
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	ks := keyspaceOf(t, db, instancestore.Owner)
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	h := &harness{
		t: t, out: &integrationstest.Outbound{}, clock: &clock{t: frozen},
		clients: map[integrations.InstanceID]download.Client{},
		builds:  map[integrations.InstanceID]int{},
		fakes:   map[integrations.InstanceID]*coretest.FakeDownloadClient{},
	}
	sec, err := secrets.Open(ctx, secrets.Options{StateDir: root, Keyspace: keyspaceOf(t, db, secretsOwner), AppID: "gotham", Now: h.clock.now})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	aks := keyspaceOf(t, db, auditOwner)
	au, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq"), Now: h.clock.now})
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	reg, err := integrations.New(integrations.Options{
		Store:    integrations.NativeInstanceStore(ks),
		NewBatch: ks.NewBatch,
		Secrets:  sec,
		Audit:    au,
		Outbound: h.out.Func(),
		Settings: func() integrations.Settings { return integrations.Settings{Disabled: h.disabled.Load()} },
		Now:      h.clock.now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	empty, err := config.SchemaFor[struct{}]()
	if err != nil {
		t.Fatal(err)
	}
	build := func(_ context.Context, _ integrations.Env, cfg integrations.InstanceConfig) (download.Client, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.builds[cfg.Ref.Instance]++
		c, ok := h.clients[cfg.Ref.Instance]
		if !ok {
			return nil, fmt.Errorf("the harness has no client for %s", cfg.Ref)
		}
		return c, nil
	}
	hello := []integrations.Descriptor{
		download.Adapter(integrations.Meta{ID: crateAdapter, DisplayName: "Bat-Crate", Settings: empty}, build),
		download.Adapter(integrations.Meta{ID: nzbAdapter, DisplayName: "Bat-NZB", Settings: empty}, build),
	}
	if err := reg.Register("hello", hello...); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(extra) > 0 {
		if err := reg.Register("core", extra...); err != nil {
			t.Fatalf("Register core: %v", err)
		}
	}
	if err := reg.Load(ctx, nil); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := reg.Close(cctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	h.reg = reg
	return h
}

func keyspaceOf(t testing.TB, db *storage.DB, o storage.Owner) storage.Keyspace {
	t.Helper()
	ks, ok := db.Keyspace(o)
	if !ok {
		t.Fatalf("owner %s has no keyspace", o)
	}
	return ks
}

// admin is the principal that may write instances (and set Local).
func admin() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage, integrationstest.Admin)
}

func manager() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage)
}

// addFake registers a FakeDownloadClient as instance ref of adapter and
// stores the instance (enabled, with priority).
func (h *harness) addFake(ref integrations.Ref, adapter integrations.AdapterID, priority int) *coretest.FakeDownloadClient {
	h.t.Helper()
	f := coretest.NewFakeDownloadClient(ref.Instance)
	h.mu.Lock()
	h.clients[ref.Instance] = f
	h.fakes[ref.Instance] = f
	h.mu.Unlock()
	h.put(ref, adapter, priority, true)
	return f
}

// serve makes c the client of ref (for a client the fake cannot be).
func (h *harness) serve(ref integrations.Ref, adapter integrations.AdapterID, c download.Client) {
	h.t.Helper()
	h.mu.Lock()
	h.clients[ref.Instance] = c
	h.mu.Unlock()
	h.put(ref, adapter, 0, true)
}

func (h *harness) put(ref integrations.Ref, adapter integrations.AdapterID, priority int, enabled bool) integrations.InstanceConfig {
	h.t.Helper()
	var version uint64
	if cur, ok := h.reg.Config(ref); ok {
		version = cur.Version // an update names the row's version it read
	}
	got, err := h.reg.Put(admin(), integrations.InstanceConfig{
		Ref: ref, Adapter: adapter, Name: string(ref.Instance), Enabled: enabled, Priority: priority, Settings: map[string]any{}, Version: version,
	}, nil)
	if err != nil {
		h.t.Fatalf("Put %s: %v", ref, err)
	}
	return got
}

func (h *harness) buildCount(ref integrations.Ref) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.builds[ref.Instance]
}

// item is a ScenarioItem of the batch-themed fixtures.
func item(id download.ItemID, name, savePath, native string, progress float64) coretest.ScenarioItem {
	return coretest.ScenarioItem{ID: id, Name: name, Category: "audiobooks", SavePath: savePath, Native: native, Progress: progress, TotalSize: 1000,
		Files: []download.File{{Path: name + "/part1.m4b", Size: 900, Progress: progress, Priority: 1}}}
}

// mapper is a PathMapper over fixed mappings per instance.
func mapper(rows map[integrations.InstanceID][]download.Mapping) download.PathMapper {
	return download.NewMapper(func(source string) []download.Mapping {
		for id, m := range rows {
			if download.Source(id) == source {
				return m
			}
		}
		return nil
	})
}

// newPool is a Pool over h with opts defaulted to h's clock.
func (h *harness) newPool(o download.PoolOptions) *download.Pool {
	h.t.Helper()
	if o.Now == nil {
		o.Now = h.clock.now
	}
	p, err := download.NewPool(h.reg, o)
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}
