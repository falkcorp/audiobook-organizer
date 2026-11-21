// file: internal/config/env_only_settings.go
// version: 2.0.0
// guid: 3e9b1c74-5a2d-4f86-b0e7-c41d8a6f2b93
// last-edited: 2026-11-10

package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"
)

// Environment-only settings: invitation email and guest access, the tunnel
// and peer perimeter, the saturated-login bound and the bootstrap key
// lifetime. (Until F4-02 this file was invite_config.go and also declared the
// saturated-login bounds; those are core auth's now, coreauth.*SaturatedRate
// and *SaturatedBurst, as are the bootstrap key TTL bounds, which lived in
// bootstrap_key_ttl.go. What stays is how ABO reads its own environment.)
//
// Every key here is ENVIRONMENT-ONLY and carries `json:"-"` on Config, which
// buys three properties at once:
//
//   - GET /api/v1/config can never return one. Two of them are secrets (the
//     SMTP password and the Cloudflare policy API token), and the others are
//     deliberately hidden too: the SMTP username is usually a person's
//     mailbox. Settings screens and admin API responses report only whether
//     each feature is configured (InviteMailConfigured and friends), never
//     the values.
//   - The config blob never stores one, so a value can only come from the
//     operator's environment and a database dump carries none of them.
//   - PUT /api/v1/config refuses a payload naming any of them, with the same
//     "environment only" message the ABS keys get (inviteEnvOnlyKeyInUpdate),
//     instead of the generic unknown-key error.
//
// Since F3-07 their values come from the operator layers under the canonical
// names the rename table gives (ABO_INTEGRATIONS_NOTIFY_EMAIL_*,
// ABO_BOOKS_CFGUESTS_*, ABO_CORE_HTTP_*, ABO_CORE_AUTH_LOGIN_*), projected
// into these fields by books' appconfig Engine; the HEAD names refuse the
// boot by name.

// The service-wide bound on password logins admitted while the aggregate login
// ceilings are spent ("saturated"): every one of them still costs a real
// argon2id derivation (an unknown name spends one too, so the answer does not
// reveal whether the name exists), and core auth's token bucket
// (coreauth.LoginCeiling) caps how many the service takes on per second,
// whatever the number of visitors. Unset keys mean core's defaults
// (coreauth.DefaultSaturatedRate, DefaultSaturatedBurst), and the accepted
// range is core's (MinSaturatedRate..MaxSaturatedRate, 1..MaxSaturatedBurst).
// Cloudflare Access logins (a verified Access identity) are outside the
// bucket. They still run the password check; they are bounded by the
// per-visitor throttle and by the allow-listed identity Cloudflare Access
// vouches for, not by this bucket.

// LoginSaturatedLimits returns the effective saturated-login rate (per second)
// and burst: the defaults for an unset key, the parsed values otherwise. An
// unusable value (not a number, NaN/Inf, a rate below coreauth.MinSaturatedRate
// or above coreauth.MaxSaturatedRate, a burst that is not an integer in 1..
// coreauth.MaxSaturatedBurst) is an error, never a silent default: the server
// refuses to start on it, because a typo here must neither remove nor tighten
// the bound unnoticed. The error names the setting and the accepted range, and
// never echoes the value it was given.
func LoginSaturatedLimits() (rate float64, burst int, err error) {
	mu.RLock()
	rawRate, rawBurst := AppConfig.LoginSaturatedRate, AppConfig.LoginSaturatedBurst
	mu.RUnlock()
	return parseLoginSaturatedLimits(rawRate, rawBurst)
}

func parseLoginSaturatedLimits(rawRate, rawBurst string) (rate float64, burst int, err error) {
	rate, burst = coreauth.DefaultSaturatedRate, coreauth.DefaultSaturatedBurst
	if rawRate = strings.TrimSpace(rawRate); rawRate != "" {
		r, perr := strconv.ParseFloat(rawRate, 64)
		if perr != nil || math.IsNaN(r) || math.IsInf(r, 0) || r < coreauth.MinSaturatedRate || r > coreauth.MaxSaturatedRate {
			return 0, 0, fmt.Errorf("LOGIN_SATURATED_RATE must be a number of logins per second from %v to %v", coreauth.MinSaturatedRate, coreauth.MaxSaturatedRate)
		}
		rate = r
	}
	if rawBurst = strings.TrimSpace(rawBurst); rawBurst != "" {
		b, perr := strconv.Atoi(rawBurst)
		if perr != nil || b < 1 || b > coreauth.MaxSaturatedBurst {
			return 0, 0, fmt.Errorf("LOGIN_SATURATED_BURST must be a whole number from 1 to %d", coreauth.MaxSaturatedBurst)
		}
		burst = b
	}
	return rate, burst, nil
}

// TunnelConnectorAddrs returns tunnel_connector_addrs under the read lock: one
// field, not a whole-struct Snapshot copy, because the public invite and
// bootstrap routes read it on every request. The value is a comma-separated
// list of IP addresses; an IPv6 link-local entry must carry its zone
// (fe80::5%eth0), since a link-local address is only unique per interface,
// and one without a zone is ignored with a warning. A numeric or wrong-case
// zone is normalised to the interface's name (a case-insensitive match must
// be unique); an ambiguous one is ignored with a warning, and one that names
// no interface is ignored with a warning and retried every 30 seconds.
func TunnelConnectorAddrs() string {
	mu.RLock()
	defer mu.RUnlock()
	return AppConfig.TunnelConnectorAddrs
}

// CFAccessTeam returns cf_access_team_domain under the read lock: one field, not
// a Snapshot copy, because the Content-Security-Policy builder reads it on every
// response.
func CFAccessTeam() string {
	mu.RLock()
	defer mu.RUnlock()
	return AppConfig.CFAccessTeamDomain
}

// AllowedPeerCIDRs returns allowed_peer_cidrs under the read lock: one field,
// not a Snapshot copy, because the peer allow-list reads it on every request.
// The value is a comma-separated list of CIDR networks and bare IP addresses;
// see Config.AllowedPeerCIDRs for the empty and all-invalid cases.
func AllowedPeerCIDRs() string {
	mu.RLock()
	defer mu.RUnlock()
	return AppConfig.AllowedPeerCIDRs
}

// InviteMailConfigured reports whether invitation email can be sent: a host and
// a From address are the minimum (no AUTH is a supported setup).
func (c *Config) InviteMailConfigured() bool {
	return c.SMTPHost != "" && c.SMTPFrom != ""
}

// GuestPolicySyncConfigured reports whether accepted invitees are added to the
// Cloudflare Access guest policy. All three settings are required; with any one
// missing the feature is inert.
func (c *Config) GuestPolicySyncConfigured() bool {
	return c.CFAccountID != "" && c.CFGuestsPolicyID != "" && c.CFPolicyAPIToken != ""
}

// GuestServiceTokensEnabled reports whether accepting an invitation mints a
// per-guest Cloudflare service token: the flag is on and the three settings it
// needs (account id, API token, the "books" policy id) are all present, and the
// books policy is not the guest email policy (CF_GUESTS_POLICY_ID): that one
// admits people by email, and a per-token rule written into it would mix the two
// kinds of access in a policy the email sync rewrites.
func (c *Config) GuestServiceTokensEnabled() bool {
	return c.CFGuestServiceTokens && c.CFAccountID != "" && c.CFPolicyAPIToken != "" && c.CFServiceAuthPolicyID != "" &&
		!c.GuestServiceTokensPolicyClash()
}

// GuestServiceTokensPolicyClash reports CF_SERVICE_AUTH_POLICY_ID naming the
// same policy as CF_GUESTS_POLICY_ID.
func (c *Config) GuestServiceTokensPolicyClash() bool {
	books := strings.ToLower(strings.TrimSpace(c.CFServiceAuthPolicyID))
	return books != "" && books == strings.ToLower(strings.TrimSpace(c.CFGuestsPolicyID))
}

// GuestServiceTokensMisconfigured reports the flag being on without everything
// it needs, so startup can say so instead of the feature silently doing nothing.
func (c *Config) GuestServiceTokensMisconfigured() bool {
	return c.CFGuestServiceTokens && !c.GuestServiceTokensEnabled()
}

// InviteBaseURL is the public origin invitation links are built on:
// PUBLIC_BASE_URL, else oauth_redirect_base_url, else fallback (the server's
// external URL). Trailing slashes are trimmed. Empty means links are relative.
func (c *Config) InviteBaseURL(fallback string) string {
	for _, v := range []string{c.PublicBaseURL, c.OAuthRedirectBaseURL, fallback} {
		if v = strings.TrimRight(strings.TrimSpace(v), "/"); v != "" {
			return v
		}
	}
	return ""
}

// ResolveBootstrapKeyTTL returns how long a bootstrap-issued API key lives,
// and a warning to log when the configuration was not used as written (empty
// when it was, or when nothing was configured).
//
// Precedence: BootstrapKeyTTL when set; else the deprecated
// BootstrapKeyTTLDays when positive; else coreauth.DefaultBootstrapKeyTTL.
// Every path is capped at coreauth.MaxBootstrapKeyTTL, and an unparseable or non-positive
// duration falls back to the default: a bootstrap key always expires.
func (c *Config) ResolveBootstrapKeyTTL() (time.Duration, string) {
	if raw := strings.TrimSpace(c.BootstrapKeyTTL); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			return coreauth.DefaultBootstrapKeyTTL, fmt.Sprintf("bootstrap_key_ttl %q is not a duration (e.g. 8h); using %s", raw, coreauth.DefaultBootstrapKeyTTL)
		case d <= 0:
			return coreauth.DefaultBootstrapKeyTTL, fmt.Sprintf("bootstrap_key_ttl %q must be positive (bootstrap keys always expire); using %s", raw, coreauth.DefaultBootstrapKeyTTL)
		case d > coreauth.MaxBootstrapKeyTTL:
			return coreauth.MaxBootstrapKeyTTL, fmt.Sprintf("bootstrap_key_ttl %q is over the %s cap; using %s", raw, coreauth.MaxBootstrapKeyTTL, coreauth.MaxBootstrapKeyTTL)
		}
		if c.BootstrapKeyTTLDays > 0 {
			return d, fmt.Sprintf("bootstrap_key_ttl_days is deprecated and ignored because bootstrap_key_ttl is set; remove bootstrap_key_ttl_days=%d", c.BootstrapKeyTTLDays)
		}
		return d, ""
	}
	if c.BootstrapKeyTTLDays > 0 {
		d := min(time.Duration(c.BootstrapKeyTTLDays)*24*time.Hour, coreauth.MaxBootstrapKeyTTL)
		return d, fmt.Sprintf("bootstrap_key_ttl_days=%d is deprecated; bootstrap keys now live at most %s, so using %s. Replace it with bootstrap_key_ttl (a duration, default %s)",
			c.BootstrapKeyTTLDays, coreauth.MaxBootstrapKeyTTL, d, coreauth.DefaultBootstrapKeyTTL)
	}
	return coreauth.DefaultBootstrapKeyTTL, ""
}
