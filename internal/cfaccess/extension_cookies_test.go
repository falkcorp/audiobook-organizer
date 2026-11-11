// file: internal/cfaccess/extension_cookies_test.go
// version: 1.0.0
// guid: 2d602357-27af-4e22-8796-d9ffa438b82f
// last-edited: 2026-11-11

package cfaccess

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"
)

func withCookies(vals ...string) func(*http.Request) {
	return func(r *http.Request) {
		for _, v := range vals {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: v})
		}
	}
}

// A header is what Cloudflare injects, so a bad one is terminal. A cookie is
// the browser's: a bad one grants nothing and must not stop the session that
// rides next to it from authenticating.
func TestCFAccess_BadCookieIsNoCredentialBadHeaderIsRefusal(t *testing.T) {
	g := newRig(t)
	good := g.acc.token(human("dick@wayne.example.test"))
	expired := g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Expiry: time.Now().Add(-time.Hour)})
	wrongAUD := g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, AUD: []string{"arkham-aud"}})
	for name, cookie := range map[string]string{"junk": "not-a-jwt", "expired": expired, "wrong audience": wrongAUD} {
		t.Run("cookie/"+name, func(t *testing.T) {
			a, err := g.do("", withCookies(cookie))
			if !errors.Is(err, coreauth.ErrNoCredential) || a != nil {
				t.Fatalf("got %+v, %v; want ErrNoCredential so the session authenticator runs", a, err)
			}
			if errors.Is(err, coreauth.ErrInvalidCredentials) {
				t.Fatal("a bad cookie refused the request")
			}
		})
	}
	t.Run("junk header is terminal", func(t *testing.T) {
		if _, err := g.do("not-a-jwt", nil); !errors.Is(err, coreauth.ErrInvalidCredentials) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("junk header is terminal even with a good cookie", func(t *testing.T) {
		if _, err := g.do("not-a-jwt", withCookies(good)); !errors.Is(err, coreauth.ErrInvalidCredentials) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a good header ignores a junk cookie", func(t *testing.T) {
		if a, err := g.do(good, withCookies("junk")); err != nil || a == nil {
			t.Fatalf("%+v, %v", a, err)
		}
	})
	if g.res.count() != 1 {
		t.Fatalf("resolver calls = %d, want 1 (the good header)", g.res.count())
	}
}

// Several CF_Authorization cookies can reach the server (one per domain scope
// or path): every one is tried, a planted one cannot hide the real one, the first
// that verifies wins, and the number tried is bounded.
func TestCFAccess_DuplicateCookiesAreAllTried(t *testing.T) {
	g := newRig(t)
	good := g.acc.token(human("dick@wayne.example.test"))
	for name, vals := range map[string][]string{
		"junk first":            {"junk", good},
		"junk after":            {good, "junk"},
		"service token first":   {g.acc.token(tokenSpec{Sub: "svc", CommonName: "friends-token-client-id"}), good},
		"junk between":          {"junk", "junk2", good, "junk3"},
		"last within the bound": append(repeat("junk", maxCookieTokens-1), good),
	} {
		t.Run(name, func(t *testing.T) {
			a, err := g.do("", withCookies(vals...))
			if err != nil || a == nil || a.User == "" {
				t.Fatalf("got %+v, %v", a, err)
			}
		})
	}
	t.Run("none verifies", func(t *testing.T) {
		if _, err := g.do("", withCookies("junk", "junk2")); !errors.Is(err, coreauth.ErrNoCredential) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("past the bound is never looked at", func(t *testing.T) {
		before := g.res.count()
		_, err := g.do("", withCookies(append(repeat("junk", maxCookieTokens), good)...))
		if !errors.Is(err, coreauth.ErrNoCredential) || g.res.count() != before {
			t.Fatalf("%v, resolver calls %d -> %d", err, before, g.res.count())
		}
	})
	t.Run("blank cookies do not count", func(t *testing.T) {
		if _, err := g.do("", withCookies(append(repeat(" ", 20), good)...)); err != nil {
			t.Fatalf("%v", err)
		}
	})
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// A key outage while only cookies are presented opens to the next method; the
// verified-identity refusals of a cookie are its answer, not skipped.
func TestCFAccess_CookieOutcomes(t *testing.T) {
	g := newRig(t)
	raw := g.acc.token(human("dick@wayne.example.test"))
	t.Run("keys unavailable", func(t *testing.T) {
		g2 := newRig(t)
		g2.acc.setMode("503")
		_, err := g2.do("", withCookies("junk", g2.acc.token(human(ownerMail))))
		if !errors.Is(err, coreauth.ErrExtensionUnavailable) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("inactive account is refused as such", func(t *testing.T) {
		g.res.err = coreauth.ErrInactiveUser
		defer func() { g.res.err = nil }()
		if _, err := g.do("", withCookies("junk", raw)); !errors.Is(err, coreauth.ErrInactiveUser) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("not admitted is no credential", func(t *testing.T) {
		g.res.err = coreauth.ErrEmailInUse
		defer func() { g.res.err = nil }()
		if _, err := g.do("", withCookies(raw)); !errors.Is(err, coreauth.ErrNoCredential) {
			t.Fatalf("%v", err)
		}
	})
}

// AuthTime is when Cloudflare issued the token, not when this request arrived.
func TestCFAccess_AuthTimeIsTheTokenIssueTime(t *testing.T) {
	g := newRig(t)
	now := g.acc.clk.Now()
	iat := now.Add(-3 * time.Hour).Truncate(time.Second)
	a, err := g.do(g.acc.token(tokenSpec{Sub: "s", Email: "dick@wayne.example.test", Iat: iat}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !a.AuthTime.Equal(iat) {
		t.Fatalf("AuthTime = %v, want the token's iat %v", a.AuthTime, iat)
	}
	t.Run("no iat is no auth time", func(t *testing.T) {
		a, err := g.do(g.acc.token(tokenSpec{Sub: "s", Email: "dick@wayne.example.test", NoIat: true}), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !a.AuthTime.IsZero() {
			t.Fatalf("AuthTime = %v, want zero", a.AuthTime)
		}
	})
	t.Run("a future iat is clamped to now", func(t *testing.T) {
		a, err := g.do(g.acc.token(tokenSpec{Sub: "s", Email: "dick@wayne.example.test", Iat: now.Add(time.Hour)}), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !a.AuthTime.Equal(now) {
			t.Fatalf("AuthTime = %v, want %v", a.AuthTime, now)
		}
	})
}

// The per-token log line is the only trace of a bad token; a flood of them is
// one line per window with the held-back count, and carries no token.
func TestCFAccess_BadTokenWarningIsRateLimited(t *testing.T) {
	g := newRig(t)
	count := func() int { return strings.Count(g.logs.String(), "did not verify") }
	for range 100 {
		if _, err := g.do("not-a-jwt-secret-token-value", nil); !errors.Is(err, coreauth.ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if n := count(); n != 1 {
		t.Fatalf("%d warnings for 100 bad tokens in one window, want 1", n)
	}
	g.acc.clk.advance(warnEvery + time.Second)
	_, _ = g.do("not-a-jwt-secret-token-value", nil)
	if n := count(); n != 2 || !strings.Contains(g.logs.String(), "suppressed=99") {
		t.Fatalf("%d warnings, logs:\n%s", n, g.logs.String())
	}
	if strings.Contains(g.logs.String(), "secret-token-value") {
		t.Fatal("the log carries the token")
	}
}
