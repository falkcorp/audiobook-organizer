// file: internal/server/listen_h3_drain_test.go
// version: 0.1.0
// guid: 5d2c8e71-0b94-4f36-a1c7-93e4b6f20d58
// last-edited: 2026-11-28

//go:build !windows

package server

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"
)

// TestR1Listener_Shutdown_AnHTTP3RequestInFlightIsDrainedNotCut: Shutdown
// drains the QUIC server (GOAWAY, then the requests already in flight) within
// ctx; Close would cut the connection under them. A request that is still being
// answered when the stop begins must therefore finish, and Shutdown must wait
// for it instead of returning at once.
//
// The two are told apart by what happens after the shutdown hooks have run (the
// last step before the HTTP/3 server is stopped): with a drain Shutdown stays
// blocked on the open request, with a hard close it returns within
// milliseconds and the client's read fails. The stream test beside it holds
// the ORDER of hooks and HTTP/3; this holds the MODE.
func TestR1Listener_Shutdown_AnHTTP3RequestInFlightIsDrainedNotCut(t *testing.T) {
	certPath, keyPath, pool := writeSelfSignedCert(t)
	port := freeUDPPort(t)
	cfg := listenTestConfig("0")
	cfg.TLSCertFile, cfg.TLSKeyFile, cfg.HTTP3Port = certPath, keyPath, port

	s := listenTestServer()
	inFlight := make(chan struct{})
	release := make(chan struct{})
	s.router.GET("/slow", func(c *gin.Context) {
		_, _ = c.Writer.WriteString("begun\n")
		c.Writer.Flush()
		close(inFlight)
		select {
		case <-release:
		case <-time.After(20 * time.Second):
		}
		_, _ = c.Writer.WriteString("finished\n")
	})
	o := listenOptionsFor(cfg)
	hookRan := make(chan struct{})
	o.OnShutdown = append(o.OnShutdown, func() { close(hookRan) })

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
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	url := "https://127.0.0.1:" + port

	require.Eventually(t, func() bool {
		resp, err := client.Get(url + "/ping")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 50*time.Millisecond, "HTTP/3 never served /ping")

	resp, err := client.Get(url + "/slow")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, 3, resp.ProtoMajor, "the request must ride HTTP/3")
	<-inFlight

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	shut := make(chan error, 1)
	go func() { shut <- ln.Shutdown(ctx) }()

	select {
	case <-hookRan:
	case <-time.After(10 * time.Second):
		t.Fatal("the shutdown hooks never ran")
	}
	// Past the hooks the HTTP/3 server is being stopped. A drain is still
	// waiting on the open request.
	select {
	case err := <-shut:
		t.Fatalf("Shutdown returned (%v) while an HTTP/3 request was still in flight: the QUIC server was closed, not drained", err)
	case <-time.After(750 * time.Millisecond):
	}

	close(release)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "the in-flight HTTP/3 request was cut instead of drained")
	require.Equal(t, "begun\nfinished\n", string(body))
	select {
	case err := <-shut:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown did not return after the request finished")
	}
}
