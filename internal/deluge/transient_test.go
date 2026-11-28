// file: internal/deluge/transient_test.go
// version: 0.1.0
// guid: 5b0d3a6e-8c14-4f27-9a52-e1c7f3d08b46
// last-edited: 2026-11-22

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

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
	t.Run("an undecodable 4xx body is permanent", func(t *testing.T) {
		err := call(serve(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<html>not found</html>")
		}))
		require.Error(t, err)
		require.False(t, IsTransient(err), "a 4xx is the server answering: the same call gets the same page")
	})
	// The error a real http.Client returns when the dial's lookup fails is a
	// *url.Error wrapping a *net.OpError wrapping the *net.DNSError, so the
	// DNS verdict has to win over the OpError rule.
	dialFails := func(dns *net.DNSError) error {
		c, err := New("http://deluge.gotham.example:8112", "arkham")
		require.NoError(t, err)
		c.client.Transport = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: dns}
		}}
		e := call(c)
		require.Error(t, e)
		var op *net.OpError
		require.ErrorAs(t, e, &op, "the fixture must have the shape a real dial failure has")
		return e
	}
	t.Run("an NXDOMAIN from a failed dial is permanent", func(t *testing.T) {
		require.False(t, IsTransient(dialFails(&net.DNSError{Err: "no such host", Name: "deluge.gotham.example", IsNotFound: true})))
	})
	t.Run("a DNS timeout from a failed dial is transient", func(t *testing.T) {
		require.True(t, IsTransient(dialFails(&net.DNSError{Err: "i/o timeout", Name: "deluge.gotham.example", IsTimeout: true})))
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
		require.True(t, IsTransient(fmt.Errorf("deluge rpc core.move_storage: %w", io.EOF)), "a bare EOF is a connection the server dropped")
		require.True(t, IsTransient(&net.OpError{Op: "read", Err: syscall.ECONNRESET}))
		// A caller that stopped the call is not a failing server, even when the
		// dial it stopped is a *net.OpError.
		require.False(t, IsTransient(context.Canceled))
		require.False(t, IsTransient(&net.OpError{Op: "dial", Err: context.Canceled}))
		require.True(t, IsTransient(&net.DNSError{IsTemporary: true}))
		require.False(t, IsTransient(&net.DNSError{IsNotFound: true}))
	})
}

// MoveStorageContext hands its ctx to the login and to the move call: a Web UI
// that never answers ends with the caller's deadline, not the client's
// five-minute timeout. Each cell hangs one of the two requests.
func TestMoveStorageContext_EndsWithTheCallersDeadline(t *testing.T) {
	for _, hang := range []string{"auth.login", "core.move_storage"} {
		t.Run("a hung "+hang, func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Method != hang {
					_, _ = io.WriteString(w, `{"id":1,"result":true,"error":null}`)
					return
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			t.Cleanup(func() { close(release); srv.Close() })
			c, err := New(srv.URL, "arkham")
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- c.MoveStorageContext(ctx, []string{"a"}, "/gotham") }()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.DeadlineExceeded)
			case <-time.After(5 * time.Second):
				t.Fatal("MoveStorageContext outlived the caller's deadline")
			}
		})
	}
}
