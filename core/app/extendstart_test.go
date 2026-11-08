// file: core/app/extendstart_test.go
// version: 1.1.0
// guid: 0d057159-dd11-4c4f-a871-b20ed3ff5611
// last-edited: 2026-11-08

//go:build !windows

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/lifecycle"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// notifySocket binds a unix datagram socket and points NOTIFY_SOCKET at it
// for the test. It lives under os.MkdirTemp rather than t.TempDir: a socket
// path is limited to 104 bytes on darwin and t.TempDir's passes that.
func notifySocket(t *testing.T) *net.UnixConn {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv(lifecycle.NotifySocketEnv, path)
	return conn
}

// nextDatagram returns the next datagram, or "" once nothing arrives within
// the timeout.
func nextDatagram(t *testing.T, conn *net.UnixConn, timeout time.Duration) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return ""
		}
		t.Fatal(err)
	}
	return string(buf[:n])
}

// TestExtendStart_ExtendsTimeout: the stateless ExtendStart, which app code
// that runs before New calls (the store's open and migration steps), asks the
// service manager for StartupStepBudget (10 minutes) more start time.
func TestExtendStart_ExtendsTimeout(t *testing.T) {
	sock := notifySocket(t)
	ExtendStart("store open")
	if got := nextDatagram(t, sock, 5*time.Second); got != "EXTEND_TIMEOUT_USEC=600000000" {
		t.Fatalf("datagram = %q, want EXTEND_TIMEOUT_USEC=600000000", got)
	}
	// One request per call: a second step asks again, and nothing else is sent.
	ExtendStart("migration 7")
	if got := nextDatagram(t, sock, 5*time.Second); got != "EXTEND_TIMEOUT_USEC=600000000" {
		t.Fatalf("second datagram = %q", got)
	}
	if got := nextDatagram(t, sock, 200*time.Millisecond); got != "" {
		t.Fatalf("extra datagram %q", got)
	}
}

// Without NOTIFY_SOCKET (Type=simple, tests, a developer machine) nothing is
// sent and nothing fails.
func TestExtendStart_WithoutSocketIsNoOp(t *testing.T) {
	t.Setenv(lifecycle.NotifySocketEnv, "")
	if err := os.Unsetenv(lifecycle.NotifySocketEnv); err != nil {
		t.Fatal(err)
	}
	ExtendStart("store open")
}

// A socket nobody listens on is a failed write: logged, never fatal, never a
// panic (HEAD's behaviour for a start-timeout extension).
func TestExtendStart_WriteFailureIsNotFatal(t *testing.T) {
	dir, err := os.MkdirTemp("", "sdn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(lifecycle.NotifySocketEnv, filepath.Join(dir, "absent.sock"))
	ExtendStart("store open")
}

// TestFastExit: --print-capabilities is answered with exactly the token list
// the deploy and rollback recipes grep for; every other argument vector is
// left to the host.
func TestFastExit(t *testing.T) {
	var out bytes.Buffer
	handled, err := FastExit([]string{"audiobook-organizer", "--print-capabilities"}, &out)
	if err != nil || !handled {
		t.Fatalf("FastExit = %v, %v, want handled", handled, err)
	}
	if out.String() != "sd_notify\n" {
		t.Fatalf("stdout = %q, want %q (byte-identical to HEAD)", out.String(), "sd_notify\n")
	}

	for _, args := range [][]string{
		nil,
		{"audiobook-organizer"},
		{"audiobook-organizer", "serve"},
		{"audiobook-organizer", "--print-capabilities", "extra"},
		{"audiobook-organizer", "serve", "--print-capabilities"},
		{"audiobook-organizer", "--print-storage-format"},
		{"audiobook-organizer", "--print-storage-levels", "extra"},
	} {
		out.Reset()
		handled, err := FastExit(args, &out)
		if handled || err != nil || out.Len() != 0 {
			t.Errorf("FastExit(%q) = %v, %v with stdout %q, want not handled and silent", args, handled, err, out.String())
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed pipe") }

// A stdout that cannot be written is reported, so the host exits non-zero
// rather than letting a recipe read an empty capability list as "none".
func TestFastExit_WriteErrorIsReturned(t *testing.T) {
	handled, err := FastExit([]string{"x", "--print-capabilities"}, failingWriter{})
	if !handled || err == nil || !strings.Contains(err.Error(), "closed pipe") {
		t.Fatalf("FastExit = %v, %v, want handled with the write error", handled, err)
	}
}

// levelsModule declares storage at a level, for --print-storage-levels.
type levelsModule struct {
	id    module.ID
	level int
}

func (m levelsModule) Manifest() module.Manifest {
	return module.Manifest{ID: m.id, Version: "1.0.0", MinCoreVersion: "0.1.0",
		Storage: storage.Spec{Keyspace: true, Level: m.level}}
}
func (levelsModule) Register(*module.Registrar) error { return nil }

// storagelessModule declares no storage, so it is no level owner.
type storagelessModule struct{}

func (storagelessModule) Manifest() module.Manifest {
	return module.Manifest{ID: "oracle", Version: "1.0.0", MinCoreVersion: "0.1.0"}
}
func (storagelessModule) Register(*module.Registrar) error { return nil }

// TestFastExit_PrintStorageLevels (05-storage §5S.3.7 "Fast-exit flags"):
// --print-storage-levels prints the build's supported vector as one JSON
// object computed from the manifests alone: core's own owner at 0 and every
// storage owner at its declared level; a module without storage is no
// owner. With no modules it is core's alone.
func TestFastExit_PrintStorageLevels(t *testing.T) {
	var out bytes.Buffer
	handled, err := FastExit([]string{"batcomputer", "--print-storage-levels"}, &out,
		levelsModule{id: "batcave", level: 2}, levelsModule{id: "gotham"}, storagelessModule{})
	if err != nil || !handled {
		t.Fatalf("FastExit = %v, %v, want handled", handled, err)
	}
	if !strings.HasSuffix(out.String(), "\n") || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("stdout = %q, want one line", out.String())
	}
	var got map[string]int
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q is not a JSON object: %v", out.String(), err)
	}
	want := map[string]int{"core": 0, "batcave": 2, "gotham": 0}
	if !maps.Equal(got, want) {
		t.Fatalf("levels = %v, want %v", got, want)
	}

	out.Reset()
	if handled, err := FastExit([]string{"batcomputer", "--print-storage-levels"}, &out); !handled || err != nil || out.String() != `{"core":0}`+"\n" {
		t.Fatalf("no modules: FastExit = %v, %v, stdout %q, want core's vector alone", handled, err, out.String())
	}
}
