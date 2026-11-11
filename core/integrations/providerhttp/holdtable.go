// file: core/integrations/providerhttp/holdtable.go
// version: 1.0.0
// guid: 5e7ac18e-dc95-4438-b438-0ad09045a2f0
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"fmt"
	"iter"
	"slices"
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

// loadHolds reads the persisted holds into the table: expired ones are
// dropped, and one beyond the maximum is clamped (HEAD's maxThrottleHold
// rule). A store that cannot be read leaves the table empty (holds fail open,
// M2.5), with one warning.
func (r *Registry) loadHolds() {
	ctx, cancel := r.storeCtx(context.Background())
	defer cancel()
	hs, err := r.store.Holds(ctx)
	if err != nil {
		r.warnStore("the persisted holds could not be read; holds are kept in memory only", err)
		return
	}
	now := r.now()
	hmax := r.settings().HoldMax
	r.hmu.Lock()
	defer r.hmu.Unlock()
	for _, h := range hs {
		if !now.Before(h.Until) {
			// Drop the expired record; a store that cannot is no harm (the
			// record is expired and is skipped again on the next load).
			if err := r.store.PutHold(ctx, integrations.Hold{Scope: h.Scope}); err != nil {
				r.warnStore("an expired hold could not be removed from the store", err)
			}
			continue
		}
		if latest := now.Add(hmax); h.Until.After(latest) {
			h.Until = latest
			if err := r.store.PutHold(ctx, h); err != nil {
				r.warnStore("a clamped hold could not be persisted", err)
			}
		}
		r.holds[h.Scope] = h
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
	now := r.now()
	r.hmu.Lock()
	defer r.hmu.Unlock()
	h, ok := r.holds[scope]
	if !ok || !now.Before(h.Until) {
		return integrations.Hold{}, false
	}
	return h, true
}

// placeHold stands scope off for d (clamped to max), keeping a longer hold
// already in place. It is persisted under the hold mutex, so the table and the
// store agree; a store write that fails leaves the hold in memory (M2.5).
func (r *Registry) placeHold(ctx context.Context, ref integrations.Ref, scope string, class integrations.Class, detail string, d, hmax time.Duration) {
	now := r.now()
	until := now.Add(min(d, hmax))
	r.hmu.Lock()
	defer r.hmu.Unlock()
	if cur, ok := r.holds[scope]; ok && now.Before(cur.Until) && !cur.Until.Before(until) {
		return
	}
	h := integrations.Hold{Scope: scope, Class: class, Detail: detail, Until: until, SetAt: now}
	r.holds[scope] = h
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
	r.hmu.Lock()
	defer r.hmu.Unlock()
	h, ok := r.holds[scope]
	if !ok || h.SetAt.After(started) {
		return
	}
	sctx, cancel := r.storeCtx(ctx)
	defer cancel()
	if err := r.store.PutHold(sctx, integrations.Hold{Scope: scope}); err != nil {
		r.warnStore("a released hold could not be removed from the store", err)
	}
	delete(r.holds, scope)
}

// ReleaseHold ends scope's hold (the one-instance form of
// core.integrations.release-holds, and DELETE .../instances/{id}/hold). It
// reads and removes the hold under the hold mutex, so it is atomic with
// respect to the scope it names: a hold set a moment later is a new hold for
// the caller to see. Releasing a scope with no hold is not an error. The
// record is deleted from the store first; a store that cannot delete leaves the
// hold in force and returns the error, so the operator is never told a hold is
// gone while a restart would bring it back.
func (r *Registry) ReleaseHold(ctx context.Context, scope string) error {
	r.hmu.Lock()
	defer r.hmu.Unlock()
	if _, ok := r.holds[scope]; !ok {
		return nil
	}
	if err := r.store.PutHold(ctx, integrations.Hold{Scope: scope}); err != nil {
		return fmt.Errorf("providerhttp: release hold: %w", err)
	}
	delete(r.holds, scope)
	return nil
}

// sameHold reports whether a and b are one hold: the identity of a hold is its
// scope, class, start and end. placeHold only replaces a hold with a longer
// one, which has a new Until and SetAt, so a replacement is never the same.
func sameHold(a, b integrations.Hold) bool {
	return a.Scope == b.Scope && a.Class == b.Class && a.SetAt.Equal(b.SetAt) && a.Until.Equal(b.Until)
}

// ReleaseHoldIf ends the hold expect only while it is still the hold in force
// for expect.Scope (compared under the hold mutex, so no hold can be placed
// between the comparison and the release). It is the form for a caller that
// read the hold with HoldFor and acts on what it saw (the hold route: an
// operator releases the hold on the screen, not one set a moment later).
//
//   - the same hold is in force: it is released (store first, as ReleaseHold)
//     and the result is nil;
//   - no hold is in force (never set, ended, or released by someone else): the
//     goal is met and the result is nil;
//   - a different hold is in force: nothing is released and the result is
//     ErrHoldChanged.
func (r *Registry) ReleaseHoldIf(ctx context.Context, expect integrations.Hold) error {
	now := r.now()
	r.hmu.Lock()
	defer r.hmu.Unlock()
	cur, ok := r.holds[expect.Scope]
	if !ok || !now.Before(cur.Until) {
		return nil
	}
	if !sameHold(cur, expect) {
		return ErrHoldChanged
	}
	if err := r.store.PutHold(ctx, integrations.Hold{Scope: expect.Scope}); err != nil {
		return fmt.Errorf("providerhttp: release hold: %w", err)
	}
	delete(r.holds, expect.Scope)
	return nil
}

// ReleaseAuthHold releases ref's ClassAuth hold, for integrations.Options.ReleaseAuthHold:
// a Put that wrote new credentials ends the hold the old ones earned (M1.3). A
// hold of another class is left alone.
func (r *Registry) ReleaseAuthHold(ctx context.Context, ref integrations.Ref) error {
	scope := r.scopeOf(ref)
	r.hmu.Lock()
	defer r.hmu.Unlock()
	h, ok := r.holds[scope]
	if !ok || h.Class != integrations.ClassAuth {
		return nil
	}
	if err := r.store.PutHold(ctx, integrations.Hold{Scope: scope}); err != nil {
		return fmt.Errorf("providerhttp: release auth hold: %w", err)
	}
	delete(r.holds, scope)
	return nil
}

// Holds iterates the active holds of every scope, sorted by scope. It reads
// the gate state only: nothing is dialled and no instance is built.
func (r *Registry) Holds() iter.Seq[integrations.Hold] {
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
// or GateFor call named, else the default (ref.String()). A hold on an
// instance that was never built since this boot is therefore found when its
// scope is the default one; otherwise it is listed by Holds, and released by
// its scope.
func (r *Registry) scopeOf(ref integrations.Ref) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.refScope[ref]; ok {
		return s
	}
	return ref.String()
}

// HoldFor is ref's active hold itself (its scope, class, start and end),
// reading the gate state only like HeldScope. Pass what it returns to
// ReleaseHoldIf to release exactly that hold.
func (r *Registry) HoldFor(ref integrations.Ref) (integrations.Hold, bool) {
	return r.activeHold(r.scopeOf(ref))
}

// HeldScope reports the scope of ref's active hold, reading the gate state
// only: it never probes the remote, builds an instance or needs the instance
// to be enabled, so a hold on a disabled or misconfigured instance is still
// visible and releasable (the seam F7-10's hold route declares).
func (r *Registry) HeldScope(ref integrations.Ref) (scope string, ok bool) {
	scope = r.scopeOf(ref)
	if _, held := r.activeHold(scope); held {
		return scope, true
	}
	return "", false
}
