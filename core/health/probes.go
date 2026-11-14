// file: core/health/probes.go
// version: 1.0.1
// guid: 3e8b6a21-9f47-4d05-a2c8-1b7d4e6f9c30
// last-edited: 2026-11-13

package health

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

// StatusPayload is the body of GET /readyz and of the authenticated
// GET /api/core/v1/system/status, and the payload of the status topic: the
// readiness bit and the fixed "<component>:<reason>" tokens that say why not.
// Never Detail, durations, versions or counts (05-health §5H.3.3).
type StatusPayload struct {
	Ready    bool     `json:"ready"`
	NotReady []string `json:"not_ready"`
}

// Status is the registry's current StatusPayload with its tokens made public
// (PublicTokens).
func (r *Registry) Status() StatusPayload {
	ready, nr := r.Ready()
	return StatusPayload{Ready: ready, NotReady: PublicTokens(nr)}
}

// SetStatusPublisher installs the sink that receives a StatusPayload whenever
// Ready() or the set of not-ready tokens changes, in the order the changes
// happened, and once with ready:false when shutdown starts. The sink runs
// under the registry's publication lock: it must not call back into the
// registry. The current state is delivered at once.
func (r *Registry) SetStatusPublisher(f func(StatusPayload)) {
	r.pubMu.Lock()
	r.sink = f
	r.last = nil
	r.observed.Store(f != nil || r.metrics != nil)
	r.pubMu.Unlock()
	r.afterChange()
}

// afterChange publishes the new status and records the ready gauge. It returns
// at once for a registry nobody observes, so an unwired registry costs a Report
// one atomic load.
func (r *Registry) afterChange() {
	if !r.observed.Load() {
		return
	}
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	p := r.Status()
	r.metrics.recordReady(p.Ready)
	if r.sink == nil {
		return
	}
	if r.last != nil && r.last.Ready == p.Ready && slices.Equal(r.last.NotReady, p.NotReady) {
		return
	}
	r.last = &p
	r.sink(p)
}

// PublicTokens returns the tokens of tokens that an unauthenticated caller may
// see, with the others generalised. A token "<component>:<reason>" is shown
// whole when its reason is a startup or readiness reason (warming, starting,
// stopping, down, unknown, a check failure, a failed startup step's token) and
// as "<component>:down" when the reason is an auth, config, backup, restore,
// storage or secrets posture token (05-health
// §5H.3.1: no posture token reaches /readyz, SEC-7). A token that does not
// match the "<component>:<reason>" grammar is dropped. The result is a new
// slice and never nil.
func PublicTokens(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		id, reason, ok := strings.Cut(t, ":")
		if !ok || !validID(ComponentID(id)) || !validReason(reason) {
			continue
		}
		if isPostureReason(reason) {
			reason = "down"
		}
		out = append(out, id+":"+reason)
	}
	return out
}

// isPostureReason reports whether reason is one of the tokens 05-health
// §5H.3.1 keeps off unauthenticated surfaces. A function with a switch, not a
// package-level set (no mutable package state, R5). The prefixes cover the
// families the spec lists (checkpoint, revocation journal, restore,
// factory-reset, config, secrets, backup, keyring and derive-key tokens); the
// exact names are the rest. TestPostureTokensGolden pins every token the
// specs name, so a token whose family is missing here fails a test rather than
// reaching /readyz.
func isPostureReason(reason string) bool {
	switch reason {
	case "storage_quarantine", "secrets_undecryptable", "secrets_keyring_not_exported",
		"auth_disabled_remote", "auto_update_paused", "operator_secret_withheld":
		return true
	}
	for _, p := range [...]string{
		"storage_", "secrets_", "revocation_journal_", "restore_", "factory_reset_", "config_", "archive_", "auth_",
		"backup_", "keyring_", "derive_key_",
	} {
		if strings.HasPrefix(reason, p) {
			return true
		}
	}
	return false
}

// LiveHandler answers GET /livez: 200 {"live":true} while the heartbeat is
// within core.health.live_stall, else 503 {"live":false}. It follows nothing
// but the heartbeat, so a Critical component that is Down never fails it.
func LiveHandler(r *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		live := r.Live()
		code := http.StatusOK
		if !live {
			code = http.StatusServiceUnavailable
		}
		writeProbe(w, code, struct {
			Live bool `json:"live"`
		}{live})
	})
}

// ReadyHandler answers GET /readyz: 200 iff startup is complete, the process
// is not shutting down, every Readiness component is Up or Degraded and no
// Critical component is Down; else 503. The body is StatusPayload with public
// tokens only.
func ReadyHandler(r *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := r.Status()
		code := http.StatusOK
		if !p.Ready {
			code = http.StatusServiceUnavailable
		}
		writeProbe(w, code, p)
	})
}

// writeProbe writes a probe body: JSON, never cached, never sniffed.
func writeProbe(w http.ResponseWriter, code int, body any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// ProbeHandlers is the two probe routes as exact method-and-path patterns for a
// net/http mux ("GET /livez", "GET /readyz"). The path is exact: "/livez/" and
// "/READYZ" match nothing and so 404 (never the SPA's 200 text/html, FE2-6).
func ProbeHandlers(r *Registry) map[string]http.Handler {
	return map[string]http.Handler{
		"GET /livez":  LiveHandler(r),
		"GET /readyz": ReadyHandler(r),
	}
}
