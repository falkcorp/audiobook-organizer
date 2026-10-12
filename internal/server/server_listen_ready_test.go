// file: internal/server/server_listen_ready_test.go
// version: 1.1.0
// guid: b665aef1-a25e-4d51-a7fe-4069066fd9e4
// last-edited: 2026-10-12

//go:build !windows

package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// fakeNotifySocket binds a unix datagram socket, points NOTIFY_SOCKET at it
// for the test and returns the listener. It lives under os.MkdirTemp rather
// than t.TempDir: a unix socket path is limited to 104 bytes on darwin, and
// t.TempDir's path (which embeds the test name) passes that.
func fakeNotifySocket(t *testing.T) *net.UnixConn {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv("NOTIFY_SOCKET", path)
	return conn
}

// readNotify returns the next datagram on conn, or "" after the timeout.
func readNotify(t *testing.T, conn *net.UnixConn, timeout time.Duration) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(timeout)))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return ""
		}
		require.NoError(t, err)
	}
	return string(buf[:n])
}

// nextStateNotify returns the next datagram that is not a start-timeout
// extension, or "" after the timeout. Long startup steps (the search index
// open inside container.Start) send EXTEND_TIMEOUT_USEC before READY=1; those
// are progress, not state, so the ordering assertions skip them.
func nextStateNotify(t *testing.T, conn *net.UnixConn, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return ""
		}
		msg := readNotify(t, conn, left)
		if !strings.HasPrefix(msg, "EXTEND_TIMEOUT_USEC=") {
			return msg
		}
	}
}

// listenTestServer is a Server with only what configureAndStartHTTP touches.
func listenTestServer() *Server {
	r := gin.New()
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	return &Server{router: r}
}

func listenTestConfig(port string) ServerConfig {
	return ServerConfig{
		Host:         "127.0.0.1",
		Port:         port,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		IdleTimeout:  time.Second,
	}
}

// occupiedPort binds a loopback port for the test and returns its number.
func occupiedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	return port
}

// writeSelfSignedCert writes a throwaway loopback key pair and returns the
// cert and key paths plus a pool that trusts the cert.
func writeSelfSignedCert(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return certPath, keyPath, pool
}

// TestConfigureAndStartHTTPListenBindsBeforeReturning: when the function
// returns nil the port already accepts connections, which is what makes it
// safe for Start to send READY=1 right after it.
func TestConfigureAndStartHTTPListenBindsBeforeReturning(t *testing.T) {
	s := listenTestServer()
	require.NoError(t, s.configureAndStartHTTP(listenTestConfig("0")))
	t.Cleanup(func() { _ = s.httpServer.Close() })

	addr := s.httpBoundAddr.Load()
	require.NotNil(t, addr, "the bound address must be recorded before returning")
	resp, err := http.Get("http://" + *addr + "/ping")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "pong", string(body))
}

// TestConfigureAndStartHTTPListenFailsOnPortInUse: a bind failure is returned
// to the caller (so the Fatal "http" step fails Start) instead of exiting the
// process from a goroutine.
func TestConfigureAndStartHTTPListenFailsOnPortInUse(t *testing.T) {
	s := listenTestServer()
	err := s.configureAndStartHTTP(listenTestConfig(occupiedPort(t)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "bind HTTP listener")
	require.Nil(t, s.httpBoundAddr.Load())
}

// TestConfigureAndStartHTTPListenTLSServesLoadedKeyPair covers the TLS
// branch: the key pair is loaded before the bind and served through
// ServeTLS(ln, "", ""), and the port-80 redirect stays non-fatal.
func TestConfigureAndStartHTTPListenTLSServesLoadedKeyPair(t *testing.T) {
	// Hold 127.0.0.1:80 if this process may bind it, so the server's redirect
	// listener takes its non-fatal failure path and no socket outlives the
	// test. Where binding :80 is not permitted the server's attempt fails the
	// same way.
	if ln80, err := net.Listen("tcp", "127.0.0.1:80"); err == nil {
		t.Cleanup(func() { _ = ln80.Close() })
	}
	certPath, keyPath, pool := writeSelfSignedCert(t)
	cfg := listenTestConfig("0")
	cfg.TLSCertFile, cfg.TLSKeyFile = certPath, keyPath

	s := listenTestServer()
	require.NoError(t, s.configureAndStartHTTP(cfg))
	t.Cleanup(func() {
		_ = s.httpServer.Close()
		if s.httpRedirectServer != nil {
			_ = s.httpRedirectServer.Close()
		}
	})

	addr := s.httpBoundAddr.Load()
	require.NotNil(t, addr)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}}
	resp, err := client.Get("https://" + *addr + "/ping")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "pong", string(body))
	// ServeTLS(ln, "", "") must keep HTTP/2 negotiation (s.httpServer.Protocols).
	require.Equal(t, 2, resp.ProtoMajor, "TLS listener did not negotiate HTTP/2")
}

// TestConfigureAndStartHTTPListenTLSBadKeyPairFails: an unreadable key pair is
// a startup error, returned before anything is bound.
func TestConfigureAndStartHTTPListenTLSBadKeyPairFails(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, []byte("not a cert"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("not a key"), 0o600))
	cfg := listenTestConfig("0")
	cfg.TLSCertFile, cfg.TLSKeyFile = certPath, keyPath

	s := listenTestServer()
	err := s.configureAndStartHTTP(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "load TLS key pair")
	require.Nil(t, s.httpBoundAddr.Load())
}

// TestStartListenFailureFailsStartWithoutReady drives the whole Start: with the
// port taken, Start returns the bind error and systemd is never told READY.
func TestStartListenFailureFailsStartWithoutReady(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	// Start got as far as the search-index worker before the Fatal "http"
	// step failed, and only Start's own shutdown path closes its queue. In
	// production the process exits here; the test closes the queue itself
	// (runs before cleanup) so teardown's bounded bgWG wait is not spent on it.
	defer server.closeIndexQueue()
	sock := fakeNotifySocket(t)

	done := make(chan error, 1)
	go func() { done <- server.Start(listenTestConfig(occupiedPort(t))) }()
	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, err.Error(), "bind HTTP listener")
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not fail on an occupied port")
	}
	require.Empty(t, nextStateNotify(t, sock, 200*time.Millisecond),
		"no state notification (READY, STATUS) may be sent when startup fails; only start-timeout extensions")
}

// TestStartNotifyReadyAfterBindAndStoppingOnShutdown boots a full server under
// a fake NOTIFY_SOCKET. The first datagram must be READY=1 and, when it
// arrives, the port must already accept connections; SIGTERM must produce
// STOPPING=1. Like TestServerStartGracefulShutdown it signals the whole test
// process, so no test in this package may use t.Parallel().
func TestStartNotifyReadyAfterBindAndStoppingOnShutdown(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	sock := fakeNotifySocket(t)

	done := make(chan error, 1)
	go func() { done <- server.Start(listenTestConfig("0")) }()

	first := nextStateNotify(t, sock, 30*time.Second)
	require.Equal(t, "READY=1", first, "the first state datagram after startup must be READY=1")
	addr := server.httpBoundAddr.Load()
	require.NotNil(t, addr, "READY=1 was sent before the listener was bound")
	conn, err := net.DialTimeout("tcp", *addr, 2*time.Second)
	require.NoError(t, err, "READY=1 was sent but the port does not accept connections")
	_ = conn.Close()

	select {
	case <-server.shutdownArmed:
	case err := <-done:
		t.Fatalf("server Start returned before arming shutdown: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for the server to arm its signal handler")
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)

	var seen []string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		msg := nextStateNotify(t, sock, time.Until(deadline))
		seen = append(seen, msg)
		if msg == "STOPPING=1" {
			break
		}
		require.True(t, strings.HasPrefix(msg, "STATUS="), "unexpected datagram %q (seen %q)", msg, seen)
	}
	require.Contains(t, seen, "STOPPING=1")

	// Budget matches TestServerStartGracefulShutdown's: the shutdown path's
	// own designed waits exceed a few seconds on a contended runner.
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(60 * time.Second):
		t.Fatal("timeout waiting for server shutdown")
	}
}
