// file: internal/books/adapters/whisper/rig_test.go
// version: 0.1.0
// guid: e809b23f-210c-4e61-8aff-3119ff338623
// last-edited: 2026-11-21

package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
)

// seen is one request that reached the worker.
type seen struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// rig is a whisper_server instance wired through a real providerhttp gate to a
// loopback upstream that plays the worker: the adapter's calls go over a real
// HTTP wire, and the gate decides what may leave.
type rig struct {
	t    testing.TB
	kit  *coretest.ProviderHTTPKit
	mux  *http.ServeMux
	inst *serverInstance

	mu   sync.Mutex
	reqs []seen
}

var gotham = integrations.Ref{Kind: integrations.KindAI, Instance: "batcave-worker"}

func (r *rig) requests() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seen(nil), r.reqs...)
}

// only returns the single request the worker saw.
func (r *rig) only() seen {
	r.t.Helper()
	reqs := r.requests()
	if len(reqs) != 1 {
		r.t.Fatalf("the worker saw %d request(s), want 1: %+v", len(reqs), reqs)
	}
	return reqs[0]
}

// on routes a "METHOD /path" pattern (ServeMux syntax) to h.
func (r *rig) on(pattern string, h http.HandlerFunc) { r.mux.HandleFunc(pattern, h) }

// newRig builds an instance. edit may change the settings before the build.
func newRig(t testing.TB, edit ...func(map[string]any)) *rig {
	t.Helper()
	kit := coretest.ProviderHTTP(t)
	d, err := ServerDescriptor()
	if err != nil {
		t.Fatalf("ServerDescriptor: %v", err)
	}
	meta := d.Meta()
	r := &rig{t: t, kit: kit, mux: http.NewServeMux()}
	kit.Handle(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		r.mu.Lock()
		r.reqs = append(r.reqs, seen{Method: req.Method, Path: req.URL.Path, Header: req.Header.Clone(), Body: body})
		r.mu.Unlock()
		r.mux.ServeHTTP(w, req)
	})
	h, g := kit.Registry.Outbound(integrations.OutboundSpec{
		Ref: gotham, Adapter: ServerAdapterID, Egress: meta.Egress, Limits: meta.Limits,
		ConfiguredOrigins: []string{"http://" + kit.Host()},
	})
	env := integrations.Env{Ref: gotham, Log: slog.New(slog.DiscardHandler), HTTP: h, Gate: g, Now: kit.Clock.Now}
	settings := map[string]any{"url": kit.URL(), "concurrency": 1}
	for _, e := range edit {
		e(settings)
	}
	b, err := buildServer(context.Background(), env, integrations.InstanceConfig{Ref: gotham, Adapter: ServerAdapterID, Enabled: true, Settings: settings})
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	r.inst = b.(*serverInstance)
	return r
}

func writeJSON(t testing.TB, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if s, ok := v.(string); ok {
		_, _ = io.WriteString(w, s)
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("rig: encode the answer: %v", err)
	}
}

// metalWorker is the /health of a healthy Apple-silicon worker that maps the
// library root and reads it.
func metalWorker(prefix string) map[string]any {
	return map[string]any{
		"model": "whisper-large-v3-turbo", "device": "metal", "batch_pipeline": true,
		"path_pipeline": true, "path_roots": []map[string]any{{"prefix": prefix, "readable": true}},
	}
}

func classOf(err error) integrations.Class { return integrations.ClassOf(context.Background(), err) }
