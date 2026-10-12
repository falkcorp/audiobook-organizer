// file: internal/search/register_sdnotify_test.go
// version: 1.0.0
// guid: a1da032b-4362-43dd-8fbe-154f8e481e27
// last-edited: 2026-10-12

//go:build !windows

package search

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/require"
)

// TestIndexServiceStartExtendsStartTimeout: opening the Bleve index is a long
// startup step, so IndexService.Start asks systemd (Type=notify) for more start
// time before it opens the index.
func TestIndexServiceStartExtendsStartTimeout(t *testing.T) {
	// os.MkdirTemp, not t.TempDir: darwin limits a socket path to 104 bytes.
	dir, err := os.MkdirTemp("", "sdn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "notify.sock")
	sock, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sock.Close() })
	t.Setenv("NOTIFY_SOCKET", sockPath)

	svc := &IndexService{cfg: &config.Config{DatabasePath: filepath.Join(t.TempDir(), "audiobooks.pebble")}}
	require.NoError(t, svc.Start(context.Background()))
	t.Cleanup(func() { _ = svc.Stop(context.Background()) })

	require.NoError(t, sock.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, 512)
	n, err := sock.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "EXTEND_TIMEOUT_USEC=600000000", string(buf[:n]))
}
