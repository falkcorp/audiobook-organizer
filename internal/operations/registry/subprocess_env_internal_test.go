// file: internal/operations/registry/subprocess_env_internal_test.go
// version: 1.1.0
// guid: 7c1d9e42-5a3b-4f68-b0e7-2d4a6c8e1f35
// last-edited: 2026-10-12

package registry

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/sdnotify"
)

func TestChildBaseEnv_StripsPprofAddr(t *testing.T) {
	parent := []string{"PATH=/bin", "ABK_PPROF_ADDR=localhost:6060", "ABK_PPROF_ADDR_OTHER=keep", "HOME=/h"}
	got := childBaseEnv(parent)
	want := []string{"PATH=/bin", "ABK_PPROF_ADDR_OTHER=keep", "HOME=/h"}
	if !slices.Equal(got, want) {
		t.Fatalf("childBaseEnv = %v, want %v", got, want)
	}
	if len(parent) != 4 || parent[1] != "ABK_PPROF_ADDR=localhost:6060" {
		t.Fatalf("childBaseEnv mutated its input: %v", parent)
	}
}

// The name is repeated from package main's pprof_debug.go (pprofAddrEnv); a
// rename there must be mirrored here or children would inherit the listener.
func TestChildBaseEnv_PprofNameMatchesRootPackage(t *testing.T) {
	if envPprofAddr != "ABK_PPROF_ADDR" {
		t.Fatalf("envPprofAddr = %q", envPprofAddr)
	}
}

// A child must not inherit systemd's readiness socket: child mode opens the
// store, which sends EXTEND_TIMEOUT_USEC, and from a non-main PID systemd
// (NotifyAccess=main) logs a warning for every child run.
func TestChildBaseEnv_StripsNotifySocket(t *testing.T) {
	parent := []string{"PATH=/bin", "NOTIFY_SOCKET=/run/systemd/notify", "NOTIFY_SOCKET_X=keep", "ABK_PPROF_ADDR=localhost:6060", "HOME=/h"}
	got := childBaseEnv(parent)
	want := []string{"PATH=/bin", "NOTIFY_SOCKET_X=keep", "HOME=/h"}
	if !slices.Equal(got, want) {
		t.Fatalf("childBaseEnv = %v, want %v", got, want)
	}
}

// The stripped name must be the one systemd sets and sdnotify reads.
func TestChildBaseEnv_NotifySocketName(t *testing.T) {
	if sdnotify.SocketEnv != "NOTIFY_SOCKET" {
		t.Fatalf("sdnotify.SocketEnv = %q", sdnotify.SocketEnv)
	}
}
