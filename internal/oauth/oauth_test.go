// file: internal/oauth/oauth_test.go
// version: 1.0.0
// guid: 0b7e2c94-3a18-4d56-9f07-1a6c5b0e9d24

package oauth

import (
	"testing"
	"time"
)

func TestIsEmailAllowed(t *testing.T) {
	c := New(Config{AllowedEmails: []string{"Owner@Example.com", " family@example.com "}})
	cases := []struct {
		email string
		want  bool
	}{
		{"owner@example.com", true},        // case-insensitive
		{"OWNER@EXAMPLE.COM", true},         // case-insensitive
		{"family@example.com", true},        // trimmed on load
		{"stranger@example.com", false},     // not on list
		{"", false},                         // empty denied
		{"owner@example.com.evil.com", false}, // no substring match
	}
	for _, tc := range cases {
		if got := c.IsEmailAllowed(tc.email); got != tc.want {
			t.Errorf("IsEmailAllowed(%q)=%v, want %v", tc.email, got, tc.want)
		}
	}
}

func TestIsEmailAllowed_EmptyListDeniesEveryone(t *testing.T) {
	c := New(Config{AllowedEmails: nil})
	if c.IsEmailAllowed("anyone@example.com") {
		t.Error("empty allowlist must deny everyone (fail-closed)")
	}
}

func TestStateCodec_RoundTrip(t *testing.T) {
	codec, err := NewStateCodec(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	in := StatePayload{State: "abc", Verifier: "xyz", Provider: "google", Return: "/library"}
	blob, err := codec.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := codec.Decode(blob)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != in.State || out.Verifier != in.Verifier || out.Provider != in.Provider || out.Return != in.Return {
		t.Errorf("round-trip mismatch: %+v vs %+v", out, in)
	}
}

func TestStateCodec_RejectsTamper(t *testing.T) {
	codec, _ := NewStateCodec(time.Minute)
	blob, _ := codec.Encode(StatePayload{State: "abc", Provider: "google"})
	// Flip a character in the signed body.
	tampered := "A" + blob[1:]
	if _, err := codec.Decode(tampered); err == nil {
		t.Error("tampered state must fail to decode")
	}
	// A different codec (different secret) must reject a valid blob.
	other, _ := NewStateCodec(time.Minute)
	if _, err := other.Decode(blob); err == nil {
		t.Error("state signed by a different secret must be rejected")
	}
}

func TestStateCodec_RejectsExpired(t *testing.T) {
	codec, _ := NewStateCodec(time.Millisecond)
	blob, _ := codec.Encode(StatePayload{State: "abc", Provider: "google"})
	time.Sleep(5 * time.Millisecond)
	if _, err := codec.Decode(blob); err == nil {
		t.Error("expired state must be rejected")
	}
}

func TestPKCE_ChallengeIsDeterministicAndURLSafe(t *testing.T) {
	verifier, err := GenerateCodeVerifier()
	if err != nil {
		t.Fatal(err)
	}
	c1 := CodeChallengeS256(verifier)
	c2 := CodeChallengeS256(verifier)
	if c1 != c2 {
		t.Error("challenge must be deterministic for a given verifier")
	}
	if c1 == verifier {
		t.Error("challenge must differ from the verifier")
	}
	for _, r := range c1 {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			t.Errorf("challenge has non-URL-safe char %q", r)
		}
	}
}

func TestProviderEnabled(t *testing.T) {
	c := New(Config{
		Enabled: true, GitHubClientID: "id", GitHubClientSecret: "secret",
	})
	if !c.ProviderEnabled(ProviderGitHub) {
		t.Error("github should be enabled when configured")
	}
	if c.ProviderEnabled(ProviderGoogle) {
		t.Error("google should be disabled when not configured")
	}
	disabled := New(Config{Enabled: false, GitHubClientID: "id", GitHubClientSecret: "s"})
	if disabled.ProviderEnabled(ProviderGitHub) {
		t.Error("no provider enabled when master switch is off")
	}
}
