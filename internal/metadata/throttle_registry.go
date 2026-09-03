// file: internal/metadata/throttle_registry.go
// version: 1.0.0
// guid: 0317e94d-7c76-4a4b-97a3-3cdfbf327945
// last-edited: 2026-09-03

package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// ErrProviderThrottled is returned instead of making a call to a provider that
// is currently held off. It is deliberately NOT classifiable by
// ClassifyProviderError: a throttle must never extend itself.
var ErrProviderThrottled = errors.New("provider is throttled: skipping call until the hold expires")

// ThrottleRegistry holds the live provider throttles.
//
// 🔴 THE GATE IS EVALUATED AT CALL TIME, NOT WHEN THE CHAIN IS BUILT.
//
// This is the whole reason the registry exists rather than a filter inside
// buildSourceChainFromConfig. metafetch.BuildSourceChain is MEMOIZED on a
// fingerprint of the CONFIG (service_search.go); it rebuilds only when a
// metadata-source setting changes. A build-time filter would therefore be
// latched to the wrong clock in both directions:
//
//   - a provider throttled AFTER the chain was built stays in the cached chain
//     and keeps getting called -- exactly the 22,934-error run this feature is
//     meant to prevent, with the throttle sitting there doing nothing;
//   - an EXPIRED throttle stays excluded until someone edits config.
//
// A control whose lifetime is decided by the wrong clock is the same failure
// this whole feature exists to fix. So ProtectedSource consults the registry on
// every call, and chain filtering (UnthrottledSources) is a per-run convenience
// for reporting and the all-throttled early stop, never the enforcement point.
type ThrottleRegistry struct {
	mu      sync.RWMutex
	entries map[string]ProviderThrottle
	store   ThrottleStore
	// warnedNoStore keeps the "throttles will not survive a restart" warning to
	// one line rather than one per transition.
	warnedNoStore bool
}

var defaultThrottles = NewThrottleRegistry()

// DefaultThrottleRegistry is the process-wide registry. The user asked for a
// GLOBAL timer per provider -- one hold that every operation respects -- so a
// single process-wide instance is the design, not an accident.
func DefaultThrottleRegistry() *ThrottleRegistry { return defaultThrottles }

// NewThrottleRegistry returns an empty, unpersisted registry.
func NewThrottleRegistry() *ThrottleRegistry {
	return &ThrottleRegistry{entries: make(map[string]ProviderThrottle)}
}

// AttachStore installs persistence and loads whatever is already on disk,
// returning the number of still-active holds restored.
//
// Restarts are the reason this exists: prod restarted 146 times in 30 days, and
// an in-memory-only hold forgets a 4-hour quota block on every one of them and
// resumes hammering.
func (r *ThrottleRegistry) AttachStore(store ThrottleStore) (int, error) {
	if store == nil {
		return 0, nil
	}
	payloads, err := store.LoadProviderThrottles()
	if err != nil {
		return 0, err
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
	restored := 0
	for id, raw := range payloads {
		var t ProviderThrottle
		if uerr := json.Unmarshal(raw, &t); uerr != nil {
			slog.Warn("dropping unreadable provider throttle row", "provider", id, "error", uerr)
			if derr := store.DeleteProviderThrottle(id); derr != nil {
				slog.Warn("could not delete unreadable provider throttle", "provider", id, "error", derr)
			}
			continue
		}
		if t.ProviderID == "" {
			t.ProviderID = id
		}
		if !t.Active(now) {
			// Expired while we were down. Drop it rather than leave a row that
			// every load has to filter forever.
			if derr := store.DeleteProviderThrottle(id); derr != nil {
				slog.Warn("could not clear expired provider throttle", "provider", id, "error", derr)
			}
			continue
		}
		r.entries[t.ProviderID] = t
		restored++
	}
	return restored, nil
}

// Get returns the ACTIVE hold for a provider. An expired entry reports false
// and is swept, so expiry needs no timer.
func (r *ThrottleRegistry) Get(providerID string) (ProviderThrottle, bool) {
	now := time.Now()
	r.mu.RLock()
	t, ok := r.entries[providerID]
	r.mu.RUnlock()
	if !ok {
		return ProviderThrottle{}, false
	}
	if t.Active(now) {
		return t, true
	}
	r.sweep(providerID)
	return ProviderThrottle{}, false
}

// Throttled reports whether calls to this provider should be skipped.
func (r *ThrottleRegistry) Throttled(providerID string) bool {
	_, ok := r.Get(providerID)
	return ok
}

// sweep drops an expired entry from memory and from the store.
func (r *ThrottleRegistry) sweep(providerID string) {
	r.mu.Lock()
	t, ok := r.entries[providerID]
	if ok && !t.Active(time.Now()) {
		delete(r.entries, providerID)
	} else {
		ok = false
	}
	store := r.store
	r.mu.Unlock()
	if ok && store != nil {
		if err := store.DeleteProviderThrottle(providerID); err != nil {
			slog.Warn("could not clear expired provider throttle", "provider", providerID, "error", err)
		}
	}
}

// List returns every active hold, provider-id ordered for a stable API response.
func (r *ThrottleRegistry) List() []ProviderThrottle {
	now := time.Now()
	r.mu.RLock()
	out := make([]ProviderThrottle, 0, len(r.entries))
	var expired []string
	for id, t := range r.entries {
		if t.Active(now) {
			out = append(out, t)
		} else {
			expired = append(expired, id)
		}
	}
	r.mu.RUnlock()
	for _, id := range expired {
		r.sweep(id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderID < out[j].ProviderID })
	return out
}

// RecordFailure classifies a provider error and, when it warrants one, installs
// a hold. It returns the hold and true only when a NEW hold was written.
//
// Called on the failing call itself, not when the circuit breaker trips. That
// ordering is load-bearing: ProtectedSource returns ErrCircuitOpen from
// AllowRequest BEFORE touching the source, and ErrCircuitOpen classifies as
// "not the provider's fault" -- so a record-on-trip design would classify the
// breaker's own sentinel forever and never write a throttle at all.
func (r *ThrottleRegistry) RecordFailure(providerID string, err error) (ProviderThrottle, bool) {
	if providerID == "" {
		return ProviderThrottle{}, false
	}
	reason, hold, ok := ClassifyProviderError(err)
	if !ok || hold <= 0 {
		return ProviderThrottle{}, false
	}
	now := time.Now()
	t := ProviderThrottle{
		ProviderID: providerID,
		Reason:     reason,
		Detail:     truncateDetail(err.Error()),
		Until:      now.Add(hold),
		SetAt:      now,
	}

	r.mu.Lock()
	// An existing ACTIVE hold is only ever replaced by a STRICTLY LONGER one of
	// a different kind.
	//
	// Two failure modes this guards, in opposite directions:
	//
	//   - Same reason must NOT re-arm. A bulk run has calls already in flight
	//     when the first 429 lands; each one that comes back would push the
	//     deadline out by another 4 hours from now, and a hold that is refreshed
	//     by its own aftermath never expires.
	//   - A milder reason must NOT shorten. A single 503 arriving during a
	//     4-hour quota hold would otherwise cut it to 30 minutes and hand the
	//     hammering straight back.
	//
	// A day quota discovered while a 15-minute burst hold is in force DOES
	// replace it -- that is the one direction worth acting on.
	if cur, exists := r.entries[providerID]; exists && cur.Active(now) {
		if cur.Reason == reason || !t.Until.After(cur.Until) {
			r.mu.Unlock()
			return cur, false
		}
	}
	r.entries[providerID] = t
	store := r.store
	warn := store == nil && !r.warnedNoStore
	if warn {
		r.warnedNoStore = true
	}
	r.mu.Unlock()

	slog.Warn("provider throttled",
		"provider", providerID, "reason", string(reason),
		"hold", hold.String(), "until", t.Until.Format(time.RFC3339),
		"detail", t.Detail)

	if store != nil {
		if payload, merr := json.Marshal(t); merr != nil {
			slog.Warn("could not marshal provider throttle", "provider", providerID, "error", merr)
		} else if serr := store.SaveProviderThrottle(providerID, payload); serr != nil {
			slog.Warn("could not persist provider throttle; it will be lost on restart",
				"provider", providerID, "error", serr)
		}
	} else if warn {
		slog.Warn("no throttle store attached; provider throttles will NOT survive a restart")
	}
	return t, true
}

// RecordSuccess releases a hold after a call actually succeeded. This is how a
// manual single-book lookup that bypasses the throttle also ENDS it: proof of
// life beats a timer.
func (r *ThrottleRegistry) RecordSuccess(providerID string) {
	r.mu.RLock()
	_, held := r.entries[providerID]
	r.mu.RUnlock()
	if !held {
		return
	}
	if err := r.Clear(providerID); err != nil {
		slog.Warn("could not clear provider throttle after a successful call", "provider", providerID, "error", err)
		return
	}
	slog.Info("provider throttle released after a successful call", "provider", providerID)
}

// Clear removes one hold (the manual reset).
func (r *ThrottleRegistry) Clear(providerID string) error {
	r.mu.Lock()
	delete(r.entries, providerID)
	store := r.store
	r.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.DeleteProviderThrottle(providerID)
}

// ClearAll removes every hold, returning how many were active.
func (r *ThrottleRegistry) ClearAll() (int, error) {
	r.mu.Lock()
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	r.entries = make(map[string]ProviderThrottle)
	store := r.store
	r.mu.Unlock()

	if store == nil {
		return len(ids), nil
	}
	var firstErr error
	for _, id := range ids {
		if err := store.DeleteProviderThrottle(id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return len(ids), firstErr
}

// truncateDetail bounds the stored provider message.
func truncateDetail(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// --- bypass -----------------------------------------------------------------

type throttleBypassKey struct{}

// WithThrottleBypass marks a context as exempt from provider throttling.
//
// The user's rule: automatic and bulk paths respect the global hold; an
// explicit, user-initiated lookup on ONE book goes through anyway. A context
// value carries that per-call rather than per-source, so the same memoized,
// process-wide chain serves both without a second set of clients -- and without
// widening MetadataSource, whose implementations all already take a ctx.
func WithThrottleBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, throttleBypassKey{}, true)
}

// ThrottleBypassed reports whether this call may ignore provider throttles.
func ThrottleBypassed(ctx context.Context) bool {
	v, _ := ctx.Value(throttleBypassKey{}).(bool)
	return v
}

// --- chain helpers ----------------------------------------------------------

// UnthrottledSources returns the members of a chain that are not currently held
// off, plus the ids that were dropped.
//
// Enforcement lives in ProtectedSource; this is for the callers that need to
// KNOW -- to report which providers were skipped, and to detect the
// all-throttled case before walking the library.
func UnthrottledSources(chain []MetadataSource) (live []MetadataSource, skipped []string) {
	r := DefaultThrottleRegistry()
	for _, src := range chain {
		id := ProviderIDOf(src)
		if id != "" && r.Throttled(id) {
			skipped = append(skipped, id)
			continue
		}
		live = append(live, src)
	}
	return live, skipped
}

// ThrottleSummary renders active holds for an operation log line.
func ThrottleSummary(ids []string) string {
	r := DefaultThrottleRegistry()
	parts := make([]string, 0, len(ids))
	now := time.Now()
	for _, id := range ids {
		if t, ok := r.Get(id); ok {
			parts = append(parts, id+" ("+string(t.Reason)+", "+t.Remaining(now).Round(time.Minute).String()+" left)")
		}
	}
	return joinComma(parts)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
