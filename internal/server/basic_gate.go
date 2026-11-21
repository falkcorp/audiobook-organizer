// file: internal/server/basic_gate.go
// version: 1.0.0
// guid: 8c1f5a27-d3b9-4e60-b784-1a0e9d52c6f3
// last-edited: 2026-11-19

package server

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/secrets"
)

// HEAD's Basic gate is core's stage 8 (core/httpapi/basicgate.go). This file
// is the app's side of it: the inputs books hands httpapi.LegacyMount.BasicGate
// (F3-07 e4), bound to HEAD's config until the config flip rebinds the
// password to the secret store (core.http.basic_gate.password) in
// basicGatePasswordFunc alone, and the readers /health and the startup line
// use for the gate's posture. Gin's BasicAuth middleware, which this replaced,
// is gone: no route reaches the engine without passing stage 8.

// basicGateHookForTest, when set, is a gin middleware NewServer installs
// right after perimeterSeen. The engine has no Basic gate (stage 8 is core's);
// a test sets it to run HEAD's frozen gin gate on the requests of its HEAD
// reference stack, which mark themselves (head_basic_gate_test.go). Nil in
// production.
var basicGateHookForTest gin.HandlerFunc

// BasicGateNoPasswordToken is the /health degraded token raised while the
// Basic gate is enabled with no effective password (05-health §5H.3.1). It is
// core's token, the one stage 8 itself reports with.
const BasicGateNoPasswordToken = httpapi.BasicGateNoPasswordToken

// BasicGateEnvPasswordWarning is the warn line logged while the gate takes its
// password from BASIC_AUTH_PASSWORD because the saved one is empty. It names
// the setting and the variable, never the value.
const BasicGateEnvPasswordWarning = "Basic authentication: the saved password (basic_auth_password) is empty; using BASIC_AUTH_PASSWORD in memory. Save a password in Settings."

// basicAuthPasswordEnv is the environment variable the gate falls back to when
// the saved password is empty (05-authn §11.1 item 5, Recovery).
const basicAuthPasswordEnv = "BASIC_AUTH_PASSWORD"

// basicGateRealm is the realm clients remembered their credentials for.
const basicGateRealm = "Audiobook Organizer"

// basicGateLog carries this file's diagnostics through the log-injection barrier.
var basicGateLog = logger.New("http")

// basicGateEnvWarnOnce makes the gate log BasicGateEnvPasswordWarning once per
// process, on the first resolution that falls back to the environment.
var basicGateEnvWarnOnce sync.Once

// basicGatePassword returns the password the gate compares against, given the
// stored basic_auth_password: the stored value, or, when that is empty,
// BASIC_AUTH_PASSWORD from the environment, in memory only and never written
// (05-authn §11.1 item 5, Recovery). fromEnv reports the fallback; "" means no
// password.
//
// A stored value counts as set when it is not "" and is compared exactly as
// saved, blanks included: a saved password wins over the env value, and the
// stored value's semantics are not this slice's to change (05-health §5H.3.1
// says "empty"). An env value that is empty or only blanks counts as unset
// (09 §9.1 "Precedence") and is compared trimmed. The env value is never
// assigned into config.AppConfig or any store: persisting it would turn a
// recovery into a saved credential.
func basicGatePassword(stored string) (pass string, fromEnv bool) {
	if stored != "" {
		return stored, false
	}
	if env := strings.TrimSpace(os.Getenv(basicAuthPasswordEnv)); env != "" {
		return env, true
	}
	return "", false
}

// basicGatePasswordFunc is stage 8's password resolver over a reader of the
// stored basic_auth_password: the effective password (basicGatePassword), or
// secrets.ErrNotSet when there is none, which stage 8 refuses on (fail
// closed). The one place that changes when the password moves to the secret
// store.
func basicGatePasswordFunc(stored func() string) httpapi.BasicGatePasswordFunc {
	return func(context.Context) (secrets.Value, error) {
		pass, fromEnv := basicGatePassword(stored())
		if pass == "" {
			return secrets.Value{}, secrets.ErrNotSet
		}
		if fromEnv {
			basicGateEnvWarnOnce.Do(func() { basicGateLog.Warn(BasicGateEnvPasswordWarning) })
		}
		return secrets.NewValue(pass), nil
	}
}

// LegacyBasicGate is what books passes as httpapi.LegacyMount.BasicGate: core
// puts the Basic gate around the whole router with these inputs: the
// effective switch and username from one config snapshot, the password from
// the same config, and HEAD's realm.
func LegacyBasicGate() *httpapi.BasicGateOptions {
	return &httpapi.BasicGateOptions{
		Settings: func() httpapi.BasicGateSettings {
			cfg := config.Snapshot()
			return httpapi.BasicGateSettings{Enabled: cfg.BasicAuthEnabled, Username: cfg.BasicAuthUsername}
		},
		Password: basicGatePasswordFunc(func() string { return config.Snapshot().BasicAuthPassword }),
		Realm:    basicGateRealm,
	}
}

// basicGateNoPassword reports whether the gate is enabled with no effective
// password: then every non-exempt route answers 401 and /health shows
// BasicGateNoPasswordToken. It reads one config snapshot.
func basicGateNoPassword() bool {
	cfg := config.Snapshot()
	if !cfg.BasicAuthEnabled {
		return false
	}
	pass, _ := basicGatePassword(cfg.BasicAuthPassword)
	return pass == ""
}

// basicGateUsesEnvPassword reports whether the gate is enabled and takes its
// password from BASIC_AUTH_PASSWORD because the saved one is empty (the
// in-memory recovery). The startup log uses it to choose the warn line over
// the error line.
func basicGateUsesEnvPassword() bool {
	cfg := config.Snapshot()
	if !cfg.BasicAuthEnabled {
		return false
	}
	_, fromEnv := basicGatePassword(cfg.BasicAuthPassword)
	return fromEnv
}
