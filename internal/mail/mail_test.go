// file: internal/mail/mail_test.go
// version: 1.1.0
// guid: 4390d39b-6ec5-4a8e-a468-83a1beaa191b
// last-edited: 2026-10-19

package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal in-process SMTP server: enough of RFC 5321 to drive
// net/smtp through EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA and QUIT.
type fakeSMTP struct {
	t            *testing.T
	ln           net.Listener
	offerTLS     bool // advertise STARTTLS
	offerAuth    bool // advertise AUTH PLAIN
	implicitTLS  bool // the listener itself is TLS (port-465 style)
	stall        bool // accept, then never say anything
	tlsCfg       *tls.Config
	mu           sync.Mutex
	startTLSUsed bool
	authPlain    string // decoded AUTH PLAIN payload
	mailFrom     string
	rcptTo       string
	data         string
	done         chan struct{}
}

func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func startFake(t *testing.T, f *fakeSMTP) (port int, pool *x509.CertPool) {
	t.Helper()
	cert, pool := testCert(t)
	f.t = t
	f.tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}}
	f.done = make(chan struct{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if f.implicitTLS {
		ln = tls.NewListener(ln, f.tlsCfg)
	}
	f.ln = ln
	t.Cleanup(func() { _ = ln.Close() })
	go f.serve()
	return ln.Addr().(*net.TCPAddr).Port, pool
}

func (f *fakeSMTP) serve() {
	defer close(f.done)
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	if f.stall {
		_, _ = io.Copy(io.Discard, conn) // until the client closes
		return
	}
	r := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	tlsActive := f.implicitTLS
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO", "HELO":
			w("250-fake")
			if f.offerTLS && !tlsActive {
				w("250-STARTTLS")
			}
			if f.offerAuth {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case "STARTTLS":
			w("220 go ahead")
			tc := tls.Server(conn, f.tlsCfg)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn = tc
			r = bufio.NewReader(conn)
			tlsActive = true
			f.mu.Lock()
			f.startTLSUsed = true
			f.mu.Unlock()
		case "AUTH":
			parts := strings.Fields(line)
			if len(parts) == 3 {
				dec, _ := base64.StdEncoding.DecodeString(parts[2])
				f.mu.Lock()
				f.authPlain = string(dec)
				f.mu.Unlock()
			}
			w("235 ok")
		case "MAIL":
			f.mu.Lock()
			f.mailFrom = line
			f.mu.Unlock()
			w("250 ok")
		case "RCPT":
			f.mu.Lock()
			f.rcptTo = line
			f.mu.Unlock()
			w("250 ok")
		case "DATA":
			w("354 send")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			w("250 queued")
		case "QUIT":
			w("221 bye")
			return
		default:
			w("502 unknown")
		}
	}
}

func (f *fakeSMTP) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("fake SMTP server did not finish")
	}
}

var testMsg = Message{
	To:      "Guest Person <guest@example.com>",
	Subject: "You're invited — Audiobooks",
	Text:    "Hello\nOpen https://books.example.com/invite/abc\n",
	HTML:    "<p>Hello &amp; welcome</p>",
}

// The production case: postfix on loopback, no AUTH, STARTTLS advertised with a
// certificate the client could not verify. Mail must go out in plain text.
func TestSend_LoopbackNoAuthSkipsStartTLS(t *testing.T) {
	f := &fakeSMTP{offerTLS: true}
	port, _ := startFake(t, f)
	s, err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: port, From: "Books <books@example.com>"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), testMsg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	f.wait(t)
	if f.startTLSUsed {
		t.Fatal("STARTTLS must not be used to loopback without a username")
	}
	if f.authPlain != "" {
		t.Fatal("AUTH must not be sent without a username")
	}
	if f.mailFrom != "MAIL FROM:<books@example.com> BODY=8BITMIME" && f.mailFrom != "MAIL FROM:<books@example.com>" {
		t.Fatalf("MAIL FROM = %q", f.mailFrom)
	}
	if f.rcptTo != "RCPT TO:<guest@example.com>" {
		t.Fatalf("RCPT TO = %q", f.rcptTo)
	}
	checkMIME(t, f.data)
}

func TestSend_StartTLSThenAuthWhenUsernameSet(t *testing.T) {
	f := &fakeSMTP{offerTLS: true, offerAuth: true}
	port, pool := startFake(t, f)
	s, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: port, From: "books@example.com",
		Username: "mailer", Password: "pw-SENTINEL",
		TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), testMsg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	f.wait(t)
	if !f.startTLSUsed {
		t.Fatal("STARTTLS must be used when offered and a username is set")
	}
	if f.authPlain != "\x00mailer\x00pw-SENTINEL" {
		t.Fatalf("AUTH PLAIN payload = %q", f.authPlain)
	}
}

func TestSend_ImplicitTLSOn465Style(t *testing.T) {
	f := &fakeSMTP{implicitTLS: true}
	port, pool := startFake(t, f)
	s, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: port, From: "books@example.com",
		TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.implicitTLS {
		t.Fatal("only port 465 selects implicit TLS")
	}
	// The fake listens on a random port; select the mode port 465 would.
	s.implicitTLS = true
	if err := s.Send(context.Background(), testMsg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	f.wait(t)
	checkMIME(t, f.data)
}

func TestSend_RemoteNoStartTLSRefusesAuth(t *testing.T) {
	// Dial the fake on loopback, but name a remote host: no STARTTLS is
	// offered, so PlainAuth must refuse to send the password in clear text.
	f := &fakeSMTP{offerAuth: true}
	port, _ := startFake(t, f)
	s, err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: port, From: "books@example.com", Username: "u", Password: "pw-SENTINEL"})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Host = "mail.invalid" // PlainAuth's server name; no STARTTLS offered
	s.dialAddr = "127.0.0.1:" + strconv.Itoa(port)
	err = s.Send(context.Background(), testMsg)
	if err == nil {
		t.Fatal("AUTH over an unencrypted remote connection must be refused")
	}
	if strings.Contains(err.Error(), "pw-SENTINEL") {
		t.Fatal("error text contains the password")
	}
	if f.authPlain != "" {
		t.Fatal("credentials were sent in clear text")
	}
}

func TestSend_NonLoopbackWithoutStartTLSRefused(t *testing.T) {
	f := &fakeSMTP{}
	port, _ := startFake(t, f)
	s, err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: port, From: "books@example.com", Username: "u", Password: "pw-SENTINEL"})
	if err != nil {
		t.Fatal(err)
	}
	s.treatAsRemote = true
	err = s.Send(context.Background(), testMsg)
	if err == nil {
		t.Fatal("a non-loopback host without STARTTLS must be refused")
	}
	if !strings.Contains(err.Error(), "STARTTLS") || strings.Contains(err.Error(), "pw-SENTINEL") {
		t.Fatalf("error = %q", err)
	}
	f.wait(t)
	if f.mailFrom != "" || f.rcptTo != "" || f.data != "" || f.authPlain != "" {
		t.Fatalf("server saw commands: from=%q rcpt=%q data=%q auth=%q", f.mailFrom, f.rcptTo, f.data, f.authPlain)
	}
}

func TestSend_NonLoopbackNoUsernameStillRequiresTLS(t *testing.T) {
	f := &fakeSMTP{}
	port, _ := startFake(t, f)
	s, err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: port, From: "books@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	s.treatAsRemote = true
	if err := s.Send(context.Background(), testMsg); err == nil {
		t.Fatal("an unauthenticated relay off this machine must still use TLS")
	}
	f.wait(t)
	if f.mailFrom != "" || f.data != "" {
		t.Fatal("message sent without TLS")
	}
}

func TestSend_NonLoopbackWithStartTLSSucceeds(t *testing.T) {
	f := &fakeSMTP{offerTLS: true}
	port, pool := startFake(t, f)
	s, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: port, From: "books@example.com",
		TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.treatAsRemote = true
	if err := s.Send(context.Background(), testMsg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	f.wait(t)
	if !f.startTLSUsed {
		t.Fatal("STARTTLS must be used for a non-loopback host")
	}
	checkMIME(t, f.data)
}

func TestSend_NonLoopbackStartTLSBadCertRefused(t *testing.T) {
	f := &fakeSMTP{offerTLS: true}
	port, _ := startFake(t, f) // pool deliberately not trusted
	s, err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: port, From: "books@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	s.treatAsRemote = true
	if err := s.Send(context.Background(), testMsg); err == nil {
		t.Fatal("an unverifiable certificate must fail the send")
	}
	f.wait(t)
	if f.data != "" {
		t.Fatal("message sent over an unverified connection")
	}
}

func TestSend_RejectsHeaderInjection(t *testing.T) {
	s, err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "books@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]Message{
		"subject": {To: "a@example.com", Subject: "hi\r\nBcc: x@example.com", Text: "x"},
		"to-lf":   {To: "a@example.com\nBcc: x@example.com", Subject: "s", Text: "x"},
		"to-bad":  {To: "not an address", Subject: "s", Text: "x"},
	} {
		if err := s.Send(context.Background(), m); !errors.Is(err, ErrInvalidHeader) {
			t.Fatalf("%s: err = %v, want ErrInvalidHeader", name, err)
		}
	}
	if _, err := NewSMTPSender(SMTPConfig{Host: "h", From: "a@example.com\r\nBcc: x@example.com"}); err == nil {
		t.Fatal("a From with CRLF must be refused")
	}
}

func TestSend_ContextCancelUnblocksStalledServer(t *testing.T) {
	f := &fakeSMTP{stall: true}
	port, _ := startFake(t, f)
	s, err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: port, From: "books@example.com", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Send(ctx, testMsg); err == nil {
		t.Fatal("Send against a silent server must fail")
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("Send ignored the context: took %v", el)
	}
}

func TestNewSMTPSender_Validation(t *testing.T) {
	if _, err := NewSMTPSender(SMTPConfig{From: "a@example.com"}); err == nil {
		t.Fatal("empty host must be refused")
	}
	if _, err := NewSMTPSender(SMTPConfig{Host: "h", Port: 70000, From: "a@example.com"}); err == nil {
		t.Fatal("bad port must be refused")
	}
	s, err := NewSMTPSender(SMTPConfig{Host: "h", From: "a@example.com"})
	if err != nil || s.cfg.Port != 587 || s.cfg.Timeout != DefaultTimeout {
		t.Fatalf("defaults: %+v %v", s, err)
	}
}

// checkMIME parses a delivered message and checks headers and both parts.
func checkMIME(t *testing.T, raw string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	for _, h := range []string{"Date", "Message-Id", "From", "To", "Subject", "Mime-Version"} {
		if m.Header.Get(h) == "" {
			t.Fatalf("missing header %s", h)
		}
	}
	if _, err := m.Header.Date(); err != nil {
		t.Fatalf("Date header does not parse: %v", err)
	}
	dec := new(mime.WordDecoder)
	subj, err := dec.DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subj != testMsg.Subject {
		t.Fatalf("Subject = %q (%v)", subj, err)
	}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("Content-Type = %q (%v)", mt, err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var got []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p) // multipart decodes quoted-printable
		got = append(got, p.Header.Get("Content-Type")+"|"+string(b))
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "text/plain") || !strings.HasPrefix(got[1], "text/html") {
		t.Fatalf("parts = %q", got)
	}
	if !strings.Contains(got[0], "https://books.example.com/invite/abc") || !strings.Contains(got[1], "Hello &amp; welcome") {
		t.Fatalf("part bodies = %q", got)
	}
}
