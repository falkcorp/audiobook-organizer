// file: internal/books/adapters/metadata/rig_test.go
// version: 0.1.0
// guid: e593bcf8-019b-49e6-b6bf-b776551cb81b
// last-edited: 2026-11-20

package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// memStore is a read-only InstanceStore over fixed rows: the cutover's output,
// as Load reads it.
type memStore struct{ rows []integrations.StoredInstance }

func (m memStore) Load(context.Context) ([]integrations.StoredInstance, error) { return m.rows, nil }
func (memStore) Put(context.Context, *storage.Batch, integrations.StoredInstance) (uint64, error) {
	return 0, errors.New("unused")
}
func (memStore) Delete(context.Context, *storage.Batch, integrations.Ref) error {
	return errors.New("unused")
}

// memSecrets serves stored secrets by name.
type memSecrets map[secrets.Name]string

func (m memSecrets) Get(_ context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	v, ok := m[n]
	if !ok {
		return secrets.Value{}, secrets.Meta{}, secrets.ErrNotSet
	}
	return secrets.NewValue(v), secrets.Meta{Name: n, Set: true, Source: secrets.SourceStored}, nil
}
func (m memSecrets) Meta(_ context.Context, n secrets.Name) (secrets.Meta, error) {
	if _, ok := m[n]; !ok {
		return secrets.Meta{}, secrets.ErrNotSet
	}
	return secrets.Meta{Name: n, Set: true, Source: secrets.SourceStored}, nil
}
func (memSecrets) Stage(context.Context, *storage.Batch, secrets.Write) error {
	return errors.New("unused")
}

type noAudit struct{}

func (noAudit) RecordIn(context.Context, *storage.Batch, audit.Event) error { return nil }

// route is an Outbound that sends every instance's requests to one httptest
// server, whatever host the client dials, and records each request. It stands
// in for providerhttp: the gate, bucket and egress guard are core's, tested there.
type route struct {
	mu   sync.Mutex
	seen []*http.Request
	// origURLs are the URLs the clients built, before the transport sent them to
	// the test server: where the request would really have gone.
	origURLs []string
	// specs are what the Registry asked its Outbound for, per instance.
	specs []integrations.OutboundSpec
	h     http.HandlerFunc
	srv   *httptest.Server
}

func newRoute(t *testing.T, h http.HandlerFunc) *route {
	t.Helper()
	r := &route{h: h}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		r.seen = append(r.seen, q.Clone(context.Background()))
		r.mu.Unlock()
		r.h(w, q)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *route) requests() []*http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*http.Request(nil), r.seen...)
}

func (r *route) urls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.origURLs...)
}

func (r *route) specFor(ref integrations.Ref) (integrations.OutboundSpec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.specs {
		if s.Ref == ref {
			return s, true
		}
	}
	return integrations.OutboundSpec{}, false
}

type rtHTTP struct{ c *http.Client }

func (h rtHTTP) Do(req *http.Request) (*http.Response, error) { return h.c.Do(req) }
func (h rtHTTP) Client() *http.Client                         { return h.c }
func (rtHTTP) Gate() integrations.Gate                        { return nil }

type redirect struct {
	to string
	r  *route
}

func (d redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	d.r.mu.Lock()
	d.r.origURLs = append(d.r.origURLs, req.URL.String())
	d.r.mu.Unlock()
	u := *req.URL
	srv, _ := http.NewRequest(http.MethodGet, d.to, nil)
	u.Scheme, u.Host = srv.URL.Scheme, srv.URL.Host
	r2 := req.Clone(req.Context())
	r2.URL, r2.Host = &u, u.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func (r *route) outbound() func(integrations.OutboundSpec) (integrations.HTTP, integrations.Gate) {
	return func(spec integrations.OutboundSpec) (integrations.HTTP, integrations.Gate) {
		r.mu.Lock()
		r.specs = append(r.specs, spec)
		r.mu.Unlock()
		return rtHTTP{&http.Client{Transport: redirect{to: r.srv.URL, r: r}}}, nil
	}
}

// load builds a Registry holding the six adapters and rows, and loads it.
func load(t *testing.T, o func(integrations.OutboundSpec) (integrations.HTTP, integrations.Gate), sec memSecrets, rows ...integrations.StoredInstance) *integrations.Registry {
	t.Helper()
	reg, err := integrations.New(integrations.Options{
		Store: memStore{rows}, NewBatch: func() *storage.Batch { return nil },
		Secrets: sec, Audit: noAudit{}, Outbound: o,
	})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	ds, err := Descriptors(Options{})
	if err != nil {
		t.Fatalf("descriptors: %v", err)
	}
	if err := reg.Register("books", ds...); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Load(context.Background(), nil); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(context.Background()) })
	return reg
}

// row is one stored metadata instance as the M6 cutover writes it: origin
// "migration", enabled, adapter "books."+id.
func row(id string, settings map[string]any) integrations.StoredInstance {
	return integrations.StoredInstance{
		Config: integrations.InstanceConfig{
			Ref:     integrations.Ref{Kind: integrations.KindMetadata, Instance: integrations.InstanceID(id)},
			Adapter: integrations.AdapterID("books." + id), Name: id, Enabled: true,
			Origin: integrations.OriginMigration, Settings: settings,
		},
		SecretsOrigin: "adapter-default",
	}
}
