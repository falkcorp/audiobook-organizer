// file: internal/config/env_only_settings_test.go
// version: 1.7.0
// guid: 8c2f4a19-7d3e-4b65-9e10-a5b7c3d9e2f1
// last-edited: 2026-11-10

package config

import (
	"strings"
	"testing"

	coreauth "github.com/falkcorp/mediacore/auth"
)

func TestInviteConfig_DefaultsAreInert(t *testing.T) {
	c := factoryDefaultConfigForTest()
	if c.InviteMailConfigured() || c.GuestPolicySyncConfigured() {
		t.Fatalf("an unconfigured server must have every invite feature off: %+v", c)
	}
	if c.TunnelConnectorAddrs != "" {
		t.Fatalf("tunnel_connector_addrs default = %q, want empty (no trusted connector)", c.TunnelConnectorAddrs)
	}
	if c.AllowedPeerCIDRs != "" {
		t.Fatalf("allowed_peer_cidrs default = %q, want empty (allow every peer, with a startup warning)", c.AllowedPeerCIDRs)
	}
}

func TestInviteConfig_PartialSettingsStayInert(t *testing.T) {
	c := Config{CFAccountID: "acct", CFGuestsPolicyID: "pol"}
	if c.GuestPolicySyncConfigured() {
		t.Fatal("policy sync without its API token must be inert")
	}
	if (&Config{SMTPHost: "h"}).InviteMailConfigured() {
		t.Fatal("mail without a From address must be unconfigured")
	}
}

func TestInviteConfig_BaseURLFallbacks(t *testing.T) {
	c := Config{OAuthRedirectBaseURL: "https://sso.example.com/"}
	if got := c.InviteBaseURL("http://ext"); got != "https://sso.example.com" {
		t.Fatalf("got %q, want the oauth redirect base", got)
	}
	if got := (&Config{}).InviteBaseURL("http://ext/"); got != "http://ext" {
		t.Fatalf("got %q, want the external URL", got)
	}
	if got := (&Config{}).InviteBaseURL(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestLoginSaturatedLimits_Defaults(t *testing.T) {
	c := factoryDefaultConfigForTest()
	rate, burst, err := parseLoginSaturatedLimits(c.LoginSaturatedRate, c.LoginSaturatedBurst)
	if err != nil || rate != coreauth.DefaultSaturatedRate || burst != coreauth.DefaultSaturatedBurst {
		t.Fatalf("unset keys: rate=%v burst=%d err=%v, want the defaults %v/%d", rate, burst, err, coreauth.DefaultSaturatedRate, coreauth.DefaultSaturatedBurst)
	}
	if coreauth.DefaultSaturatedRate != 2 || coreauth.DefaultSaturatedBurst != 8 {
		t.Fatalf("defaults are documented as 2/s burst 8, got %v/%d", coreauth.DefaultSaturatedRate, coreauth.DefaultSaturatedBurst)
	}
}

func TestLoginSaturatedLimits_RejectsUnusableValues(t *testing.T) {
	for _, tc := range []struct{ rate, burst, want string }{
		{"abc", "", "LOGIN_SATURATED_RATE"},
		{"0", "", "LOGIN_SATURATED_RATE"},
		{"-1", "", "LOGIN_SATURATED_RATE"},
		{"NaN", "", "LOGIN_SATURATED_RATE"},
		{"Inf", "", "LOGIN_SATURATED_RATE"},
		{"1e9", "", "LOGIN_SATURATED_RATE"},
		{"50.5", "", "LOGIN_SATURATED_RATE"},
		{"0.009", "", "LOGIN_SATURATED_RATE"}, // below the floor: a refused caller would wait minutes
		{"0.0001", "", "LOGIN_SATURATED_RATE"},
		{"", "0", "LOGIN_SATURATED_BURST"},
		{"", "-3", "LOGIN_SATURATED_BURST"},
		{"", "2.5", "LOGIN_SATURATED_BURST"},
		{"", "lots", "LOGIN_SATURATED_BURST"},
		{"", "201", "LOGIN_SATURATED_BURST"},
	} {
		if _, _, err := parseLoginSaturatedLimits(tc.rate, tc.burst); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("rate=%q burst=%q: err=%v, want an error naming %s", tc.rate, tc.burst, err, tc.want)
		}
	}
	// The raw value is operator input read from the environment: the error names
	// the setting and the accepted range, and never echoes what was typed.
	for _, tc := range []struct{ rate, burst string }{{"zz-7.31-rate", ""}, {"", "zz-7.31-burst"}, {"0.0049999", ""}, {"", "4242424"}} {
		_, _, err := parseLoginSaturatedLimits(tc.rate, tc.burst)
		if err == nil {
			t.Fatalf("rate=%q burst=%q: expected an error", tc.rate, tc.burst)
		}
		for _, raw := range []string{tc.rate, tc.burst} {
			if raw != "" && strings.Contains(err.Error(), raw) {
				t.Errorf("error %q echoes the raw value %q", err, raw)
			}
		}
	}
	for _, tc := range []struct {
		rate, burst string
		wantRate    float64
		wantBurst   int
	}{
		{"50", "200", 50, 200}, {"0.01", "1", 0.01, 1}, {"", "", 2, 8},
	} {
		r, b, err := parseLoginSaturatedLimits(tc.rate, tc.burst)
		if err != nil || r != tc.wantRate || b != tc.wantBurst {
			t.Errorf("rate=%q burst=%q: %v/%d err=%v, want %v/%d", tc.rate, tc.burst, r, b, err, tc.wantRate, tc.wantBurst)
		}
	}
}

func TestGuestServiceTokens_FlagAndSettings(t *testing.T) {
	full := Config{CFGuestServiceTokens: true, CFAccountID: "a", CFPolicyAPIToken: "t", CFServiceAuthPolicyID: "p"}
	if !full.GuestServiceTokensEnabled() || full.GuestServiceTokensMisconfigured() {
		t.Fatal("a complete configuration must be enabled")
	}
	for name, mut := range map[string]func(*Config){
		"flag off":   func(c *Config) { c.CFGuestServiceTokens = false },
		"no account": func(c *Config) { c.CFAccountID = "" },
		"no token":   func(c *Config) { c.CFPolicyAPIToken = "" },
		"no policy":  func(c *Config) { c.CFServiceAuthPolicyID = "" },
		// The books policy must not be the guest email policy.
		"same policy as the guests policy": func(c *Config) { c.CFGuestsPolicyID, c.CFServiceAuthPolicyID = "Pol-1", " pol-1 " },
	} {
		c := full
		mut(&c)
		if c.GuestServiceTokensEnabled() {
			t.Errorf("%s: still enabled", name)
		}
		if name != "flag off" && !c.GuestServiceTokensMisconfigured() {
			t.Errorf("%s: flag on but incomplete must be reported as misconfigured", name)
		}
	}
	if (&Config{}).GuestServiceTokensEnabled() {
		t.Fatal("the zero Config (default) must be off")
	}
}

func TestGuestServiceTokens_DefaultOff(t *testing.T) {
	c := factoryDefaultConfigForTest()
	if c.CFGuestServiceTokens || c.InviteEmailServiceToken || c.CFServiceAuthPolicyID != "" {
		t.Fatalf("defaults must be off/empty: %+v", c)
	}
}
