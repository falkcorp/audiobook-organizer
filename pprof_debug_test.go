// file: pprof_debug_test.go
// version: 1.0.0
// guid: 5b0c7e1a-3d42-4f86-9a17-c2e8d6f4b913
// last-edited: 2026-10-11

//go:build pprof

package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestStartPprofFromEnv_UnsetStartsNothing(t *testing.T) {
	called := false
	addr, started := startPprofFromEnv(func(string) string { return "" }, func(string, http.Handler) { called = true })
	if started || addr != "" || called {
		t.Fatalf("got addr=%q started=%v called=%v, want nothing started", addr, started, called)
	}
}

func TestStartPprofFromEnv_SetCallsServe(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	var gotAddr string
	var gotHandler http.Handler
	addr, started := startPprofFromEnv(func(string) string { return ":6060" }, func(a string, h http.Handler) {
		defer wg.Done()
		gotAddr, gotHandler = a, h
	})
	wg.Wait()
	if !started || addr != "127.0.0.1:6060" || gotAddr != "127.0.0.1:6060" || gotHandler == nil {
		t.Fatalf("got addr=%q started=%v serve addr=%q handler=%v", addr, started, gotAddr, gotHandler)
	}
}

func TestResolvePprofAddr(t *testing.T) {
	for in, want := range map[string]string{
		":6060":          "127.0.0.1:6060",
		"localhost:6060": "localhost:6060",
		"127.0.0.1:6060": "127.0.0.1:6060",
		"":               "",
	} {
		if got := resolvePprofAddr(in); got != want {
			t.Errorf("resolvePprofAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPprofMuxServesGoroutineProfile(t *testing.T) {
	srv := httptest.NewServer(newPprofMux())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestPprofMuxDoesNotUseDefaultMux(t *testing.T) {
	http.DefaultServeMux.HandleFunc("/pprof-test-only-route", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(newPprofMux())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/pprof-test-only-route")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (pprof mux must not serve DefaultServeMux routes)", resp.StatusCode)
	}
}
