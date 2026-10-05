// file: internal/authority/authority.go
// version: 1.0.0
// guid: 5baf3d40-9887-4365-8743-452d7a73c619
// last-edited: 2026-10-04

// Package authority keeps the authority lists: a rebuildable reference index
// of known people (authors, narrators, cast members) and publishers, with
// their contributor ASINs and an evidence tier, built from structured sources
// only.
//
// WHY. Every author-credit decision in the library (split a "A, B" credit or
// keep it whole, is this single word a pen name or a series tag, is this name
// a publisher) is made today from the library's own rows, which are exactly
// the rows that may be wrong. Providers already tell us who is a person: an
// Audible product lists its authors and narrators one by one, many with a
// contributor ASIN. This package collects those facts into one index that
// later PRs consult as EVIDENCE (design: owner-approved 2026-10-04).
//
// Rules (owner decisions 2026-10-04):
//
//   - Presence is evidence; absence is nothing. A name missing from the index
//     says nothing about it, and nothing here ever records a miss.
//   - Tiers, best first: O (the owner's own library, or an owner override) >
//     A (a structured contributor list entry WITH a contributor ASIN) > B
//     (structured, no ASIN) > C (a single whole value from a joined cache; not
//     ingested yet).
//   - Cast lists are cast_author only. Author-array members of a cast-context
//     product (manual-only: Doctor Who / Big Finish / Torchwood; dramatized or
//     full cast; more than 3 authors) are recorded as cast_author and are
//     never author evidence.
//   - A hit adds evidence; it never creates an author. Nothing in this package
//     writes a library row.
//   - One fold, several ASINs is a homonym: reported, never resolved by
//     picking one.
//   - Owner overrides (ref_ovr:) are never touched by a rebuild.
//
// Keyspace (raw keys on the main store, through database.RawKVStore, which
// the production indexedStore forwards; no capability assertion involved):
//
//	ref_person:<fold>          Person JSON           (rebuilt)
//	ref_pub:<fold>             Publisher JSON        (rebuilt)
//	ref_asin:person:<ASIN>     ASINRef JSON          (rebuilt)
//	ref_src:<source>:<item>    ingest digest         (rebuilt; idempotence ledger)
//	ref_ovr:person:<fold>      PersonOverride JSON   (owner; never rebuilt)
//	ref_ovr:pub:<fold>         PublisherOverride JSON (owner; never rebuilt)
//
// <fold> is authorcredit.LettersKey of the name: "J.N. Chaney" and "J N
// Chaney" share one entry.
package authority

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
)

// Key prefixes. Every one starts with "ref_" so none can nest under (or
// contain) another registered family; see internal/database/keyfamilies.go.
const (
	PersonPrefix    = "ref_person:"
	PublisherPrefix = "ref_pub:"
	ASINPrefix      = "ref_asin:"
	SourcePrefix    = "ref_src:"
	OverridePrefix  = "ref_ovr:"

	personASINKind      = "person"
	overridePersonKind  = "person"
	overridePubKind     = "pub"
	personASINKeyPrefix = ASINPrefix + personASINKind + ":"
)

// RebuildablePrefixes are the prefixes an apply rewrites and prunes. ref_ovr:
// is deliberately absent: owner overrides survive every rebuild.
func RebuildablePrefixes() []string {
	return []string{PersonPrefix, PublisherPrefix, ASINPrefix, SourcePrefix}
}

// KeyPrefixes is every prefix this package owns, overrides included. Tests
// use it to prove a dry run writes nothing; a rollback drops exactly these.
func KeyPrefixes() []string {
	return append(RebuildablePrefixes(), OverridePrefix)
}

// Sources. Each is the <source> segment of a ref_src: ledger key and a value
// in an entry's Sources.
const (
	SourceSeed          = "owner_library_seed"
	SourceCatalog       = "catalog"
	SourceLibraryExport = "owner_library_export"
	SourceOverride      = "owner_override"
)

// Tier is how strong a piece of evidence is. Compare with Rank, never with
// string order.
type Tier string

// Tiers, best first.
const (
	TierO Tier = "O"
	TierA Tier = "A"
	TierB Tier = "B"
	TierC Tier = "C"
)

// Rank orders tiers: O=4 > A=3 > B=2 > C=1; anything else is 0.
func (t Tier) Rank() int {
	switch t {
	case TierO:
		return 4
	case TierA:
		return 3
	case TierB:
		return 2
	case TierC:
		return 1
	}
	return 0
}

// better returns the stronger of two tiers.
func better(a, b Tier) Tier {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// Role is how a person was credited.
type Role string

// Roles. RoleCastAuthor is an author-array member of a cast-context product:
// it is recorded so a later consumer can see "this name is a cast member",
// and it is NEVER author evidence.
const (
	RoleAuthor     Role = "author"
	RoleNarrator   Role = "narrator"
	RoleCastAuthor Role = "cast_author"
)

// Fold is the index key of a name: authorcredit.LettersKey.
func Fold(name string) string {
	return authorcredit.LettersKey(strings.TrimSpace(name))
}

// PersonKey is the ref_person: key for a name ("" when the name folds to
// nothing).
func PersonKey(name string) string {
	f := Fold(name)
	if f == "" {
		return ""
	}
	return PersonPrefix + f
}

// PublisherKey is the ref_pub: key for a name ("" when it folds to nothing).
func PublisherKey(name string) string {
	f := Fold(name)
	if f == "" {
		return ""
	}
	return PublisherPrefix + f
}

// PersonASINKey is the ref_asin:person: key for a contributor ASIN.
func PersonASINKey(asin string) string {
	a := normalizeASIN(asin)
	if a == "" {
		return ""
	}
	return personASINKeyPrefix + a
}

func personOverrideKey(fold string) string { return OverridePrefix + overridePersonKind + ":" + fold }
func pubOverrideKey(fold string) string    { return OverridePrefix + overridePubKind + ":" + fold }
func sourceKey(source, item string) string { return SourcePrefix + source + ":" + item }

func normalizeASIN(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// isSingleWord reports whether a display name is one word ("Actus"). Such
// names are reported, and a later consumer may use a hit as evidence, but a
// hit never creates an author (owner rule: single-word pen names at import
// stay review-only).
func isSingleWord(display string) bool {
	return len(strings.Fields(display)) == 1
}
