// file: core/integrations/providerhttp/holdtable.go
// version: 1.1.0
// guid: 5e7ac18e-dc95-4438-b438-0ad09045a2f0
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"iter"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// storeCtx is a context for one store write: it keeps ctx's values but not its
// cancellation, because a caller that gave up must not lose the hold or the
// count its attempt earned, and is bounded so a stuck store cannot hang the
// transport.
func (r *Registry) storeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

// holdRetryEvery is how often a Registry whose store could not be read at boot
// tries again (N5): often enough that a store that was down for a minute is
// picked up, rarely enough that a store that is down costs one read per period
// and not one per request.
const holdRetryEvery = 30 * time.Second

// ensureState loads the persisted holds and scope mappings if they have not
// been read yet. New calls it once; a store that was unreadable then is tried
// again, at most once per holdRetryEvery and by one caller at a time (the
// others go on without waiting), from every path that reads the table, until a
// read succeeds. Holds fail open meanwhile (M2.5): the table holds what this
// process placed.
func (r *Registry) ensureState() {
	if r.stateLoaded.Load() {
		return
	}
	if !r.loadMu.TryLock() {
		return
	}
	defer r.loadMu.Unlock()
	if r.stateLoaded.Load() {
		return
	}
	now := r.now()
	if !r.nextLoad.IsZero() && now.Before(r.nextLoad) {
		return
	}
	if !r.loadState() {
		r.nextLoad = now.Add(holdRetryEvery)
		return
	}
	r.stateLoaded.Store(true)
	if r.stateRetried {
		r.log.Info("providerhttp: the persisted holds were read after an earlier failure")
	}
}

// loadState reads the persisted holds into the table (an expired one is
// dropped, one beyond the maximum is clamped: HEAD's maxThrottleHold rule) and
// the persisted ref-to-scope mappings into refScope. A hold already in the
// table (placed since boot, before this read succeeded) is kept when it
// outlasts the persisted one. It reports whether the store could be read; an
// unreadable store leaves the table as it is, with one warning.
func (r *Registry) loadState() bool {
	ctx, cancel := r.storeCtx(context.Background())
	defer cancel()
	hs, err := r.store.Holds(ctx)
	if err == nil {
		var ss map[integrations.Ref]string
		if ss, err = r.store.Scopes(ctx); err == nil {
			r.mergeScopes(ctx, ss)
		}
	}
	if err != nil {
		r.stateRetried = true
		r.warnStore("the persisted holds could not be read; holds are kept in memory and the read is retried", err)
		return false
	}
	now := r.now()
	hmax := r.settings().HoldMax
	for _, h := range hs {
		r.adopt(ctx, h, now, hmax)
	}
	return true
}

// adopt puts one persisted hold into the table under its scope's lock. The
// store is written (to drop an expired record or persist a clamp) outside the
// table mutex.
func (r *Registry) adopt(ctx context.Context, h integrations.Hold, now time.Time, hmax time.Duration) {
	unlock := r.hlock.Lock(h.Scope)
	defer unlock()
	if !now.Before(h.Until) {
		r.hmu.Lock()
		cur, ok := r.holds[h.Scope]
		active := ok && now.Before(cur.Until)
		r.hmu.Unlock()
		if active { // a newer hold has already replaced the record
			return
		}
		// Drop the expired record; a store that cannot is no harm (the record
		// is expired and is skipped again on the next load).
		if err := r.store.PutHold(ctx, integrations.Hold{Scope: h.Scope}); err != nil {
			r.warnStore("an expired hold could not be removed from the store", err)
		}
		return
	}
	clamped := false
	if latest := now.Add(hmax); h.Until.After(latest) {
		h.Until, clamped = latest, true
	}
	r.hmu.Lock()
	if cur, ok := r.holds[h.Scope]; ok && now.Before(cur.Until) && !cur.Until.Before(h.Until) {
		r.hmu.Unlock()
		return
	}
	r.holds[h.Scope] = h
	r.hmu.Unlock()
	if clamped {
		if err := r.store.PutHold(ctx, h); err != nil {
			r.warnStore("a clamped hold could not be persisted", err)
		}
	}
}

// warnStore logs one gate-store failure per Registry (M2.5: one warning).
func (r *Registry) warnStore(msg string, err error) {
	if r.warned.CompareAndSwap(false, true) {
		r.log.Warn("providerhttp: "+msg, "error", err)
	}
}

// activeHold is scope's hold while it runs.
func (r *Registry) activeHold(scope string) (integrations.Hold, bool) {
	r.ensureState()
	now := r.now()
	r.hmu.Lock()
	defer r.hmu.Unlock()
	h, ok := r.holds[scope]
	if !ok || !now.Before(h.Until) {
		return integrations.Hold{}, false
	}
	return h, true
}

// The hold table is guarded by two locks that never nest the wrong way round:
// hlock serialises everything that touches ONE scope's hold (the read of the
// table, the decision and the store write), and hmu guards the map itself and
// is held only for the map operation, never across a store call. A stuck store
// therefore stalls the writers of its own scope and nobody else: admission
// (activeHold), Holds, HoldFor and the other scopes' writers keep going (N4).
// The order is always hlock, then hmu.

// placeHold stands scope off for d (clamped to max), keeping a longer hold
// already in place. The table and the store agree because one scope's writers
// are serialised; a store write that fails leaves the hold in memory (M2.5).
func (r *Registry) placeHold(ctx context.Context, ref integrations.Ref, scope string, class integrations.Class, detail string, d, hmax time.Duration) {
	now := r.now()
	until := now.Add(min(d, hmax))
	unlock := r.hlock.Lock(scope)
	defer unlock()
	r.hmu.Lock()
	if cur, ok := r.holds[scope]; ok && now.Before(cur.Until) && !cur.Until.Before(until) {
		r.hmu.Unlock()
		return
	}
	h := integrations.Hold{Scope: scope, Class: class, Detail: detail, Until: until, SetAt: now}
	r.holds[scope] = h
	r.hmu.Unlock()
	sctx, cancel := r.storeCtx(ctx)
	defer cancel()
	if err := r.store.PutHold(sctx, h); err != nil {
		r.warnStore("a hold could not be persisted; it stays in memory for "+ref.String(), err)
	}
}

// releaseIfStarted ends scope's hold when a SUCCESS ends it: only a hold that
// already existed when the succeeding attempt started (the RecordSuccess rule
// of throttle_registry.go:303-318, kept), so an attempt that was in flight
// when the hold was set cannot undo it.
func (r *Registry) releaseIfStarted(ctx context.Context, scope string, started time.Time) {
	unlock := r.hlock.Lock(scope)
	defer unlock()
	r.hmu.Lock()
	h, ok := r.holds[scope]
	r.hmu.Unlock()
	if !ok || h.SetAt.After(started) {
		return
	}
	sctx, cancel := r.storeCtx(ctx)
	defer cancel()
	if err := r.store.PutHold(sctx, integrations.Hold{Scope: scope}); err != nil {
		r.warnStore("a released hold could not be removed from the store", err)
	}
	r.hmu.Lock()
	delete(r.holds, scope)
	r.hmu.Unlock()
}

// sameHold reports whether a and b are one hold: the identity of a hold is its
// scope, class, start and end. placeHold only replaces a hold with a longer
// one, which has a new Until and SetAt, so a replacement is never the same.
func sameHold(a, b integrations.Hold) bool {
	return a.Scope == b.Scope && a.Class == b.Class && a.SetAt.Equal(b.SetAt) && a.Until.Equal(b.Until)
}

// ReleaseHoldIf ends the hold expect only while it is still the hold in force
// for expect.Scope (compared under the scope's lock, so no hold can be placed
// between the comparison and the release). It is the form for a caller that
// read the hold with HoldFor and acts on what it saw (the hold route: an
// operator releases the hold on the screen, not one set a moment later).
//
//   - the same hold is in force: it is released (store first) and the result
//     is nil;
//   - no hold is in force (never set, ended, or released by someone else): the
//     goal is met and the result is nil;
//   - a different hold is in force: nothing is released and the result is
//     ErrHoldChanged.
func (r *Registry) ReleaseHoldIf(ctx context.Context, expect integrations.Hold) error {
	r.ensureState()
	now := r.now()
	unlock := r.hlock.Lock(expect.Scope)
	defer unlock()
	r.hmu.Lock()
	cur, ok := r.holds[expect.Scope]
	r.hmu.Unlock()
	if !ok || !now.Before(cur.Until) {
		return nil
	}
	if !sameHold(cur, expect) {
		return ErrHoldChanged
	}
	if err := r.store.PutHold(ctx, integrations.Hold{Scope: expect.Scope}); err != nil {
		return fmt.Errorf("providerhttp: release hold: %w", err)
	}
	r.hmu.Lock()
	delete(r.holds, expect.Scope)
	r.hmu.Unlock()
	return nil
}

// ReleaseAuthHold releases ref's ClassAuth hold, for integrations.Options.ReleaseAuthHold:
// a Put that wrote new credentials ends the hold the old ones earned (M1.3). A
// hold of another class is left alone.
func (r *Registry) ReleaseAuthHold(ctx context.Context, ref integrations.Ref) error {
	scope := r.scopeOf(ref)
	unlock := r.hlock.Lock(scope)
	defer unlock()
	r.hmu.Lock()
	h, ok := r.holds[scope]
	r.hmu.Unlock()
	if !ok || h.Class != integrations.ClassAuth {
		return nil
	}
	if err := r.store.PutHold(ctx, integrations.Hold{Scope: scope}); err != nil {
		return fmt.Errorf("providerhttp: release auth hold: %w", err)
	}
	r.hmu.Lock()
	delete(r.holds, scope)
	r.hmu.Unlock()
	return nil
}

// Holds iterates the active holds of every scope, sorted by scope. It reads
// the gate state only: nothing is dialled and no instance is built.
func (r *Registry) Holds() iter.Seq[integrations.Hold] {
	r.ensureState()
	now := r.now()
	r.hmu.Lock()
	out := make([]integrations.Hold, 0, len(r.holds))
	for _, h := range r.holds {
		if now.Before(h.Until) {
			out = append(out, h)
		}
	}
	r.hmu.Unlock()
	slices.SortFunc(out, func(a, b integrations.Hold) int { return compareStr(a.Scope, b.Scope) })
	return slices.Values(out)
}

// scopeOf is the scope ref's gate state is keyed by: the one its last Client
// or GateFor call named, else the one a previous run recorded for it (the
// store keeps the ref-to-scope mapping of every instance whose scope is not the
// default, so an instance not built since this boot is found too), else the
// default (ref.String()).
func (r *Registry) scopeOf(ref integrations.Ref) string {
	r.ensureState()
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.refScope[ref]; ok {
		return s
	}
	return ref.String()
}

// mergeScopes adopts the persisted ref-to-scope mappings: a ref this process has
// already named keeps its own scope (the build is newer than the record), and
// the record is rewritten when the two differ.
func (r *Registry) mergeScopes(ctx context.Context, ss map[integrations.Ref]string) {
	var stale []integrations.Ref
	r.mu.Lock()
	for ref, sc := range ss {
		r.scopePersisted[ref] = sc
		if _, built := r.refScope[ref]; !built {
			r.refScope[ref] = sc
		}
	}
	for ref, sc := range r.refScope {
		if r.scopePersisted[ref] != storedScope(ref, sc) {
			stale = append(stale, ref)
		}
	}
	r.mu.Unlock()
	for _, ref := range stale {
		r.persistScope(ctx, ref)
	}
}

// storedScope is what the store keeps for ref's scope: nothing (empty) for the
// default, which needs no record.
func storedScope(ref integrations.Ref, scope string) string {
	if scope == ref.String() {
		return ""
	}
	return scope
}

// persistScope makes the store's mapping for ref match refScope. A ref whose
// scope is the default has no record, and a record left by an earlier custom
// scope is removed, so HoldFor never follows a stale mapping.
func (r *Registry) persistScope(ctx context.Context, ref integrations.Ref) {
	unlock := r.slock.Lock(ref.String())
	defer unlock()
	r.mu.Lock()
	want := storedScope(ref, r.scopeLocked(ref))
	have := r.scopePersisted[ref]
	r.mu.Unlock()
	if want == have {
		return
	}
	sctx, cancel := r.storeCtx(ctx)
	defer cancel()
	if err := r.store.PutScope(sctx, ref, want); err != nil {
		r.warnStore("an instance's scope could not be persisted; its hold may not be found by ref after a restart", err)
		return
	}
	r.mu.Lock()
	r.scopePersisted[ref] = want
	r.mu.Unlock()
}

// scopeLocked is ref's scope; the caller holds r.mu.
func (r *Registry) scopeLocked(ref integrations.Ref) string {
	if s, ok := r.refScope[ref]; ok {
		return s
	}
	return ref.String()
}

// HoldFor is ref's active hold itself (its scope, class, start and end),
// reading the gate state only: it never probes the remote, builds an instance
// or needs the instance to be enabled, so a hold on a disabled or
// misconfigured instance is still visible and releasable (the seam F7-10's
// hold route declares). Pass what it returns to ReleaseHoldIf to release
// exactly that hold. There is deliberately no release by scope alone: a hold
// set between a read and such a release would be released unseen.
func (r *Registry) HoldFor(ref integrations.Ref) (integrations.Hold, bool) {
	return r.activeHold(r.scopeOf(ref))
}

// maxLogDetail bounds a hold's Detail in a log line.
const maxLogDetail = 120

// scopeAttrs names scope in a log line without ever carrying a caller-chosen
// string: a Policy.Scope can hold a host, a path or a credential's hash. The
// owning instance is logged when some Client or GateFor call named this scope
// (instance=<kind>/<id>); a scope that IS the default form of a valid Ref
// (kind/instance ID, nothing else can pass Ref.Validate) is that Ref; any
// other scope is an opaque digest (scope_digest=<12 hex characters>), enough to
// tell two scopes apart in one log and not to recover either.
func (r *Registry) scopeAttrs(scope string) []any {
	r.mu.Lock()
	for ref, s := range r.refScope {
		if s == scope {
			r.mu.Unlock()
			return []any{"instance", ref.String()}
		}
	}
	r.mu.Unlock()
	if kind, id, ok := strings.Cut(scope, "/"); ok {
		ref := integrations.Ref{Kind: integrations.Kind(kind), Instance: integrations.InstanceID(id)}
		if ref.Validate() == nil && ref.String() == scope {
			return []any{"instance", scope}
		}
	}
	sum := sha256.Sum256([]byte(scope))
	return []any{"scope_digest", hex.EncodeToString(sum[:6])}
}

// logDetail is a hold's Detail as a log line may carry it: scrubbed of
// credentials like an error's text and cut to maxLogDetail bytes. A hold's
// Detail is a constant ("class=...") when this Registry placed it, but a
// persisted record is another writer's input.
func logDetail(d string) string {
	return scrubber{}.bounded(d, maxLogDetail)
}
