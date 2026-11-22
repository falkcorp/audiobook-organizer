// file: core/httpapi/listener_test.go
// version: 1.0.0
// guid: 5f61a5fe-fc3d-441b-96b9-9de3766681ae
// last-edited: 2026-11-07

package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestListener_ServeAndShutdown(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "Wayne Manor") })
	l, err := Listen(ListenOptions{Addr: "localhost:0"}, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- l.Serve() }()

	resp, err := http.Get("http://" + l.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "Wayne Manor" {
		t.Fatalf("body = %q", body)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := l.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatalf("Serve after Shutdown = %v, want nil", err)
	}
}

// OnShutdown functions run when Shutdown begins, before the connections drain:
// a long-lived stream (the realtime hub's SSE) is told to end there, or the
// drain would wait for it until the budget ran out.
func TestListen_OnShutdownRunsWhenShutdownBegins(t *testing.T) {
	ran := make(chan struct{})
	l, err := Listen(ListenOptions{Addr: "localhost:0", OnShutdown: []func(){func() { close(ran) }}}, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = l.Serve() }()
	select {
	case <-ran:
		t.Fatal("the shutdown hook ran before Shutdown")
	default:
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := l.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown never ran the OnShutdown hook")
	}
}

func TestListen_Refusals(t *testing.T) {
	busy, err := Listen(ListenOptions{Addr: "localhost:0"}, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Shutdown(t.Context()) })
	// An occupied port fails at Listen, before readiness.
	if _, err := Listen(ListenOptions{Addr: busy.Addr().String()}, http.NotFoundHandler(), nil); err == nil {
		t.Fatal("Listen on an occupied port succeeded")
	}
	if _, err := Listen(ListenOptions{Addr: "localhost:0", TLSCertFile: "cert.pem"}, http.NotFoundHandler(), nil); err == nil {
		t.Fatal("Listen with a certificate and no key succeeded")
	}
	_, err = Listen(ListenOptions{Addr: "localhost:0", TLSCertFile: "missing.pem", TLSKeyFile: "missing.key"}, http.NotFoundHandler(), nil)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Listen with an unreadable TLS pair = %v, want a load error", err)
	}
}
