// file: internal/config/abs_config.go
// version: 1.0.0
// guid: d5f92b03-6a17-4c48-b9e0-2751c8ad3f6b
// last-edited: 2026-07-30

package config

import "github.com/spf13/viper"

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
//   - the token TTLs are 720h = 30 DAYS, not one hour (§1.6 item 1). Many ABS clients
//     implement no refresh at all; a 1h access token logs them out hourly.
//   - abs_jwt_secret has NO default and is never generated. When the ABS API is enabled
//     without it, the server fails closed at boot — an auto-generated secret would
//     invalidate every client's stored token on every restart.
func registerABSDefaults() {
	viper.SetDefault("abs_api_enabled", false)
	viper.SetDefault("abs_auth_modes", "cf,jwt")
	viper.SetDefault("abs_jwt_secret", "")
	viper.SetDefault("abs_access_token_ttl", "720h")
	viper.SetDefault("abs_refresh_token_ttl", "720h")
	viper.SetDefault("abs_refresh_grace", "10m")
	viper.SetDefault("abs_server_version", "2.36.0")
	viper.SetDefault("abs_default_library_id", "b5e3a5b2-a76e-471f-b18b-915e4716d053")

	viper.BindEnv("abs_api_enabled", "ABS_API_ENABLED")               //nolint:errcheck
	viper.BindEnv("abs_auth_modes", "ABS_AUTH_MODES")                 //nolint:errcheck
	viper.BindEnv("abs_jwt_secret", "ABS_JWT_SECRET")                 //nolint:errcheck
	viper.BindEnv("abs_access_token_ttl", "ABS_ACCESS_TOKEN_TTL")     //nolint:errcheck
	viper.BindEnv("abs_refresh_token_ttl", "ABS_REFRESH_TOKEN_TTL")   //nolint:errcheck
	viper.BindEnv("abs_refresh_grace", "ABS_REFRESH_GRACE")           //nolint:errcheck
	viper.BindEnv("abs_server_version", "ABS_SERVER_VERSION")         //nolint:errcheck
	viper.BindEnv("abs_default_library_id", "ABS_DEFAULT_LIBRARY_ID") //nolint:errcheck
}
