// file: internal/server/middleware/log_redact_test.go
// version: 1.5.2
// guid: 67c415d9-c181-4b1c-9481-527366cb1090
// last-edited: 2026-11-08

package middleware

import (
	"testing"
)

func TestRedactQueryCredentials(t *testing.T) {
	secret := "abk_" + "test"
	for in, want := range map[string]string{
		"/api/items/x/file/y?token=" + secret:                  "/api/items/x/file/y?token=REDACTED",
		"/api/items/x/cover?width=400&token=jwt.a.b&format=jp": "/api/items/x/cover?width=400&token=REDACTED&format=jp",
		"/x?API_KEY=" + secret + "&q=needle":                   "/x?API_KEY=REDACTED&q=needle",
		"/cb?code=one-time&state=s":                            "/cb?code=REDACTED&state=s",
		"/x?access_token=a&refresh_token=b":                    "/x?access_token=REDACTED&refresh_token=REDACTED",
		"/x?q=token":                                           "/x?q=token",
		"/plain/path":                                          "/plain/path",
		"/x?tok%65n=" + secret:                                 "/x?tok%65n=REDACTED",
	} {
		if got := RedactQueryCredentials(in); got != want {
			t.Errorf("RedactQueryCredentials(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactLogPath_BothRedactions(t *testing.T) {
	got := RedactLogPath("/api/v1/auth/invite/some-token?token=abc&x=1")
	want := "/api/v1/auth/invite/[REDACTED]?token=REDACTED&x=1"
	if got != want {
		t.Fatalf("RedactLogPath = %q, want %q", got, want)
	}
}

// SF2: RequestURI keeps %2F escaped; the token behind an encoded slash is masked.
func TestRedactLogPath_EncodedSlashBeforeTheInviteToken(t *testing.T) {
	for in, want := range map[string]string{
		"/api/v1/auth/invite%2Fsome-token":           "/api/v1/auth/invite/[REDACTED]",
		"/api/v1/auth/invite%2fsome-token?token=abc": "/api/v1/auth/invite/[REDACTED]?token=REDACTED",
		"/api/v1/users/invites%2Fsome-token/resend":  "/api/v1/users/invites/[REDACTED]/resend",
		"/invite%2Fsome-token?x=1":                   "/invite/[REDACTED]?x=1",
		// A dot segment after the token must not walk the route away first.
		"/invite%2Fsome-token/..":              "/invite/[REDACTED]/[REDACTED]",
		"/x/../invite/some-token/..?token=abc": "/[REDACTED]?token=REDACTED",
		"/invite%252Fsome-token%252F..":        "/invite/[REDACTED]/[REDACTED]",
	} {
		if got := RedactLogPath(in); got != want {
			t.Fatalf("RedactLogPath(%q) = %q, want %q", in, got, want)
		}
	}
}
