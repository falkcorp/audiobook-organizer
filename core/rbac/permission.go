// file: core/rbac/permission.go
// version: 1.0.0
// guid: 44f4ed0d-73ea-45c0-95e1-96e0bbb710a8
// last-edited: 2026-11-06

// Package rbac holds core's permission model. This file set is the leaf part
// that lands in release R1 (05-rbac §3, "Landing"): the scope type, the
// requirement values a route, task, topic or settings section demands, the
// role names, and the Checker interface. It imports only the standard
// library. The catalog, roles store, service and compiled checker land with
// R2's foundation PRs and identity flip; a member exists from the release that
// ships the package it names, and before that it is absent, not stubbed
// (06 §6.4.10).
package rbac

import (
	"errors"
	"fmt"
	"strings"
)

// ErrBadScope is wrapped by the errors ParsePermission and ParsePattern
// return. It is the one member of 05-rbac §3.4's error block this leaf needs
// early; the rest of the block lands with the checker implementation.
var ErrBadScope = errors.New("rbac: malformed scope")

// Permission is "<ns>:<action>[.<sub>]" or a role-only wildcard (§4.2).
// ns     = module ID or "core"            [a-z][a-z0-9]{1,15}
// action = [a-z][a-z0-9_]{0,31}
// sub    = [a-z][a-z0-9_]{0,31}  (one level only)
type Permission string

// ParsePermission parses a concrete scope with the strict grammar above; it
// rejects wildcards.
func ParsePermission(s string) (Permission, error) {
	return parse(s, false)
}

// ParsePattern is ParsePermission that also accepts "<ns>:*" and
// "<ns>:<action>.*".
func ParsePattern(s string) (Permission, error) {
	return parse(s, true)
}

// CoreAdmin is the superuser scope: it satisfies every requirement except a
// Sensitive scope for a non-interactive principal (§4.4). It is AuthRequired.
const CoreAdmin Permission = "core:admin"

// Namespace is the part before the colon: the module ID or "core". It is ""
// for a string that holds no colon, which ParsePermission never returns.
func (s Permission) Namespace() string {
	ns, _, ok := strings.Cut(string(s), ":")
	if !ok {
		return ""
	}
	return ns
}

// Action is the part after the colon up to a ".<sub>" suffix: "edit" for
// "books:edit.metadata" and for "books:edit.*", "*" for "books:*".
func (s Permission) Action() string {
	_, rest, ok := strings.Cut(string(s), ":")
	if !ok {
		return ""
	}
	action, _, _ := strings.Cut(rest, ".")
	return action
}

// IsWildcard reports a "<ns>:*" or "<ns>:<action>.*" pattern, which only
// ParsePattern accepts.
func (s Permission) IsWildcard() bool {
	return strings.HasSuffix(string(s), ":*") || strings.HasSuffix(string(s), ".*")
}

func parse(s string, allowWildcard bool) (Permission, error) {
	bad := func(why string) (Permission, error) {
		return "", fmt.Errorf("%w: %q: %s", ErrBadScope, s, why)
	}
	ns, rest, ok := strings.Cut(s, ":")
	if !ok {
		return bad(`want "<ns>:<action>[.<sub>]"`)
	}
	if !validNamespace(ns) {
		return bad("namespace must match [a-z][a-z0-9]{1,15}")
	}

	// "<ns>:*"
	if rest == "*" {
		if !allowWildcard {
			return bad("wildcards are accepted only by ParsePattern")
		}
		return Permission(s), nil
	}

	action, sub, hasSub := strings.Cut(rest, ".")
	if !validName(action) {
		return bad("action must match [a-z][a-z0-9_]{0,31}")
	}
	if !hasSub {
		return Permission(s), nil
	}
	// "<ns>:<action>.*"
	if sub == "*" {
		if !allowWildcard {
			return bad("wildcards are accepted only by ParsePattern")
		}
		return Permission(s), nil
	}
	if !validName(sub) { // also rejects a second sub level: "." is not in the class
		return bad("sub-action must match [a-z][a-z0-9_]{0,31} (one level only)")
	}
	return Permission(s), nil
}

// validNamespace reports [a-z][a-z0-9]{1,15}.
func validNamespace(s string) bool {
	if len(s) < 2 || len(s) > 16 || !isLower(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isLower(s[i]) && !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// validName reports [a-z][a-z0-9_]{0,31}.
func validName(s string) bool {
	if len(s) < 1 || len(s) > 32 || !isLower(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isLower(s[i]) && !isDigit(s[i]) && s[i] != '_' {
			return false
		}
	}
	return true
}

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
