// file: core/integrations/notify/mail/email.go
// version: 1.0.0
// guid: a9d25e71-4c08-4b36-8e1f-37f0c6a2d584
// last-edited: 2026-11-13

package mail

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/textproto"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/secrets"
)

// AdapterID is the email channel's adapter ID.
const AdapterID integrations.AdapterID = "core.email"

// maxRecipients bounds Settings.To.
const maxRecipients = 20

// Settings are an instance's settings (05-notify section 3.1). Host and Port
// are the instance's destination (origin smtp://host:port): the Registry
// clears a stored Password when either changes unless the write re-supplies it.
// A recipient list is fixed per instance; a rule that names several instances
// reaches several lists, and each instance retries on its own.
//
// Host and Port are optional so an operator-set value can supply them; an
// instance with no effective host does not build (Registry.Test reports it
// invalid, Health misconfigured), and Port 0 is the SMTP submission port, 587.
type Settings struct {
	Host     string      `json:"host,omitzero"`
	Port     int         `json:"port,omitzero" schema:"minimum=1,maximum=65535"`
	Username string      `json:"username,omitzero"`
	Password secrets.Ref `json:"password,omitzero"`
	From     string      `json:"from"`
	To       []string    `json:"to"`
}

// Descriptor is the adapter for app.Options.Adapters. It returns an error only
// if Settings cannot be reflected, which a test of this package rules out.
func Descriptor() (integrations.Descriptor, error) {
	schema, err := config.SchemaFor[Settings]()
	if err != nil {
		return integrations.Descriptor{}, fmt.Errorf("mail: settings schema: %w", err)
	}
	return notify.Adapter(integrations.Meta{
		ID:          AdapterID,
		DisplayName: "Email",
		Version:     "1.0.0",
		Settings:    schema,
		// SMTP does not pass through providerhttp's dial guard: the gate
		// admits and paces, the host is whatever the instance names.
		Egress: integrations.EgressConfigured,
		Limits: integrations.Limits{RPS: 0.5, Burst: 2, MaxRetries: 0, Timeout: DefaultTimeout},
	}, buildWith(func(cfg SMTPConfig) (Sender, error) { return NewSMTPSender(cfg) })), nil
}

// senderFunc makes the Sender of one send from its configuration; the
// password is read per send, so a rotation takes effect without a rebuild.
type senderFunc func(SMTPConfig) (Sender, error)

// buildWith is the Descriptor's build, with the Sender factory a test replaces.
func buildWith(mk senderFunc) func(context.Context, integrations.Env, integrations.InstanceConfig) (notify.Notifier, error) {
	return func(_ context.Context, env integrations.Env, cfg integrations.InstanceConfig) (notify.Notifier, error) {
		c, err := parseSettings(cfg.Settings)
		if err != nil {
			return nil, err
		}
		// Validate the whole configuration now, with no password: the sender
		// is rebuilt per send, and a bad host or From must fail the build, not
		// the first delivery.
		if _, err := mk(c.smtp); err != nil {
			return nil, fmt.Errorf("%w: %v", integrations.ErrInvalid, err)
		}
		return &channel{env: env, cfg: c, mk: mk}, nil
	}
}

// config is an instance's validated settings.
type instanceConfig struct {
	smtp     SMTPConfig
	to       []string
	password bool // a password is configured
}

func parseSettings(settings map[string]any) (instanceConfig, error) {
	var c instanceConfig
	c.smtp.Host, _ = settings["host"].(string)
	switch p := settings["port"].(type) {
	case nil:
	case float64:
		c.smtp.Port = int(p)
	case int:
		c.smtp.Port = p
	case int64:
		c.smtp.Port = int(p)
	default:
		return c, fmt.Errorf("%w: port is not a number", integrations.ErrInvalid)
	}
	c.smtp.Username, _ = settings["username"].(string)
	c.smtp.From, _ = settings["from"].(string)
	c.password = settings["password"] != nil
	switch to := settings["to"].(type) {
	case []any:
		for _, v := range to {
			s, ok := v.(string)
			if !ok {
				return c, fmt.Errorf("%w: a recipient is not a string", integrations.ErrInvalid)
			}
			c.to = append(c.to, s)
		}
	case []string:
		c.to = slices.Clone(to)
	}
	if len(c.to) == 0 || len(c.to) > maxRecipients {
		return c, fmt.Errorf("%w: to needs 1 to %d recipients", integrations.ErrInvalid, maxRecipients)
	}
	for _, r := range c.to {
		if _, err := parseAddress(r); err != nil {
			return c, fmt.Errorf("%w: a recipient is not a single address", integrations.ErrInvalid)
		}
	}
	return c, nil
}

// channel is one email instance.
type channel struct {
	env integrations.Env
	cfg instanceConfig
	mk  senderFunc
}

var (
	_ notify.Notifier     = (*channel)(nil)
	_ integrations.Tester = (*channel)(nil)
)

// Caps: plain text; a message is not truncated.
func (*channel) Caps() notify.Caps { return notify.Caps{} }

// Close releases nothing: every send opens its own connection.
func (*channel) Close() error { return nil }

// Health opens no connection: a probe that does would be a login per poll. A
// build that succeeded is healthy; Test sends.
func (*channel) Health(context.Context) integrations.Health {
	return integrations.Health{Status: integrations.HealthOK}
}

// Test sends the test notification to the instance's recipients.
func (c *channel) Test(ctx context.Context) error {
	now := c.env.Now()
	return c.Send(ctx, notify.Notification{
		ID: "test", Topic: notify.TestTopic, Severity: notify.SeverityInfo, At: now,
		Title: "Test notification",
		Body:  "This is a test notification. Nothing has happened and nothing needs doing.",
	}, notify.Target{Instance: c.env.Ref})
}

// NewChannel wraps a Sender as the Notifier of ref, sending to to and acquiring
// ticket from gate (nil: no admission). It is how the kernel makes the invite
// flow's Sender, and coretest.MailRecorder, an email instance (05-notify
// section 10, the mailSenderOverride seam).
func NewChannel(ref integrations.Ref, gate integrations.Gate, s Sender, to []string) notify.Notifier {
	return &channel{
		env: integrations.Env{Ref: ref, Gate: gate, Now: time.Now},
		cfg: instanceConfig{to: slices.Clone(to)},
		mk:  func(SMTPConfig) (Sender, error) { return s, nil },
	}
}

// Send delivers n as one message to every recipient: one SMTP transaction, so
// it reaches all of them or none and a retry never repeats a partial send.
func (c *channel) Send(ctx context.Context, n notify.Notification, _ notify.Target) error {
	fail := func(class integrations.Class, status int, err error) error {
		return &integrations.Error{Ref: c.env.Ref, Op: "send", Class: class, Status: status, Err: err}
	}
	smtpCfg := c.cfg.smtp
	if c.cfg.password {
		pw, err := c.env.Secrets.Secret(ctx, "password")
		if err != nil {
			class := integrations.ClassOf(ctx, err)
			if class == integrations.ClassUnknown {
				class = integrations.ClassRefusedLocal
			}
			return fail(class, 0, errors.New("the password could not be read"))
		}
		smtpCfg.Password = pw.Reveal()
	}
	sender, err := c.mk(smtpCfg)
	if err != nil {
		return fail(integrations.ClassInvalid, 0, errors.New("the instance's settings do not describe a sender"))
	}
	msg := Message{To: c.cfg.to[0], Subject: subject(n), Text: Layout(n)}
	if len(c.cfg.to) > 1 {
		msg.AlsoTo = c.cfg.to[1:]
	}

	var ticket integrations.Ticket
	if c.env.Gate != nil {
		if ticket, err = c.env.Gate.Acquire(ctx, ""); err != nil {
			return err // the gate's own *integrations.Error: a local refusal
		}
	}
	err = sender.Send(ctx, msg)
	if ticket != nil {
		ticket.Done(err)
	}
	if err == nil {
		return nil
	}
	class, status := classify(ctx, err)
	return fail(class, status, errors.New(summary(class)))
}

// summary is the text a failed send carries by class: the server's own reply
// and the connection's host stay out of every error this channel returns.
func summary(class integrations.Class) string {
	switch class {
	case integrations.ClassTransient:
		return "the server did not take the message right now"
	case integrations.ClassAuth:
		return "the server rejected the credentials"
	case integrations.ClassInvalid:
		return "the server or the message was refused as invalid"
	case integrations.ClassRefusedLocal:
		return "the send was refused locally before any message data"
	case integrations.ClassCanceled:
		return "the send was canceled"
	}
	return "the send failed"
}

// classify maps a Sender error to a class and the SMTP reply code, if any.
//
//	ErrInvalidHeader                        invalid
//	ErrTLSRequired                          refused locally: nothing was sent
//	ErrAuthUnavailable, 530, 534, 535       auth: a human changes credentials
//	other 5xx                               invalid
//	4xx                                     transient
//	a network error or a deadline           transient (integrations.ClassOf)
func classify(ctx context.Context, err error) (integrations.Class, int) {
	switch {
	case errors.Is(err, ErrInvalidHeader):
		return integrations.ClassInvalid, 0
	case errors.Is(err, ErrTLSRequired):
		return integrations.ClassRefusedLocal, 0
	case errors.Is(err, ErrAuthUnavailable):
		return integrations.ClassAuth, 0
	}
	if te, ok := errors.AsType[*textproto.Error](err); ok {
		switch {
		case te.Code == 530 || te.Code == 534 || te.Code == 535:
			return integrations.ClassAuth, te.Code
		case te.Code >= 500:
			return integrations.ClassInvalid, te.Code
		case te.Code >= 400:
			return integrations.ClassTransient, te.Code
		}
	}
	return integrations.ClassOf(ctx, err), 0
}

// subject is "[severity] title" on one line.
func subject(n notify.Notification) string {
	one := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, n.Title)), " ")
	return "[" + n.Severity.String() + "] " + one
}

// Layout is the fixed plain-text body: the title, the body, the links, the
// fields (sorted by key) and a footer naming the notification, each section
// present only when it has content.
func Layout(n notify.Notification) string {
	var b strings.Builder
	b.WriteString(n.Title + "\n")
	if n.Body != "" {
		b.WriteString("\n" + n.Body + "\n")
	}
	if len(n.Links) > 0 {
		b.WriteString("\nLinks:\n")
		for _, l := range n.Links {
			b.WriteString("  " + l.Label + ": " + l.URL + "\n")
		}
	}
	if len(n.Fields) > 0 {
		b.WriteString("\nDetails:\n")
		for _, k := range slices.Sorted(maps.Keys(n.Fields)) {
			b.WriteString("  " + k + ": " + n.Fields[k] + "\n")
		}
	}
	b.WriteString("\n-- \nNotification " + n.ID + " (" + n.Topic + ") at " + n.At.UTC().Format(time.RFC3339) + "\n")
	return b.String()
}
