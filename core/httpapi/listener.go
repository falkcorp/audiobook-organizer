// file: core/httpapi/listener.go
// version: 1.0.0
// guid: 4cbe821c-8f47-4a8c-8914-74f5fd4a409c
// last-edited: 2026-11-07

package httpapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Listener defaults (05-http-api §10).
const (
	DefaultListen            = ":8484" // core.http.listen
	defaultReadHeaderTimeout = 15 * time.Second
	defaultIdleTimeout       = 120 * time.Second
)

// ListenOptions are the listener keys of 05-http-api §10, resolved by the
// kernel at 06 §6.5.2 start step (7) (in release R1 from
// app.Options.R1Values, 05-config §1.12.1).
type ListenOptions struct {
	Addr                    string        // core.http.listen; "" = DefaultListen
	TLSCertFile, TLSKeyFile string        // both or neither
	ReadHeaderTimeout       time.Duration // core.http.read_header_timeout; 0 = 15s
	IdleTimeout             time.Duration // core.http.idle_timeout; 0 = 120s
}

// Listener is one bound HTTP listener: the listener lifecycle of 05-http-api
// §5 step 4. Listen binds (so an occupied port fails before readiness), Serve
// serves until Shutdown, and Shutdown drains within its ctx.
type Listener struct {
	srv *http.Server
	ln  net.Listener
	tls bool
}

// Listen binds o.Addr and prepares an http.Server for h. A TLS pair that
// does not load fails here, before any request.
func Listen(o ListenOptions, h http.Handler, log *slog.Logger) (*Listener, error) {
	if o.Addr == "" {
		o.Addr = DefaultListen
	}
	if o.ReadHeaderTimeout <= 0 {
		o.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = defaultIdleTimeout
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: o.ReadHeaderTimeout, IdleTimeout: o.IdleTimeout}
	if log != nil {
		srv.ErrorLog = slog.NewLogLogger(log.Handler(), slog.LevelWarn)
	}
	useTLS := o.TLSCertFile != "" || o.TLSKeyFile != ""
	if useTLS {
		if o.TLSCertFile == "" || o.TLSKeyFile == "" {
			return nil, errors.New("httpapi: listen: TLS needs both a certificate and a key file")
		}
		cert, err := tls.LoadX509KeyPair(o.TLSCertFile, o.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("httpapi: listen: load TLS pair: %w", err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		return nil, fmt.Errorf("httpapi: listen %s: %w", o.Addr, err)
	}
	return &Listener{srv: srv, ln: ln, tls: useTLS}, nil
}

// Addr is the bound address (useful with port 0).
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Serve blocks serving requests until Shutdown, then returns nil; any other
// end is returned.
func (l *Listener) Serve() error {
	var err error
	if l.tls {
		err = l.srv.ServeTLS(l.ln, "", "")
	} else {
		err = l.srv.Serve(l.ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops accepting, drains in-flight requests until ctx is done, then
// closes what is left (http.Server.Shutdown, then Close on the deadline).
func (l *Listener) Shutdown(ctx context.Context) error {
	err := l.srv.Shutdown(ctx)
	if err != nil {
		if cerr := l.srv.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}
	return err
}
