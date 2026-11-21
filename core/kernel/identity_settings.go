// file: core/kernel/identity_settings.go
// version: 0.1.0
// guid: fdb0b9c4-2525-4d3a-8909-2f6a05c28c6e
// last-edited: 2026-11-20

package kernel

import (
	"errors"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/rbac"
)

// This file maps the core.auth section (core_sections.go, 05-authn §8) onto
// auth.Settings, the value auth.Deps.Settings returns on every call (F4-02:
// auth never imports config). It is dark: F4-08's boot wiring (B3) builds
// the reader from the section's config handle and passes it to
// buildIdentity; nothing calls it before then.
//
// Every value passes through raw. auth's Settings.withDefaults owns every
// default and every cap (zero or negative takes the default; max_ttl,
// bootstrap.key_ttl and extension_max_age are capped there), so the kernel
// keeps no second copy that could drift. Two exceptions, both by the spec:
//
//   - core.auth.session.cookie_name: the section declares "" because its
//     default is the template <AppID>_session (05-authn §8 and §4.3, SEC-11),
//     which the kernel fills here; auth's own fallback, "mediacore_session",
//     applies only when the app has no ID.
//   - core.auth.owner.required and core.auth.disabled_allow_remote: auth's
//     nil means "derive" (an owner verifier that reports an owner set; the
//     app's Options.DefaultDisabledAllowRemote), but the section can only
//     declare false. Mapping the declared false to &false would switch the
//     owner guard off on an install whose verifier is configured, and would
//     override ABO's compiled true. So each maps to a pointer only when a
//     layer above the declared default sets it (config.KeyState.Source is
//     not SourceDefault), and to nil otherwise.
//
// Keys with no auth.Settings field (nothing in auth reads them):
// session.idle_timeout, bootstrap.token_ttl (auth uses its constant
// DefaultBootstrapTokenTTL), oidc.redirect_base_url, oidc.providers, the
// per-provider secret oidc.<id>.client_secret and the two schedules. The one Settings field with no key is Login.MaxKeys, which
// auth documents as not a setting.

// Paths of the two keys whose declared default is not auth's nil.
const (
	authOwnerRequiredKey       config.Path = "core.auth.owner.required"
	authDisabledAllowRemoteKey config.Path = "core.auth.disabled_allow_remote"
)

// authKeyExplicit reports whether a core.auth key's effective value comes
// from a layer above its declared default.
type authKeyExplicit func(config.Path) bool

// keyLookup is the part of *config.Store the provenance read needs.
type keyLookup interface {
	Lookup(p config.Path) (config.KeyState, bool)
}

// configExplicit is authKeyExplicit over a config store: a key is explicit
// when its effective value's Source is any layer but SourceDefault. A key
// the store does not know is not explicit (nothing can have set it).
func configExplicit(st keyLookup) authKeyExplicit {
	return func(p config.Path) bool {
		ks, ok := st.Lookup(p)
		return ok && ks.Source != config.SourceDefault
	}
}

// authSettingsReader returns auth.Deps.Settings over the core.auth section.
// get is the section's live value (the boot passes its config handle's Get,
// never Pending, so a restart-class key keeps its boot value); explicit is
// the provenance read (configExplicit over the boot's store); appID fills
// the cookie name's <AppID>_session template.
//
// The provenance of owner.required and disabled_allow_remote is read once,
// here, at boot: both keys are restart-class, so a value stored later and
// still pending must not change which side the mapping takes before the
// restart that makes it effective. A nil get or explicit is refused: without
// get every operator value would be silently ignored, and without explicit
// the two derived keys cannot be mapped without guessing.
func authSettingsReader(get func() *authSettings, explicit authKeyExplicit, appID string) (func() auth.Settings, error) {
	if get == nil {
		return nil, errors.New("kernel: identity: the core.auth reader needs the section's value")
	}
	if explicit == nil {
		return nil, errors.New("kernel: identity: the core.auth reader needs the key provenance (owner.required and disabled_allow_remote default to auth's derivation)")
	}
	ownerSet := explicit(authOwnerRequiredKey)
	remoteSet := explicit(authDisabledAllowRemoteKey)
	return func() auth.Settings {
		return authSettingsFrom(get(), ownerSet, remoteSet, appID)
	}, nil
}

// authSettingsFrom is the mapping itself. s nil (a config handle never
// returns one) is the declared defaults, which are auth's own: a zero
// authSettings would read cookie_host_prefix as false and drop the
// __Host- prefix.
func authSettingsFrom(s *authSettings, ownerSet, remoteSet bool, appID string) auth.Settings {
	if s == nil {
		return auth.Settings{SessionCookie: sessionCookieName("", appID)}
	}
	return auth.Settings{
		Mode:                auth.AuthMode(s.Mode),
		SessionTTL:          time.Duration(s.Session.TTL),
		RememberTTL:         time.Duration(s.Session.RememberTTL),
		SessionCookie:       sessionCookieName(s.Session.CookieName, appID),
		NoHostPrefix:        !s.Session.CookieHostPrefix,
		MinPasswordLength:   s.Password.MinLength,
		KeyDefaultTTL:       time.Duration(s.APIKey.DefaultTTL),
		KeyMaxTTL:           time.Duration(s.APIKey.MaxTTL),
		BootstrapEnabled:    new(s.Bootstrap.Enabled),
		BootstrapKeyTTL:     time.Duration(s.Bootstrap.KeyTTL),
		ReadOnlyKey:         s.Bootstrap.ReadonlyKey.Enabled,
		ReadOnlyKeyScopes:   permissions(s.Bootstrap.ReadonlyKey.Scopes),
		SetupRequiresToken:  new(s.SetupRequiresToken),
		ExtensionMaxAge:     time.Duration(s.ExtensionMaxAge),
		TempLoginTTL:        time.Duration(s.TempLogin.TTL),
		InviteTTL:           time.Duration(s.Invite.TTL),
		SaturatedRate:       s.Login.SaturatedRate,
		SaturatedBurst:      s.Login.SaturatedBurst,
		OwnerRequired:       explicitBool(s.Owner.Required, ownerSet),
		DisabledAllowRemote: explicitBool(s.DisabledAllowRemote, remoteSet),
		Login: auth.ThrottleLimits{
			MaxFailuresPerSource: s.Login.MaxFailuresPerSource,
			Window:               time.Duration(s.Login.Window),
			SoftThreshold:        s.Login.SoftThreshold,
			SoftStep:             time.Duration(s.Login.SoftStep),
			SoftMax:              time.Duration(s.Login.SoftMax),
		},
	}
}

// sessionCookieName is core.auth.session.cookie_name with its <AppID>_session
// default filled; "" (no app ID either) leaves auth's own fallback.
func sessionCookieName(name, appID string) string {
	if name != "" || appID == "" {
		return name
	}
	return appID + "_session"
}

// explicitBool is v as a pointer when a layer set it, else nil (auth derives).
func explicitBool(v, set bool) *bool {
	if !set {
		return nil
	}
	return new(v)
}

// permissions is a scope list as rbac permissions; nil stays nil, so auth's
// default start-up key scopes apply.
func permissions(ss []string) []rbac.Permission {
	if ss == nil {
		return nil
	}
	out := make([]rbac.Permission, len(ss))
	for i, s := range ss {
		out[i] = rbac.Permission(s)
	}
	return out
}
