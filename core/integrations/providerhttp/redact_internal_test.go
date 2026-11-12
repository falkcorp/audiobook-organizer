// file: core/integrations/providerhttp/redact_internal_test.go
// version: 1.1.0
// guid: 6f1fdc2e-3b2b-4a8b-a3a5-2d8f9e0b7d11
// last-edited: 2026-11-11

package providerhttp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// TestScrubText_Leaks (M2): every form of credential the security review found
// in an error body, a transport error or a log line is replaced.
func TestScrubText_Leaks(t *testing.T) {
	const s = "SECRETvalue"
	for _, tc := range []struct{ name, in string }{
		{"authorization bearer", "Authorization: Bearer " + s + ".part-two"},
		{"authorization basic", "Authorization: Basic " + s},
		{"authorization lower case", "authorization=Bearer " + s},
		{"proxy authorization", "Proxy-Authorization: Basic " + s},
		{"authorization json", `{"Authorization": "Bearer ` + s + `"}`},
		{"bare bearer", "invalid credentials: Bearer " + s + " expired"},
		{"refresh_token", "refresh_token=" + s + "&x=1"},
		{"id_token", "id_token=" + s},
		{"access-token", "access-token=" + s},
		{"client_secret json", `{"client_secret":"` + s + `"}`},
		{"single quoted key", "{'key': '" + s + "'}"},
		{"unquoted json value", `{"key": ` + s + `}`},
		{"x-plex-token", "X-Plex-Token: " + s},
		{"x-plex-token query", "?X-Plex-Token=" + s},
		{"session", "session=" + s},
		{"jsessionid", "JSESSIONID=" + s},
		{"passwd", "passwd=" + s},
		{"pwd", "pwd=" + s},
		{"password spaced", "password = " + s},
		{"signature", "X-Amz-Signature=" + s},
		{"sig", "sig=" + s},
		{"set-cookie", "Set-Cookie: sid=" + s},
		{"apikey", "apikey=" + s},
		{"API_KEY", "API_KEY=" + s},
		{"oauth", "oauth=" + s},
		{"userinfo in text", "Get \"https://alfred:" + s + "@example.test/x\": dial"},
		{"long name", strings.Repeat("a", 300) + "key=" + s},
		{"long name session", strings.Repeat("a", 300) + "session=" + s},
		{"x-custom adapter name", "gotham_pin=" + s},
		{"authorization other scheme", "Authorization: Negotiate " + s + "AB"},
		{"auth header scheme and token", "X-Auth: Token " + s},
	} {
		got := newScrubber([]string{"gotham_pin"}).text(tc.in)
		if strings.Contains(got, s) {
			t.Errorf("%s: %q still holds the secret: %q", tc.name, tc.in, got)
		}
		if !strings.Contains(got, redacted) {
			t.Errorf("%s: %q has no mark: %q", tc.name, tc.in, got)
		}
	}
}

// TestScrubText_KeepsWhatIsNotACredential: a book library's own field names
// (author, narrator) and ordinary prose survive.
func TestScrubText_KeepsWhatIsNotACredential(t *testing.T) {
	for _, in := range []string{
		`{"author":"Bruce Wayne","authors":["A","B"],"narrator":"Alfred","title":"Year One"}`,
		"author=Bruce+Wayne&title=Year+One&narrator=Alfred&authority=Gotham",
		"the keyword was not found; monkey business; tokenize=true",
		"auth: not a name followed by nothing", // 'auth' IS a name: see below
	} {
		got := scrubber{}.text(in)
		if strings.HasPrefix(in, "auth:") {
			if !strings.Contains(got, redacted) {
				t.Errorf("auth: is a credential name: %q", got)
			}
			continue
		}
		if got != in {
			t.Errorf("%q was changed to %q", in, got)
		}
	}
}

// TestScrubText_StopsAtTheValue: what follows a value is kept.
func TestScrubText_StopsAtTheValue(t *testing.T) {
	got := scrubber{}.text("a=1&apikey=XYZ&page=2 and Authorization: Bearer ABC, next=3")
	if got != "a=1&apikey=REDACTED&page=2 and Authorization: REDACTED, next=3" {
		t.Fatalf("got %q", got)
	}
	if got := (scrubber{}).text(`{"token": "unterminated`); got != `{"token": "REDACTED` {
		t.Fatalf("unterminated quote: %q", got)
	}
}

// TestScrubURL_Leaks (M2): the query, userinfo, path and unparseable forms.
func TestScrubURL_Leaks(t *testing.T) {
	const s = "SECRETvalue"
	for _, tc := range []struct{ name, in, want string }{
		{"refresh_token", "https://h.test/x?q=1&refresh_token=" + s, "https://h.test/x?q=1&refresh_token=REDACTED"},
		{"x-plex-token", "https://h.test/x?X-Plex-Token=" + s, "https://h.test/x?X-Plex-Token=REDACTED"},
		{"encoded name", "https://h.test/x?api%5Fkey=" + s, "https://h.test/x?api%5Fkey=REDACTED"},
		{"valueless", "https://h.test/x?key&q=1", "https://h.test/x?key&q=1"},
		{"userinfo", "https://alfred:" + s + "@h.test/x", "https://REDACTED@h.test/x"},
		{"fragment", "https://h.test/x#access_token=" + s, "https://h.test/x"},
		{"bot token path", "https://h.test/bot123456:AAEhBOweik6ad9r_QXMENQjcrTu-oLrbSZ8/getMe", "https://h.test/bot123456:REDACTED/getMe"},
		{"bot token path no prefix", "https://h.test/123456:AAEhBOweik6ad9r_QXMENQjcrTu/x", "https://h.test/123456:REDACTED/x"},
		{"author survives", "https://h.test/x?author=Bruce&q=1", "https://h.test/x?author=Bruce&q=1"},
		{"time of day in path", "https://h.test/at/12:30:45/x", "https://h.test/at/12:30:45/x"},
		{"unparseable query", "http://alfred:p[ss@h.test/x?key=" + s, "http://h.test"},
		{"unparseable slash in password", "http://alfred:pa/ss[x@h.test/p?key=" + s, "http://h.test"},
		{"unparseable no scheme", "%zz?key=" + s, unparseableURL},
		{"unparseable odd host", "http://h t.test:[x/?key=" + s, unparseableURL},
		{"opaque", "mailto:alfred@h.test?key=" + s, "mailto:" + redacted},
	} {
		got := newScrubber(nil).url(tc.in)
		if got != tc.want {
			t.Errorf("%s: url(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if strings.Contains(got, s) {
			t.Errorf("%s: leaks %q", tc.name, got)
		}
	}
}

// TestScrubError_URLErrorAndChain: a *url.Error is rewritten and the chain
// survives.
func TestScrubError_Chain(t *testing.T) {
	inner := errors.New("boom: token=SECRETvalue")
	err := scrubber{}.err(inner)
	if strings.Contains(err.Error(), "SECRETvalue") || !errors.Is(err, inner) {
		t.Fatalf("err = %v", err)
	}
	if got := dropLiteral(inner, "SECRETvalue"); strings.Contains(got.Error(), "SECRETvalue") || !errors.Is(got, inner) {
		t.Fatalf("dropLiteral = %v", got)
	}
	if got := dropLiteral(inner, ""); got != inner {
		t.Fatal("an empty literal must change nothing")
	}
	if dropLiteral(nil, "x") != nil || (scrubber{}).err(nil) != nil {
		t.Fatal("nil in, nil out")
	}
}

// TestScrubSecretRules: the name rules, one by one.
func TestScrubSecretRules(t *testing.T) {
	s := newScrubber([]string{"X_Batcave_Pin"})
	for name, want := range map[string]bool{
		"key": true, "API_KEY": true, "api-key": true, "ApiKey": true, "x-api-key": true, "monkey": true,
		"token": true, "Access_Token": true, "refresh_token": true, "X-Plex-Token": true,
		"secret": true, "client_secret": true, "password": true, "passwd": true, "pwd": true, "pass": true,
		"session": true, "sessionid": true, "JSESSIONID": true, "sig": true, "signature": true, "X-Amz-Signature": true,
		"credential": true, "X-Amz-Credential": true, "sig_v4": true, "sigv4": true, "authorization": true, "Proxy-Authorization": true,
		"auth": true, "oauth": true, "cookie": true, "Set-Cookie": true, "sid": true,
		"x-batcave-pin": true, "x_batcave_pin": true,
		"author": false, "authors": false, "authority": false, "narrator": false, "title": false, "q": false,
		"keyword": false, "page": false, "": false, "tokenize": false, "Content-Type": false, "User-Agent": false,
	} {
		if got := s.secret(name); got != want {
			t.Errorf("secret(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestScrub_HostileBodyIsBounded (M2): 16 MiB of every shape that was
// quadratic or slow costs next to nothing through the excerpt path, and the
// scrubber itself is linear on the raw input.
func TestScrub_HostileBodyIsBounded(t *testing.T) {
	const size = 16 << 20
	shapes := map[string]string{
		"name run":            strings.Repeat("a", size),
		"key=":                strings.Repeat("key=", size/4),
		"quotes":              strings.Repeat(`"`, size),
		"names, no value":     strings.Repeat("token ", size/6),
		"separators":          strings.Repeat("a-", size/2),
		"long names":          strings.Repeat(strings.Repeat("a", 200)+"=", size/201),
		"authorization lines": strings.Repeat("Authorization: x\n", size/17),
		"unterminated quote":  `{"key": "` + strings.Repeat("z", size),
		"bearers":             strings.Repeat("Bearer ", size/7),
		"urls":                strings.Repeat("http://a:b@c ", size/13),
	}
	for name, body := range shapes {
		start := time.Now()
		got := scrubber{}.bounded(body, integrationsMaxDetail)
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Errorf("%s: bounded took %v, want < 50ms", name, d)
		}
		if len(got) > integrationsMaxDetail {
			t.Errorf("%s: %d bytes, over the bound", name, len(got))
		}
	}
	for name, body := range shapes {
		start := time.Now()
		_ = scrubber{}.text(body[:len(body)/4])
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("%s: text took %v on 16 MiB: not linear", name, d)
		}
	}
}

const integrationsMaxDetail = integrations.MaxDetail

// TestBounded_NoFragmentOfASecret (M2): cut, scrub, cut again. A secret that
// starts before the cut and ends after it leaves no fragment, and the result is
// valid UTF-8 within the bound.
func TestBounded_NoFragmentOfASecret(t *testing.T) {
	const max = 64
	secret := "SECRETSECRETSECRETSECRET"
	for pad := range 4*max + 300 {
		in := strings.Repeat("é", 1) + strings.Repeat("x ", pad/2) + "apikey=" + secret + " tail"
		got := scrubber{}.bounded(in, max)
		if strings.Contains(got, "SECRE") || len(got) > max {
			t.Fatalf("pad %d: %q", pad, got)
		}
		for _, r := range got {
			if r == '�' {
				t.Fatalf("pad %d: invalid UTF-8 in %q", pad, got)
			}
		}
	}
	if got := (scrubber{}).bounded("ünï", 4); got != "ün" {
		t.Fatalf("a cut inside a rune must back off: %q", got)
	}
	if got := (scrubber{}).bounded("a\xffb", 10); got != "ab" {
		t.Fatalf("invalid bytes dropped: %q", got)
	}
}

// TestIsBlockedIP_Table (N11): every range the security review named, in both
// columns: refused, and never reachable by an instance trusted for private
// networks (except the ordinary private ranges, which are).
func TestIsBlockedIP_Table(t *testing.T) {
	type row struct {
		ip        string
		blocked   bool
		bypassOK  bool // reachable under AllowPrivate
		comment   string
		skipMixed bool
	}
	rows := []row{
		// ordinary private space: blocked, but a trusted instance reaches it.
		{"127.0.0.1", true, true, "loopback", false},
		{"::1", true, true, "loopback v6", false},
		{"10.1.1.1", true, true, "rfc1918", false},
		{"172.31.255.254", true, true, "rfc1918", false},
		{"192.168.1.1", true, true, "rfc1918", false},
		{"100.64.0.1", true, true, "cgnat", false},
		{"fd12::1", true, true, "ula", false},
		{"0.0.0.0", true, true, "unspecified", false},
		{"::ffff:127.0.0.1", true, true, "mapped loopback", false},
		// never reachable.
		{"169.254.169.254", true, false, "link-local metadata", false},
		{"::ffff:169.254.169.254", true, false, "mapped link-local", false},
		{"fe80::1", true, false, "link-local v6", false},
		{"168.63.129.16", true, false, "azure wire server", false},
		{"100.100.100.200", true, false, "alibaba metadata, inside cgnat", false},
		{"fd00:ec2::254", true, false, "aws v6 metadata, inside ula", false},
		{"::127.0.0.1", true, false, "ipv4-compatible loopback", false},
		{"::7f00:1", true, false, "ipv4-compatible loopback", false},
		{"64:ff9b::7f00:1", true, false, "nat64", false},
		{"64:ff9b:1::7f00:1", true, false, "local-use nat64", false},
		{"::ffff:0:7f00:1", true, false, "siit", false},
		{"fec0::1", true, false, "site-local", false},
		{"2001::1", true, false, "teredo", false},
		{"100::1", true, false, "discard", false},
		{"2001:db8::1", true, false, "documentation", false},
		{"2002:7f00:1::", true, false, "6to4", false},
		{"ff02::1", true, false, "multicast", false},
		{"224.0.0.1", true, false, "multicast", false},
		{"240.0.0.1", true, false, "reserved", false},
		{"198.18.0.1", true, false, "benchmarking", false},
		{"192.0.0.170", true, false, "ietf assignments", false},
		{"0.1.2.3", true, false, "this network", false},
		// public (and the documentation stand-ins).
		{"192.0.2.10", false, false, "test-net-1", false},
		{"198.51.100.7", false, false, "test-net-2", false},
		{"203.0.113.9", false, false, "test-net-3", false},
		{"8.8.8.8", false, false, "public", false},
		{"168.63.129.17", false, false, "next to the wire server", false},
		{"100.63.255.255", false, false, "just below cgnat", false},
		{"100.128.0.1", false, false, "just above cgnat", false},
		{"2606:4700:4700::1111", false, false, "public v6", false},
		{"2001:4860:4860::8888", false, false, "public v6 next to teredo", false},
		{"2001:1::1", false, false, "outside teredo /32", false},
		{"100:0:0:1::1", false, false, "outside the discard /64", false},
	}
	strict, trusted := &Dialer{}, &Dialer{AllowPrivate: true}
	for _, r := range rows {
		ip := net.ParseIP(r.ip)
		if ip == nil {
			t.Fatalf("bad row %q", r.ip)
		}
		if got := IsBlockedIP(ip); got != r.blocked {
			t.Errorf("IsBlockedIP(%s) [%s] = %v, want %v", r.ip, r.comment, got, r.blocked)
		}
		if got := strict.blocked(ip); got != r.blocked {
			t.Errorf("strict dial of %s [%s]: blocked = %v, want %v", r.ip, r.comment, got, r.blocked)
		}
		if got := !trusted.blocked(ip); got != (r.bypassOK || !r.blocked) {
			t.Errorf("trusted dial of %s [%s]: reachable = %v, want %v", r.ip, r.comment, got, r.bypassOK || !r.blocked)
		}
	}
	if !IsBlockedIP(nil) || !IsBlockedIP(net.IP{1, 2, 3}) {
		t.Error("a nil or malformed address must be blocked")
	}
	// The guard built by newGuard parses the table once and agrees.
	g := newGuard(false, nil, nil, nil)
	if len(g.nets) == 0 || !g.blocked(net.ParseIP("168.63.129.16")) || g.blocked(net.ParseIP("8.8.8.8")) {
		t.Error("newGuard must carry the parsed table")
	}
}

// TestDialRefusal_NamesNoAddress (N12): the error says only that the
// destination is not allowed; the address goes to a debug log.
func TestDialRefusal_NamesNoAddress(t *testing.T) {
	var buf lockedBuf
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	for name, ips := range map[string][]net.IP{
		"blocked":   {net.ParseIP("10.9.8.7")},
		"mixed":     {net.ParseIP("192.0.2.1"), net.ParseIP("10.9.8.7")},
		"no answer": nil,
	} {
		d := newGuard(false, func(context.Context, string) ([]net.IP, error) { return ips, nil },
			func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("must not dial") }, log)
		_, err := d.DialContext(context.Background(), "tcp", "batcave.example.test:80")
		if !errors.Is(err, ErrBlockedAddress) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if strings.Contains(err.Error(), "10.9.8.7") || strings.Contains(err.Error(), "batcave") {
			t.Errorf("%s: the refusal names what it resolved: %v", name, err)
		}
	}
	if !strings.Contains(buf.String(), "10.9.8.7") || !strings.Contains(buf.String(), "batcave.example.test") {
		t.Errorf("the debug log must carry the detail: %q", buf.String())
	}
	// A nil Log is allowed.
	d := newGuard(false, func(context.Context, string) ([]net.IP, error) { return nil, nil }, nil, nil)
	if _, err := d.DialContext(context.Background(), "tcp", "x.test:80"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatal(err)
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestWaitFor_NeverOverflows (NIT): the wait of a reservation is a positive
// duration at most maxBucketWait, whatever the rate.
func TestWaitFor_NeverOverflows(t *testing.T) {
	for _, tc := range []struct {
		owing, rps float64
		want       time.Duration
	}{
		{1, 2, 500 * time.Millisecond},
		{1, 1, time.Second},
		{1e-12, 1000, 1}, // never zero: a taker that owes must wait
		{0, 1, 1},        // nothing owed rounds to the minimum too
		{-3, 1, 1},       // so does a negative debt
		{1, 1e-300, maxBucketWait},
		{1e18, 1, maxBucketWait},
		{1, 0, maxBucketWait},
		{1, -1, maxBucketWait},
		{1, math.NaN(), maxBucketWait},
		{math.Inf(1), 1, maxBucketWait},
		{1, 1.0 / 86400, maxBucketWait}, // exactly the floor: a day, clamped to the cap
	} {
		if got := waitFor(tc.owing, tc.rps); got != tc.want {
			t.Errorf("waitFor(%g, %g) = %v, want %v", tc.owing, tc.rps, got, tc.want)
		}
	}
}

// TestNormalizeLimits_RPSFloor (NIT).
func TestNormalizeLimits_RPSFloor(t *testing.T) {
	set := Settings{}.withDefaults()
	for _, rps := range []float64{1e-300, 1e-9, minRPS / 2} {
		if got := normalizeLimits(integrations.Limits{RPS: rps}, set).RPS; got != minRPS {
			t.Errorf("RPS %g normalised to %g, want the floor %g", rps, got, minRPS)
		}
	}
	if got := normalizeLimits(integrations.Limits{RPS: 5}, set).RPS; got != 5 {
		t.Errorf("RPS 5 became %g", got)
	}
	if got := normalizeLimits(integrations.Limits{RPS: -1}, set).RPS; got != defaultRPS {
		t.Errorf("a negative RPS must take the fallback, got %g", got)
	}
	tiny := Settings{Fallback: integrations.Limits{RPS: 1e-300}}.withDefaults()
	if got := normalizeLimits(integrations.Limits{}, tiny).RPS; got != minRPS {
		t.Errorf("a tiny fallback must be floored too, got %g", got)
	}
}
