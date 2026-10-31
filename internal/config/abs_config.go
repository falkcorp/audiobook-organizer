// file: internal/config/abs_config.go
// version: 1.1.1
// guid: d5f92b03-6a17-4c48-b9e0-2751c8ad3f6b
// last-edited: 2026-10-30

package config

// registerABSDefaults declares the Audiobookshelf-compatible sync API's viper keys and
// their environment bindings. Called from InitConfig.
//
// Every key needs an explicit viper.BindEnv: this codebase does NOT enable
// viper.AutomaticEnv, so an unbound key silently ignores the operator's environment
// variable — the exact failure mode that made ABS_JWT_SECRET-style settings vanish in
// past incidents.
//
// The defaults encode three decisions from the design spec that are easy to get wrong:
//
//   - abs_api_enabled is FALSE. The whole surface is feature-flagged off, so pulling
//     this change into an existing deployment registers nothing and changes nothing.
//   - the access-token TTL is 720h = 30 DAYS, not one hour (§1.6 item 1). Many ABS
//     clients implement no refresh at all; a 1h access token logs them out hourly.
//   - the refresh TTL is the SESSION IDLE TTL, 1440h = 60 days, and must stay longer
//     than the access TTL. Refresh and authenticated use slide the session's expiry
//     forward, so the client's 401 at access-token expiry lands while the session can
//     still be refreshed. With both at 30d (the default until 2026-10-19) every
//     session died at the same instant as its token and the app was logged out.
//     absauth.Load clamps the access TTL if an operator configures them the wrong
//     way round.
//   - abs_session_max_ttl is an optional absolute cap on a session's lifetime from
//     login. "0" (the default) means off.
//   - abs_jwt_secret has NO default and is never generated. When the ABS API is enabled
//     without it, the server fails closed at boot — an auto-generated secret would
//     invalidate every client's stored token on every restart.
func registerABSDefaults(r defaultRegistrar) {
	r.SetDefault("abs_api_enabled", false)
	r.SetDefault("abs_auth_modes", "cf,jwt")
	r.SetDefault("abs_jwt_secret", "")
	r.SetDefault("abs_access_token_ttl", "720h")
	r.SetDefault("abs_refresh_token_ttl", "1440h")
	r.SetDefault("abs_refresh_grace", "10m")
	r.SetDefault("abs_session_max_ttl", "0")
	r.SetDefault("abs_server_version", "2.36.0")
	r.SetDefault("abs_default_library_id", "b5e3a5b2-a76e-471f-b18b-915e4716d053")

	r.BindEnv("abs_api_enabled", "ABS_API_ENABLED")               //nolint:errcheck
	r.BindEnv("abs_auth_modes", "ABS_AUTH_MODES")                 //nolint:errcheck
	r.BindEnv("abs_jwt_secret", "ABS_JWT_SECRET")                 //nolint:errcheck
	r.BindEnv("abs_access_token_ttl", "ABS_ACCESS_TOKEN_TTL")     //nolint:errcheck
	r.BindEnv("abs_refresh_token_ttl", "ABS_REFRESH_TOKEN_TTL")   //nolint:errcheck
	r.BindEnv("abs_refresh_grace", "ABS_REFRESH_GRACE")           //nolint:errcheck
	r.BindEnv("abs_session_max_ttl", "ABS_SESSION_MAX_TTL")       //nolint:errcheck
	r.BindEnv("abs_server_version", "ABS_SERVER_VERSION")         //nolint:errcheck
	r.BindEnv("abs_default_library_id", "ABS_DEFAULT_LIBRARY_ID") //nolint:errcheck
}
