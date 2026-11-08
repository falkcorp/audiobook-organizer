// file: internal/server/server_listen_ready_test.go
// version: 1.2.0
// guid: b665aef1-a25e-4d51-a7fe-4069066fd9e4
// last-edited: 2026-11-08

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
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// startListenersForTest binds cfg's listener stack (listenR1) with the
// engine as the root handler and serves it, without the post-listen start:
// the listener half of Start, which HEAD's configureAndStartHTTP was.
func (s *Server) startListenersForTest(cfg ServerConfig) error {
	ln, err := s.listenR1(cfg, listenOptionsFor(cfg), s.router)
	if err != nil {
		return err
	}
	go func() { _ = ln.serveListeners() }()
	return nil
}

// listenTestServer is a Server with only what listenR1 touches.
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

// TestListenR1BindsBeforeReturning: when the function
// returns nil the port already accepts connections, which is what makes it
// safe for Start to send READY=1 right after it.
func TestListenR1BindsBeforeReturning(t *testing.T) {
	s := listenTestServer()
	require.NoError(t, s.startListenersForTest(listenTestConfig("0")))
	t.Cleanup(func() { _ = s.httpServer.Close() })

	addr := s.httpBoundAddr.Load()
	require.NotNil(t, addr, "the bound address must be recorded before returning")
	resp, err := http.Get("http://" + *addr + "/ping")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "pong", string(body))
}

// TestListenR1FailsOnPortInUse: a bind failure is returned
// to the caller (so the Fatal "http" step fails Start) instead of exiting the
// process from a goroutine.
func TestListenR1FailsOnPortInUse(t *testing.T) {
	s := listenTestServer()
	err := s.startListenersForTest(listenTestConfig(occupiedPort(t)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "bind HTTP listener")
	require.Nil(t, s.httpBoundAddr.Load())
}

// TestListenR1TLSServesLoadedKeyPair covers the TLS
// branch: the key pair is loaded before the bind and served through
// ServeTLS(ln, "", ""), and the port-80 redirect stays non-fatal.
func TestListenR1TLSServesLoadedKeyPair(t *testing.T) {
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
	require.NoError(t, s.startListenersForTest(cfg))
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

// TestListenR1TLSBadKeyPairFails: an unreadable key pair is
// a startup error, returned before anything is bound.
func TestListenR1TLSBadKeyPairFails(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, []byte("not a cert"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("not a key"), 0o600))
	cfg := listenTestConfig("0")
	cfg.TLSCertFile, cfg.TLSKeyFile = certPath, keyPath

	s := listenTestServer()
	err := s.startListenersForTest(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "load TLS key pair")
	require.Nil(t, s.httpBoundAddr.Load())
}

// TestStartListenFailureFailsStart drives the whole Start: with the port
// taken, Start returns the bind error. (READY=1 and STOPPING=1 are the
// kernel's since R1-16; core/kernel's TestSupervisor_NotifiesReadyAndStopping
// and TestSupervisor_NoReadyWhenListenerFails cover them.)
func TestStartListenFailureFailsStart(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	// Start got as far as the search-index worker before the Fatal "http"
	// step failed, and only Start's own shutdown path closes its queue. In
	// production the process exits here; the test closes the queue itself
	// (runs before cleanup) so teardown's bounded bgWG wait is not spent on it.
	defer server.closeIndexQueue()

	done := make(chan error, 1)
	go func() { done <- server.Start(listenTestConfig(occupiedPort(t))) }()
	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, err.Error(), "bind HTTP listener")
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not fail on an occupied port")
	}
}
