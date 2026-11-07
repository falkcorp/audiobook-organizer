// file: core/log/redactor_test.go
// version: 1.1.1
// guid: bd738719-8f12-4c45-b718-d225f6302c0e
// last-edited: 2026-11-07

package log

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Obviously fake credentials. Every assertion checks these strings are absent.
const (
	fakeAPIKey  = "abk_test_batcave000"
	fakeToken   = "tok_test_batsignal"
	fakeInvite  = "invite_test_gotham_000"
	fakePass    = "pw_test_alfred_000"
	fakeCode    = "code_test_oracle_000"
	fakeVerify  = "verifier_test_lucius_000"
	fakeRefresh = "refresh_test_robin_000"
)

// redactingHandler is the minimal stand-in for core's pipeline (R1-36 builds
// the real one): every attribute of a record goes through Redactor.Attr before
// the stdlib text or JSON handler formats it.
type redactingHandler struct {
	next slog.Handler
	r    *Redactor
}

func (h redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h redactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.r.Attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h redactingHandler) WithAttrs(as []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(as))
	for i, a := range as {
		red[i] = h.r.Attr(a)
	}
	return redactingHandler{next: h.next.WithAttrs(red), r: h.r}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(name), r: h.r}
}

// logBoth logs attrs once through a text handler and once through a JSON
// handler, both behind r, and returns the two outputs.
func logBoth(t *testing.T, r *Redactor, attrs ...slog.Attr) (text, js string) {
	t.Helper()
	var tb, jb bytes.Buffer
	slog.New(redactingHandler{next: slog.NewTextHandler(&tb, nil), r: r}).LogAttrs(context.Background(), slog.LevelInfo, "batman", attrs...)
	slog.New(redactingHandler{next: slog.NewJSONHandler(&jb, nil), r: r}).LogAttrs(context.Background(), slog.LevelInfo, "batman", attrs...)
	return tb.String(), jb.String()
}

func assertAbsent(t *testing.T, where, out string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("%s: secret %q leaked: %s", where, s, out)
		}
	}
}

// TestDefaultLists_SpecContents pins the four default lists to 05-logging §3
// verbatim, so a list can never shrink silently.
func TestDefaultLists_SpecContents(t *testing.T) {
	for name, tc := range map[string]struct{ got, want []string }{
		"DefaultRedactKeys": {DefaultRedactKeys(), []string{"password", "passwd", "secret", "token", "apikey",
			"authorization", "cookie", "setcookie", "privatekey", "clientsecret", "credential", "credentials",
			"accesstoken", "refreshtoken", "sessionid"}},
		"DefaultRedactQuery": {DefaultRedactQuery(), []string{"token", "access_token", "refresh_token", "api_key",
			"apikey", "key", "password", "secret", "code", "code_verifier", "sig", "signature"}},
		"URLKeys": {URLKeys(), []string{"path", "url", "uri", "request_uri", "referer", "url.full", "url.path",
			"url.query", "http.target", "http.url"}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s() = %q, want %q", name, tc.got, tc.want)
		}
	}
	rules := DefaultPathRules()
	if len(rules) != 1 || rules[0].Name != "invite" || rules[0].Fn == nil {
		t.Fatalf("DefaultPathRules() = %+v, want one rule named invite", rules)
	}
}

func TestDefaultLists_FreshCopies(t *testing.T) {
	before := NewRedactor(nil)
	wantKeys, wantQuery, wantURL := DefaultRedactKeys(), DefaultRedactQuery(), URLKeys()

	// Edit every element and append to every slice one call returns.
	k := DefaultRedactKeys()
	for i := range k {
		k[i] = "batman"
	}
	_ = append(k, "robin")
	q := DefaultRedactQuery()
	for i := range q {
		q[i] = "batman"
	}
	_ = append(q, "robin")
	u := URLKeys()
	for i := range u {
		u[i] = "batman"
	}
	_ = append(u, "robin")
	pr := DefaultPathRules()
	for i := range pr {
		pr[i].Name = "joker"
		pr[i].Fn = func(p string) string { return p }
	}
	_ = append(pr, PathRule{Name: "riddler", Fn: func(string) string { return "/" }})

	if got := DefaultRedactKeys(); !slices.Equal(got, wantKeys) {
		t.Errorf("DefaultRedactKeys changed after editing a copy: %q", got)
	}
	if got := DefaultRedactQuery(); !slices.Equal(got, wantQuery) {
		t.Errorf("DefaultRedactQuery changed after editing a copy: %q", got)
	}
	if got := URLKeys(); !slices.Equal(got, wantURL) {
		t.Errorf("URLKeys changed after editing a copy: %q", got)
	}
	if got := DefaultPathRules(); len(got) != 1 || got[0].Name != "invite" {
		t.Errorf("DefaultPathRules changed after editing a copy: %+v", got)
	}
	after := NewRedactor(nil)
	for name, r := range map[string]*Redactor{"built before": before, "built after": after} {
		if got := r.String("password", fakePass); got != "[REDACTED]" {
			t.Errorf("%s: key deny-list weakened: %q", name, got)
		}
		if got := r.URL("/x?apikey=" + fakeAPIKey); strings.Contains(got, fakeAPIKey) {
			t.Errorf("%s: query list weakened: %q", name, got)
		}
		if got := r.String("url.full", "https://example.test/invite/"+fakeInvite); strings.Contains(got, fakeInvite) {
			t.Errorf("%s: URL keys or path rules weakened: %q", name, got)
		}
		if got := r.Path("/invite/" + fakeInvite); strings.Contains(got, fakeInvite) {
			t.Errorf("%s: invite path rule weakened: %q", name, got)
		}
	}
}

func TestRedact_DefaultKeys(t *testing.T) {
	r := NewRedactor([]string{"Batarang_Code"})
	keys := append(DefaultRedactKeys(),
		// Matched case-insensitively after stripping '-', '_' and '.'.
		"PASSWORD", "Api-Key", "api_key", "x.Access_Token", "Set-Cookie", "client_secret", "session.id",
		// The last dotted segment of an attribute key also matches.
		"http.request.header.authorization", "db.password",
		// An extra key from NewRedactor, in another spelling.
		"batarang-code")
	for _, k := range keys {
		if got := r.Attr(slog.String(k, fakePass)); got.Value.String() != "[REDACTED]" || got.Key != k {
			t.Errorf("Attr(%q) = %v, want [REDACTED] under the same key", k, got)
		}
		if got := r.Attr(slog.Int(k, 42)); got.Value.String() != "[REDACTED]" {
			t.Errorf("Attr(%q, int) = %v, want [REDACTED]", k, got)
		}
		if got := r.String(k, fakePass); got != "[REDACTED]" {
			t.Errorf("String(%q) = %q, want [REDACTED]", k, got)
		}
	}
	for _, k := range []string{"user", "title", "tokens_used", "url.path", "author"} {
		if got := r.Attr(slog.String(k, "Batman: Year One")); got.Value.String() != "Batman: Year One" {
			t.Errorf("Attr(%q) changed a harmless value: %v", k, got)
		}
	}
	text, js := logBoth(t, r, slog.String("password", fakePass), slog.String("Authorization", "Bearer "+fakeToken),
		slog.String("title", "The Dark Knight Returns"))
	for where, out := range map[string]string{"text": text, "json": js} {
		assertAbsent(t, where, out, fakePass, fakeToken)
		if !strings.Contains(out, "The Dark Knight Returns") || !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s: unexpected output: %s", where, out)
		}
	}
}

func TestRedact_NestedGroups(t *testing.T) {
	r := NewRedactor(nil)
	a := slog.Group("request",
		slog.String("user", "bruce"),
		slog.Group("auth", slog.String("token", fakeToken),
			slog.Group("inner", slog.String("password", fakePass), slog.String("url", "/x?apikey="+fakeAPIKey))),
		slog.Group("credentials", slog.String("user", "alfred"), slog.String("pin", fakeCode)),
	)
	text, js := logBoth(t, r, a)
	for where, out := range map[string]string{"text": text, "json": js} {
		assertAbsent(t, where, out, fakeToken, fakePass, fakeAPIKey, fakeCode)
		if !strings.Contains(out, "bruce") {
			t.Errorf("%s: a harmless nested value was lost: %s", where, out)
		}
	}
	// A deny-listed group key replaces the whole group.
	got := r.Attr(slog.Group("credentials", slog.String("user", "alfred")))
	if got.Value.Kind() != slog.KindString || got.Value.String() != "[REDACTED]" {
		t.Errorf("deny-listed group = %v, want [REDACTED]", got)
	}
}

func TestRedact_URLUserinfoAndQuery(t *testing.T) {
	r := NewRedactor(nil)
	raw := "https://bruce:" + fakePass + "@example.test/cave?apikey=" + fakeAPIKey + "&page=2&Token=" + fakeToken + "#frag"
	want := "https://example.test/cave?apikey=[REDACTED]&page=2&Token=[REDACTED]#frag"
	if got := r.URL(raw); got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string]slog.Attr{
		"url key":                 slog.String("url", raw),
		"referer key":             slog.String("referer", raw),
		"*url.URL value":          slog.Any("target", u),
		"url.URL value":           slog.Any("target", *u),
		"URL-shaped other key":    slog.String("location", raw),
		"URL inside a message":    slog.String("detail", `Get "`+raw+`": dial tcp 192.0.2.1:443: refused`),
		"error carrying a URL":    slog.Any("err", errors.New(`Get "`+raw+`": EOF`)),
		"semicolon query":         slog.String("url", "/x?page=1;token="+fakeToken),
		"percent-encoded name":    slog.String("url", "/x?%61pikey="+fakeAPIKey),
		"name decodes upper case": slog.String("url", "/x?%54OKEN="+fakeToken),
		"network-path reference":  slog.String("url", "//bruce:"+fakePass+"@example.test/x"),
		"url.query":               slog.String("url.query", "apikey="+fakeAPIKey+"&token="+fakeToken),
		"url.query with ?":        slog.String("url.query", "?apikey="+fakeAPIKey),
		"query in fragment":       slog.String("url", "/login#token="+fakeToken),
		"key-listed query name":   slog.String("url", "/cb?client_secret="+fakePass),
	} {
		got := r.Attr(a)
		assertAbsent(t, name, got.Value.String(), fakePass, fakeAPIKey, fakeToken)
	}
	// A query name without a value, and harmless parameters, are kept as written.
	if got := r.URL("/books?token&sort=title%20asc"); got != "/books?token&sort=title%20asc" {
		t.Errorf("URL changed a harmless query: %q", got)
	}
	text, js := logBoth(t, r, slog.String("url", raw))
	for where, out := range map[string]string{"text": text, "json": js} {
		assertAbsent(t, where, out, fakePass, fakeAPIKey, fakeToken)
	}
}

// fakeSecret stands in for secrets.Value, which core/secrets adds at F3: a
// value that redacts itself through slog.LogValuer. When secrets lands, a
// secrets.Value cell joins this test.
type fakeSecret string

func (fakeSecret) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

func TestRedact_SecretsValue(t *testing.T) {
	r := NewRedactor(nil)
	s := fakeSecret(fakeToken)
	if got := r.Attr(slog.Any("batcomputer", s)); got.Value.String() != "[REDACTED]" {
		t.Errorf("Attr(LogValuer) = %v, want [REDACTED]", got)
	}
	text, js := logBoth(t, r, slog.Any("batcomputer", s), slog.Group("g", slog.Any("nested", s)))
	for where, out := range map[string]string{"text": text, "json": js} {
		assertAbsent(t, where, out, fakeToken)
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s: LogValuer output missing: %s", where, out)
		}
	}
}

func TestRedact_CoreInvitePath(t *testing.T) {
	r := NewRedactor(nil)
	core := "/api/core/v1/auth/invites/" + fakeInvite
	page := "/invite/" + fakeInvite
	var attrs []slog.Attr
	for _, p := range []string{core, page} {
		attrs = append(attrs,
			slog.String("path", p),
			slog.String("url.path", p),
			slog.String("url.full", "https://example.test"+p+"?ref=mail"),
			slog.String("url", "https://example.test"+strings.Replace(p, "/"+fakeInvite, "%2F"+fakeInvite, 1)),
			slog.String("http.target", strings.Replace(p, "invite", "%69nvite", 1)),
			slog.String("note", "GET "+p),
		)
	}
	for _, a := range attrs {
		if got := r.Attr(a).Value.String(); strings.Contains(got, fakeInvite) {
			t.Errorf("Attr(%s=%q) = %q still carries the token", a.Key, a.Value.String(), got)
		}
	}
	if got := r.Path(core); got != "/api/core/v1/auth/invites/[REDACTED]" {
		t.Errorf("Path(core route) = %q", got)
	}
	text, js := logBoth(t, r, attrs...)
	for where, out := range map[string]string{"text": text, "json": js} {
		assertAbsent(t, where, out, fakeInvite)
	}
}

// credentialQueryKeysAtHEAD is a fixture copy of HEAD's credentialQueryKeys
// (internal/server/middleware/log_redact.go).
var credentialQueryKeysAtHEAD = []string{
	"token", "access_token", "refresh_token", "api_key", "apikey", "key", "password", "secret", "code", "code_verifier",
}

func TestRedactQuery_SupersetOfHEAD(t *testing.T) {
	q := DefaultRedactQuery()
	for _, k := range credentialQueryKeysAtHEAD {
		if !slices.Contains(q, k) {
			t.Errorf("DefaultRedactQuery() lacks HEAD's %q", k)
		}
	}
	r := NewRedactor(nil)
	in := "/api/core/v1/auth/oidc/callback?code=" + fakeCode + "&code_verifier=" + fakeVerify +
		"&refresh_token=" + fakeRefresh + "&SECRET=" + fakePass + "&state=batsignal"
	want := "/api/core/v1/auth/oidc/callback?code=[REDACTED]&code_verifier=[REDACTED]&refresh_token=[REDACTED]&SECRET=[REDACTED]&state=batsignal"
	if got := r.URL(in); got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
	for _, k := range q {
		if got := r.URL("/x?" + k + "=" + fakeToken); strings.Contains(got, fakeToken) {
			t.Errorf("query %q not redacted: %q", k, got)
		}
	}
}

func TestRedact_TempLoginTokenQuery(t *testing.T) {
	r := NewRedactor(nil)
	in := "/api/core/v1/auth/temp-login?token=" + fakeToken
	want := "/api/core/v1/auth/temp-login?token=[REDACTED]"
	for name, got := range map[string]string{
		"URL":         r.URL(in),
		"http.target": r.String("http.target", in),
		"request_uri": r.Attr(slog.String("request_uri", in)).Value.String(),
	} {
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestRedact_PathRuleFailsClosed(t *testing.T) {
	r := NewRedactor(nil)
	long := "/invite/" + fakeInvite + strings.Repeat("a", maxRedactPathBytes)
	for name, in := range map[string]string{
		"over-long":                 long,
		"over-long, not invite":     "/books/" + strings.Repeat("a", maxRedactPathBytes),
		"over-escaped":              "/invite%2525252F" + fakeInvite,
		"over-escaped route name":   "/%2525252569nvite/" + fakeInvite,
		"dot-walked":                "/x/../invite/" + fakeInvite + "/..",
		"dot-walked invites prefix": "/api/v1/users/q/../invites/" + fakeInvite + "/..",
		"dot-walked core route":     "/api/core/v1/q/../auth/invites/" + fakeInvite + "/..",
	} {
		if got := r.Path(in); got != "/[REDACTED]" {
			t.Errorf("%s: Path = %.60q, want /[REDACTED]", name, got)
		}
		if got := r.String("url.path", in); !strings.HasPrefix(got, "/[REDACTED]") {
			t.Errorf("%s: String(url.path) = %.60q, want /[REDACTED]", name, got)
		}
		if got := r.URL("https://example.test" + in + "?a=b"); strings.Contains(got, fakeInvite) {
			t.Errorf("%s: URL kept the token: %.80q", name, got)
		}
	}
	// A module rule that panics fails closed too, and never crashes the caller.
	pr := NewRedactor(nil, PathRule{Name: "joker", Fn: func(string) string { panic("ha") }})
	if got := pr.Path("/books/1"); got != "/[REDACTED]" {
		t.Errorf("panicking rule: Path = %q, want /[REDACTED]", got)
	}
	// A rule with no function is ignored rather than called.
	if got := NewRedactor(nil, PathRule{Name: "empty"}).Path("/books/1"); got != "/books/1" {
		t.Errorf("nil-Fn rule: Path = %q, want unchanged", got)
	}
}

// A "?" or "#" in a decoded path is path text: the token behind it is still
// redacted under a path key.
func TestRedact_DecodedDelimitersInPath(t *testing.T) {
	r := NewRedactor(nil)
	for _, in := range []string{
		"/invite/?" + fakeInvite,
		"/invite/#" + fakeInvite,
		"/api/core/v1/auth/invites/?" + fakeInvite,
	} {
		for _, k := range []string{"path", "url.path"} {
			if got := r.String(k, in); strings.Contains(got, fakeInvite) {
				t.Errorf("String(%s, %q) = %q", k, in, got)
			}
		}
	}
	if got := r.String("path", "/books/1/cover?token="+fakeToken); strings.Contains(got, fakeToken) {
		t.Errorf("credential after a decoded ? in a path kept: %q", got)
	}
}

func TestRedactor_SpanAttributes(t *testing.T) {
	r := NewRedactor(nil)
	for _, tc := range []struct{ key, in string }{
		{"url.full", "https://example.test/api/v1/books?apikey=" + fakeAPIKey + "&token=" + fakeToken},
		{"url.query", "apikey=" + fakeAPIKey + "&token=" + fakeToken},
		{"http.target", "/api/v1/books/1/cover?apikey=" + fakeAPIKey + "&token=" + fakeToken},
		{"http.url", "http://example.test/x?token=" + fakeToken},
	} {
		got := r.String(tc.key, tc.in)
		assertAbsent(t, tc.key, got, fakeAPIKey, fakeToken)
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("String(%s) = %q, want a [REDACTED] marker", tc.key, got)
		}
	}
}

type panickyValuer struct{}

func (panickyValuer) LogValue() slog.Value { panic("the Joker was here") }

type loopValuer struct{}

func (l loopValuer) LogValue() slog.Value { return slog.AnyValue(l) }

type panickyError struct{}

func (panickyError) Error() string { panic("error text panicked") }

func TestRedact_PanickingValuerFailsOpen(t *testing.T) {
	r := NewRedactor(nil)
	for name, a := range map[string]slog.Attr{
		"panicking LogValue":  slog.Any("v", panickyValuer{}),
		"endless LogValuer":   slog.Any("v", loopValuer{}),
		"panicking Error":     slog.Any("v", panickyError{}),
		"nil *url.URL":        slog.Any("v", (*url.URL)(nil)),
		"in a group":          slog.Group("g", slog.Any("v", panickyValuer{})),
		"deny-listed and bad": slog.Any("token", panickyValuer{}),
	} {
		got := r.Attr(a)
		v := got.Value
		if v.Kind() == slog.KindGroup {
			v = v.Group()[0].Value
		}
		switch name {
		case "nil *url.URL":
			if v.Kind() == slog.KindString && strings.Contains(v.String(), "panic") {
				t.Errorf("%s: nil pointer treated as a panic: %v", name, got)
			}
		default:
			if v.String() != "[REDACTED:panic]" {
				t.Errorf("%s: Attr = %v, want [REDACTED:panic]", name, got)
			}
		}
	}
	text, js := logBoth(t, r, slog.Any("v", panickyValuer{}))
	for where, out := range map[string]string{"text": text, "json": js} {
		if !strings.Contains(out, "[REDACTED:panic]") || strings.Contains(out, "Joker") {
			t.Errorf("%s: %s", where, out)
		}
	}
}

func TestRedactor_StringKinds(t *testing.T) {
	r := NewRedactor(nil)
	for _, tc := range []struct {
		key, in string
		want    RedactionKind
	}{
		{"password", fakePass, RedactKey},
		{"password", "[REDACTED]", 0},
		{"url.full", "https://bruce:" + fakePass + "@example.test/", RedactURL},
		{"url.full", "https://example.test/x?token=" + fakeToken, RedactURL},
		{"url.path", "/invite/" + fakeInvite, RedactPath},
		{"url.full", "https://example.test/invite/" + fakeInvite + "?token=" + fakeToken, RedactURL | RedactPath},
		{"", "GET /api/core/v1/auth/invites/" + fakeInvite, RedactPath},
		{"title", "Batman: Hush", 0},
		{"url.full", "https://example.test/x?token=[REDACTED]", 0},
	} {
		out, got := r.StringKinds(tc.key, tc.in)
		if got != tc.want {
			t.Errorf("StringKinds(%q, %q) kinds = %v, want %v (out %q)", tc.key, tc.in, got, tc.want, out)
		}
		if s := r.String(tc.key, tc.in); s != out {
			t.Errorf("String and StringKinds disagree for %q: %q vs %q", tc.in, s, out)
		}
	}
	if got := (RedactKey | RedactPath).String(); got != "key|path" {
		t.Errorf("RedactionKind.String = %q", got)
	}
	if got := RedactValuer.String(); got != "valuer" {
		t.Errorf("RedactValuer.String = %q", got)
	}
}

// The Redactor is immutable after NewRedactor: many goroutines can share one
// (run under -race).
func TestRedactor_ConcurrentUse(t *testing.T) {
	r := NewRedactor([]string{"batarang"}, PathRule{Name: "cave", Fn: func(p string) string { return p }})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			for range 200 {
				_ = r.Attr(slog.Group("g", slog.String("token", fakeToken), slog.String("url", "/invite/"+fakeInvite)))
				if got := r.URL("https://example.test/x?apikey=" + fakeAPIKey); strings.Contains(got, fakeAPIKey) {
					t.Errorf("goroutine %d: %q", i, got)
					return
				}
				_ = r.Path("/api/core/v1/auth/invites/" + fakeInvite)
				_, _ = r.StringKinds("batarang", "x")
			}
		})
	}
	wg.Wait()
}

// Review item 1: credential names beyond the spec's default list. The
// exported DefaultRedactKeys stays the spec's text; the Redactor also denies
// ABO's credential headers and any key ending in a credential suffix.
func TestRedact_CredentialHeadersAndSuffixes(t *testing.T) {
	r := NewRedactor(nil)
	for _, k := range []string{
		"http.request.header.x-api-key", "X-Api-Key", "Proxy-Authorization", "Cf-Access-Jwt-Assertion",
		"Cf-Access-Client-Secret", "http.request.header.cf-access-jwt-assertion",
		"http.request.header.proxy-authorization", "csrf_token", "bootstrap_token", "audible.refresh_token",
		"webhook_secret", "smtp_password", "bearer", "id_token", "signing-privatekey",
	} {
		if got := r.String(k, fakeToken); got != "[REDACTED]" {
			t.Errorf("String(%q) = %q, want [REDACTED]", k, got)
		}
		if got := r.Attr(slog.String(k, fakeToken)); got.Value.String() != "[REDACTED]" {
			t.Errorf("Attr(%q) = %v, want [REDACTED]", k, got)
		}
	}
	for _, k := range []string{"has_password", "hasToken", "tokens_used", "token_count",
		"cache_key", "title", "author", "url.path", "route", "response", "nonce", "cnonce", "http.response"} {
		if got := r.String(k, "Batman: Hush"); got != "Batman: Hush" {
			t.Errorf("String(%q) redacted a harmless key: %q", k, got)
		}
	}
	// An exact deny-list entry wins over a harmless suffix.
	if got := NewRedactor([]string{"has_password"}).String("has_password", "true"); got != "[REDACTED]" {
		t.Errorf("extra key has_password = %q, want [REDACTED]", got)
	}
}

// Review item 2: a URL or path inside a token, after a quote, a bracket, '='
// or ':', and credential pairs in free text.
func TestRedact_TextScanForms(t *testing.T) {
	r := NewRedactor(nil)
	core := "/api/core/v1/auth/invites/" + fakeInvite
	for _, in := range []string{
		`"` + core + `"`,
		`'` + core + `'`,
		"`" + core + "`",
		"<" + core + ">",
		"(see " + core + ")",
		"[" + core + "]",
		"{" + core + "}",
		"next=" + core,
		"next=" + core + "?r=https://example.test/",
		"url:" + core,
		`redirect to "` + core + `".`,
		`location="/invite/` + fakeInvite + `"`,
		"<https://example.test" + core + ">",
		`Get "https://example.test` + core + `": EOF`,
	} {
		for _, k := range []string{"detail", ""} {
			if got := r.String(k, in); strings.Contains(got, fakeInvite) {
				t.Errorf("String(%q, %q) = %q still carries the token", k, in, got)
			}
		}
	}
	// A URL or path inside a query value.
	for _, in := range []string{
		"/login?next=" + core,
		"https://example.test/cb?redirect_uri=https://example.test/x?token=" + fakeToken,
		"/login?next=/x%3Fy&page=1&return=" + core,
	} {
		got := r.String("url", in)
		assertAbsent(t, "nested "+in, got, fakeInvite, fakeToken)
	}
	// Credential pairs and bearer values in free text.
	for _, in := range []string{
		"token=" + fakeToken,
		"login failed: password:" + fakePass,
		"Authorization: Bearer " + fakeToken,
		"Proxy-Authorization: Basic " + fakeToken,
		"cookie: session=" + fakeToken,
		"Set-Cookie: theme=dark; sid=" + fakeToken + "; Path=/",
		"token: " + fakeToken,
		"auth bearer " + fakeToken,
		"x-api-key=" + fakeAPIKey + ", retrying",
		"{Name:bruce password:" + fakePass + " Title:Hush}",
		`client_secret="` + fakePass + `"`,
	} {
		got, k := r.StringKinds("detail", in)
		assertAbsent(t, in, got, fakeToken, fakePass, fakeAPIKey)
		if k&RedactKey == 0 {
			t.Errorf("StringKinds(%q) kinds = %v, want key", in, k)
		}
	}
	for _, in := range []string{"Batman: Year One", "ratio=1:2", "/api/v1/books/7", "10:47:00", "https://example.test:8443/a"} {
		if got := r.String("detail", in); got != in {
			t.Errorf("String(detail, %q) = %q, want unchanged", in, got)
		}
	}
}

type caseFile struct {
	Name     string
	Password string
	URL      string
	Key      string `json:"api_key"`
	Skipped  string `json:"-"`
	notes    string
}

type batNode struct {
	Next  *batNode
	Token string
	Title string
}

type stringerURL struct{}

func (stringerURL) String() string { return "GET /invite/" + fakeInvite }

type unexportedSecret struct {
	Title    string
	password string
}

// Review item 3: KindAny containers are walked, bounded, and their printed
// text is scanned.
func TestRedact_AnyContainers(t *testing.T) {
	r := NewRedactor(nil)
	loop := &batNode{Title: "Hush", Token: fakeToken}
	loop.Next = loop
	big := make([]int, maxAnyNodes+10)
	cases := map[string]any{
		"map[string]any": map[string]any{"token": fakeToken, "user": "bruce",
			"nested": map[string]any{"password": fakePass, "link": "/invite/" + fakeInvite}},
		"map[string]string": map[string]string{"apikey": fakeAPIKey, "user": "bruce"},
		"http.Header": http.Header{"Authorization": {"Bearer " + fakeToken}, "X-Api-Key": {fakeAPIKey},
			"Accept": {"*/*"}},
		"struct": caseFile{Name: "bruce", Password: fakePass, URL: "https://example.test/x?token=" + fakeToken,
			Key: fakeAPIKey, Skipped: "x", notes: "n"},
		"pointer to struct":  &caseFile{Name: "bruce", Password: fakePass},
		"[]string":           []string{"/invite/" + fakeInvite, "bruce"},
		"[]any":              []any{map[string]any{"secret": fakePass}, "bruce"},
		"cyclic":             loop,
		"stringer":           stringerURL{},
		"unexported field":   unexportedSecret{Title: "Hush", password: fakePass},
		"too many nodes":     big,
		"map with int keys":  map[int]string{1: "token=" + fakeToken},
		"named string value": url.Values{"token": {fakeToken}},
	}
	for name, v := range cases {
		got := r.Attr(slog.Any("v", v))
		text, js := logBoth(t, r, slog.Any("v", v))
		for where, out := range map[string]string{"attr": got.Value.String(), "text": text, "json": js} {
			assertAbsent(t, name+" "+where, out, fakeToken, fakePass, fakeAPIKey, fakeInvite)
		}
	}
	// A value with nothing to redact is passed on as it was.
	plain := caseFile{Name: "bruce", URL: "https://example.test/books/7"}
	if got := r.Attr(slog.Any("v", plain)); got.Value.Kind() != slog.KindAny || got.Value.Any() != any(plain) {
		t.Errorf("harmless struct changed: %v", got)
	}
	// Harmless branches of a redacted value keep their original values.
	got := r.Attr(slog.Any("v", map[string]any{"token": fakeToken, "user": "bruce", "n": 7}))
	m, ok := got.Value.Any().(map[string]any)
	if !ok || m["user"] != "bruce" || m["n"] != 7 || m["token"] != "[REDACTED]" {
		t.Errorf("rebuilt map = %#v", got.Value.Any())
	}
}

type pinnedCredential struct {
	// The Go name is denied; the json name alone would not be.
	Password string `json:"p"`
	Title    string `json:"title"`
}

type textCredential struct{}

func (textCredential) MarshalText() ([]byte, error) { return []byte("token=" + fakeToken), nil }

type hiddenURL struct {
	Title string
	link  *url.URL
}

// Second review pass: opaque leaves inside the walk, the Go name of a struct
// field, and percent-encoded or depth-limited nested URLs.
func TestRedact_WalkOpaqueLeavesAndNestedURLs(t *testing.T) {
	r := NewRedactor(nil)
	u, err := url.Parse("https://example.test/x?apikey=" + fakeAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{
		"error in a map":        map[string]any{"cause": errors.New("GET /invite/" + fakeInvite + " failed")},
		"Stringer in a map":     map[string]any{"req": stringerURL{}},
		"TextMarshaler in a []": []any{textCredential{}},
		"*url.URL in a struct":  struct{ Link *url.URL }{u},
		"url.URL in a map":      map[string]url.URL{"link": *u},
		"Go name denied":        pinnedCredential{Password: fakePass, Title: "Hush"},
		"int-keyed map":         map[int]any{1: errors.New("token=" + fakeToken)},
		"[]byte in a map":       map[string]any{"body": []byte("password=" + fakePass)},
		"unexported field":      map[string]any{"h": hiddenURL{Title: "Hush", link: u}},
	} {
		text, js := logBoth(t, r, slog.Any("v", v))
		got := r.Attr(slog.Any("v", v))
		for where, out := range map[string]string{"attr": got.Value.String(), "text": text, "json": js} {
			assertAbsent(t, name+" "+where, out, fakeToken, fakePass, fakeAPIKey, fakeInvite)
		}
	}
	if got := r.Attr(slog.Any("v", pinnedCredential{Password: fakePass, Title: "Hush"})); got.Value.Any().(map[string]any)["p"] != "[REDACTED]" {
		t.Errorf("Go-name-denied field = %v", got)
	}

	// A percent-encoded invite path in next= is decoded and the whole value
	// replaced.
	enc := "/login?next=%2Fapi%2Fcore%2Fv1%2Fauth%2Finvites%2F" + fakeInvite + "&page=2"
	if got := r.URL(enc); got != "/login?next=[REDACTED]&page=2" {
		t.Errorf("URL(encoded next) = %q", got)
	}
	// An invalid escape does not hide one.
	if got := r.URL("/login?next=%2Finvite%2F" + fakeInvite + "%zz"); strings.Contains(got, fakeInvite) {
		t.Errorf("URL(next with a bad escape) = %q", got)
	}
	// At the depth limit a value that still holds a URL or path fails closed.
	deep := "/a?n=/b?n=/c?n=/d"
	if got := r.URL(deep); got != "/a?n=/b?n=/c?n=[REDACTED]" {
		t.Errorf("URL(%q) = %q, want the deepest value redacted", deep, got)
	}
	if got := r.URL("/a?n=/b?n=/c?n=https://example.test/x"); !strings.HasSuffix(got, "n=[REDACTED]") {
		t.Errorf("URL at the depth limit = %q", got)
	}
	// Below the limit a plain nested path is kept.
	if got := r.URL("/a?next=/books/7"); got != "/a?next=/books/7" {
		t.Errorf("URL(harmless next) = %q", got)
	}
}

// Third review pass: the scheme words without '=' or ':', a padded value
// after a spaced separator, and the whole Digest credential.
func TestRedact_AuthSchemesAndPadding(t *testing.T) {
	r := NewRedactor(nil)
	for _, in := range []string{
		"Basic " + fakeToken,
		`Digest username="bruce", response="` + fakeToken + `"`,
		`WWW-Authenticate: Digest realm="gotham", nonce="` + fakeToken + `"`,
		"Negotiate " + fakeToken,
		"BEARER " + fakeToken,
		"token: " + fakeToken + "==",
		"token: " + fakeToken + "=",
		"password: https://" + fakePass,
		`Authorization: Digest username="bruce", response="` + fakeToken + `", nonce="` + fakePass + `", cnonce="` + fakeAPIKey + `"`,
		`header "Bearer ` + fakeToken + `" rejected`,
	} {
		got, k := r.StringKinds("detail", in)
		assertAbsent(t, in, got, fakeToken, fakePass, fakeAPIKey)
		if k&RedactKey == 0 {
			t.Errorf("StringKinds(%q) kinds = %v, want key", in, k)
		}
	}
	// The closing quote ends the credential; text after it stays.
	if got := r.String("detail", `header "Bearer `+fakeToken+`" rejected`); got != `header "Bearer [REDACTED]" rejected` {
		t.Errorf("quoted bearer = %q", got)
	}
	// Digest's credential parameters are redacted inside a Digest credential
	// only: as attribute keys they are ordinary words (review M8).
	for _, k := range []string{"response", "nonce", "cnonce"} {
		if got := r.String(k, "200 OK"); got != "200 OK" {
			t.Errorf("String(%q) = %q, want unchanged", k, got)
		}
	}
	// An empty field in %+v output is still read as the next pair.
	if got := r.String("detail", "{Password: Title:Hush}"); got != "{Password: Title:Hush}" {
		t.Errorf("empty %%+v field = %q", got)
	}
}

// assertUnchanged fails when StringKinds changes in or reports a kind.
func assertUnchanged(t *testing.T, r *Redactor, key, in string) {
	t.Helper()
	if got, k := r.StringKinds(key, in); got != in || k != 0 {
		t.Errorf("StringKinds(%q, %q) = %q (%v), want unchanged", key, in, got, k)
	}
}

// Independent review B1: JSON and quoted values in strings, []byte, errors
// and json.RawMessage; a quoted value ends at its matching quote, not at the
// first space.
func TestRedact_JSONAndQuotedValues(t *testing.T) {
	r := NewRedactor(nil)
	for _, in := range []string{
		`{"access_token":"` + fakeToken + `","token_type":"bearer"}`,
		`{"password": "` + fakePass + `"}`,
		`upstream said: {"api_key":"` + fakeAPIKey + `"}`,
		`password="correct horse ` + fakePass + `"`,
		`password='correct horse ` + fakePass + `'`,
		`body="{\"refresh_token\":\"` + fakeRefresh + `\"}"`,
		`{"password" : "a \"quoted\" ` + fakePass + `"}`,
	} {
		got, k := r.StringKinds("msg", in)
		assertAbsent(t, in, got, fakeToken, fakePass, fakeAPIKey, fakeRefresh)
		if k&RedactKey == 0 {
			t.Errorf("StringKinds(%q) kinds = %v, want key", in, k)
		}
	}
	// The value ends at its closing quote: what follows stays.
	if got := r.String("msg", `{"access_token":"`+fakeToken+`","token_type":"bearer"}`); got != `{"access_token":"[REDACTED]","token_type":"bearer"}` {
		t.Errorf("JSON body = %q", got)
	}
	if got := r.String("msg", `password="correct horse `+fakePass+`" user=bruce`); got != `password="[REDACTED]" user=bruce` {
		t.Errorf("quoted value = %q", got)
	}
	for name, v := range map[string]any{
		"json.RawMessage":    json.RawMessage(`{"access_token":"` + fakeToken + `"}`),
		"RawMessage in map":  map[string]any{"body": json.RawMessage(`{"password":"` + fakePass + `"}`)},
		"[]byte":             []byte(`{"api_key":"` + fakeAPIKey + `"}`),
		"wrapped JSON error": fmt.Errorf("exchange: %s", `{"refresh_token":"`+fakeRefresh+`"}`),
		"joined errors":      errors.Join(errors.New("a"), errors.New("password="+fakePass)),
	} {
		text, js := logBoth(t, r, slog.Any("v", v))
		for where, out := range map[string]string{"text": text, "json": js} {
			assertAbsent(t, name+" "+where, out, fakeToken, fakePass, fakeAPIKey, fakeRefresh)
		}
	}
}

// Independent review M1: the pair scan is one linear pass. Each input is a
// unit repeated to 1 MiB; the step count (every byte any loop reads) must
// stay within a small multiple of the length, where the old scan read to
// the end of the line once per occurrence.
func TestRedact_PairScanIsLinear(t *testing.T) {
	r := NewRedactor(nil)
	const size = 1 << 20
	for _, unit := range []string{
		"cookie=", "bearer a ", "password: ", "/a?x=1&", `password:"[REDACTED `, "Authorization: Basic Basic ",
		"password: password: ", `"token":"`, "token=[", "a=1&token=x;",
	} {
		s := strings.Repeat(unit, size/len(unit))
		steps := 0
		r.scanPairsN(s, &steps)
		if steps > 8*len(s) {
			t.Errorf("scanPairsN(%q x %d): %d steps for %d bytes, want <= %d", unit, size/len(unit), steps, len(s), 8*len(s))
		}
	}
	// Past maxRedactTextBytes the text is not examined: it is replaced.
	big := "title=Hush " + strings.Repeat("cookie=", size/7)
	for _, key := range []string{"msg", "url", "url.query"} {
		got, k := r.StringKinds(key, big)
		if !strings.HasSuffix(got, "[REDACTED]") || len(got) > maxRedactTextBytes+len("[REDACTED]") || k&RedactKey == 0 {
			t.Errorf("StringKinds(%q, 1 MiB) = %d bytes (%v), want at most %d ending in [REDACTED]", key, len(got), k, maxRedactTextBytes+len("[REDACTED]"))
		}
	}
	if got := r.URL("/a?x=" + strings.Repeat("y", size)); len(got) > maxRedactTextBytes+len("[REDACTED]") {
		t.Errorf("URL(1 MiB) = %d bytes", len(got))
	}
}

// basicBruce is a Basic credential: base64 of "bruce:" and the fake password.
const basicBruce = "YnJ1Y2U6cHdfdGVzdF9hbGZyZWRfMDAw"

// Independent review M3: Basic, Digest and Negotiate need a credential-shaped
// value, stricter outside header context; Bearer fires anywhere.
func TestRedact_AuthSchemeContext(t *testing.T) {
	r := NewRedactor(nil)
	for _, c := range []struct{ key, in string }{
		{"title", "Basic Economics: A Citizen's Guide"},
		{"title", "How to Negotiate Anything"},
		{"op", "digest rollup for 2026-10-01"},
		{"status", "failed to compute digest of /lib/a.m4b"},
		{"msg", "basic auth failed for bruce"},
		{"title", "Negotiate Anything"},
		{"msg", `response="200 OK"`},
		{"msg", "Response:{Code:200 Body:ok}"},
		{"msg", "negotiate a deal with the Penguin"},
		{"msg", "we negotiate terms before the heist"},
		{"msg", "a basic setup"},
	} {
		assertUnchanged(t, r, c.key, c.in)
	}
	for _, in := range []string{
		"Basic " + fakeToken,
		"Negotiate YIIGhgYJKoZIhvcSAQICAQBuggZ1MIIGcaADAgEF",
		`"Basic ` + fakeToken + `"`,
		"WWW-Authenticate: Negotiate " + fakeToken,
		"http.request.header.authorization=Basic " + fakeToken,
		`Digest username="bruce", response="` + fakeToken + `"`,
		"proxy-authenticate: Digest realm=\"gotham\", nonce=\"" + fakeToken + "\"",
		"the bearer " + fakeToken,
		"auth Bearer " + fakeToken,
		// Security review: outside header context, Basic redacts a value that
		// base64-decodes to user:password, and Negotiate and Digest a token68
		// of at least 20 characters.
		"sent Basic " + basicBruce,
		"upstream rejected Basic " + basicBruce + ", retrying",
		"server sent Negotiate YIIGhgYJKoZIhvcSAQICAQBuggZ1MIIGcaADAgEF",
		"got Digest " + fakeToken + fakeToken,
	} {
		got, k := r.StringKinds("msg", in)
		assertAbsent(t, in, got, fakeToken, "YIIGhgYJ", basicBruce)
		if k&RedactKey == 0 {
			t.Errorf("StringKinds(%q) kinds = %v, want key", in, k)
		}
	}
	// The scheme word stays.
	if got := r.String("msg", "Basic "+fakeToken); got != "Basic [REDACTED]" {
		t.Errorf("Basic credential = %q", got)
	}
}

// Independent review M4: the %v of an http.Header, url.Values or
// map[string][]string, and a bracketed or braced value.
func TestRedact_BracketedValues(t *testing.T) {
	r := NewRedactor(nil)
	for _, in := range []string{
		"map[token:[" + fakeToken + "]]",
		"headers=map[Authorization:[Bearer " + fakeToken + "] X-Api-Key:[" + fakeAPIKey + "]]",
		fmt.Sprint(http.Header{"X-Api-Key": {fakeAPIKey}, "Accept": {"*/*"}}),
		fmt.Sprint(url.Values{"token": {fakeToken, "second " + fakeToken}}),
		fmt.Sprintf("%v", map[string][]string{"password": {"correct horse " + fakePass}}),
		"token={" + fakeToken + "}",
		"token=(" + fakeToken + ")",
	} {
		got, k := r.StringKinds("msg", in)
		assertAbsent(t, in, got, fakeToken, fakeAPIKey, fakePass)
		if k&RedactKey == 0 {
			t.Errorf("StringKinds(%q) kinds = %v, want key", in, k)
		}
	}
	if got := r.String("msg", "map[Accept:[*/*] token:["+fakeToken+"]]"); got != "map[Accept:[*/*] token:[REDACTED]]" {
		t.Errorf("header map = %q", got)
	}
	// Only the literal [REDACTED prefix is skipped, so a second pass changes
	// nothing.
	for _, in := range []string{"map[token:[REDACTED]]", "token=[REDACTED:panic]", `{"password":"[REDACTED]"}`, "Bearer [REDACTED]"} {
		assertUnchanged(t, r, "msg", in)
	}
}

type batVault struct{ combination string }

func (batVault) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

type caveConfig struct {
	Owner string
	Vault batVault
}

type caveHidden struct {
	Owner string
	vault batVault
}

type groupValuer struct{}

func (groupValuer) LogValue() slog.Value {
	return slog.GroupValue(slog.String("user", "bruce"), slog.String("password", fakePass))
}

// Independent review M5: a LogValuer nested in a struct, map or slice is
// resolved (the handlers print its fields otherwise); one in an unexported
// field cannot be called and fails closed.
func TestRedact_NestedLogValuer(t *testing.T) {
	r := NewRedactor(nil)
	for name, v := range map[string]any{
		"exported field":   caveConfig{Owner: "bruce", Vault: batVault{combination: fakePass}},
		"unexported field": caveHidden{Owner: "bruce", vault: batVault{combination: fakePass}},
		"map value":        map[string]any{"vault": batVault{combination: fakePass}},
		"slice element":    []any{batVault{combination: fakePass}},
		"group valuer":     map[string]any{"login": groupValuer{}},
		"panicking valuer": map[string]any{"v": panickyValuer{}},
	} {
		text, js := logBoth(t, r, slog.Any("v", v))
		for where, out := range map[string]string{"text": text, "json": js} {
			assertAbsent(t, name+" "+where, out, fakePass)
		}
	}
	got := r.Attr(slog.Any("v", caveConfig{Owner: "bruce", Vault: batVault{combination: fakePass}}))
	if m, ok := got.Value.Any().(map[string]any); !ok || m["Vault"] != "[REDACTED]" || m["Owner"] != "bruce" {
		t.Errorf("nested LogValuer = %#v", got.Value.Any())
	}
}

// leakyText prints a harmless String but a credential through MarshalText
// and MarshalJSON, which the TextHandler and JSONHandler prefer.
type leakyText struct{ v string }

func (leakyText) String() string                 { return "benign" }
func (l leakyText) MarshalText() ([]byte, error) { return []byte("token=" + l.v), nil }
func (l leakyText) MarshalJSON() ([]byte, error) { return []byte(`{"token":"` + l.v + `"}`), nil }

type leakyAppender struct{ v string }

func (l leakyAppender) AppendText(b []byte) ([]byte, error) {
	return append(b, "password="+l.v...), nil
}

// leakyJSONError prints a harmless Error but a credential through
// MarshalJSON, which the JSONHandler prefers over Error.
type leakyJSONError struct{ v string }

func (leakyJSONError) Error() string { return "upstream refused" }
func (l leakyJSONError) MarshalJSON() ([]byte, error) {
	return []byte(`{"api_key":"` + l.v + `"}`), nil
}

type failingText struct{ v string }

func (f failingText) MarshalText() ([]byte, error) {
	return nil, errors.New("cannot marshal secret=" + f.v)
}

// Independent review M6: every text a handler can print is scanned
// (AppendText, MarshalText, MarshalJSON, Error, String, and a marshal
// error's text).
func TestRedact_EveryPrintedText(t *testing.T) {
	r := NewRedactor(nil)
	for name, v := range map[string]any{
		"MarshalText and MarshalJSON": leakyText{v: fakeToken},
		"AppendText":                  leakyAppender{v: fakePass},
		"MarshalJSON over Error":      leakyJSONError{v: fakeAPIKey},
		"marshal error":               failingText{v: fakePass},
		"nested in a map":             map[string]any{"x": leakyText{v: fakeToken}},
	} {
		text, js := logBoth(t, r, slog.Any("v", v))
		got := r.Attr(slog.Any("v", v))
		for where, out := range map[string]string{"attr": got.Value.String(), "text": text, "json": js} {
			assertAbsent(t, name+" "+where, out, fakeToken, fakePass, fakeAPIKey)
		}
	}
}

// Independent review M7: plurals and further credential names.
func TestRedact_PluralAndNamedCredentialKeys(t *testing.T) {
	r := NewRedactor(nil)
	for _, k := range []string{"cookies", "tokens", "secrets", "passwords", "secret_key", "aws_secret_access_key",
		"AccessKey", "passphrase", "jwt", "id_jwt"} {
		if got := r.String(k, fakeToken); got != "[REDACTED]" {
			t.Errorf("String(%q) = %q, want [REDACTED]", k, got)
		}
		if got := r.String("msg", k+"="+fakeToken); strings.Contains(got, fakeToken) {
			t.Errorf("text %s= = %q", k, got)
		}
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "abo_session="+fakeToken+"; theme=dark")
	text, js := logBoth(t, r, slog.Any("cookies", req.Cookies()), slog.Any("secrets", []string{fakePass}))
	for where, out := range map[string]string{"text": text, "json": js} {
		assertAbsent(t, where, out, fakeToken, fakePass)
	}
}

type settingRow struct {
	Key         string
	IsSecret    bool
	Response    int
	Token       string
	PinPassword int
	MaxTokens   int
}

// Independent review M8: a credential suffix alone never redacts a bool or
// a number (presence flags and counts); an exact deny-list key still does.
func TestRedact_ScalarsUnderSuffixKeys(t *testing.T) {
	r := NewRedactor(nil)
	for _, a := range []slog.Attr{
		slog.Bool("cf_cookie", true), slog.Bool("cf_authorization", false), slog.Bool("cf_client_secret", true),
		slog.Bool("query_token", false), slog.Bool("is_secret", true), slog.Int("prompt_tokens", 12),
		slog.Int("cf_cookie_len", 40), slog.Float64("max_tokens", 2048), slog.Int("response", 200),
		slog.Uint64("completion_tokens", 7), slog.Duration("token_ttl", 0),
	} {
		if got, k := r.attr(a); !got.Equal(a) || k != 0 {
			t.Errorf("attr(%v) = %v (%v), want unchanged", a, got, k)
		}
	}
	// Security review: a number under a suffix match that is not count-shaped
	// may be a numeric credential.
	for _, a := range []slog.Attr{slog.Bool("password", true), slog.Int("token", 5), slog.Int("session_id", 9),
		slog.String("is_secret", "false"), slog.Any("cf_cookie", []string{"x"}), slog.Int("pin_password", 1234),
		slog.Int("user_secret", 9876), slog.Float64("api_token", 3), slog.Uint64("session_token", 5),
		slog.Any("vault_passwd", 4321)} {
		if got := r.Attr(a); got.Value.String() != "[REDACTED]" {
			t.Errorf("Attr(%v) = %v, want [REDACTED]", a, got)
		}
	}
	got := r.Attr(slog.Any("row", settingRow{Key: "smtp", IsSecret: true, Response: 200, Token: fakeToken,
		PinPassword: 1234, MaxTokens: 2048}))
	m, ok := got.Value.Any().(map[string]any)
	if !ok || m["IsSecret"] != true || m["Response"] != 200 || m["Token"] != "[REDACTED]" ||
		m["PinPassword"] != "[REDACTED]" || m["MaxTokens"] != 2048 {
		t.Errorf("settings row = %#v", got.Value.Any())
	}
}

// Independent review M9: a double-encoded nested URL is decoded up to
// maxDecodeRounds times, and a value that still decodes after the last round
// fails closed.
func TestRedact_MultiplyEncodedNestedURL(t *testing.T) {
	r := NewRedactor(nil)
	for _, in := range []string{
		"https://example.test/p?next=%252Finvite%252F" + fakeInvite,
		"https://example.test/p?next=https%253A%252F%252Fbruce%253A" + fakePass + "%2540example.test",
		"/p?next=%25252Finvite%25252F" + fakeInvite,
	} {
		got := r.URL(in)
		assertAbsent(t, in, got, fakeInvite, fakePass)
		if !strings.HasSuffix(got, "next=[REDACTED]") {
			t.Errorf("URL(%q) = %q, want next=[REDACTED]", in, got)
		}
	}
	// Four layers: still decodable after three rounds, so it fails closed
	// whatever it hides (nestedValue, or the invite rule's whole-rest pass
	// first, which fails closed on the same depth).
	if got := r.URL("/p?next=%2525252Fbooks%2525252F7"); strings.Contains(got, "books") || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("URL(four layers) = %q", got)
	}
	if got, _ := r.nestedValue("%2525252Fbooks%2525252F7", 0); got != "[REDACTED]" {
		t.Errorf("nestedValue(four layers) = %q", got)
	}
	// One harmless layer is kept.
	if got := r.URL("/p?next=%2Fbooks%2F7&page=2"); got != "/p?next=%2Fbooks%2F7&page=2" {
		t.Errorf("URL(harmless encoded next) = %q", got)
	}
}

// Independent review MINORs and the probe's URL rows: spaces around '=', a
// newline after ':', tails after ',' and '&', the text scan of url.query, a
// password holding an unencoded '/' or '#' in userinfo, and an encoded
// invite path in free text.
func TestRedact_PairAndURLEdgeForms(t *testing.T) {
	r := NewRedactor(nil)
	for _, c := range []struct{ key, in string }{
		{"msg", "password = " + fakePass},
		{"msg", "password :" + fakePass},
		{"msg", "password:\n  " + fakePass},
		{"msg", "password=ab," + fakePass},
		{"msg", "password=ab&" + fakePass},
		{"msg", "password=ab;" + fakePass},
		// Security review: a doubled separator after a denied name.
		{"msg", "password==" + fakePass},
		{"msg", "password::" + fakePass},
		{"msg", "token == " + fakePass},
		{"url.query", "q=Bearer " + fakePass},
		{"url", "https://bruce:pa/" + fakePass + "@example.test/x"},
		{"url", "https://bruce:pa#" + fakePass + "@example.test/x"},
		{"msg", "see https://bruce:pa?" + fakePass + "@example.test/x"},
		{"msg", "%2Finvite%2F" + fakePass},
		{"msg", "open %252Fapi%252Fcore%252Fv1%252Fauth%252Finvites%252F" + fakePass + ": not found"},
	} {
		got, k := r.StringKinds(c.key, c.in)
		assertAbsent(t, c.key+" "+c.in, got, fakePass)
		if k == 0 {
			t.Errorf("StringKinds(%q, %q) reported no kind", c.key, c.in)
		}
	}
	if got := r.URL("https://bruce:pa/" + fakePass + "@example.test/x?page=2"); got != "https://example.test/x?page=2" {
		t.Errorf("userinfo with '/' = %q", got)
	}
	for _, c := range []struct{ key, in string }{
		{"url", "https://example.test:8443/a/b@c"},
		{"msg", "a == b"},
		{"msg", "progress 50% of /library/a.m4b"},
		{"msg", "%2Flibrary%2FBatman%2FHush.m4b"},
		{"url.query", "page=2&sort=title"},
		{"url", "/a?x=1&&y=2;;z"},
	} {
		assertUnchanged(t, r, c.key, c.in)
	}
	// maskQuery keeps empty segments and a trailing separator.
	if got := r.URL("/a?token=" + fakeToken + ";&page=2&"); got != "/a?token=[REDACTED];&page=2&" {
		t.Errorf("URL(separators) = %q", got)
	}
}
