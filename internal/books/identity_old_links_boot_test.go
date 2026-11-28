// file: internal/books/identity_old_links_boot_test.go
// version: 0.1.0
// guid: 6e85d4e0-77c4-4e68-9bf7-36e4bfc59367
// last-edited: 2026-11-23

package books

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/httpapi"
)

// TestOldLinkFormsNotServed pins the identity flip's one-way link change. The
// old temp-login consumer is absent, the old invite path reaches only the
// build's page fallback, and gin's old invite lookup and acceptance endpoints
// are retired (410). None starts a session, carries its token into a redirect,
// or exposes its bearer token in the access log.
//
// The page fallback is the build's own (oldInviteFallbackStatus): the SPA
// document with the embedded frontend, whose router has no /invite/:token page,
// and without it (how CI runs) the placeholder's 302 to "/". Either way the old
// link opens no invite; only the bare root may be a Location.
func TestOldLinkFormsNotServed(t *testing.T) {
	c := bootComposed(t, false, func(cfg *config.Config) { cfg.EnableAuth = true }, nil)

	for ri := range c.app.Routes() {
		if ri.Kind == httpapi.RouteKindLegacy && (ri.Name == "/auth/temp-login" || strings.HasPrefix(ri.Name, "/invite/")) {
			t.Fatalf("the identity flip still registers the old link route %s %s", ri.Method, ri.Name)
		}
	}

	const token = "batman-old-link-token"
	cases := []struct {
		name   string
		method string
		path   string
		want   int
		// location is the one Location the answer may carry: "" for none.
		location string
	}{
		{"temp-login query link", http.MethodGet, "/auth/temp-login?token=" + token, http.StatusNotFound, ""},
		// The server answers a navigation path with its page fallback. App.tsx
		// deliberately routes /invite but not /invite/:token, so with the SPA
		// embedded the old form lands on the client-side not-found page instead
		// of opening an invite; without it, on the placeholder home page.
		{"invite path link", http.MethodGet, "/invite/" + token, oldInviteFallbackStatus, oldInviteFallbackLocation},
		{"legacy invite lookup", http.MethodGet, "/api/v1/auth/invite/" + token, http.StatusGone, ""},
		{"legacy invite acceptance", http.MethodPost, "/api/v1/auth/accept-invite", http.StatusGone, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := c.serve(tc.method, tc.path, nil)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			loc := rec.Header().Get("Location")
			require.Equal(t, tc.location, loc, "an old link must not redirect into a compatibility window")
			require.NotContains(t, loc, token, "a redirect carried the old link's token")
			require.NotContains(t, loc, "invite", "a redirect led to an invite page")
			require.Empty(t, rec.Result().Cookies(), "an old link must not start a session")
		})
	}

	logged := c.newLog.String()
	require.NotContains(t, logged, token, "the old bearer form reached the access log without redaction")
	require.Contains(t, logged, "/auth/temp-login?token=[REDACTED]", "the old temp-login path was not logged in redacted form")
	require.Contains(t, logged, "/invite/[REDACTED]", "the old invite path was not logged in redacted form")
}
