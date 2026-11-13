// file: core/integrations/notify/mail/email_test.go
// version: 1.0.0
// guid: 5d17e2c9-83a4-4b60-9e1f-c7a02b6d4f38
// last-edited: 2026-11-13

package mail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/secrets"
)

var batphone = integrations.Ref{Kind: integrations.KindNotify, Instance: "commissioner"}

// countingGate counts the admissions and what each attempt ended with.
type countingGate struct {
	mu       sync.Mutex
	acquired int
	done     []error
	refuse   error
}

func (g *countingGate) Acquire(context.Context, string) (integrations.Ticket, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refuse != nil {
		return nil, g.refuse
	}
	g.acquired++
	return ticket{g}, nil
}
func (*countingGate) State() integrations.GateState { return integrations.GateState{} }

type ticket struct{ g *countingGate }

func (t ticket) Done(err error) {
	t.g.mu.Lock()
	defer t.g.mu.Unlock()
	t.g.done = append(t.g.done, err)
}

// pwReader serves one password.
type pwReader struct{ pw string }

func (r pwReader) Secret(_ context.Context, field string) (secrets.Value, error) {
	if field != "password" {
		return secrets.Value{}, errors.New("no such field")
	}
	return secrets.NewValue(r.pw), nil
}

var signalNote = notify.Notification{
	ID: "01JBATSIGNAL000000000000000", Topic: "gotham.signal.raised.v1", Severity: notify.SeverityWarning,
	Title: "The signal is lit", Body: "Commissioner Gordon asks for the Batman.",
	Links:  []notify.Link{{Label: "Roof", URL: "https://gotham.example/roof"}},
	Entity: &entity.Ref{Module: "gotham", Kind: "signal", ID: "7"},
	Fields: map[string]string{"villain": "Joker", "block": "12"},
	At:     time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC), App: "batcave",
}

// emailInstance builds an email instance from the Descriptor's own build,
// with every sender pointed at the fake server and held to the production
// TLS rules (treatAsRemote: the fake listens on loopback, which the rules
// would otherwise let speak plain text).
func emailInstance(t *testing.T, port int, pool tlsPool, settings map[string]any, gate integrations.Gate) notify.Notifier {
	t.Helper()
	mk := func(cfg SMTPConfig) (Sender, error) {
		cfg.Host, cfg.Port = "127.0.0.1", port
		if pool != nil {
			cfg.TLSConfig = &tls.Config{RootCAs: pool.pool(), MinVersion: tls.VersionTLS12}
		}
		s, err := NewSMTPSender(cfg)
		if err != nil {
			return nil, err
		}
		s.treatAsRemote = true
		return s, nil
	}
	n, err := buildWith(mk)(context.Background(), integrations.Env{
		Ref: batphone, Gate: gate, Secrets: pwReader{pw: "pw-SENTINEL"}, Now: func() time.Time { return signalNote.At },
	}, integrations.InstanceConfig{Ref: batphone, Settings: settings})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return n
}

// tlsPool is the trusted roots of a fake server.
type tlsPool interface{ pool() *x509.CertPool }

type poolOf struct{ p *x509.CertPool }

func (p poolOf) pool() *x509.CertPool { return p.p }

func settings(extra map[string]any) map[string]any {
	m := map[string]any{"host": "mail.example.com", "port": float64(587), "from": "Gotham Alerts <alerts@example.com>",
		"to": []any{"Jim Gordon <jim@example.com>", "alfred@example.com"}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestEmail_DescriptorReflects(t *testing.T) {
	d, err := Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	m := d.Meta()
	if m.ID != AdapterID || m.Kind != integrations.KindNotify || m.Limits.RPS != 0.5 {
		t.Errorf("Meta = %+v", m)
	}
}

// The channel is the moved sender behind a gate and a layout: the transport
// rules of mail.go (TLS for any host off this machine, certificates verified,
// no fallback to plain text) apply to it unchanged, and one message reaches
// every recipient in one transaction.
func TestEmail_ReusesMailTLSRules(t *testing.T) {
	ctx := context.Background()
	t.Run("a relay without STARTTLS is refused before any command", func(t *testing.T) {
		f := &fakeSMTP{}
		port, _ := startFake(t, f)
		g := &countingGate{}
		n := emailInstance(t, port, nil, settings(map[string]any{"username": "alfred", "password": secrets.RefTo("integrations.notify.commissioner.password")}), g)
		err := n.Send(ctx, signalNote, notify.Target{Instance: batphone})
		e, ok := errors.AsType[*integrations.Error](err)
		if !ok || e.Class != integrations.ClassRefusedLocal {
			t.Fatalf("err = %#v, want an *integrations.Error of class refused_local", err)
		}
		f.wait(t)
		if f.mailFrom != "" || f.data != "" || f.authPlain != "" {
			t.Fatalf("the server saw MAIL/DATA/AUTH: from=%q data=%q auth=%q", f.mailFrom, f.data, f.authPlain)
		}
		if strings.Contains(err.Error(), "pw-SENTINEL") || strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("error text leaks a secret or the host: %q", err)
		}
		if len(g.done) != 1 || g.done[0] == nil {
			t.Errorf("the gate ticket was not closed with the failure: %v", g.done)
		}
	})
	t.Run("an unverifiable certificate fails the send", func(t *testing.T) {
		f := &fakeSMTP{offerTLS: true}
		port, _ := startFake(t, f) // pool deliberately not trusted
		n := emailInstance(t, port, nil, settings(nil), &countingGate{})
		if err := n.Send(ctx, signalNote, notify.Target{}); err == nil {
			t.Fatal("a send over an unverifiable certificate succeeded")
		}
		f.wait(t)
		if f.data != "" {
			t.Fatal("the message went out over an unverified connection")
		}
	})
	t.Run("STARTTLS with a trusted certificate delivers one message to every recipient", func(t *testing.T) {
		f := &fakeSMTP{offerTLS: true}
		port, pool := startFake(t, f)
		g := &countingGate{}
		n := emailInstance(t, port, poolOf{pool}, settings(nil), g)
		if err := n.Send(ctx, signalNote, notify.Target{}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		f.wait(t)
		if !f.startTLSUsed {
			t.Error("STARTTLS was not used")
		}
		if len(f.rcptAll) != 2 {
			t.Errorf("RCPT TO lines = %q, want one per recipient", f.rcptAll)
		}
		if !strings.Contains(f.data, "To: \"Jim Gordon\" <jim@example.com>, <alfred@example.com>") &&
			!strings.Contains(f.data, "jim@example.com") {
			t.Errorf("the To header does not list the recipients: %q", f.data)
		}
		if g.acquired != 1 || len(g.done) != 1 || g.done[0] != nil {
			t.Errorf("gate: acquired %d, done %v; want one admission closed with nil", g.acquired, g.done)
		}
	})
}

func TestEmail_GateRefusalIsReturnedBeforeAnyConnection(t *testing.T) {
	f := &fakeSMTP{offerTLS: true}
	port, _ := startFake(t, f)
	refusal := &integrations.Error{Ref: batphone, Op: "acquire", Class: integrations.ClassRefusedLocal, Err: integrations.ErrDisabled}
	n := emailInstance(t, port, nil, settings(nil), &countingGate{refuse: refusal})
	err := n.Send(context.Background(), signalNote, notify.Target{})
	if !errors.Is(err, integrations.ErrDisabled) {
		t.Fatalf("err = %v, want the gate's ErrDisabled", err)
	}
	// Nothing dialled: closing the listener must not leave a connection to see.
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mailFrom != "" || f.startTLSUsed {
		t.Error("the sender dialled although the gate refused")
	}
}

func TestEmail_FixedLayout(t *testing.T) {
	got := Layout(signalNote)
	want := "The signal is lit\n" +
		"\nCommissioner Gordon asks for the Batman.\n" +
		"\nLinks:\n  Roof: https://gotham.example/roof\n" +
		"\nDetails:\n  block: 12\n  villain: Joker\n" +
		"\n-- \nNotification 01JBATSIGNAL000000000000000 (gotham.signal.raised.v1) at 2039-04-01T22:00:00Z\n"
	if got != want {
		t.Errorf("Layout =\n%q\nwant\n%q", got, want)
	}
	bare := Layout(notify.Notification{ID: "x", Topic: "t.a.b.v1", Title: "Only a title", At: signalNote.At})
	if strings.Contains(bare, "Links:") || strings.Contains(bare, "Details:") {
		t.Errorf("empty sections are rendered: %q", bare)
	}
	if got := subject(notify.Notification{Severity: notify.SeverityError, Title: "Two\r\nlines\there"}); got != "[error] Two lines here" {
		t.Errorf("subject = %q", got)
	}
}

func TestEmail_ClassifiesSMTPReplies(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		err    error
		class  integrations.Class
		status int
	}{
		{"greylisting", &textproto.Error{Code: 451, Msg: "try later"}, integrations.ClassTransient, 451},
		{"busy", &textproto.Error{Code: 421, Msg: "busy"}, integrations.ClassTransient, 421},
		{"mailbox gone", &textproto.Error{Code: 550, Msg: "no such user"}, integrations.ClassInvalid, 550},
		{"bad credentials", &textproto.Error{Code: 535, Msg: "no"}, integrations.ClassAuth, 535},
		{"wrapped by the sender", errors.Join(errors.New("mail: RCPT TO"), &textproto.Error{Code: 554, Msg: "x"}), integrations.ClassInvalid, 554},
		{"header injection", ErrInvalidHeader, integrations.ClassInvalid, 0},
		{"no TLS", ErrTLSRequired, integrations.ClassRefusedLocal, 0},
		{"no AUTH", ErrAuthUnavailable, integrations.ClassAuth, 0},
		{"refused connection", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, integrations.ClassTransient, 0},
	} {
		class, status := classify(ctx, c.err)
		if class != c.class || status != c.status {
			t.Errorf("%s: classify = %v/%d, want %v/%d", c.name, class, status, c.class, c.status)
		}
	}
}

func TestEmail_SettingsAreChecked(t *testing.T) {
	build := buildWith(func(cfg SMTPConfig) (Sender, error) { return NewSMTPSender(cfg) })
	for name, s := range map[string]map[string]any{
		"no recipients":     settings(map[string]any{"to": []any{}}),
		"a header in to":    settings(map[string]any{"to": []any{"a@example.com\r\nBcc: b@example.com"}}),
		"a list in an item": settings(map[string]any{"to": []any{"a@example.com, b@example.com"}}),
		"no host":           settings(map[string]any{"host": ""}),
		"no from":           settings(map[string]any{"from": ""}),
		"too many":          settings(map[string]any{"to": manyRecipients(maxRecipients + 1)}),
	} {
		_, err := build(context.Background(), integrations.Env{Ref: batphone}, integrations.InstanceConfig{Settings: s})
		if !errors.Is(err, integrations.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func manyRecipients(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = "robin" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + "@example.com"
	}
	return out
}
