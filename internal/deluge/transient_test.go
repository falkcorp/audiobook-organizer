// file: internal/deluge/transient_test.go
// version: 0.1.0
// guid: 5b0d3a6e-8c14-4f27-9a52-e1c7f3d08b46
// last-edited: 2026-11-22

package deluge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIsTransient pins the split the Deluge follow-up's bounded retry rests on:
// what a second attempt can outlive (transport, a deadline, a 5xx from a server
// or the proxy in front of it) against what the Web UI answered on purpose.
func TestIsTransient(t *testing.T) {
	serve := func(h http.HandlerFunc) *Client {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		c, err := New(srv.URL, "arkham")
		require.NoError(t, err)
		return c
	}
	call := func(c *Client) error {
		_, err := c.callCtx(context.Background(), "core.move_storage", []string{"a"}, "/gotham")
		return err
	}

	t.Run("a Deluge RPC refusal is permanent", func(t *testing.T) {
		err := call(serve(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"id":1,"result":null,"error":{"message":"InvalidTorrentError: torrent_id a not in session.","code":4}}`)
		}))
		require.Error(t, err)
		require.False(t, IsTransient(err))
	})
	t.Run("a 502 page from the proxy is transient", func(t *testing.T) {
		err := call(serve(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "<html>bad gateway</html>")
		}))
		require.Error(t, err)
		require.True(t, IsTransient(err))
	})
	t.Run("a 200 body that does not decode is permanent", func(t *testing.T) {
		err := call(serve(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>login</html>") }))
		require.Error(t, err)
		require.False(t, IsTransient(err), "the server answered; asking again gets the same page")
	})
	t.Run("a connection refused is transient", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		c, err := New(srv.URL, "arkham")
		require.NoError(t, err)
		srv.Close()
		require.True(t, IsTransient(call(c)))
	})
	t.Run("a bad URL scheme is permanent although url.Error is a net.Error", func(t *testing.T) {
		c, err := New("gopher://gotham", "arkham")
		require.NoError(t, err)
		e := call(c)
		require.Error(t, e)
		require.False(t, IsTransient(e))
	})
	t.Run("a canceled context is not transient", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := serve(func(http.ResponseWriter, *http.Request) {})
		_, err := c.callCtx(ctx, "core.move_storage")
		require.Error(t, err)
		require.False(t, IsTransient(err))
	})
	t.Run("plain errors", func(t *testing.T) {
		require.False(t, IsTransient(nil))
		require.False(t, IsTransient(errors.New("move failed")))
		require.False(t, IsTransient(fmt.Errorf("wrap: %w", ErrTorrentNotFound)))
		require.True(t, IsTransient(context.DeadlineExceeded))
		require.True(t, IsTransient(fmt.Errorf("read response: %w", io.ErrUnexpectedEOF)))
		require.True(t, IsTransient(&net.OpError{Op: "read", Err: syscall.ECONNRESET}))
		require.True(t, IsTransient(&net.DNSError{IsTemporary: true}))
		require.False(t, IsTransient(&net.DNSError{IsNotFound: true}))
	})
}
