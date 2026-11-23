// file: internal/server/listen_h3_shutdown_test.go
// version: 0.1.0
// guid: 8b3e5a17-6c20-4d94-b1f8-2a7d09c4e613
// last-edited: 2026-11-22

//go:build !windows

package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"
)

// freeUDPPort returns a loopback UDP port nothing holds right now.
func freeUDPPort(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(pc.LocalAddr().String())
	require.NoError(t, err)
	require.NoError(t, pc.Close())
	return port
}

// TestR1Listener_Shutdown_AnHTTP3ClientReceivesTheShutdownFrame: the realtime
// hub's close (ListenOptions.OnShutdown) writes closed{shutdown} to every open
// event stream and waits for each to return. An HTTP/3 client holding such a
// stream must receive that frame, which holds only when Shutdown runs the hook
// BEFORE it closes the QUIC server: closed first, quic-go sends
// CONNECTION_CLOSE and the frame never reaches the client (the client's read
// fails instead). Hub A did the same in beforeDrain, ahead of the HTTP/3 close.
func TestR1Listener_Shutdown_AnHTTP3ClientReceivesTheShutdownFrame(t *testing.T) {
	certPath, keyPath, pool := writeSelfSignedCert(t)
	port := freeUDPPort(t)
	cfg := listenTestConfig("0")
	cfg.TLSCertFile, cfg.TLSKeyFile, cfg.HTTP3Port = certPath, keyPath, port

	s := listenTestServer()
	hookRan := make(chan struct{})
	streamDone := make(chan struct{})
	s.router.GET("/stream", func(c *gin.Context) {
		defer close(streamDone)
		_, _ = c.Writer.WriteString("open\n")
		c.Writer.Flush()
		select {
		case <-hookRan:
		case <-time.After(10 * time.Second):
			return
		}
		_, _ = c.Writer.WriteString("closed shutdown\n")
		c.Writer.Flush()
	})
	o := listenOptionsFor(cfg)
	// The hub's Close: tell the streams, wait for them to return.
	o.OnShutdown = append(o.OnShutdown, func() {
		close(hookRan)
		select {
		case <-streamDone:
		case <-time.After(5 * time.Second):
		}
	})

	ln, err := s.listenR1(cfg, o, s.router)
	require.NoError(t, err)
	go func() { _ = ln.serveListeners() }()
	t.Cleanup(func() {
		_ = s.httpServer.Close()
		_ = s.http3Server.Close()
		if s.httpRedirectServer != nil {
			_ = s.httpRedirectServer.Close()
		}
	})

	tr := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}
	t.Cleanup(func() { _ = tr.Close() })
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}
	url := "https://127.0.0.1:" + port

	// Wait until QUIC is serving (it binds inside ListenAndServeTLS).
	require.Eventually(t, func() bool {
		resp, err := client.Get(url + "/ping")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 50*time.Millisecond, "HTTP/3 never served /ping")

	resp, err := client.Get(url + "/stream")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, 3, resp.ProtoMajor, "the stream must ride HTTP/3")
	rd := bufio.NewReader(resp.Body)
	first, err := rd.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "open\n", first)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shut := make(chan error, 1)
	go func() { shut <- ln.Shutdown(ctx) }()

	rest, err := rd.ReadString('\n')
	require.NoError(t, err, "the HTTP/3 stream ended before the shutdown frame reached the client")
	require.Equal(t, "closed shutdown\n", rest)
	require.True(t, strings.HasSuffix(rest, "\n"))
	select {
	case err := <-shut:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown did not return")
	}
}
