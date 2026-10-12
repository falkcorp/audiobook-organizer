// file: pprof_debug_test.go
// version: 1.1.0
// guid: 5b0c7e1a-3d42-4f86-9a17-c2e8d6f4b913
// last-edited: 2026-10-11

//go:build pprof

package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
		":6060":            "127.0.0.1:6060",
		"localhost:6060":   "localhost:6060",
		"127.0.0.1:6060":   "127.0.0.1:6060",
		"":                 "",
		"0.0.0.0:6060":     "0.0.0.0:6060", // explicit all-interfaces is kept (and warned about), not rewritten
		"[::]:6060":        "[::]:6060",
		"example.com:6060": "example.com:6060",
		"6060":             "6060", // no colon: SplitHostPort errors, returned unchanged
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

func TestStartPprofFromEnv_WarnsOnUnspecifiedHost(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	for in, wantWarn := range map[string]bool{
		"0.0.0.0:6060":   true,
		"[::]:6060":      true,
		"127.0.0.1:6060": false,
		"localhost:6060": false,
	} {
		buf.Reset()
		var wg sync.WaitGroup
		wg.Add(1)
		startPprofFromEnv(func(string) string { return in }, func(string, http.Handler) { wg.Done() })
		wg.Wait()
		if got := strings.Contains(buf.String(), "binds every interface"); got != wantWarn {
			t.Errorf("%q: warned=%v want %v; log=%s", in, got, wantWarn, buf.String())
		}
	}
}
