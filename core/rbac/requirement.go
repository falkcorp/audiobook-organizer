// file: core/rbac/requirement.go
// version: 1.0.0
// guid: 55253247-dbc8-4af0-ab4e-33f9c8b9ffe5
// last-edited: 2026-11-06

package rbac

import (
	"slices"
	"strings"
)

// Requirement is what a route, task, topic or settings section demands.
// Its zero value is invalid; every registration API rejects it (R6).
//
// A Requirement is built only by the constructors below. It holds a slice, so
// it is not comparable with ==: use IsZero and Kind.
type Requirement struct {
	kind  RequirementKind
	perms []Permission // KindAllOf, KindAnyOf: the scopes, in order, deduplicated
	// reason is a KindDelegated requirement's stated reason (Delegated).
	reason string
	// atom and readSide are a KindLegacyAtom or KindLegacyCheck requirement's
	// payload (LegacyAtom, LegacyCheck). Nothing in rbac evaluates them.
	atom     string
	readSide bool
}

// RequirementKind is the kind a Requirement was built with. Need(s) reports
// KindAllOf with one scope. The zero value KindInvalid is the zero Requirement's.
type RequirementKind uint8

const (
	KindInvalid RequirementKind = iota
	KindPublic
	KindAuthenticated
	KindAllOf
	KindAnyOf
	KindDelegated
	// KindLegacyAtom and KindLegacyCheck are the kinds of LegacyAtom and
	// LegacyCheck values (04 §8.1 R1): they only carry an atom, and the
	// checker that evaluates them lands at F4. They are appended after the
	// spec's six kinds so those keep their values.
	KindLegacyAtom
	KindLegacyCheck
)

var (
	Public           = Requirement{kind: KindPublic}        // no principal needed
	AnyAuthenticated = Requirement{kind: KindAuthenticated} // a principal holding core:self or any non-Confining scope (§4.10)
)

// Need requires the one scope s. It reports KindAllOf with one scope.
func Need(s Permission) Requirement { return AllOf(s) }

// AllOf requires every one of s. With no scope, or with a scope that is not a
// concrete scope under ParsePermission, it returns the zero Requirement, which
// every registration API rejects: a malformed requirement fails closed instead
// of being satisfiable by everyone. Duplicates are dropped, order is kept.
func AllOf(s ...Permission) Requirement { return scopes(KindAllOf, s) }

// AnyOf requires at least one of s. The same rule as AllOf for no scope or a
// malformed one.
func AnyOf(s ...Permission) Requirement { return scopes(KindAnyOf, s) }

func scopes(kind RequirementKind, s []Permission) Requirement {
	if len(s) == 0 {
		return Requirement{}
	}
	perms := make([]Permission, 0, len(s))
	for _, p := range s {
		if _, err := ParsePermission(string(p)); err != nil {
			return Requirement{}
		}
		if !slices.Contains(perms, p) {
			perms = append(perms, p)
		}
	}
	return Requirement{kind: kind, perms: perms}
}

// Delegated marks a gin-mount route whose permission is enforced inside the
// mounted handler by its own compat authenticator (05-http-api §8.2,
// LegacyRoute.OwnAuth). Accepted only by httpapi.MountLegacy, and only on an
// OwnAuth route that names a compat method its module registered with
// Registrar.AuthMethod (SEC2-17); never by Handle.
func Delegated(reason string) Requirement {
	return Requirement{kind: KindDelegated, reason: reason}
}

// LegacyAtom is the requirement of a legacy route or adapted operation
// definition that names a pre-core permission atom (04 §8.1 R1). It only
// carries the atom: rbac neither resolves it nor evaluates it, and
// resolution through books' atom -> scope table lands at F4 (COR6-1). An
// empty atom returns the zero Requirement.
func LegacyAtom(atom string) Requirement {
	if atom == "" {
		return Requirement{}
	}
	return Requirement{kind: KindLegacyAtom, atom: atom}
}

// LegacyCheck is the value an in-handler check of a legacy atom carries,
// where readSide marks a read-side check (04 §8.1 R1). Distinct from
// LegacyAtom because the two differ for the auth-disabled principal (05-tasks
// §5T.3.7); like it, it only carries the atom. An empty atom returns the zero
// Requirement.
func LegacyCheck(atom string, readSide bool) Requirement {
	if atom == "" {
		return Requirement{}
	}
	return Requirement{kind: KindLegacyCheck, atom: atom, readSide: readSide}
}

// IsZero reports the invalid zero Requirement.
func (r Requirement) IsZero() bool { return r.kind == KindInvalid }

// Kind reports the kind r was built with, so authz, httpapi and tests outside
// rbac can tell AllOf from AnyOf.
func (r Requirement) Kind() RequirementKind { return r.kind }

// Permissions returns a copy of the scopes of an AllOf, AnyOf or Need
// requirement, and nil for every other kind.
func (r Requirement) Permissions() []Permission {
	if len(r.perms) == 0 {
		return nil
	}
	return slices.Clone(r.perms)
}

// Atom returns the legacy atom of a LegacyAtom or LegacyCheck requirement and
// "" for every other kind.
func (r Requirement) Atom() string { return r.atom }

// ReadSide reports whether r is a LegacyCheck marked read-side; false for
// every other kind, LegacyAtom included.
func (r Requirement) ReadSide() bool { return r.readSide }

// String is "books:edit" for Need, "public", "authenticated", "all(a,b)" for
// AllOf of two or more scopes and "any(a,b)" for AnyOf; "delegated(reason)",
// "legacy(atom)", "legacy_check(atom)" and "legacy_check(atom,read)"; and
// "invalid" for the zero value.
func (r Requirement) String() string {
	switch r.kind {
	case KindPublic:
		return "public"
	case KindAuthenticated:
		return "authenticated"
	case KindAllOf:
		if len(r.perms) == 1 {
			return string(r.perms[0])
		}
		return "all(" + joinPerms(r.perms) + ")"
	case KindAnyOf:
		return "any(" + joinPerms(r.perms) + ")"
	case KindDelegated:
		return "delegated(" + r.reason + ")"
	case KindLegacyAtom:
		return "legacy(" + r.atom + ")"
	case KindLegacyCheck:
		if r.readSide {
			return "legacy_check(" + r.atom + ",read)"
		}
		return "legacy_check(" + r.atom + ")"
	default:
		return "invalid"
	}
}

func joinPerms(ps []Permission) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = string(p)
	}
	return strings.Join(parts, ",")
}
