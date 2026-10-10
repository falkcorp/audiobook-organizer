// file: internal/telemetry/endpoint_fuzz_test.go
// version: 1.1.0
// guid: 3f8a1c64-9d27-4e50-b6a3-0c71e5d9f284
// last-edited: 2026-10-10

package telemetry

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// sentinel can never appear in an accepted endpoint: '_' is not valid in a
// host, a port, a scheme or an IPv6 address, so an accepted Display() that
// contains it is a leak.
const sentinel = "SENTINEL_SECRET"

// canonicalRE is the only shape Display() may have.
var canonicalRE = regexp.MustCompile(`^((https?://|dns:///)?)(([A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?)|\[[0-9a-f:.]+\]):[1-9][0-9]{0,4}$`)

func wantRefusal(key string) string {
	return key + " is not a valid OTLP/gRPC endpoint (expected host:port, http(s)://host:port or dns:///host:port)"
}

// endpointSeeds: every example from review rounds 1-4, with the sentinel in
// each slot (userinfo, path, query, fragment, port, scheme, IPv6 brackets),
// plus the accepted forms.
func endpointSeeds() []string {
	S := sentinel
	return []string{
		// accepted
		"127.0.0.1:4317", "localhost:4317", "[::1]:4317", "tempo:4317", "192.0.2.10:4317",
		"http://127.0.0.1:4317", "https://collector.example.invalid:4317", "https://collector.example.invalid:4317/",
		"dns:///tempo:4317", " http://127.0.0.1:4317 ", "HTTPS://Collector:4317", "http://[2001:db8::1]:4317",
		"https://your-otel-collector:4317", "collector:04317",
		// userinfo
		"https://user:" + S + "@collector:4317", "dns:///user:" + S + "@192.0.2.1:4317", S + "@collector:4317",
		"user:" + S + "@192.0.2.1:4317", "https://u:pa/" + S + "@h:4317", "https://u:p@" + S + "@h:4317",
		"http://u:pa#" + S + "@h:4317", "dns:///u:pa?" + S + "@h:4317", "https://" + S + "@collector",
		// path
		"collector:4317/" + S, "https://collector/v1/" + S, "https://collector:4317/" + S, "https://collector:4317/p/" + S,
		"https://collector:4317//", "https://collector:4317/path@" + S, "grpc://collector:4317/" + S, "dns:///collector:4317/" + S,
		"dns://collector:4317/" + S, "http://collector:4317/./" + S,
		// query / fragment
		"https://collector:4317?tok=" + S, "https://collector:4317/?api_key=" + S, "https://collector:4317#token=" + S,
		"https://collector:4317?tok=a@" + S, "collector:4317?" + S, "collector:4317#" + S, "dns:///collector:4317?k=" + S + "#f=" + S,
		// port
		"collector:" + S, "collector:abc", "collector:0", "collector:70000", "collector:", "collector", "https://collector",
		"https://collector:" + S, "collector:-1", "collector:4317:4317", ":4317", "http://:4317",
		// scheme
		S + "://collector:4317", "ftp://collector:21", "invalid://endpoint", "://x", "dns:/collector:4317", "dns:///", "dns://",
		"ht tp://collector:4317", "http//collector:4317",
		// IPv6 brackets
		"[" + S + "]:4317", "https://[" + S + "]:4317", "[::1" + S + "]:4317", "[::1%" + S + "]:4317", "[fe80::1%eth0]:4317",
		"[::1]", "[::1]:", "[192.0.2.1]:4317", "[::1]x:4317",
		// whitespace, control, unicode, encoding
		"collector :4317", "collector:4317 " + S, "collector\n:4317", "collector\x00:4317", "colléctor:4317",
		// hosts that gRPC would read as a resolver scheme or a number
		"unix:4317", "dns:4317", "https://dns:443", "passthrough:4317", "unix-abstract:4317", "http://unix:4317",
		"4317:4317", "1.2.3:4317", "300.1.1.1:4317", "a..b:4317", "a.-b:4317", strings.Repeat("a", 64) + ":4317",
		"https://collector%2f" + S + ":4317", "https://collector:4317/%2e%2e/" + S, "",
	}
}

// checkEndpoint is the invariant that ends the strip-and-preserve loop: for
// any input and either config key, the endpoint is REFUSED with exactly the
// fixed string for that key, or ACCEPTED as a canonical rebuild that holds no
// sentinel, is what gets dialled, and is stable when parsed again.
func checkEndpoint(t *testing.T, in string) {
	t.Helper()
	for _, key := range []string{keyTraceEndpoint, keyMetricsEndpoint} {
		tgt, err := parseOTLPEndpoint(key, in)
		if err != nil {
			if err.Error() != wantRefusal(key) {
				t.Fatalf("%q (%s): error %q is not the fixed string", in, key, err)
			}
			if tgt.Display() != "" {
				t.Fatalf("%q (%s): refused but Display() = %q", in, key, tgt.Display())
			}
			continue
		}
		disp := tgt.Display()
		if !canonicalRE.MatchString(disp) {
			t.Fatalf("%q (%s): Display() %q is not canonical", in, key, disp)
		}
		if strings.Contains(disp, sentinel) {
			t.Fatalf("%q (%s): Display() %q holds the sentinel", in, key, disp)
		}
		// What is dialled is what is displayed.
		dial := tgt.Target
		if tgt.URL != "" {
			dial = tgt.URL
		}
		if dial != disp {
			t.Fatalf("%q (%s): dial target %q != Display() %q", in, key, dial, disp)
		}
		// gRPC must read the explicit target as exactly dns:///host:port.
		hostPort := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(disp, "https://"), "http://"), "dns:///")
		if got := canonicalGRPCTarget(t, tgt.GRPCTarget()); got != "dns:///"+hostPort {
			t.Fatalf("%q (%s): gRPC reads %q as %q, want dns:///%s", in, key, tgt.GRPCTarget(), got, hostPort)
		}
		again, err := parseOTLPEndpoint(key, disp)
		if err != nil || again != tgt {
			t.Fatalf("%q (%s): canonical %q does not parse back to itself (%+v, %v)", in, key, disp, again, err)
		}
		if tgt.URL != "" {
			if _, err := traceEndpointOption(in); key == keyTraceEndpoint && err != nil {
				t.Fatalf("%q: accepted by the parser but traceEndpointOption failed: %v", in, err)
			}
		}
	}
}

// canonicalGRPCTarget creates (it does not dial: grpc.NewClient connects on
// first use) and closes a client for target and returns how gRPC canonicalises
// it.
func canonicalGRPCTarget(t *testing.T, target string) string {
	t.Helper()
	cc, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient(%q): %v", target, err)
	}
	defer cc.Close()
	return cc.CanonicalTarget()
}

// checkNoLeak runs the same input through initSummary and the error handler
// and asserts the sentinel reaches no message or attribute.
func checkNoLeak(t *testing.T, in string) {
	t.Helper()
	cfg := LoadConfig("t", in, WithMetricsOTLP(in, time.Minute, false))
	_, terr := parseOTLPEndpoint(keyTraceEndpoint, in)
	_, merr := parseOTLPEndpoint(keyMetricsEndpoint, in)
	var flat []string
	_, msg, attrs := initSummary(cfg, terr == nil, terr, otlpStatus{Enabled: merr == nil, Err: merr})
	flat = append(flat, msg)
	for _, a := range attrs {
		flat = append(flat, fmt.Sprint(a))
	}
	// The error handler sees third-party text that quotes the dial target,
	// which is only ever the canonical form.
	h := newRateLimitedErrorHandler(time.Minute, time.Now, func(_ slog.Level, m string, a ...any) {
		flat = append(flat, m)
		for _, x := range a {
			flat = append(flat, fmt.Sprint(x))
		}
	})
	if tgt, err := parseOTLPEndpoint(keyMetricsEndpoint, in); err == nil {
		h.Handle(errors.New("rpc error: code = Unavailable desc = dial " + tgt.Display() + ": connection refused"))
	}
	for _, f := range flat {
		if strings.Contains(f, sentinel) {
			t.Fatalf("%q: sentinel reached the log: %q", in, f)
		}
	}
}

func TestOTLPEndpoint_Table(t *testing.T) {
	accepted := map[string]string{
		"127.0.0.1:4317":                          "127.0.0.1:4317",
		"localhost:4317":                          "localhost:4317",
		"[::1]:4317":                              "[::1]:4317",
		"[2001:DB8:0::1]:4317":                    "[2001:db8::1]:4317",
		"tempo:4317":                              "tempo:4317",
		" http://127.0.0.1:4317 ":                 "http://127.0.0.1:4317",
		"http://127.0.0.1:4317/":                  "http://127.0.0.1:4317",
		"HTTPS://Collector:4317":                  "https://Collector:4317",
		"https://collector.example.invalid:4317/": "https://collector.example.invalid:4317",
		"https://[::1]:4317":                      "https://[::1]:4317",
		"dns:///tempo:4317":                       "dns:///tempo:4317",
		"DNS:///tempo:4317":                       "dns:///tempo:4317",
		"collector:04317":                         "collector:4317",
		"192.0.2.10:65535":                        "192.0.2.10:65535",
	}
	for in, want := range accepted {
		for _, key := range []string{keyTraceEndpoint, keyMetricsEndpoint} {
			tgt, err := parseOTLPEndpoint(key, in)
			if err != nil || tgt.Display() != want {
				t.Errorf("%q (%s): Display %q, err %v; want %q", in, key, tgt.Display(), err, want)
			}
		}
	}
	refused := []string{
		"", "127.0.0.1", "http://127.0.0.1", "http://:4317", ":4317", "ftp://host:21", "invalid://endpoint", "dns:///", "dns://x:4317",
		"collector:4317/x", "https://collector:4317/x", "https://collector:4317//", "https://collector:4317/?a=b", "https://collector:4317#f",
		"https://u:p@collector:4317", "u@collector:4317", "collector:4317?x", "collector:abc", "collector:0", "collector:65536",
		"collector:+1", "col lector:4317", "[::1%eth0]:4317", "[192.0.2.1]:4317", "-h:4317", "h-:4317", ".h:4317", "h.:4317", "4317:4317", "1.2.3:4317", "300.1.1.1:4317", "a..b:4317", "a.-b:4317", strings.Repeat("a", 64) + ":4317",
		"https://%7e:4317", "dns:///tempo:4317/", "dns:///u@tempo:4317", strings.Repeat("a", 254) + ":4317",
	}
	for _, in := range refused {
		for _, key := range []string{keyTraceEndpoint, keyMetricsEndpoint} {
			if _, err := parseOTLPEndpoint(key, in); err == nil || err.Error() != wantRefusal(key) {
				t.Errorf("%q (%s): err = %v, want the fixed refusal", in, key, err)
			}
		}
	}
	if _, err := parseOTLPEndpoint(keyTraceEndpoint, strings.Join([]string{strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63), strings.Repeat("d", 61)}, ".")+":4317"); err != nil {
		t.Errorf("a 253-byte host was refused: %v", err)
	}
}

// TestOTLPEndpoint_SeedCorpus runs every seed through the invariants without
// the fuzzer, so a plain `go test` enforces them.
func TestOTLPEndpoint_SeedCorpus(t *testing.T) {
	for _, in := range endpointSeeds() {
		checkEndpoint(t, in)
		checkNoLeak(t, in)
	}
}

func FuzzParseOTLPEndpoint(f *testing.F) {
	for _, in := range endpointSeeds() {
		f.Add(in)
	}
	f.Fuzz(func(t *testing.T, in string) {
		checkEndpoint(t, in)
		checkNoLeak(t, in)
	})
}

// The backstop for third-party error text only has to handle URL-shaped
// tokens: the sentinel in the userinfo, path, query and fragment slots.
func TestRedactEndpointSecrets_SentinelSlots(t *testing.T) {
	S := sentinel
	for _, in := range []string{
		"dial https://u:" + S + "@collector:4317: refused",
		"dial dns:///u:" + S + "@collector:4317: refused",
		"dial https://collector:4317/" + S + ": refused",
		"dial https://collector:4317?k=" + S + ": refused",
		"dial https://collector:4317#" + S + ": refused",
		"dial https://u:pa/" + S + "@collector:4317?k=" + S + "#" + S,
	} {
		if got := redactEndpointSecrets(in); strings.Contains(got, S) {
			t.Errorf("redactEndpointSecrets(%q) = %q, sentinel survived", in, got)
		}
	}
}
