// file: core/kernel/identity_cookiename_test.go
// version: 0.1.0
// guid: 5a8d3f27-1c94-4e60-b7d2-0e93c6a41b58
// last-edited: 2026-11-21

package kernel

import (
	"encoding/json"
	"testing"
)

// Batman names only.

// sessionCookieScheme is the name the core OpenAPI document's sessionCookie
// security scheme carries.
func sessionCookieScheme(t *testing.T, k *Kernel) string {
	t.Helper()
	d, err := k.describe()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			SecuritySchemes map[string]struct {
				Name string `json:"name"`
			} `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(d.Core, &doc); err != nil {
		t.Fatal(err)
	}
	s, ok := doc.Components.SecuritySchemes["sessionCookie"]
	if !ok {
		t.Fatalf("the core document has no sessionCookie scheme: %s", d.Core)
	}
	return s.Name
}

// TestOpenAPICookieName_FollowsTheConfiguredCookie: the document's sessionCookie
// scheme names the cookie auth sets. An identity boot with core.auth.session
// .cookie_name set carries that name; unset it carries <AppID>_session, as it
// did before the name was read from config; a boot that did not declare the
// identity flip never reads the key, so an operator value changes nothing there.
func TestOpenAPICookieName_FollowsTheConfiguredCookie(t *testing.T) {
	const key = "core.auth.session.cookie_name"
	t.Run("identity boot, name set", func(t *testing.T) {
		h, c, _ := mountConfig(t, true)
		withCore(&c, key, "bat_session")
		if got := sessionCookieScheme(t, h.mustBoot(c)); got != "bat_session" {
			t.Errorf("sessionCookie scheme name = %q, want the configured %q", got, "bat_session")
		}
	})
	t.Run("identity boot, name unset", func(t *testing.T) {
		h, c, _ := mountConfig(t, true)
		if got, want := sessionCookieScheme(t, h.mustBoot(c)), c.AppID+"_session"; got != want {
			t.Errorf("sessionCookie scheme name = %q, want the default %q", got, want)
		}
	})
	t.Run("no identity boot, name set", func(t *testing.T) {
		h := newSwapHost(t)
		c := h.envConfig(nil)
		withCore(&c, key, "bat_session")
		if got, want := sessionCookieScheme(t, h.mustBoot(c)), c.AppID+"_session"; got != want {
			t.Errorf("sessionCookie scheme name = %q, want the default %q: a boot without identity must not read the key", got, want)
		}
	})
}

// TestOpenAPICookieName_ConfiguredNameIsTheCookieAuthSets: the name in the
// document and the one stage 12's login cookie carries come from one reader.
func TestOpenAPICookieName_ConfiguredNameIsTheCookieAuthSets(t *testing.T) {
	h, c, _ := mountConfig(t, true)
	withCore(&c, "core.auth.session.cookie_name", "bat_session")
	k := h.mustBoot(c)
	if got := k.ident.settings().SessionCookie; got != sessionCookieScheme(t, k) {
		t.Errorf("auth's session cookie %q differs from the document's %q", got, sessionCookieScheme(t, k))
	}
}
