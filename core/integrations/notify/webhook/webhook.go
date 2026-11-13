// file: core/integrations/notify/webhook/webhook.go
// version: 1.0.2
// guid: e03a7c58-91d4-4f26-b8a5-2c6d7f19e340
// last-edited: 2026-11-13

// Package webhook is the generic webhook channel (core.webhook) of the notify
// kind (05-notify §3.1): one POST (or PUT) of one fixed JSON body per
// notification, signed when the instance has a secret.
//
// The format is the mediacore one (D10): HEAD's X-Audiobook-* headers and
// body are not kept. A receiver dedupes on X-Mediacore-Delivery, which is
// "<notification ID>.<instance>" and identical on every retry of one delivery,
// and verifies X-Mediacore-Signature (see Sign) over the delivery ID, the
// configured URL and the exact body bytes, so a captured request cannot be
// replayed to another URL or against another delivery, and an old one is
// recognisable by its timestamp.
//
// The adapter is dark until the integrations flip (F7-09): nothing registers
// it, an app does through app.Options.Adapters.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/secrets"
)

// AdapterID is the adapter's ID.
const AdapterID integrations.AdapterID = "core.webhook"

// SchemaID names the body format; it is the body's "schema" member.
const SchemaID = "mediacore.notification.v1"

// The headers the adapter sets.
const (
	DeliveryHeader  = "X-Mediacore-Delivery"
	SignatureHeader = "X-Mediacore-Signature"
)

// sendTimeout is the per-attempt budget of the adapter's gate.
const sendTimeout = 15 * time.Second

// maxHeaders bounds the custom headers of one instance.
const maxHeaders = 16

// maxResponse bounds what is read of a receiver's answer; it is read only so
// the connection can be reused and the status classified, and never kept.
const maxResponse = 4 << 10

// Settings are an instance's settings. Secret and Token are references into
// c/secrets/, read at send time: Secret signs, Token is sent as a bearer
// token. Changing URL re-binds both (the Registry treats url as the
// destination, so a stored secret never follows the instance to a new host).
type Settings struct {
	URL     string      `json:"url" schema:"format=uri"`
	Method  string      `json:"method,omitzero" schema:"enum=POST|PUT"`
	Headers []Header    `json:"headers,omitzero"`
	Secret  secrets.Ref `json:"secret,omitzero"`
	Token   secrets.Ref `json:"token,omitzero"`
}

// Descriptor is the adapter for app.Options.Adapters. It returns an error only
// if Settings cannot be reflected, which a test of this package rules out.
func Descriptor() (integrations.Descriptor, error) {
	schema, err := config.SchemaFor[Settings]()
	if err != nil {
		return integrations.Descriptor{}, fmt.Errorf("webhook: settings schema: %w", err)
	}
	return notify.Adapter(integrations.Meta{
		ID:          AdapterID,
		DisplayName: "Webhook",
		Version:     "1.0.0",
		Settings:    schema,
		Egress:      integrations.EgressConfigured,
		// One notification is one request, and a retry is the outbox's, so
		// the gate does not retry on its own: MaxRetries is -1, the Limits
		// value for none (zero means the gate's default of several, which
		// would resend a POST the receiver cannot tell from the first, after
		// sleeping through the receiver's Retry-After inside Send).
		Limits: integrations.Limits{RPS: 1, Burst: 5, MaxRetries: -1, Timeout: sendTimeout},
	}, build), nil
}

// Reserved reports whether name is a header the adapter owns: any
// X-Mediacore-*, Authorization and Content-Type (and the framing headers Go
// sets itself), in any case. Settings.Headers must not set one.
func Reserved(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "authorization", "content-type", "content-length", "host", "transfer-encoding", "connection", "user-agent":
		return true
	}
	return strings.HasPrefix(n, "x-mediacore-")
}

// validHeader reports whether name and value can go on the wire as given.
func validHeader(name, value string) error {
	if name == "" {
		return errors.New("a header has an empty name")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return fmt.Errorf("header %q is not a valid header name", name)
		}
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return fmt.Errorf("header %q has a control character in its value", name)
		}
	}
	return nil
}

// config is a validated instance configuration.
type instanceConfig struct {
	url     string // as stored: the signature covers exactly this
	method  string
	headers []Header
	// signed and bearer: the instance has a secret / token configured. A
	// configured secret that cannot be read fails the send; sending the body
	// unsigned instead would hand the receiver a delivery it must reject, or,
	// worse, accept.
	signed, bearer bool
}

// parse reads and validates the settings of one instance.
func parse(settings map[string]any) (instanceConfig, error) {
	c := instanceConfig{method: http.MethodPost}
	raw, _ := settings["url"].(string)
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return c, fmt.Errorf("%w: the webhook url must be an http or https URL without credentials", integrations.ErrInvalid)
	}
	c.url = strings.TrimSpace(raw)
	c.signed, c.bearer = settings["secret"] != nil, settings["token"] != nil
	if m, _ := settings["method"].(string); m != "" {
		if m != http.MethodPost && m != http.MethodPut {
			return c, fmt.Errorf("%w: the webhook method must be POST or PUT", integrations.ErrInvalid)
		}
		c.method = m
	}
	switch h := settings["headers"].(type) {
	case nil:
	case []any:
		if len(h) > maxHeaders {
			return c, fmt.Errorf("%w: at most %d custom headers", integrations.ErrInvalid, maxHeaders)
		}
		c.headers = make([]Header, 0, len(h))
		for _, item := range h {
			m, _ := item.(map[string]any)
			name, _ := m["name"].(string)
			val, vok := m["value"].(string)
			if !vok {
				return c, fmt.Errorf("%w: header %q has no string value", integrations.ErrInvalid, name)
			}
			// The schema refuses a reserved name on Put and Test; this is the
			// same rule for rows that reach a build another way, such as the
			// Importer, which stores settings as given.
			if Reserved(name) {
				return c, fmt.Errorf("%w: header %q is reserved (X-Mediacore-*, Authorization and Content-Type are set by the adapter)", integrations.ErrInvalid, name)
			}
			if err := validHeader(name, val); err != nil {
				return c, fmt.Errorf("%w: %v", integrations.ErrInvalid, err)
			}
			c.headers = append(c.headers, Header{Name: name, Value: val})
		}
	default:
		return c, fmt.Errorf("%w: headers is not a list", integrations.ErrInvalid)
	}
	return c, nil
}

// build makes an instance. It does no I/O. A reserved or malformed header
// fails the build, so the instance never sends: Registry.Test reports
// ClassInvalid and Health reports it misconfigured. This is the one place the
// rule holds on Put, Test and the Importer alike, because the Importer stores
// settings as given.
func build(_ context.Context, env integrations.Env, cfg integrations.InstanceConfig) (notify.Notifier, error) {
	c, err := parse(cfg.Settings)
	if err != nil {
		return nil, err
	}
	return &instance{cfg: c, env: env}, nil
}

type instance struct {
	cfg instanceConfig
	env integrations.Env
}

var (
	_ notify.Notifier     = (*instance)(nil)
	_ integrations.Tester = (*instance)(nil)
)

// Caps: plain text only, no body limit (a receiver takes JSON of any size the
// notification bounds allow).
func (*instance) Caps() notify.Caps { return notify.Caps{} }

// Close releases nothing: the connection pool belongs to the instance's HTTP.
func (*instance) Close() error { return nil }

// Health probes nothing: a webhook has no read-only request, and a POST to a
// receiver is a notification. A build that succeeded is healthy; Test sends
// one.
func (*instance) Health(context.Context) integrations.Health {
	return integrations.Health{Status: integrations.HealthOK}
}

// Test sends a test notification, which is what a receiver needs to see to be
// set up.
func (i *instance) Test(ctx context.Context) error {
	now := i.env.Now()
	n := notify.Notification{
		ID: "test-" + strconv.FormatInt(now.UnixNano(), 36), Topic: notify.TestTopic,
		Severity: notify.SeverityInfo, Title: "Test notification",
		Body: "This is a test notification. Nothing has happened and nothing needs doing.",
		At:   now,
	}
	return i.Send(ctx, n, notify.Target{Instance: i.env.Ref})
}

// body is the one wire format. Links, entity and fields are always present
// (empty, not absent) so a receiver reads one shape.
type body struct {
	Schema   string            `json:"schema"`
	ID       string            `json:"id"`
	Topic    string            `json:"topic"`
	Severity string            `json:"severity"`
	Title    string            `json:"title"`
	Body     string            `json:"body"`
	Links    []notify.Link     `json:"links"`
	Entity   *entityRef        `json:"entity"`
	Fields   map[string]string `json:"fields"`
	At       time.Time         `json:"at"`
	App      string            `json:"app"`
}

type entityRef struct {
	Module string `json:"module"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}

// Marshal is the exact body bytes of n.
func Marshal(n notify.Notification) ([]byte, error) {
	b := body{
		Schema: SchemaID, ID: n.ID, Topic: n.Topic, Severity: n.Severity.String(),
		Title: n.Title, Body: n.Body, Links: n.Links, Fields: n.Fields,
		At: n.At.UTC(), App: n.App,
	}
	if b.Links == nil {
		b.Links = []notify.Link{}
	}
	if b.Fields == nil {
		b.Fields = map[string]string{}
	}
	if n.Entity != nil {
		b.Entity = &entityRef{Module: n.Entity.Module, Kind: n.Entity.Kind, ID: n.Entity.ID}
	}
	return json.Marshal(b)
}

// DeliveryID is the X-Mediacore-Delivery value: stable across every retry of
// one delivery, distinct per channel.
func DeliveryID(notificationID string, instance integrations.InstanceID) string {
	return notificationID + "." + string(instance)
}

// Sign is the X-Mediacore-Signature value for a request sent at t:
//
//	t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<delivery>.<url>.<body>")>
//
// delivery is the X-Mediacore-Delivery value, url the configured URL exactly
// as stored, body the exact bytes sent. A receiver recomputes it, compares in
// constant time, and rejects a t it considers too old.
func Sign(secret []byte, t time.Time, delivery, url string, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "." + delivery + "." + url + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// errors with no remote text: a dead-letter row and a log line carry them.
var (
	errTransport = errors.New("the request did not complete")
	errRedirect  = errors.New("the receiver answered with a redirect, which is never followed")
)

// Send delivers n: one request, no retry (the outbox retries).
func (i *instance) Send(ctx context.Context, n notify.Notification, t notify.Target) error {
	fail := func(class integrations.Class, status int, wait time.Duration, err error) error {
		return &integrations.Error{Ref: i.env.Ref, Op: "send", Class: class, Status: status, RetryAfter: wait, Err: err}
	}
	// Re-validate: the Importer stores settings as given, and a stored row
	// can reach a build only through parse, but the rule is cheap to hold twice.
	payload, err := Marshal(n)
	if err != nil {
		return fail(integrations.ClassInvalid, 0, 0, errors.New("the notification does not encode"))
	}
	req, err := http.NewRequestWithContext(ctx, i.cfg.method, i.cfg.url, bytes.NewReader(payload))
	if err != nil {
		return fail(integrations.ClassInvalid, 0, 0, errors.New("the request could not be built"))
	}
	for _, h := range i.cfg.headers {
		req.Header.Set(h.Name, h.Value)
	}
	req.Header.Set("Content-Type", "application/json")
	delivery := DeliveryID(n.ID, t.Instance.Instance)
	req.Header.Set(DeliveryHeader, delivery)

	if i.cfg.signed {
		sec, err := i.env.Secrets.Secret(ctx, "secret")
		if err != nil {
			return i.secretFailure(ctx, err)
		}
		req.Header.Set(SignatureHeader, Sign([]byte(sec.Reveal()), i.env.Now(), delivery, i.cfg.url, payload))
	}
	if i.cfg.bearer {
		tok, err := i.env.Secrets.Secret(ctx, "token")
		if err != nil {
			return i.secretFailure(ctx, err)
		}
		req.Header.Set("Authorization", "Bearer "+tok.Reveal())
	}

	resp, err := i.client().Do(req)
	if err != nil {
		if e, ok := errors.AsType[*integrations.Error](err); ok {
			// The gate's refusal, or a classified transport failure: keep its
			// class, and drop the *url.Error around it, which names the URL.
			return fail(e.Class, e.Status, e.RetryAfter, e.Err)
		}
		cause := errTransport
		return fail(providerhttp.Classify(ctx, nil, nil, err), 0, 0, cause)
	}
	defer resp.Body.Close()
	excerpt := readExcerpt(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fail(integrations.ClassInvalid, resp.StatusCode, 0, errRedirect)
	}
	class := providerhttp.Classify(ctx, resp, excerpt, nil)
	var wait time.Duration
	if class == integrations.ClassRateLimited || class == integrations.ClassTransient || class == integrations.ClassQuota {
		wait = retryAfter(resp.Header, i.env.Now())
	}
	return fail(class, resp.StatusCode, wait, fmt.Errorf("the receiver answered %d", resp.StatusCode))
}

// readExcerpt reads at most maxResponse bytes of a response, which only help
// classify a non-2xx answer (a quota page against a plain 5xx). A read that
// fails part way yields no excerpt at all: half a body could point the
// classifier the wrong way, and the status line alone still classifies.
func readExcerpt(r io.Reader) []byte {
	b, err := io.ReadAll(io.LimitReader(r, maxResponse))
	if err != nil {
		return nil
	}
	return b
}

// secretFailure is the failure of reading a secret before sending.
func (i *instance) secretFailure(ctx context.Context, err error) error {
	class := integrations.ClassOf(ctx, err)
	if class == integrations.ClassUnknown {
		class = integrations.ClassRefusedLocal
	}
	return &integrations.Error{Ref: i.env.Ref, Op: "send", Class: class, Err: errors.New("a secret of the instance could not be read")}
}

// client is the instance's outbound client with redirects and cookies off: a
// 3xx comes back as the answer, so the body and its signature never go to a
// host nobody configured. Its transport runs the gate and the dial guards.
func (i *instance) client() *http.Client {
	if base := i.env.HTTP.Client(); base != nil {
		cl := *base
		cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		cl.Jar = nil
		return &cl
	}
	return &http.Client{Transport: doerTransport{i.env.HTTP}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// doerTransport adapts an HTTP that offers no *http.Client.
type doerTransport struct{ h integrations.HTTP }

func (d doerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return d.h.Do(r) }

// retryAfter reads a Retry-After header: delta seconds or an HTTP date.
func retryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(min(secs, 1_000_000_000)) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
