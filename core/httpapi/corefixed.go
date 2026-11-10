// file: core/httpapi/corefixed.go
// version: 1.0.0
// guid: 92e0747b-5722-4eae-b386-250870d1015f
// last-edited: 2026-11-10

package httpapi

import (
	"fmt"
	"slices"
	"strings"
)

// This file is the one place a core route may live outside /api/core/v1
// (05-http-api §3.5, §4.9): HEAD's OAuth callback and start paths, which stay
// where they are because the identity providers' registered redirect URIs
// name them. Core serves exactly those two patterns, on a CoreFixedGroup, and
// Build refuses every other CoreFixedGroup route and every legacy manifest
// route over them.

// coreFixedPrefix keys a CoreFixedGroup in Router.groups. A module ID never
// holds a space, so no module's group can share a key with one.
const coreFixedPrefix = "core-fixed "

// CoreExactPatterns returns the exact core patterns allowed inside another
// owner's claim (05-http-api §4.9): HEAD's OAuth callback and start. It is a
// function, not a variable, so a caller can never change the list Build
// enforces (03 R5); each call returns a fresh slice. Adding a third entry is a
// spec change.
func CoreExactPatterns() []string {
	return []string{
		"GET /api/v1/auth/oauth/{provider}/callback",
		"GET /api/v1/auth/oauth/{provider}/start",
	}
}

// CoreFixedGroup returns core's group at base, a HEAD path kept because that
// is cheaper than moving it (ABO: app.Options.Auth.OIDCCallbackBase,
// /api/v1/auth/oauth). Its routes run the whole core chain exactly as
// CoreGroup's do and record RouteInfo.Owner "core"; the group claims nothing,
// so it never collides with the claim of the legacy mount whose /api/v1/ it
// sits in. Only core/internal/authhttp uses it. Build refuses a route on it
// whose METHOD and full pattern is not an entry of CoreExactPatterns()
// (ErrPrefixCollision), and refuses any legacy manifest route that matches a
// path an entry the router serves matches. Every call for one base returns the
// same group. A base that is not a clean literal path below "/" (no wildcard,
// no trailing slash), or that lies in core's own /api/core/ space, is recorded
// as ErrPrefixCollision, like an invalid claim, and refuses every route on the
// group.
func (r *Router) CoreFixedGroup(base string) *Group {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := coreFixedPrefix + base
	if g, ok := r.groups[key]; ok {
		return g
	}
	g := &Group{router: r, owner: "core", root: base, base: base}
	r.groups[key] = g
	if err := r.late("group " + base); err != nil {
		g.err = err
		return g
	}
	if err := r.checkFixedBase(base); err != nil {
		g.err = err
		r.record(err)
	}
	return g
}

// checkFixedBase reports a base CoreFixedGroup cannot serve; r.mu is held.
func (r *Router) checkFixedBase(base string) error {
	if !strings.HasPrefix(base, "/") || base == "/" || strings.HasSuffix(base, "/") ||
		strings.Contains(base, "{") || cleanUnder(base, "/") != nil {
		return fmt.Errorf("%w: core: fixed group base %q is not a clean literal path below /", ErrPrefixCollision, base)
	}
	coreRoot := r.opts.CoreGroupBase[:strings.LastIndexByte(r.opts.CoreGroupBase, '/')]
	if claimsOverlap(coreRoot+"/", base+"/") {
		return fmt.Errorf("%w: core: fixed group base %q is inside core's own %s/ space", ErrPrefixCollision, base, coreRoot)
	}
	return nil
}

// fixedBases are the bases of the valid fixed groups; r.mu is held. A group
// with a bad base is left out: its routes are refused through Group.err, and
// counting them as fixed would misread a CoreGroup route as one.
func (r *Router) fixedBases() []string {
	var out []string
	for key, g := range r.groups {
		if strings.HasPrefix(key, coreFixedPrefix) && g.err == nil {
			out = append(out, g.base)
		}
	}
	return out
}

// isFixedRoute reports a native core route registered on a CoreFixedGroup (or
// a Sub or Version of one): its pattern is a fixed base or lies under one. A
// CoreGroup route never does, because a fixed base inside /api/core/ is
// refused. r.mu is held.
func (r *Router) isFixedRoute(e *routeEntry) bool {
	if e.info.Owner != "core" || e.info.Kind != RouteKindNative {
		return false
	}
	return slices.ContainsFunc(r.fixedBases(), func(b string) bool {
		return e.info.Pattern == b || strings.HasPrefix(e.info.Pattern, b+"/")
	})
}

// routeKey is a route's "METHOD pattern", the form CoreExactPatterns lists.
func routeKey(e *routeEntry) string { return e.info.Method + " " + e.info.Pattern }

// isCoreExact reports a fixed-group route that is an entry of
// CoreExactPatterns(): the one kind of core route Build lets sit inside
// another owner's claim, and therefore the one the shadow check skips. r.mu is
// held.
func (r *Router) isCoreExact(e *routeEntry) bool {
	return r.isFixedRoute(e) && slices.Contains(CoreExactPatterns(), routeKey(e))
}

// checkCoreFixed is Build's refusal of 05-http-api §3.5 and §4.9: a
// fixed-group route that is not an entry of CoreExactPatterns(), and a legacy
// manifest route that matches a path of an entry the router serves, are both
// ErrPrefixCollision. The second applies only once a fixed-group route serves
// the entry, so a router with no fixed-group route accepts and refuses exactly
// what it did before CoreFixedGroup existed. r.mu is held.
func (r *Router) checkCoreFixed() []error {
	if len(r.fixedBases()) == 0 {
		return nil
	}
	var errs []error
	var served []muxPat
	for _, e := range r.routes {
		if !r.isFixedRoute(e) {
			continue
		}
		if !r.isCoreExact(e) {
			errs = append(errs, fmt.Errorf("%w: %s is a CoreFixedGroup route outside CoreExactPatterns() (%s)",
				ErrPrefixCollision, describe(e.info), strings.Join(CoreExactPatterns(), ", ")))
			continue
		}
		if p, ok := parseMuxPattern(e.info.Method, e.info.Pattern); ok {
			served = append(served, p)
		}
	}
	if len(served) == 0 {
		return errs
	}
	for mi, m := range r.mounts {
		if mi >= len(r.legacy.routes) {
			continue
		}
		for _, lr := range r.legacy.routes[mi] {
			lp, err := translateGinPath(lr.Method, lr.GinPath)
			if err != nil {
				continue // legacyNode reports a path ServeMux cannot express
			}
			for _, sp := range served {
				if overlaps(lp, sp) {
					errs = append(errs, fmt.Errorf("%w: %s %s %s matches a path of core's exact pattern %s; delete the legacy route in the PR that mounts core's",
						ErrPrefixCollision, m.Owner, lr.Method, lr.GinPath, sp))
				}
			}
		}
	}
	return errs
}
