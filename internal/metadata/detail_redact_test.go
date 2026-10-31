// file: internal/metadata/detail_redact_test.go
// version: 1.0.0
// guid: c23eadf0-b924-43fd-86a4-fea8ab6291c9
// last-edited: 2026-10-30

package metadata

import (
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
	want := `Get "https://books.example.test/v1/volumes?key=***&q=***": EOF`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDetailForError_WrappedURLErrorKeepsPrefix(t *testing.T) {
	inner := &url.Error{Op: "Get", URL: "https://books.example.test/x?key=abc", Err: errors.New("EOF")}
	got := DetailForError(fmt.Errorf("googlebooks: %w", inner))
	if strings.Contains(got, "abc") || !strings.HasPrefix(got, "googlebooks: Get ") || !strings.Contains(got, "key=***") {
		t.Fatalf("got %q", got)
	}
}

func TestRedactProviderDetail(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"userinfo dropped", "Get https://robin:hunter2@host.example.test/p?a=1", "Get https://host.example.test/p?a=***"},
		{"fragment dropped", "see https://host.example.test/p#access_token=zzz", "see https://host.example.test/p"},
		{"host and path kept", "https://host.example.test/a/b/c?key=abc", "https://host.example.test/a/b/c?key=***"},
		{"trailing punctuation kept outside", "failed (https://h.example.test/p?key=abc).", "failed (https://h.example.test/p?key=***)."},
		{"bare api_key", "bad request api_key=xyz", "bad request api_key=***"},
		{"bare mixed case and prefix", "Client_Secret=s3 and TOKEN=t4 apikey=k5", "Client_Secret=*** and TOKEN=*** apikey=***"},
		{"malformed escape still redacted", "https://h.example.test/p?key=%zz&q=1", "https://h.example.test/p?key=***&q=***"},
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
	if !strings.Contains(got.Detail, "key=***") {
		t.Fatalf("expected the redacted key in %q", got.Detail)
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
