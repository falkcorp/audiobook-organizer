// file: core/integrations/notify/webhook/webhook_test.go
// version: 1.0.2
// guid: c41d8e07-5a92-4f36-b0e8-72a9d3f15b86
// last-edited: 2026-11-13

package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/notify/webhook"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

const (
	secretsOwner storage.Owner = "core/secrets"
	auditOwner   storage.Owner = "core/audit"
)

// noon is the fixed time every cell signs and verifies at.
var noon = time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)

var roof = integrations.Ref{Kind: integrations.KindNotify, Instance: "commissioner"}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// admin is an interactive core:admin session that may manage integrations:
// the only principal that can mark an instance Local.
func admin() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage, integrationstest.Admin)
}

// manager may manage integrations but is not an admin.
func manager() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage)
}

// rig is a Registry with the webhook adapter over the real outbound layer
// (providerhttp, whose egress guard decides what a URL may reach), the real
// secrets and audit stores, and a fixed clock.
type rig struct {
	t   testing.TB
	reg *integrations.Registry
	sec *secrets.Store
}

func newRig(t testing.TB) *rig {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: "/batcave/webhook.pebble",
		Owners: []storage.OwnerSpec{
			{Owner: instancestore.Owner, Spec: instancestore.StorageSpec(), Enabled: true},
			{Owner: secretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
			{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build: "coretest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	must(t, err)
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks := func(o storage.Owner) storage.Keyspace {
		k, ok := db.Keyspace(o)
		if !ok {
			t.Fatalf("no keyspace for %s", o)
		}
		return k
	}
	root, err := os.OpenRoot(t.TempDir())
	must(t, err)
	t.Cleanup(func() { _ = root.Close() })
	now := func() time.Time { return noon }
	sec, err := secrets.Open(ctx, secrets.Options{StateDir: root, Keyspace: ks(secretsOwner), AppID: "gotham", Now: now})
	must(t, err)
	aks := ks(auditOwner)
	au, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq"), Now: now})
	must(t, err)
	ph, err := providerhttp.New(providerhttp.Options{Now: now})
	must(t, err)
	iks := ks(instancestore.Owner)
	reg, err := integrations.New(integrations.Options{
		Store: integrations.NativeInstanceStore(iks), NewBatch: iks.NewBatch, Secrets: sec, Audit: au,
		Outbound: ph.Outbound, Now: now,
	})
	must(t, err)
	d, err := webhook.Descriptor()
	must(t, err)
	must(t, reg.Register("core", d))
	must(t, reg.Load(ctx, nil))
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = reg.Close(cctx)
	})
	return &rig{t: t, reg: reg, sec: sec}
}

// put stores an admin-made instance of the webhook adapter at url, marked
// Local (the receivers here are on loopback), with the given extra settings
// and secrets.
func (r *rig) put(ref integrations.Ref, url string, extra map[string]any, in map[string]secrets.Value) integrations.InstanceConfig {
	r.t.Helper()
	s := map[string]any{"url": url}
	maps.Copy(s, extra)
	got, err := r.reg.Put(admin(), integrations.InstanceConfig{
		Ref: ref, Adapter: webhook.AdapterID, Name: string(ref.Instance), Enabled: true, Local: true, Settings: s,
	}, in)
	if err != nil {
		r.t.Fatalf("Put %s: %v", ref, err)
	}
	return got
}

// send leases ref and sends n through its channel.
func (r *rig) send(ref integrations.Ref, n notify.Notification) error {
	r.t.Helper()
	nr, release, err := integrations.Lease[notify.Notifier](context.Background(), r.reg, ref)
	if err != nil {
		r.t.Fatalf("Lease %s: %v", ref, err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return nr.Send(ctx, n, notify.Target{Instance: ref})
}

// classOf is the class of a failed send.
func classOf(err error) integrations.Class {
	return integrations.ClassOf(context.Background(), err)
}

// signalNote is a full notification: every optional member present.
func signalNote() notify.Notification {
	return notify.Notification{
		ID: "01JBATSIGNAL000000000000000", Topic: "gotham.signal.raised.v1", Severity: notify.SeverityWarning,
		Title: "The signal is lit", Body: "Commissioner Gordon asks for the Batman.",
		Links:  []notify.Link{{Label: "Roof", URL: "https://gotham.example/roof"}},
		Entity: &entity.Ref{Module: "gotham", Kind: "signal", ID: "7"},
		Fields: map[string]string{"villain": "Joker", "block": "12"},
		At:     noon, App: "batcave",
	}
}

// ---- verification, written apart from the sender ---------------------------

// verify is a receiver's check of a request: it shares nothing with
// webhook.Sign but the rule itself, written out from the documented format.
func verify(secret, url, delivery, signature string, body []byte, now time.Time, tolerance time.Duration) error {
	var ts, sig string
	for part := range strings.SplitSeq(signature, ",") {
		switch k, v, _ := strings.Cut(part, "="); k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return errors.New("malformed signature")
	}
	if age := now.Sub(time.Unix(secs, 0)); age > tolerance || age < -tolerance {
		return errors.New("timestamp outside the tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s.%s.%s.", ts, delivery, url)
	mac.Write(body)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig)) {
		return errors.New("signature mismatch")
	}
	return nil
}

// A signed request carries X-Mediacore-Signature: t=<unix>,v1=<hex HMAC>
// over the timestamp, the delivery ID, the configured URL and the body. Each
// of those, altered, fails verification (I3; SEC-8).
func TestWebhook_SignatureWithTimestamp(t *testing.T) {
	const secret = "hush-hush-batcave"
	r := newRig(t)
	rcv := coretest.NewWebhookReceiver(t, secret)
	r.put(roof, rcv.URL, nil, map[string]secrets.Value{"secret": secrets.NewValue(secret)})
	rcv.SetNow(func() time.Time { return noon })

	if err := r.send(roof, signalNote()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := rcv.Deliveries()
	if len(got) != 1 {
		t.Fatalf("the receiver took %d requests, want 1", len(got))
	}
	d := got[0]
	if d.SignatureErr != nil {
		t.Fatalf("the coretest receiver rejected the signature: %v", d.SignatureErr)
	}
	sig, delivery := d.Header.Get(webhook.SignatureHeader), d.Header.Get(webhook.DeliveryHeader)
	want := "01JBATSIGNAL000000000000000.commissioner"
	if delivery != want {
		t.Fatalf("X-Mediacore-Delivery = %q, want %q", delivery, want)
	}
	if !strings.HasPrefix(sig, "t="+strconv.FormatInt(noon.Unix(), 10)+",v1=") {
		t.Fatalf("X-Mediacore-Signature = %q: want t=<unix>,v1=<hex>", sig)
	}
	tol := 5 * time.Minute
	if err := verify(secret, rcv.URL, delivery, sig, d.Body, noon, tol); err != nil {
		t.Fatalf("an independent verification of the genuine request failed: %v", err)
	}

	// Each altered cell must fail.
	t0 := strconv.FormatInt(noon.Unix(), 10)
	t1 := strconv.FormatInt(noon.Unix()+1, 10)
	cells := map[string]error{
		"the timestamp, altered by a second": verify(secret, rcv.URL, delivery, strings.Replace(sig, "t="+t0, "t="+t1, 1), d.Body, noon, tol),
		"the delivery ID":                    verify(secret, rcv.URL, delivery+"x", sig, d.Body, noon, tol),
		"another channel's delivery ID":      verify(secret, rcv.URL, "01JBATSIGNAL000000000000000.alfred", sig, d.Body, noon, tol),
		"the destination URL":                verify(secret, rcv.URL+"x", delivery, sig, d.Body, noon, tol),
		"the destination host":               verify(secret, strings.Replace(rcv.URL, "127.0.0.1", "localhost", 1), delivery, sig, d.Body, noon, tol),
		"the body":                           verify(secret, rcv.URL, delivery, sig, append(append([]byte(nil), d.Body...), ' '), noon, tol),
		"the secret":                         verify(secret+"x", rcv.URL, delivery, sig, d.Body, noon, tol),
		"a replay past the tolerance":        verify(secret, rcv.URL, delivery, sig, d.Body, noon.Add(tol+time.Second), tol),
	}
	for name, err := range cells {
		if err == nil {
			t.Errorf("a request with %s altered still verifies", name)
		}
	}

	// A known answer, computed outside this code base (Python hmac over the
	// same string), so a symmetric mistake in Sign and in verify cannot pass.
	got2 := webhook.Sign([]byte("hush-hush-batcave"), noon,
		"01JBATSIGNAL000000000000000.commissioner", "https://gotham.example/hooks/roof",
		[]byte(`{"schema":"mediacore.notification.v1","id":"01JBATSIGNAL000000000000000"}`))
	const known = "t=2185308000,v1=cd9971b074573a4d3099d794cb32df55ddfa2e60076351b40a889fd8802e3f4a"
	if got2 != known {
		t.Errorf("Sign = %q, want the known answer %q", got2, known)
	}
}

// The body is the one format: schema, the notification's members in a fixed
// order, severity as its name, an empty list and map and a null entity where
// there is none, the time in UTC.
func TestWebhook_JSONBodyGolden(t *testing.T) {
	const full = `{"schema":"mediacore.notification.v1","id":"01JBATSIGNAL000000000000000","topic":"gotham.signal.raised.v1",` +
		`"severity":"warning","title":"The signal is lit","body":"Commissioner Gordon asks for the Batman.",` +
		`"links":[{"label":"Roof","url":"https://gotham.example/roof"}],` +
		`"entity":{"module":"gotham","kind":"signal","id":"7"},"fields":{"block":"12","villain":"Joker"},` +
		`"at":"2039-04-01T22:00:00Z","app":"batcave"}`
	const bare = `{"schema":"mediacore.notification.v1","id":"01JBATBARE0000000000000000","topic":"gotham.signal.raised.v1",` +
		`"severity":"info","title":"Quiet night","body":"","links":[],"entity":null,"fields":{},` +
		`"at":"2039-04-01T22:00:00Z","app":""}`

	got, err := webhook.Marshal(signalNote())
	must(t, err)
	if string(got) != full {
		t.Errorf("full body =\n%s\nwant\n%s", got, full)
	}
	loc := time.FixedZone("gotham", -5*3600)
	got, err = webhook.Marshal(notify.Notification{ID: "01JBATBARE0000000000000000", Topic: "gotham.signal.raised.v1",
		Title: "Quiet night", At: noon.In(loc)})
	must(t, err)
	if string(got) != bare {
		t.Errorf("bare body =\n%s\nwant\n%s", got, bare)
	}

	// What goes over the wire is exactly Marshal's bytes, as JSON.
	r := newRig(t)
	rcv := coretest.NewWebhookReceiver(t, "")
	r.put(roof, rcv.URL, nil, nil)
	must(t, r.send(roof, signalNote()))
	d := rcv.Deliveries()[0]
	if string(d.Body) != full {
		t.Errorf("the request body differs from Marshal:\n%s", d.Body)
	}
	if ct := d.Header.Get("Content-Type"); ct != "application/json" || d.Method != http.MethodPost {
		t.Errorf("%s with Content-Type %q, want POST application/json", d.Method, ct)
	}
	if d.Header.Get(webhook.SignatureHeader) != "" {
		t.Error("an instance with no secret sent a signature")
	}
}

// X-Mediacore-Delivery is the notification ID and the channel, the same on
// every retry of one delivery and different for another channel, so a
// receiver can dedupe a retry (the outbox is at-least-once).
func TestWebhook_DeliveryHeaderStable(t *testing.T) {
	r := newRig(t)
	rcv := coretest.NewWebhookReceiver(t, "")
	rcv.SetRespond(func(n int, _ coretest.WebhookDelivery) (int, string) {
		if n <= 2 {
			return http.StatusNotFound, "" // not a status that holds the provider: the next attempt is sent
		}
		return http.StatusNoContent, ""
	})
	r.put(roof, rcv.URL, nil, nil)
	other := integrations.Ref{Kind: integrations.KindNotify, Instance: "alfred"}
	r.put(other, rcv.URL, nil, nil)

	n := signalNote()
	for range 2 {
		if err := r.send(roof, n); err == nil {
			t.Fatal("a 404 was accepted")
		}
	}
	must(t, r.send(roof, n))
	must(t, r.send(other, n))
	got := rcv.Deliveries()
	if len(got) != 4 {
		t.Fatalf("%d requests, want 4", len(got))
	}
	ids := make([]string, len(got))
	for i, d := range got {
		ids[i] = d.Header.Get(webhook.DeliveryHeader)
	}
	if ids[0] != ids[1] || ids[1] != ids[2] || ids[0] != "01JBATSIGNAL000000000000000.commissioner" {
		t.Errorf("the retries carried %q, want one stable ID", ids[:3])
	}
	if ids[3] != "01JBATSIGNAL000000000000000.alfred" || ids[3] == ids[0] {
		t.Errorf("another channel's delivery ID = %q", ids[3])
	}
	if string(got[0].Body) != string(got[2].Body) {
		t.Error("a retry's body differs from the first attempt's")
	}
}

// ---- reserved headers ---------------------------------------------------

// reservedNames are spelled the ways a caller might, with the case variants.
func reservedNames() []string {
	var out []string
	for _, base := range []string{"Authorization", "Content-Type", "X-Mediacore-Delivery", "X-Mediacore-Signature", "X-Mediacore-Anything"} {
		out = append(out, base, strings.ToLower(base), strings.ToUpper(base))
	}
	return append(out, "aUtHoRiZaTiOn", "cOnTeNt-TyPe", "x-MEDIAcore-signature")
}

// Reserved header names are refused on Put and on Test as invalid settings,
// and a row that reaches a build some other way (the importer stores
// settings as given) still fails the build. A custom header goes through.
func TestWebhook_ReservedHeadersRefused(t *testing.T) {
	r := newRig(t)
	rcv := coretest.NewWebhookReceiver(t, "")
	withHeader := func(name string) map[string]any {
		return map[string]any{"url": rcv.URL, "headers": []any{map[string]any{"name": name, "value": "x"}}}
	}
	for _, name := range reservedNames() {
		inst := integrations.InstanceConfig{Ref: roof, Adapter: webhook.AdapterID, Name: "x", Enabled: true, Local: true, Settings: withHeader(name)}
		if _, err := r.reg.Put(admin(), inst, nil); !errors.Is(err, integrations.ErrInvalid) {
			t.Errorf("Put with header %q: err = %v, want ErrInvalid", name, err)
		}
		if res, err := r.reg.Test(admin(), inst, nil); err == nil && res.OK {
			t.Errorf("Test with header %q succeeded", name)
		}
	}
	if len(rcv.Deliveries()) != 0 {
		t.Fatal("a refused instance sent a request")
	}
	// The framing headers the transport owns are not in the schema's pattern,
	// so a Put takes them, but the instance cannot be built: it is never
	// leased, and nothing is sent.
	for i, name := range []string{"Host", "Content-Length", "Transfer-Encoding", "Connection", "User-Agent", "host"} {
		ref := integrations.Ref{Kind: integrations.KindNotify, Instance: integrations.InstanceID("framing-" + strconv.Itoa(i))}
		inst := integrations.InstanceConfig{Ref: ref, Adapter: webhook.AdapterID, Name: "x", Enabled: true, Local: true, Settings: withHeader(name)}
		if _, err := r.reg.Put(admin(), inst, nil); err != nil {
			continue // refused earlier still: fine
		}
		if _, release, err := integrations.Lease[notify.Notifier](context.Background(), r.reg, ref); err == nil {
			release()
			t.Errorf("an instance with header %q was built", name)
		}
	}
	if len(rcv.Deliveries()) != 0 {
		t.Fatal("an instance with a framing header sent a request")
	}
	// A header name that is not a token at all.
	for _, name := range []string{"", "Bad Name", "Bad:Name", "Bad\r\nName"} {
		inst := integrations.InstanceConfig{Ref: roof, Adapter: webhook.AdapterID, Name: "x", Enabled: true, Local: true, Settings: withHeader(name)}
		if _, err := r.reg.Put(admin(), inst, nil); err == nil {
			t.Errorf("Put with header %q succeeded", name)
		}
	}
	// A custom header reaches the receiver, and cannot displace the adapter's own.
	custom := integrations.InstanceConfig{Ref: roof, Adapter: webhook.AdapterID, Name: "x", Enabled: true, Local: true,
		Settings: withHeader("X-Gotham-Block")}
	if _, err := r.reg.Put(admin(), custom, nil); err != nil {
		t.Fatalf("Put with a custom header: %v", err)
	}
	must(t, r.send(roof, signalNote()))
	d := rcv.Deliveries()[0]
	if d.Header.Get("X-Gotham-Block") != "x" || d.Header.Get("Content-Type") != "application/json" ||
		d.Header.Get(webhook.DeliveryHeader) != "01JBATSIGNAL000000000000000.commissioner" {
		t.Errorf("headers = %v", d.Header)
	}
}

// The settings schema spells the reserved rule as a pattern; this holds the
// pattern to Reserved (and to the three names the schema refuses) over every
// letter-case spelling of each name and the names around them.
func TestHeaderNamePattern(t *testing.T) {
	tag := reflect.TypeFor[webhook.Header]().Field(0).Tag.Get("schema")
	pat, ok := strings.CutPrefix(tag, "pattern=")
	if !ok {
		t.Fatalf("Header.Name has no pattern in its schema tag: %q", tag)
	}
	re := regexp.MustCompile(pat)

	var names []string
	for _, base := range []string{"authorization", "content-type", "x-mediacore-"} {
		for _, spelling := range caseSpellings(base) {
			names = append(names, spelling, spelling+"x", spelling+"-", spelling+"0")
			if len(spelling) > 1 {
				names = append(names, spelling[:len(spelling)-1], "x"+spelling)
			}
		}
	}
	names = append(names, "X-Gotham", "X-Mediacor", "X-Mediacore", "x-mediacore-", "Accept", "Authorizatio", "Authorizationx",
		"Content-Types", "Content-Typ", "Content-Length", "A", "X", "a-b", "_", "~", "1", "AuthorizatioN")
	for _, n := range names {
		wantMatch := !(strings.EqualFold(n, "authorization") || strings.EqualFold(n, "content-type") ||
			strings.HasPrefix(strings.ToLower(n), "x-mediacore-"))
		if got := re.MatchString(n); got != wantMatch {
			t.Errorf("pattern matches %q = %v, want %v", n, got, wantMatch)
		}
		if webhook.Reserved(n) && re.MatchString(n) &&
			(strings.EqualFold(n, "authorization") || strings.EqualFold(n, "content-type") || strings.HasPrefix(strings.ToLower(n), "x-mediacore-")) {
			t.Errorf("%q is reserved yet the pattern admits it", n)
		}
	}
	for _, bad := range []string{"", "a b", "a:b", "a\nb", "a`b", "a\x00b"} {
		if re.MatchString(bad) {
			t.Errorf("the pattern admits %q, which is not a header token", bad)
		}
	}
}

// caseSpellings is every upper/lower-case spelling of s (at most 2^12).
func caseSpellings(s string) []string {
	out := []string{""}
	for _, c := range s {
		lo, up := string(c), strings.ToUpper(string(c))
		var next []string
		for _, p := range out {
			next = append(next, p+lo)
			if up != lo {
				next = append(next, p+up)
			}
		}
		out = next
	}
	return out
}

// ---- egress -------------------------------------------------------------

// A URL on a private or loopback address is refused unless an interactive
// admin marked the instance Local: a local refusal, with nothing sent.
func TestWebhook_PrivateURLNeedsLocal(t *testing.T) {
	r := newRig(t)
	rcv := coretest.NewWebhookReceiver(t, "")

	// Not Local: the dial is refused before any connection.
	plain, err := r.reg.Put(admin(), integrations.InstanceConfig{
		Ref: roof, Adapter: webhook.AdapterID, Name: "x", Enabled: true, Settings: map[string]any{"url": rcv.URL},
	}, nil)
	must(t, err)
	err = r.send(roof, signalNote())
	if err == nil || classOf(err) != integrations.ClassRefusedLocal {
		t.Fatalf("a loopback URL on a non-Local instance: err = %v (class %v), want refused_local", err, classOf(err))
	}
	if len(rcv.Deliveries()) != 0 {
		t.Fatal("the receiver was reached through the egress guard")
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), rcv.URL) {
		t.Errorf("the error names the destination: %q", err)
	}

	// Local, set by an interactive admin: delivered.
	plain.Local = true
	if _, err := r.reg.Put(admin(), plain, nil); err != nil {
		t.Fatalf("Put Local: %v", err)
	}
	must(t, r.send(roof, signalNote()))
	if len(rcv.Deliveries()) != 1 {
		t.Fatalf("%d requests after marking the instance Local, want 1", len(rcv.Deliveries()))
	}

	// A manager who is not an admin cannot grant it.
	cfg, _ := r.reg.Config(roof)
	cfg.Local = true
	cfg.Ref = integrations.Ref{Kind: integrations.KindNotify, Instance: "oracle"}
	cfg.Version = 0
	if _, err := r.reg.Put(manager(), cfg, nil); err == nil {
		if got, _ := r.reg.Config(cfg.Ref); got.Local {
			t.Error("a manager who is not an admin created a Local instance")
		}
	}
}

// ---- classes and secrets ------------------------------------------------

// What the receiver answers decides the class, the Retry-After is read, a
// redirect is never followed, and the error never carries the URL or the
// receiver's words (a dead-letter row stores the text).
func TestWebhook_ClassifiesAnswers(t *testing.T) {
	r := newRig(t)
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the redirect target was contacted")
	}))
	t.Cleanup(moved.Close)
	for i, c := range []struct {
		name   string
		status int
		retry  string
		class  integrations.Class
		wait   time.Duration
	}{
		{"accepted", 202, "", 0, 0},
		{"rate limited with seconds", 429, "7", integrations.ClassRateLimited, 7 * time.Second},
		{"unavailable with a date", 503, noon.Add(90 * time.Second).UTC().Format(http.TimeFormat), integrations.ClassTransient, 90 * time.Second},
		{"bad gateway", 502, "", integrations.ClassTransient, 0},
		{"unauthorised", 401, "", integrations.ClassAuth, 0},
		{"forbidden", 403, "", integrations.ClassAuth, 0},
		{"gone", 410, "", integrations.ClassNotFound, 0},
		{"a redirect", 302, "", integrations.ClassInvalid, 0},
	} {
		rcv := coretest.NewWebhookReceiver(t, "")
		status, retry := c.status, c.retry
		rcv.SetRespond(func(int, coretest.WebhookDelivery) (int, string) { return status, retry })
		url := rcv.URL
		if c.status == 302 {
			retry = ""
			// Answer with a Location header the client must not follow.
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", moved.URL)
				w.WriteHeader(http.StatusFound)
			}))
			t.Cleanup(srv.Close)
			url = srv.URL
		}
		// A ref of its own: a transient answer holds the provider's scope.
		ref := integrations.Ref{Kind: integrations.KindNotify, Instance: integrations.InstanceID("case-" + strconv.Itoa(i))}
		r.put(ref, url, nil, nil)
		err := r.send(ref, signalNote())
		if c.class == 0 {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil || classOf(err) != c.class {
			t.Errorf("%s: err = %v (class %v), want class %v", c.name, err, classOf(err), c.class)
			continue
		}
		e, ok := errors.AsType[*integrations.Error](err)
		if !ok || e.Status != c.status || e.RetryAfter != c.wait {
			t.Errorf("%s: error = %#v, want status %d retry-after %v", c.name, err, c.status, c.wait)
		}
		if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "gotham") {
			t.Errorf("%s: the error text carries the destination: %q", c.name, err)
		}
	}
}

// A token is sent as a bearer credential, a secret signs, and neither is
// ever in an error. A secret that is configured but cannot be read fails the
// send: the request is never sent unsigned.
func TestWebhook_SecretsAreUsedAndNeverSentUnsigned(t *testing.T) {
	const secret, token = "hush-hush-batcave", "tok-GOTHAM-SENTINEL"
	r := newRig(t)
	rcv := coretest.NewWebhookReceiver(t, secret)
	rcv.SetNow(func() time.Time { return noon })
	r.put(roof, rcv.URL, map[string]any{"method": "PUT"}, map[string]secrets.Value{
		"secret": secrets.NewValue(secret), "token": secrets.NewValue(token),
	})
	must(t, r.send(roof, signalNote()))
	d := rcv.Deliveries()[0]
	if d.Method != http.MethodPut || d.Header.Get("Authorization") != "Bearer "+token || d.SignatureErr != nil {
		t.Errorf("method %s, Authorization %q, signature error %v", d.Method, d.Header.Get("Authorization"), d.SignatureErr)
	}

	// Lose the secret out from under the instance.
	name := secrets.Name("integrations.notify.commissioner.secret")
	must(t, r.sec.Delete(context.Background(), name, "test"))
	err := r.send(roof, signalNote())
	if err == nil {
		t.Fatal("a send with an unreadable signing secret succeeded")
	}
	if n := len(rcv.Deliveries()); n != 1 {
		t.Fatalf("the receiver took %d requests, want only the first: nothing is sent unsigned", n)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), token) {
		t.Errorf("the error carries a secret: %q", err)
	}
}

// Test sends a test notification, and Health does not call the receiver.
func TestWebhook_TestSendsATestNotification(t *testing.T) {
	r := newRig(t)
	rcv := coretest.NewWebhookReceiver(t, "")
	inst := r.put(roof, rcv.URL, nil, nil)
	res, err := r.reg.Test(admin(), inst, nil)
	if err != nil || !res.OK {
		t.Fatalf("Test = %+v, %v", res, err)
	}
	got := rcv.Deliveries()
	if len(got) != 1 || !strings.Contains(string(got[0].Body), `"topic":"core.notify.test.v1"`) {
		t.Fatalf("the test request = %+v", got)
	}
	before := len(rcv.Deliveries())
	_, _ = r.reg.Health(context.Background(), roof)
	if len(rcv.Deliveries()) != before {
		t.Error("a health probe called the receiver")
	}
}

// One Send is one request, for either method: the outbound gate does not
// retry on its own (the outbox does, and tells the receiver nothing new), and
// the receiver's Retry-After comes back to the caller instead of being slept
// through inside Send.
func TestWebhook_OneRequestPerSend(t *testing.T) {
	for _, method := range []string{"POST", "PUT"} {
		t.Run(method, func(t *testing.T) {
			r := newRig(t)
			rcv := coretest.NewWebhookReceiver(t, "")
			rcv.SetRespond(func(int, coretest.WebhookDelivery) (int, string) { return http.StatusServiceUnavailable, "30" })
			r.put(roof, rcv.URL, map[string]any{"method": method}, nil)
			start := time.Now()
			err := r.send(roof, signalNote())
			if classOf(err) != integrations.ClassTransient {
				t.Fatalf("err = %v (class %v), want transient", err, classOf(err))
			}
			if e, ok := errors.AsType[*integrations.Error](err); !ok || e.RetryAfter != 30*time.Second {
				t.Errorf("RetryAfter = %v, want the receiver's 30s handed back", err)
			}
			if n := len(rcv.Deliveries()); n != 1 {
				t.Errorf("%d requests for one Send, want 1", n)
			}
			if took := time.Since(start); took > 3*time.Second {
				t.Errorf("Send took %v: it slept through the Retry-After", took)
			}
		})
	}
}
