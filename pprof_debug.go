// file: pprof_debug.go
// version: 1.2.0
// guid: f9e1a23b-4c56-7890-d1e2-f3a4b5c6d7e8
// last-edited: 2026-10-11

//go:build pprof

package main

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"time"
)

// pprofAddrEnv names the env var that opts into the pprof debug listener.
// Even in the `pprof` build the listener stays OFF unless this is set, so a
// pprof-enabled binary never silently exposes unauthenticated goroutine dumps,
// heap profiles, source paths, and CPU profiling (pen-test finding HIGH-1).
// To enable for a local debugging session, bind to loopback explicitly:
//
//	ABK_PPROF_ADDR=localhost:6060 ./audiobook-organizer
//
// For a remote host, prefer SSH port-forwarding over binding a public address.
//
// Importing net/http/pprof registers its handlers on http.DefaultServeMux as an
// import side effect, even though this file only calls its exported handler
// functions. That registration is inert because nothing in this binary serves
// DefaultServeMux and the listener below uses its own mux;
// TestPprofGuard_NothingServesDefaultServeMux enforces the first half.
//
// Child processes (isolated operations re-exec this binary) do not inherit
// ABK_PPROF_ADDR: registry.childBaseEnv strips it.
const pprofAddrEnv = "ABK_PPROF_ADDR"

// newPprofMux returns a dedicated mux with the pprof handlers mounted
// explicitly. The named profiles (heap, goroutine, goroutineleak, ...) are
// served by pprof.Index.
func newPprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// resolvePprofAddr pins an address with an empty host (":6060") to loopback so
// a bare port never binds every interface. Anything else is returned unchanged.
func resolvePprofAddr(raw string) string {
	host, port, err := net.SplitHostPort(raw)
	if err != nil || host != "" {
		return raw
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// startPprofFromEnv starts the listener only when ABK_PPROF_ADDR is non-empty,
// by calling the injected serve in a goroutine. Tests inject a no-op serve.
func startPprofFromEnv(getenv func(string) string, serve func(addr string, h http.Handler)) (addr string, started bool) {
	raw := getenv(pprofAddrEnv)
	if raw == "" {
		slog.Info("pprof build active but listener disabled; set " + pprofAddrEnv + " (e.g. localhost:6060) to enable")
		return "", false
	}
	addr = resolvePprofAddr(raw)
	if addr != raw {
		slog.Warn("pprof address had no host; pinned to loopback", "requested", raw, "addr", addr)
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			slog.Warn("pprof address binds every interface; profiling endpoints are unauthenticated", "addr", addr)
		}
	}
	go func() {
		slog.Warn("pprof listener enabled — exposes unauthenticated profiling data; bind to loopback only", "addr", addr)
		serve(addr, newPprofMux())
	}()
	return addr, true
}

func init() {
	startPprofFromEnv(os.Getenv, func(addr string, h http.Handler) {
		srv := &http.Server{
			Addr:              addr,
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second, // no write timeout: CPU profiles run 30s+
		}
		if err := srv.ListenAndServe(); err != nil {
			slog.Warn("pprof server failed", "error", err)
		}
	})
}
