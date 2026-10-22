// file: internal/config/invite_config.go
// version: 1.6.0
// guid: 3e9b1c74-5a2d-4f86-b0e7-c41d8a6f2b93
// last-edited: 2026-10-21

package config

import (
	"strings"

	"github.com/spf13/viper"
)

// Invitation email and guest-access settings.
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
// Every key needs an explicit viper.BindEnv: this codebase does not enable
// viper.AutomaticEnv for these names.
var inviteEnvOnlySettings = []struct {
	key    string // viper / payload key
	envVar string
}{
	{"smtp_host", "SMTP_HOST"},
	{"smtp_port", "SMTP_PORT"},
	{"smtp_username", "SMTP_USERNAME"},
	{"smtp_password", "SMTP_PASSWORD"},
	{"smtp_from", "SMTP_FROM"},
	{"public_base_url", "PUBLIC_BASE_URL"},
	{"cf_account_id", "CF_ACCOUNT_ID"},
	{"cf_guests_policy_id", "CF_GUESTS_POLICY_ID"},
	{"cf_policy_api_token", "CF_POLICY_API_TOKEN"},
	{"tunnel_connector_addrs", "TUNNEL_CONNECTOR_ADDRS"},
	{"allowed_peer_cidrs", "ALLOWED_PEER_CIDRS"},
}

// DefaultSMTPPort is used when SMTP_HOST is set and SMTP_PORT is not.
const DefaultSMTPPort = 587

// registerInviteDefaults declares the invitation keys and binds each to its
// environment variable. Called from InitConfig. Every default is empty (port:
// DefaultSMTPPort), so an unconfigured server sends no mail, shows no headers
// and never calls Cloudflare.
func registerInviteDefaults() {
	for _, s := range inviteEnvOnlySettings {
		if s.key == "smtp_port" {
			viper.SetDefault(s.key, DefaultSMTPPort)
		} else {
			viper.SetDefault(s.key, "")
		}
		viper.BindEnv(s.key, s.envVar) //nolint:errcheck
	}
}

// applyInviteEnv copies the invitation keys from viper onto c. It runs both when
// the config is first built from viper and from applyEnvAuthoritativeConfig after
// the blob load, which replaces the whole struct (and the blob never holds these).
func applyInviteEnv(c *Config) {
	c.SMTPHost = strings.TrimSpace(viper.GetString("smtp_host"))
	c.SMTPPort = viper.GetInt("smtp_port")
	c.SMTPUsername = strings.TrimSpace(viper.GetString("smtp_username"))
	c.SMTPPassword = viper.GetString("smtp_password")
	c.SMTPFrom = strings.TrimSpace(viper.GetString("smtp_from"))
	c.PublicBaseURL = strings.TrimSpace(viper.GetString("public_base_url"))
	c.CFAccountID = strings.TrimSpace(viper.GetString("cf_account_id"))
	c.CFGuestsPolicyID = strings.TrimSpace(viper.GetString("cf_guests_policy_id"))
	c.CFPolicyAPIToken = strings.TrimSpace(viper.GetString("cf_policy_api_token"))
	c.TunnelConnectorAddrs = strings.TrimSpace(viper.GetString("tunnel_connector_addrs"))
	c.AllowedPeerCIDRs = strings.TrimSpace(viper.GetString("allowed_peer_cidrs"))
}

// inviteEnvOnlyKeyInUpdate returns the refusal for a PUT /config payload that
// names an invitation key at the top level, or ok=false when none is present.
// The keys have no JSON field (json:"-"), so without this the payload would be
// refused by unknownConfigKeys with a message that hides why.
func inviteEnvOnlyKeyInUpdate(payload map[string]any) (string, bool) {
	var hit []string
	for _, s := range inviteEnvOnlySettings {
		for k := range payload {
			if strings.EqualFold(k, s.key) {
				hit = append(hit, s.key+" ("+s.envVar+")")
			}
		}
	}
	if len(hit) == 0 {
		return "", false
	}
	return EnvOnlySettingsRefusedMessage + ": " + strings.Join(hit, ", "), true
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
