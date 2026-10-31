// file: internal/metadata/detail_redact_test.go
// version: 1.1.1
// guid: c23eadf0-b924-43fd-86a4-fea8ab6291c9
// last-edited: 2026-10-30

package metadata

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestDetailForError_URLErrorQueryValuesRedacted(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://books.example.test/v1/volumes?key=abc&q=batman", Err: errors.New("EOF")}
	got := DetailForError(err)
	want := `Get "https://books.example.test/v1/volumes?***": EOF`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDetailForError_WrappedURLErrorKeepsPrefix(t *testing.T) {
	inner := &url.Error{Op: "Get", URL: "https://books.example.test/x?key=abc", Err: errors.New("EOF")}
	got := DetailForError(fmt.Errorf("googlebooks: %w", inner))
	if strings.Contains(got, "abc") || !strings.HasPrefix(got, "googlebooks: Get ") || !strings.Contains(got, "?***") {
		t.Fatalf("got %q", got)
	}
}

func TestRedactProviderDetail(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"userinfo dropped", "Get https://robin:hunter2@host.example.test/p?a=1", "Get https://***@host.example.test/p?***"},
		{"password with slash", "https://robin:pa/ss@host.example.test/p", "https://***@host.example.test/p"},
		{"password with question mark", "https://robin:pa?ss@host.example.test/p", "https://***"},
		{"password with hash", "https://robin:pa#ss@host.example.test/p", "https://***"},
		{"password with at", "https://robin:p@ss@host.example.test/p", "https://***@host.example.test/p"},
		{"password with all four", "https://robin:a/b?c#d@e@host.example.test/p?k=v", "https://***"},
		{"at in query collapses token", "https://h/p?key=abc@x&t=secret", "https://***"},
		{"userinfo then query", "https://u:p@h/p?x=1", "https://***@h/p?***"},
		{"at in fragment after query", "https://h/p?x=1#f@g", "https://***"},
		{"at after hash", "https://h/p#f@g", "https://***"},
		{"at after hash then query", "https://h/p#f@g?k=v", "https://***"},
		{"bare pairs without a URL", "retry later ?token=abc&key=def&q=batman", "retry later ?token=***&key=***&q=batman"},
		{"fragment dropped", "see https://host.example.test/p#access_token=zzz", "see https://host.example.test/p"},
		{"fragment before query dropped", "https://host.example.test/p#frag?key=zzz", "https://host.example.test/p"},
		{"query replaced wholesale", "https://host.example.test/a/b/c?key=abc&q=batman&odd;x", "https://host.example.test/a/b/c?***"},
		{"nameless query value", "https://host.example.test/p?SECRET", "https://host.example.test/p?***"},
		{"no query untouched", "https://host.example.test/a/b", "https://host.example.test/a/b"},
		{"trailing punctuation kept outside", "failed (https://h.example.test/p?key=abc).", "failed (https://h.example.test/p?***)."},
		{"bare api_key", "bad request api_key=xyz", "bad request api_key=***"},
		{"bare mixed case and prefix", "Client_Secret=s3 and TOKEN=t4 apikey=k5", "Client_Secret=*** and TOKEN=*** apikey=***"},
		{"malformed escape still redacted", "https://h.example.test/p?key=%zz&q=1", "https://h.example.test/p?***"},
		{"non-URL text unchanged", "rate limit exceeded; retry in 30s (monkey=1)", "rate limit exceeded; retry in 30s (monkey=1)"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactProviderDetail(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetailForError_NilAndPlain(t *testing.T) {
	if DetailForError(nil) != "" {
		t.Fatal("nil error must render empty")
	}
	if got := DetailForError(errors.New("connection reset")); got != "connection reset" {
		t.Fatalf("got %q", got)
	}
}

// Redaction runs before truncation, so a cut can never leave half a secret.
func TestRegistry_RecordFailureRedactsBeforeTruncating(t *testing.T) {
	secret := "SECRETVALUE123456"
	// The secret straddles the 300-byte cut: a truncate-first order would keep
	// its leading characters.
	u := "https://books.example.test/" + strings.Repeat("p", 251) + "?q=batman&key=" + secret + "&tail=" + strings.Repeat("z", 100)
	err := &url.Error{Op: "Get", URL: u, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}

	r := NewThrottleRegistry()
	got, ok := r.RecordFailure("google-books", err)
	if !ok {
		t.Fatal("transport failure should install a hold")
	}
	if strings.Contains(got.Detail, "SECR") || strings.Contains(got.Detail, secret) {
		t.Fatalf("secret fragment survived in %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "?***") {
		t.Fatalf("expected the redacted query in %q", got.Detail)
	}
}

func TestRegistry_AttachStoreScrubsLegacyDetail(t *testing.T) {
	store := newFakeThrottleStore()
	seed := NewThrottleRegistry()
	if _, err := seed.AttachStore(store); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	seed.RecordFailure("hardcover", statusErrorFor(t, "hardcover", 503, "down", nil))
	raw, _ := store.LoadProviderThrottles()
	var row ProviderThrottle
	if err := jsonUnmarshalForTest(raw["hardcover"], &row); err != nil {
		t.Fatal(err)
	}
	row.Detail = `Get "https://h.example.test/p?key=LEAKED": EOF`
	_ = store.SaveProviderThrottle("hardcover", jsonMarshalForTest(t, row))

	restarted := NewThrottleRegistry()
	if _, err := restarted.AttachStore(store); err != nil {
		t.Fatal(err)
	}
	got, _ := restarted.Get("hardcover")
	if strings.Contains(got.Detail, "LEAKED") {
		t.Fatalf("legacy row served its secret: %q", got.Detail)
	}
}

// A *url.Error wrapped for redaction must classify exactly as the original:
// ClassifyProviderError walks the chain with errors.AsType, never the text.
func TestRedactProviderError_ClassificationUnchanged(t *testing.T) {
	const u = "https://books.example.test/v1?key=LEAKED"
	cases := []struct {
		name string
		err  error
	}{
		{"transport", &url.Error{Op: "Get", URL: u, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}}},
		{"wrapped transport", fmt.Errorf("search: %w", &url.Error{Op: "Get", URL: u, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}})},
		{"our deadline", &url.Error{Op: "Get", URL: u, Err: context.DeadlineExceeded}},
		{"our cancel", &url.Error{Op: "Get", URL: u, Err: context.Canceled}},
		{"not a provider fault", &url.Error{Op: "Get", URL: u, Err: errors.New("bad body")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			red := RedactProviderError(tc.err)
			if strings.Contains(red.Error(), "LEAKED") {
				t.Fatalf("message leaks: %q", red.Error())
			}
			if _, ok := errors.AsType[*url.Error](red); !ok {
				t.Fatal("errors.As lost the *url.Error")
			}
			wr, wh, wok := ClassifyProviderError(tc.err)
			gr, gh, gok := ClassifyProviderError(red)
			if wr != gr || wh != gh || wok != gok {
				t.Fatalf("classification changed: (%v,%v,%v) -> (%v,%v,%v)", wr, wh, wok, gr, gh, gok)
			}
			if errors.Is(tc.err, context.Canceled) != errors.Is(red, context.Canceled) ||
				errors.Is(tc.err, context.DeadlineExceeded) != errors.Is(red, context.DeadlineExceeded) {
				t.Fatal("errors.Is changed")
			}
		})
	}
	plain := errors.New("boom")
	if RedactProviderError(plain) != plain {
		t.Fatal("an error with nothing to redact must be returned as-is")
	}
	if RedactProviderError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

// Every error leaving a chain-built source is redacted, and the hold is still
// recorded, because the registry classifies the unredacted original.
func TestProtectedSource_ReturnsRedactedErrorAndStillThrottles(t *testing.T) {
	resetDefaultThrottles(t)
	stub := &stubSource{id: "google-books", err: &url.Error{
		Op: "Get", URL: "https://books.example.test/v1?key=LEAKED",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")},
	}}
	ps := NewChainSource(stub)
	_, err := ps.SearchByTitle(context.Background(), "dune")
	if err == nil || strings.Contains(err.Error(), "LEAKED") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Fatal("the *url.Error is no longer reachable with errors.As")
	}
	if !DefaultThrottleRegistry().Throttled("google-books") {
		t.Fatal("a transport failure did not install a hold")
	}
	if h, _ := DefaultThrottleRegistry().Get("google-books"); strings.Contains(h.Detail, "LEAKED") {
		t.Fatalf("hold detail leaks: %q", h.Detail)
	}
}
