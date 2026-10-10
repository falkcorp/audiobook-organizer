// file: internal/startup/steps.go
// version: 1.0.0
// guid: b4c26097-4214-45ff-a1a8-d434f6b64fb1
// last-edited: 2026-10-10

// Package startup holds the one reviewed table that says, for every step of
// server startup, whether its failure stops the process (Fatal) or leaves the
// server up and reports a fixed token in /health's `degraded` list
// (Degraded).
//
// It is a leaf package (stdlib only, layer 0 in internal/arch): most steps (opening the store, telemetry, the secure
// state directory, the encryption key, loading persisted config) run in cmd
// before any *server.Server exists, so the table and the token set cannot
// live in package server. cmd builds one *Steps, runs its steps through Run,
// and hands the same value to the server through ServerConfig.StartupSteps.
//
// Every Run call names its step with a string literal that must be a row of
// Table, and every row must be named by some Run call. steps_test.go enforces
// both with an AST scan of cmd/ and internal/, so a new startup step cannot
// be added without a reviewed class, and a removed one cannot leave a dead row.
package startup

import (
	"fmt"
	"log"
	"sort"
	"sync"
)

// Class says what a failed step does to startup.
type Class int

const (
	// Fatal: the error is returned and the process stops before it serves.
	Fatal Class = iota + 1
	// Degraded: the error is logged at error level, the step's token is
	// recorded for /health, and startup continues.
	Degraded
)

// String renders the class for logs and test messages.
func (c Class) String() string {
	switch c {
	case Fatal:
		return "fatal"
	case Degraded:
		return "degraded"
	}
	return fmt.Sprintf("Class(%d)", int(c))
}

// Step is one row of the startup table.
type Step struct {
	// Name is the literal every Run call for this step passes.
	Name string
	// Token is the fixed machine string /health reports while this step is
	// degraded. Empty for Fatal steps, which never report one.
	Token string
	Class Class
	// Reason is why the step has this class. It is the review record: a
	// change of class must change the reason in the same diff.
	Reason string
}

// Token values reported in /health's `degraded` list. Fixed strings, never
// error text: /health is unauthenticated.
const (
	TokenMetricsDisabled = "metrics_disabled"
	TokenSearchIndex     = "search_index"
)

// Table classifies every startup step, in the order startup runs them.
//
// Where each step lives (verified 2026-10-10):
//   - initializeStore:             database.InitializeStore, internal/database/store.go:1501; run in cmd/root.go serveCmd
//   - InitOTEL:                    telemetry.InitOTEL, internal/telemetry/telemetry.go:42 (tracing is already non-fatal inside it)
//   - EnsureSecureStateDir:        config.EnsureSecureStateDir, internal/config/state_dir.go:133
//   - guardAgainstKeyRegeneration: cmd/root.go guardAgainstKeyRegeneration
//   - initEncryption:              database.InitEncryption, internal/database/settings.go:109
//   - loadConfigFromDB:            config.LoadConfigFromDatabase, internal/config/persistence.go:698
//   - config.Validate:             (*config.Config).Validate, internal/config/config.go:3510
//   - opRegistrationGate:          internal/server/server_lifecycle.go (*Server).opRegistrationGate
//   - container.Start:             internal/server/server_lifecycle.go (*Server).Start
//   - searchindex:                 search.(*IndexService).Start, internal/search/register.go:41 (logs and leaves Index() nil)
//   - http:                        internal/server/server_lifecycle.go (*Server).configureAndStartHTTP
var Table = []Step{
	{Name: "initializeStore", Class: Fatal,
		Reason: "nothing works without the store"},
	{Name: "InitOTEL", Class: Degraded, Token: TokenMetricsDisabled,
		Reason: "observability must not take the library down; only the metrics half can fail here, tracing already degrades inside InitOTEL"},
	{Name: "EnsureSecureStateDir", Class: Fatal,
		Reason: "the secrets location must be sane before a key is read or written"},
	{Name: "guardAgainstKeyRegeneration", Class: Fatal,
		Reason: "regenerating the key would orphan every encrypted setting"},
	{Name: "initEncryption", Class: Fatal,
		Reason: "without the key, secrets are unreadable and a save would write them under the wrong key"},
	{Name: "loadConfigFromDB", Class: Fatal,
		Reason: "SaveConfigToDatabase persists the WHOLE config struct as config_blob, so a server running on defaults overwrites the stored config on its first settings save"},
	{Name: "config.Validate", Class: Fatal,
		Reason: "bad config must not serve"},
	{Name: "opRegistrationGate", Class: Fatal,
		Reason: "a hole in the operations registry makes ops vanish silently (existing behaviour)"},
	{Name: "container.Start", Class: Fatal,
		Reason: "service start failures roll back the started services (existing behaviour)"},
	{Name: "searchindex", Class: Degraded, Token: TokenSearchIndex,
		Reason: "the server already runs without Bleve; search falls back, nothing is written wrong"},
	{Name: "http", Class: Fatal,
		Reason: "nothing to serve; a bind failure inside the listener goroutine exits the process"},
}

// Lookup returns the row named name.
func Lookup(name string) (Step, bool) {
	for _, st := range Table {
		if st.Name == name {
			return st, true
		}
	}
	return Step{}, false
}

// Steps runs startup steps against Table and holds the set of degraded
// tokens. The zero value is not usable; use New. Safe for concurrent use:
// /health reads Tokens while startup may still be recording.
type Steps struct {
	mu       sync.Mutex
	degraded map[string]struct{}
	errorLog func(format string, args ...any)
}

// New returns an empty Steps that logs degraded failures through the
// standard library logger until SetErrorLog installs the app's logger. The
// default exists so a degraded failure is never swallowed unlogged; the
// package cannot import internal/logger and stay a leaf.
func New() *Steps {
	return &Steps{
		degraded: map[string]struct{}{},
		errorLog: func(format string, args ...any) { log.Printf("ERROR [startup] "+format, args...) },
	}
}

// SetErrorLog sets the printf-style function Run reports degraded failures
// through (cmd passes internal/logger's Error). Nil is ignored. Returns st so
// it chains off New.
func (st *Steps) SetErrorLog(f func(format string, args ...any)) *Steps {
	if f == nil {
		return st
	}
	st.mu.Lock()
	st.errorLog = f
	st.mu.Unlock()
	return st
}

// Run runs the step called name and applies its class to fn's error.
//
//   - Fatal: fn's error is returned unchanged; no token is recorded.
//   - Degraded: fn's error is logged at error level (SetErrorLog), the step's
//     token is recorded, and Run returns nil.
//
// A name missing from Table fails closed: fn is not run and an error is
// returned. The AST test makes that unreachable for literal names.
func (st *Steps) Run(name string, fn func() error) error {
	step, ok := Lookup(name)
	if !ok {
		return fmt.Errorf("startup: step %q is not in startup.Table", name)
	}
	err := fn()
	if err == nil {
		return nil
	}
	if step.Class != Degraded {
		return err
	}
	st.mu.Lock()
	st.degraded[step.Token] = struct{}{}
	logf := st.errorLog
	st.mu.Unlock()
	logf("startup step %s failed, continuing degraded (%s): %v", step.Name, step.Token, err)
	return nil
}

// Clear removes token from the degraded set. For a degraded step whose
// failure is later repaired by a retry path.
func (st *Steps) Clear(token string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.degraded, token)
}

// Tokens returns the degraded tokens, sorted, as a fresh slice. A nil
// *Steps has none.
func (st *Steps) Tokens() []string {
	if st == nil {
		return nil
	}
	st.mu.Lock()
	out := make([]string, 0, len(st.degraded))
	for tok := range st.degraded {
		out = append(out, tok)
	}
	st.mu.Unlock()
	sort.Strings(out)
	return out
}
