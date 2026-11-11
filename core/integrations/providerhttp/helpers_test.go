// file: core/integrations/providerhttp/helpers_test.go
// version: 1.0.0
// guid: e67a0ad0-2034-4b3e-917c-c3b16a984944
// last-edited: 2026-11-11

package providerhttp_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/storage"
)

// clock is the Registry's clock in a test: Sleep advances it at once and
// records the wait, so a back-off or a bucket wait is asserted, not slept.
type clock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newClock() *clock { return &clock{t: time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return nil
}

func (c *clock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// batcave is the instance most tests act as.
var batcave = integrations.Ref{Kind: integrations.KindMetadata, Instance: "batcave"}

func ref(name string) integrations.Ref {
	return integrations.Ref{Kind: integrations.KindMetadata, Instance: integrations.InstanceID(name)}
}

// newRegistry builds a Registry on clk; mut edits the Options.
func newRegistry(t testing.TB, clk *clock, mut ...func(*providerhttp.Options)) *providerhttp.Registry {
	t.Helper()
	o := providerhttp.Options{Now: clk.Now, Sleep: clk.Sleep, Jitter: func(time.Duration) time.Duration { return 0 }}
	for _, m := range mut {
		m(&o)
	}
	r, err := providerhttp.New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(r.Close)
	return r
}

// anyPrivate trusts every endpoint for private addresses: the loopback
// httptest servers a test reaches are private.
func anyPrivate(string) bool { return true }

// noRetry is a Limits with retries off ("negative" is the way to say zero).
func limits(rps float64, burst int) integrations.Limits {
	return integrations.Limits{RPS: rps, Burst: burst, MaxRetries: -1, Timeout: 5 * time.Second}
}

// server starts a loopback server whose handler counts its hits.
func server(t testing.TB, h http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s, &n
}

func ok(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") }

// get issues a GET through c.Do and closes the body.
func get(t testing.TB, c *providerhttp.Client, url string) (*http.Response, error) {
	return getCtx(t, context.Background(), c, url)
}

func getCtx(t testing.TB, ctx context.Context, c *providerhttp.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return resp, err
}

// store boots a core store with the providerhttp keyspace over memory and
// returns it with that keyspace.
func store(t testing.TB) storage.Keyspace {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(t.TempDir(), "main.pebble"),
		Owners:          []storage.OwnerSpec{{Owner: providerhttp.Owner, Spec: providerhttp.StorageSpec(), Enabled: true}},
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
	ks, ok := db.Keyspace(providerhttp.Owner)
	if !ok {
		t.Fatal("no providerhttp keyspace")
	}
	return ks
}

// hostOf is the "host:port" of a test server.
func hostOf(s *httptest.Server) string {
	return s.Listener.Addr().(*net.TCPAddr).String()
}
