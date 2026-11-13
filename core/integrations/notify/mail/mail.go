// file: core/integrations/notify/mail/mail.go
// version: 1.3.2
// guid: 11234e57-50d2-4d9a-be89-62e983dfeda9
// last-edited: 2026-11-13

// Package mail sends email over SMTP, in two shapes: the Sender primitive for
// transactional mail that is not a notification (user invitations), and the
// core.email channel of the notify kind (email.go, 05-notify section 3.1),
// which wraps SMTPSender.
//
// The sender is deliberately small and dependency-free: net/smtp for the
// protocol, the standard mime packages for a multipart/alternative text + HTML
// body. mail.go imports nothing from this module, logs nothing, and never puts
// the SMTP password into an error, so callers can log its errors as they are.
// Only email.go knows about integrations and notify.
//
// Transport rules (owner decision 2026-10-19, prod relays through a local
// postfix on 127.0.0.1:25 with no AUTH):
//   - A loopback host (127.0.0.0/8, ::1, "localhost") may be spoken to in plain
//     text: that connection never leaves the machine.
//   - Any other host REQUIRES TLS: implicit TLS on port 465, or a successful
//     STARTTLS. If such a server does not offer STARTTLS the send is refused
//     before MAIL FROM, and it never falls back to plain text. Certificates are
//     always verified.
//   - STARTTLS is used whenever the server offers it, EXCEPT to a loopback host
//     with no username configured: a local postfix usually offers STARTTLS with
//     a self-signed certificate that would fail verification and stop all mail.
//   - AUTH PLAIN is sent only over TLS or to a loopback host (smtp.PlainAuth
//     enforces this), and only when a username is configured.
//   - Every connection has one deadline (Timeout, default 15s) and is closed
//     the moment the caller's context ends.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds a whole send: dial, TLS, AUTH and DATA.
const DefaultTimeout = 15 * time.Second

// Message is one email to one recipient.
type Message struct {
	To      string // a single address, "a@example.com" or "Name <a@example.com>"
	Subject string
	Text    string // text/plain part
	HTML    string // text/html part; optional
	// AlsoTo are further recipients of the same message, each a single
	// address: they are listed in the To header and receive it in the same SMTP
	// transaction as To, so a send either reaches all of them or none.
	AlsoTo []string
}

// Sender delivers a Message. Implementations must be safe for concurrent use.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// ErrInvalidHeader is returned for a header value carrying CR or LF (header
// injection) or an address that does not parse.
var ErrInvalidHeader = errors.New("mail: invalid header value")

// ErrTLSRequired is returned, before AUTH, MAIL FROM or any message data, when
// the server is not a loopback host and offers no STARTTLS on a port that is
// not implicit TLS. Nothing was sent, and sending again changes nothing until
// the host, the port or the server does.
var ErrTLSRequired = errors.New("mail: refusing to send without TLS")

// ErrAuthUnavailable is returned when a username is configured and the server
// does not offer AUTH.
var ErrAuthUnavailable = errors.New("mail: the server does not offer AUTH")

// sentinelError is an error whose text is msg and which is errors.Is(is). It
// lets a refusal keep the exact admin-visible wording the invite flow always
// showed while a caller can still match the sentinel.
type sentinelError struct {
	msg string
	is  error
}

func (e *sentinelError) Error() string { return e.msg }
func (e *sentinelError) Unwrap() error { return e.is }

// SMTPConfig configures SMTPSender.
type SMTPConfig struct {
	Host     string
	Port     int // 0 means 587
	Username string
	Password string
	From     string // "Name <addr@example.com>" or a bare address
	// Timeout bounds one whole send; 0 means DefaultTimeout.
	Timeout time.Duration
	// TLSConfig overrides the TLS client config (tests inject a root pool).
	// ServerName defaults to Host.
	TLSConfig *tls.Config
	// HelloName is sent in EHLO; empty means "localhost".
	HelloName string
}

// SMTPSender sends mail through one SMTP server.
type SMTPSender struct {
	cfg  SMTPConfig
	from *mail.Address
	// dialAddr is host:port; implicitTLS is Port == 465. Both are fields so
	// tests can point a sender at a fake server without changing its Host,
	// which is also the TLS server name and PlainAuth's loopback check.
	dialAddr    string
	implicitTLS bool
	// treatAsRemote forces the require-TLS rule for a Host that is loopback.
	// Unexported test seam: tests can only listen on 127.0.0.1.
	treatAsRemote bool
}

// NewSMTPSender validates cfg and returns a sender.
func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	cfg.Host = strings.TrimSpace(cfg.Host)
	if cfg.Host == "" {
		return nil, errors.New("mail: SMTP host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("mail: invalid SMTP port %d", cfg.Port)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	from, err := parseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("mail: SMTP from address: %w", err)
	}
	return &SMTPSender{
		cfg: cfg, from: from,
		dialAddr:    net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		implicitTLS: cfg.Port == 465,
	}, nil
}

// parseAddress parses exactly one address, refusing CR/LF anywhere.
func parseAddress(s string) (*mail.Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, ErrInvalidHeader
	}
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidHeader, err)
	}
	if strings.ContainsAny(a.Address, "\r\n") || strings.ContainsAny(a.Name, "\r\n") {
		return nil, ErrInvalidHeader
	}
	return a, nil
}

// isLoopbackHost reports whether host names this machine. Only literal
// loopback addresses and "localhost" count; no DNS lookup is made.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Send delivers msg. It honours ctx: cancelling it closes the connection.
func (s *SMTPSender) Send(ctx context.Context, msg Message) (err error) {
	to, err := parseAddress(msg.To)
	if err != nil {
		return fmt.Errorf("mail: recipient: %w", err)
	}
	rcpts := []*mail.Address{to}
	for _, extra := range msg.AlsoTo {
		a, err := parseAddress(extra)
		if err != nil {
			return fmt.Errorf("mail: recipient: %w", err)
		}
		rcpts = append(rcpts, a)
	}
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return fmt.Errorf("mail: subject: %w", ErrInvalidHeader)
	}
	body, err := buildMessage(s.from, rcpts, msg, time.Now())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	addr := s.dialAddr
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mail: connect %s: %w", addr, err)
	}
	// One deadline for the whole exchange, and close on cancellation so a
	// stalled server cannot hold the caller past its context.
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			_ = conn.Close()
			return fmt.Errorf("mail: set the deadline on the connection to %s: %w", addr, err)
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	tlsCfg := s.tlsConfig()
	implicitTLS := s.implicitTLS
	if implicitTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("mail: TLS handshake with %s: %w", addr, err)
		}
		conn = tc
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return s.ctxErr(ctx, fmt.Errorf("mail: SMTP greeting from %s: %w", addr, err))
	}
	// Close the session on every way out except a QUIT the server took (QUIT
	// closes it). A failed Close of an abandoned session joins the failure that
	// is already being returned; it never turns an accepted message into an
	// error, and a connection the context already closed is not worth saying.
	closeSession := true
	defer func() {
		if !closeSession {
			return
		}
		if cerr := c.Close(); cerr != nil && err != nil && !errors.Is(cerr, net.ErrClosed) {
			err = errors.Join(err, cerr)
		}
	}()

	hello := s.cfg.HelloName
	if hello == "" {
		hello = "localhost"
	}
	if err := c.Hello(hello); err != nil {
		return s.ctxErr(ctx, fmt.Errorf("mail: EHLO: %w", err))
	}

	if !implicitTLS {
		offered, _ := c.Extension("STARTTLS")
		switch {
		case offered && s.wantStartTLS():
			if err := c.StartTLS(tlsCfg); err != nil {
				return s.ctxErr(ctx, fmt.Errorf("mail: STARTTLS: %w", err))
			}
		case s.requiresTLS():
			// Refuse before AUTH, MAIL FROM or any message data; never
			// fall back to plain text for a host off this machine.
			return &sentinelError{
				msg: fmt.Sprintf("mail: %s does not offer STARTTLS and is not a loopback host: refusing to send without TLS (use port 465 or a relay that supports STARTTLS)", s.cfg.Host),
				is:  ErrTLSRequired,
			}
		}
	}

	if s.cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return &sentinelError{msg: "mail: SMTP_USERNAME is set but the server does not offer AUTH", is: ErrAuthUnavailable}
		}
		// PlainAuth refuses to send credentials over an unencrypted
		// connection to anything but localhost.
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return s.ctxErr(ctx, fmt.Errorf("mail: AUTH: %w", redact(err, s.cfg.Password)))
		}
	}

	if err := c.Mail(s.from.Address); err != nil {
		return s.ctxErr(ctx, fmt.Errorf("mail: MAIL FROM: %w", err))
	}
	for _, rcpt := range rcpts {
		if err := c.Rcpt(rcpt.Address); err != nil {
			return s.ctxErr(ctx, fmt.Errorf("mail: RCPT TO: %w", err))
		}
	}
	w, err := c.Data()
	if err != nil {
		return s.ctxErr(ctx, fmt.Errorf("mail: DATA: %w", err))
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return s.ctxErr(ctx, fmt.Errorf("mail: write body: %w", err))
	}
	if err := w.Close(); err != nil {
		return s.ctxErr(ctx, fmt.Errorf("mail: end of data: %w", err))
	}
	// The message is accepted once DATA closes, so a failed QUIT is not a
	// failure: the session is dropped by the deferred close instead.
	closeSession = c.Quit() != nil
	return nil
}

// requiresTLS: every non-loopback host must be reached over TLS.
func (s *SMTPSender) requiresTLS() bool {
	return s.treatAsRemote || !isLoopbackHost(s.cfg.Host)
}

// wantStartTLS: STARTTLS when offered, except to loopback with no username.
func (s *SMTPSender) wantStartTLS() bool {
	return s.cfg.Username != "" || s.requiresTLS()
}

func (s *SMTPSender) tlsConfig() *tls.Config {
	var c *tls.Config
	if s.cfg.TLSConfig != nil {
		c = s.cfg.TLSConfig.Clone()
	} else {
		c = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if c.ServerName == "" {
		c.ServerName = s.cfg.Host
	}
	return c
}

// ctxErr reports the context's error when it is what ended the exchange, so a
// timeout reads as one instead of as "use of closed network connection".
func (s *SMTPSender) ctxErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("%w (%v)", err, cerr)
	}
	return err
}

// redact removes secret from err's text. net/smtp does not echo credentials,
// but this keeps that true whatever a server replies.
func redact(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[redacted]"))
}

// BuildMessage renders msg as an RFC 5322 message with a multipart/alternative
// body (text, then HTML when present), quoted-printable encoded. Exported for
// tests and for previewing; Send calls it.
func BuildMessage(from, to *mail.Address, msg Message, now time.Time) ([]byte, error) {
	return buildMessage(from, []*mail.Address{to}, msg, now)
}

// buildMessage is BuildMessage for one or more recipients, all in the To
// header.
func buildMessage(from *mail.Address, to []*mail.Address, msg Message, now time.Time) ([]byte, error) {
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, fmt.Errorf("mail: subject: %w", ErrInvalidHeader)
	}
	boundary, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	idPart, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	domain := "localhost"
	if at := strings.LastIndexByte(from.Address, '@'); at >= 0 && at < len(from.Address)-1 {
		domain = from.Address[at+1:]
	}

	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+idPart+"@"+domain+">")
	header("From", from.String())
	list := make([]string, len(to))
	for i, a := range to {
		list[i] = a.String()
	}
	header("To", strings.Join(list, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	header("MIME-Version", "1.0")
	header("Auto-Submitted", "auto-generated")
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")

	writePart := func(contentType, content string) error {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + contentType + "; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		qp := quotedprintable.NewWriter(&b)
		if _, err := qp.Write([]byte(normalizeNewlines(content))); err != nil {
			return err
		}
		if err := qp.Close(); err != nil {
			return err
		}
		b.WriteString("\r\n")
		return nil
	}
	if err := writePart("text/plain", msg.Text); err != nil {
		return nil, err
	}
	if msg.HTML != "" {
		if err := writePart("text/html", msg.HTML); err != nil {
			return nil, err
		}
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

// normalizeNewlines turns every line ending into CRLF, as SMTP requires.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
