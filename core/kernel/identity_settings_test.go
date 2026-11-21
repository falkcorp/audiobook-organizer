// file: core/kernel/identity_settings_test.go
// version: 0.1.0
// guid: e95811ee-f6af-46ec-b672-9af1c56adfeb
// last-edited: 2026-11-20

package kernel

import (
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/rbac"
)

// declaredAuthStore registers the real core sections in a config store with
// env as the operator layer, loads it, and returns the store and the
// core.auth handle's value: what the boot's handle would return.
func declaredAuthStore(t *testing.T, env ...string) (*config.Store, *authSettings) {
	t.Helper()
	st, err := config.NewStore(config.Options{AppID: "gotham", Environ: env, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(t.Context()) })
	hs, err := config.RegisterSections(st.Registry(), coreSections()...)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, h := range hs {
		if ah, ok := h.(*config.Handle[authSettings]); ok {
			v := *ah.Get()
			return st, &v
		}
	}
	t.Fatal("no core.auth handle among the core sections")
	return nil, nil
}

// declaredAuth is the core.auth section's declared defaults, a fresh copy.
func declaredAuth(t *testing.T) *authSettings {
	t.Helper()
	_, s := declaredAuthStore(t)
	return s
}

// TestAuthSettings_DeclaredDefaultsAreAuthDefaults is the drift detector:
// every core.auth default, mapped, is auth's own default, key by key, so the
// section and auth cannot disagree on what an untouched install gets. The
// one intended difference is the cookie name, which the kernel fills from
// the <AppID>_session template (05-authn §8); auth's "mediacore_session" is
// its fallback for a kernel that passes none.
func TestAuthSettings_DeclaredDefaultsAreAuthDefaults(t *testing.T) {
	d := declaredAuth(t)
	got := authSettingsFrom(d, false, false, "gotham")
	want := auth.Settings{
		Mode:              auth.ModeRequired,
		SessionTTL:        auth.DefaultSessionTTL,
		RememberTTL:       auth.DefaultRememberTTL,
		KeyDefaultTTL:     auth.DefaultKeyTTL,
		KeyMaxTTL:         auth.DefaultKeyMaxTTL,
		SessionCookie:     "gotham_session",
		NoHostPrefix:      false,
		MinPasswordLength: auth.DefaultMinPasswordLength,
		Login: auth.ThrottleLimits{
			MaxFailuresPerSource: auth.DefaultMaxFailuresPerSource,
			Window:               auth.DefaultThrottleWindow,
			SoftThreshold:        auth.DefaultSoftThreshold,
			SoftStep:             auth.DefaultSoftStep,
			SoftMax:              auth.DefaultSoftMax,
		},
		SaturatedRate:      auth.DefaultSaturatedRate,
		SaturatedBurst:     auth.DefaultSaturatedBurst,
		TempLoginTTL:       auth.DefaultTempLoginTTL,
		InviteTTL:          auth.DefaultInviteTTL,
		OwnerRequired:      nil, // auth derives it
		BootstrapKeyTTL:    auth.DefaultBootstrapKeyTTL,
		SetupRequiresToken: new(true), // auth's nil default
		BootstrapEnabled:   new(true), // auth's nil default
		ReadOnlyKey:        false,
		// auth's default start-up key scopes (defaultReadOnlyScopes, used
		// when the list is empty) are exactly this list.
		ReadOnlyKeyScopes:   []rbac.Permission{"core:system.view"},
		DisabledAllowRemote: nil, // app Options.DefaultDisabledAllowRemote
		ExtensionMaxAge:     auth.DefaultExtensionMaxAge,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("declared core.auth defaults map to\n%+v\nwant auth's defaults\n%+v", got, want)
	}
	// Keys with no Settings field still agree with the constant auth uses.
	if time.Duration(d.Bootstrap.TokenTTL) != auth.DefaultBootstrapTokenTTL {
		t.Errorf("bootstrap.token_ttl declares %v, auth uses %v", time.Duration(d.Bootstrap.TokenTTL), auth.DefaultBootstrapTokenTTL)
	}
	// seams.d/F4-08-apikey-expiry.md item 3: max_ttl is never below 1096 days.
	if got.KeyMaxTTL < 26304*time.Hour {
		t.Errorf("max_ttl default %v is below 26304h", got.KeyMaxTTL)
	}
}

// TestAuthSettings_EveryKeyMapsToItsField: one cell per mapped core.auth key.
// Each cell changes that key alone from the declared defaults; the mapped
// Settings must equal the defaults' mapping with exactly that field changed,
// so a key mapped to the wrong field, or not at all, fails its cell.
func TestAuthSettings_EveryKeyMapsToItsField(t *testing.T) {
	base := declaredAuth(t)
	for _, c := range []struct {
		key string
		set func(*authSettings)
		exp func(*auth.Settings)
	}{
		{"mode", func(s *authSettings) { s.Mode = "disabled" }, func(w *auth.Settings) { w.Mode = auth.ModeDisabled }},
		{"session.ttl", func(s *authSettings) { s.Session.TTL = dur(2 * time.Hour) }, func(w *auth.Settings) { w.SessionTTL = 2 * time.Hour }},
		{"session.remember_ttl", func(s *authSettings) { s.Session.RememberTTL = dur(9 * time.Hour) }, func(w *auth.Settings) { w.RememberTTL = 9 * time.Hour }},
		{"session.cookie_name", func(s *authSettings) { s.Session.CookieName = "batcave" }, func(w *auth.Settings) { w.SessionCookie = "batcave" }},
		{"session.cookie_host_prefix", func(s *authSettings) { s.Session.CookieHostPrefix = false }, func(w *auth.Settings) { w.NoHostPrefix = true }},
		{"password.min_length", func(s *authSettings) { s.Password.MinLength = 14 }, func(w *auth.Settings) { w.MinPasswordLength = 14 }},
		{"apikey.default_ttl", func(s *authSettings) { s.APIKey.DefaultTTL = dur(48 * time.Hour) }, func(w *auth.Settings) { w.KeyDefaultTTL = 48 * time.Hour }},
		{"apikey.max_ttl", func(s *authSettings) { s.APIKey.MaxTTL = dur(100 * time.Hour) }, func(w *auth.Settings) { w.KeyMaxTTL = 100 * time.Hour }},
		// Above auth's cap: passed through raw, auth caps it; the kernel never
		// clamps it lower (seams.d/F4-08-apikey-expiry.md item 3).
		{"apikey.max_ttl above the cap", func(s *authSettings) { s.APIKey.MaxTTL = dur(40000 * time.Hour) }, func(w *auth.Settings) { w.KeyMaxTTL = 40000 * time.Hour }},
		{"bootstrap.enabled", func(s *authSettings) { s.Bootstrap.Enabled = false }, func(w *auth.Settings) { w.BootstrapEnabled = new(false) }},
		{"bootstrap.key_ttl", func(s *authSettings) { s.Bootstrap.KeyTTL = dur(3 * time.Hour) }, func(w *auth.Settings) { w.BootstrapKeyTTL = 3 * time.Hour }},
		{"bootstrap.readonly_key.enabled", func(s *authSettings) { s.Bootstrap.ReadonlyKey.Enabled = true }, func(w *auth.Settings) { w.ReadOnlyKey = true }},
		{
			"bootstrap.readonly_key.scopes",
			func(s *authSettings) {
				s.Bootstrap.ReadonlyKey.Scopes = []string{"core:metrics.read", "core:system.view"}
			},
			func(w *auth.Settings) {
				w.ReadOnlyKeyScopes = []rbac.Permission{"core:metrics.read", "core:system.view"}
			},
		},
		{"bootstrap.readonly_key.scopes nil", func(s *authSettings) { s.Bootstrap.ReadonlyKey.Scopes = nil }, func(w *auth.Settings) { w.ReadOnlyKeyScopes = nil }},
		{"setup_requires_token", func(s *authSettings) { s.SetupRequiresToken = false }, func(w *auth.Settings) { w.SetupRequiresToken = new(false) }},
		{"extension_max_age", func(s *authSettings) { s.ExtensionMaxAge = dur(48 * time.Hour) }, func(w *auth.Settings) { w.ExtensionMaxAge = 48 * time.Hour }},
		// Zero and negative pass through; auth's withDefaults takes the 24h
		// default for both (TestIdentity_ExtensionMaxAgeReachesStage12 shows it
		// through the assembled service).
		{"extension_max_age zero", func(s *authSettings) { s.ExtensionMaxAge = 0 }, func(w *auth.Settings) { w.ExtensionMaxAge = 0 }},
		{"extension_max_age negative", func(s *authSettings) { s.ExtensionMaxAge = dur(-time.Hour) }, func(w *auth.Settings) { w.ExtensionMaxAge = -time.Hour }},
		{"login.max_failures_per_source", func(s *authSettings) { s.Login.MaxFailuresPerSource = 3 }, func(w *auth.Settings) { w.Login.MaxFailuresPerSource = 3 }},
		{"login.window", func(s *authSettings) { s.Login.Window = dur(7 * time.Minute) }, func(w *auth.Settings) { w.Login.Window = 7 * time.Minute }},
		{"login.soft_threshold", func(s *authSettings) { s.Login.SoftThreshold = 2 }, func(w *auth.Settings) { w.Login.SoftThreshold = 2 }},
		{"login.soft_step", func(s *authSettings) { s.Login.SoftStep = dur(50 * time.Millisecond) }, func(w *auth.Settings) { w.Login.SoftStep = 50 * time.Millisecond }},
		{"login.soft_max", func(s *authSettings) { s.Login.SoftMax = dur(9 * time.Second) }, func(w *auth.Settings) { w.Login.SoftMax = 9 * time.Second }},
		{"login.saturated_rate", func(s *authSettings) { s.Login.SaturatedRate = 4.5 }, func(w *auth.Settings) { w.SaturatedRate = 4.5 }},
		{"login.saturated_burst", func(s *authSettings) { s.Login.SaturatedBurst = 20 }, func(w *auth.Settings) { w.SaturatedBurst = 20 }},
		{"invite.ttl", func(s *authSettings) { s.Invite.TTL = dur(5 * time.Hour) }, func(w *auth.Settings) { w.InviteTTL = 5 * time.Hour }},
		{"temp_login.ttl", func(s *authSettings) { s.TempLogin.TTL = dur(4 * time.Minute) }, func(w *auth.Settings) { w.TempLoginTTL = 4 * time.Minute }},
	} {
		s := *base
		s.Bootstrap.ReadonlyKey.Scopes = slices.Clone(base.Bootstrap.ReadonlyKey.Scopes)
		c.set(&s)
		want := authSettingsFrom(base, false, false, "gotham")
		c.exp(&want)
		if got := authSettingsFrom(&s, false, false, "gotham"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.key, got, want)
		}
	}
}

// TestAuthSettings_CookieNameTemplate: an empty cookie_name is the app's
// <AppID>_session; with no app ID either it stays empty, so auth's fallback
// applies; a set name wins over the template.
func TestAuthSettings_CookieNameTemplate(t *testing.T) {
	s := declaredAuth(t)
	if got := authSettingsFrom(s, false, false, "gotham").SessionCookie; got != "gotham_session" {
		t.Errorf("empty name, app gotham: %q", got)
	}
	if got := authSettingsFrom(s, false, false, "").SessionCookie; got != "" {
		t.Errorf("empty name, no app ID: %q, want empty (auth's fallback)", got)
	}
	s.Session.CookieName = "belfry"
	if got := authSettingsFrom(s, false, false, "gotham").SessionCookie; got != "belfry" {
		t.Errorf("set name: %q", got)
	}
}

// TestAuthSettings_DerivedKeysFollowProvenance: owner.required and
// disabled_allow_remote, read through a real config store. At their declared
// default they map to nil (auth derives: the owner verifier, the app's
// Options.DefaultDisabledAllowRemote); set by an operator layer, to the value
// set, false included. Mapping a declared false to &false would turn the
// owner guard off where a verifier is configured.
func TestAuthSettings_DerivedKeysFollowProvenance(t *testing.T) {
	for _, c := range []struct {
		name          string
		env           []string
		owner, remote *bool
	}{
		{"declared defaults", nil, nil, nil},
		{"owner.required=false set", []string{"GOTHAM_CORE_AUTH_OWNER_REQUIRED=false"}, new(false), nil},
		{"owner.required=true set", []string{"GOTHAM_CORE_AUTH_OWNER_REQUIRED=true"}, new(true), nil},
		{"disabled_allow_remote=false set", []string{"GOTHAM_CORE_AUTH_DISABLED_ALLOW_REMOTE=false"}, nil, new(false)},
		{"disabled_allow_remote=true set", []string{"GOTHAM_CORE_AUTH_DISABLED_ALLOW_REMOTE=true"}, nil, new(true)},
	} {
		st, s := declaredAuthStore(t, c.env...)
		read, err := authSettingsReader(func() *authSettings { return s }, configExplicit(st), "gotham")
		if err != nil {
			t.Fatal(err)
		}
		got := read()
		if !reflect.DeepEqual(got.OwnerRequired, c.owner) {
			t.Errorf("%s: OwnerRequired = %v, want %v", c.name, ptrStr(got.OwnerRequired), ptrStr(c.owner))
		}
		if !reflect.DeepEqual(got.DisabledAllowRemote, c.remote) {
			t.Errorf("%s: DisabledAllowRemote = %v, want %v", c.name, ptrStr(got.DisabledAllowRemote), ptrStr(c.remote))
		}
	}
}

func ptrStr(p *bool) string {
	if p == nil {
		return "nil"
	}
	if *p {
		return "&true"
	}
	return "&false"
}

// TestAuthSettings_ConfigExplicit: a key at its declared default is not
// explicit, one an operator layer sets is, and a key the store does not
// declare is not.
func TestAuthSettings_ConfigExplicit(t *testing.T) {
	st, _ := declaredAuthStore(t, "GOTHAM_CORE_AUTH_OWNER_REQUIRED=false")
	ex := configExplicit(st)
	if !ex(authOwnerRequiredKey) {
		t.Error("an env-set owner.required is not explicit")
	}
	if ex(authDisabledAllowRemoteKey) {
		t.Error("a defaulted disabled_allow_remote is explicit")
	}
	if ex("core.auth.no_such_key") {
		t.Error("an undeclared key is explicit")
	}
}

// TestAuthSettings_ProvenanceIsReadOnceAtBoot: both derived keys are
// restart-class, so the reader takes their provenance when it is built; a
// later change (a stored override still pending a restart) does not move
// the mapping to the other side before the restart.
func TestAuthSettings_ProvenanceIsReadOnceAtBoot(t *testing.T) {
	s := declaredAuth(t)
	explicitNow := false
	read, err := authSettingsReader(func() *authSettings { return s },
		func(config.Path) bool { return explicitNow }, "gotham")
	if err != nil {
		t.Fatal(err)
	}
	explicitNow = true
	if got := read(); got.OwnerRequired != nil || got.DisabledAllowRemote != nil {
		t.Errorf("provenance changed after boot moved the mapping: owner %s, remote %s",
			ptrStr(got.OwnerRequired), ptrStr(got.DisabledAllowRemote))
	}
}

// TestAuthSettings_ReaderIsLive: the reader reads the section on every call,
// so a live key (session.ttl) reaches auth without a rebuild.
func TestAuthSettings_ReaderIsLive(t *testing.T) {
	cur := declaredAuth(t)
	read, err := authSettingsReader(func() *authSettings { return cur }, func(config.Path) bool { return false }, "gotham")
	if err != nil {
		t.Fatal(err)
	}
	if got := read().SessionTTL; got != auth.DefaultSessionTTL {
		t.Fatalf("SessionTTL %v", got)
	}
	next := *cur
	next.Session.TTL = dur(90 * time.Minute)
	cur = &next
	if got := read().SessionTTL; got != 90*time.Minute {
		t.Errorf("SessionTTL after a live change = %v, want 1h30m", got)
	}
}

// TestAuthSettings_ReaderRefusesNilInputs: without the section's value every
// operator value would be ignored, and without the provenance read the
// derived keys would be guessed; both refuse.
func TestAuthSettings_ReaderRefusesNilInputs(t *testing.T) {
	s := declaredAuth(t)
	if _, err := authSettingsReader(nil, func(config.Path) bool { return false }, "gotham"); err == nil {
		t.Error("a reader without the section's value built")
	}
	if _, err := authSettingsReader(func() *authSettings { return s }, nil, "gotham"); err == nil {
		t.Error("a reader without the provenance read built")
	}
}

// TestAuthSettings_NilSectionIsAuthDefaults: a nil section value (a config
// handle never returns one) maps to auth's defaults with the cookie template
// filled, never to a zero section, which would read cookie_host_prefix as
// false and drop the __Host- prefix.
func TestAuthSettings_NilSectionIsAuthDefaults(t *testing.T) {
	got := authSettingsFrom(nil, true, true, "gotham")
	if want := (auth.Settings{SessionCookie: "gotham_session"}); !reflect.DeepEqual(got, want) {
		t.Errorf("nil section maps to %+v, want %+v", got, want)
	}
}

// TestAuthSettings_EverySectionKeyIsAccountedFor: every key the core.auth
// section declares is either mapped (a cell above) or listed as having no
// auth.Settings field, so a key added to the section fails here until the
// mapping takes it or the list says why not.
func TestAuthSettings_EverySectionKeyIsAccountedFor(t *testing.T) {
	mapped := []string{
		"mode", "disabled_allow_remote", "session.ttl", "session.remember_ttl", "session.cookie_name",
		"session.cookie_host_prefix", "password.min_length", "apikey.default_ttl", "apikey.max_ttl",
		"bootstrap.enabled", "bootstrap.key_ttl", "bootstrap.readonly_key.enabled", "bootstrap.readonly_key.scopes",
		"setup_requires_token", "extension_max_age", "login.max_failures_per_source", "login.window",
		"login.soft_threshold", "login.soft_step", "login.soft_max", "login.saturated_rate", "login.saturated_burst",
		"invite.ttl", "temp_login.ttl", "owner.required",
	}
	noField := []string{
		"session.idle_timeout",   // auth has no idle timeout
		"bootstrap.token_ttl",    // auth uses DefaultBootstrapTokenTTL
		"oidc.redirect_base_url", // the OIDC resolver's, not auth.Settings
		"oidc.providers",
		"oidc.<id>.client_secret", // a secret key per provider entry, the OIDC resolver's
		"schedules.session_prune", // the tasks flip's schedules
		"schedules.onetime_prune",
	}
	accounted := map[string]bool{}
	for _, k := range append(mapped, noField...) {
		accounted["core.auth."+k] = true
	}
	seen := map[string]bool{}
	for path := range allLeaves(t) {
		if !strings.HasPrefix(path, "core.auth.") {
			continue
		}
		seen[path] = true
		if !accounted[path] {
			t.Errorf("core.auth key %s is neither mapped nor listed as having no Settings field", path)
		}
	}
	for k := range accounted {
		if !seen[k] {
			t.Errorf("listed key %s is not declared by the core.auth section", k)
		}
	}
}
