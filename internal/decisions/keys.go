// file: internal/decisions/keys.go
// version: 1.1.0
// guid: 3b8f2d6e-41c7-4a95-9e0d-6f12a7c8b534
// last-edited: 2026-10-21

package decisions

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Key families, all in the main Pebble store. Each is registered in
// internal/database/keyfamilies.go.
//
//	decision:r:<ULID>                                                 -> Record JSON (append-only log; the dataset)
//	decision:v:<op_kind>:<subject_key>                                -> Verdict JSON (current state of one proposal)
//	decision:va:<op_kind>:<subject_key>                               -> ActiveRow JSON (one row per LIVE reject; what Load scans)
//	decision:pe:<entity_type>:<lo>:<hi>:<family>:<record_id>          -> empty (pair-edge veto, one row per rejecting record)
//	decision:mb:<entity_type>:<entity_id>:<family>:<record_id>        -> empty (membership of a rejected set larger than MaxEdgeSet)
//	decision:ent:<entity_type>:<entity_id>:<ULID>                     -> empty (history of one entity)
//	decision:op:<op_kind>:<ULID>                                      -> empty (history of one op kind, export order)
//
// decision:va: exists so Load reads only the proposals that can veto
// something, not every label-only verdict ever written (approve, applied,
// skip and undo each leave one). It holds the rejected material hashes and is
// written and deleted in the same atomic batch as the verdict, so the two can
// never disagree: it exists exactly while the verdict is active or reopened.
//
// Every variable component is escaped (escapeComponent) so a ':' inside an
// op_kind such as "repairs:dedup.split-books" can never be mistaken for a
// delimiter, and so "a" and "a:b" never share a prefix.
//
// DURABILITY: none of these keys has a TTL and nothing prunes them. They are
// owner decisions and labelled training data. retention_test.go runs every
// log, activity, undo-journal and metrics prune entry point and asserts that
// the decision: families survive.
const (
	prefixRecord  = "decision:r:"
	prefixVerdict = "decision:v:"
	prefixActive  = "decision:va:"
	prefixEdge    = "decision:pe:"
	prefixMember  = "decision:mb:"
	prefixEntity  = "decision:ent:"
	prefixOp      = "decision:op:"
)

// Prefixes lists every key prefix the package writes, for tests and tooling
// that must cover the whole decision keyspace.
func Prefixes() []string {
	return []string{prefixRecord, prefixVerdict, prefixActive, prefixEdge, prefixMember, prefixEntity, prefixOp}
}

// ErrInvalidComponent is returned for a key component that is empty or holds a
// NUL or other control character.
var ErrInvalidComponent = errors.New("decisions: invalid key component")

// escapeComponent makes s safe to embed between ':' delimiters: '%' becomes
// "%25", ':' becomes "%3A" and every byte below 0x20 becomes "%XX". It is
// injective, so distinct components give distinct keys.
func escapeComponent(s string) string {
	if !strings.ContainsAny(s, "%:") && !hasControl(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%' || c == ':' || c < 0x20:
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
			return true
		}
	}
	return false
}

// checkComponent rejects what can never be a key component: empty, or holding
// a control character (which would make the key unreadable in a census).
func checkComponent(field, s string) error {
	if s == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidComponent, field)
	}
	if hasControl(s) {
		return fmt.Errorf("%w: %s holds a control character", ErrInvalidComponent, field)
	}
	return nil
}

func recordKey(id string) string { return prefixRecord + id }

func verdictKey(opKind, subjectKey string) string {
	return prefixVerdict + escapeComponent(opKind) + ":" + escapeComponent(subjectKey)
}

func activeKey(opKind, subjectKey string) string {
	return prefixActive + escapeComponent(opKind) + ":" + escapeComponent(subjectKey)
}

func opPrefix(opKind string) string { return prefixOp + escapeComponent(opKind) + ":" }

func opKey(opKind, id string) string { return opPrefix(opKind) + id }

func entityPrefix(entityType, entityID string) string {
	return prefixEntity + escapeComponent(entityType) + ":" + escapeComponent(entityID) + ":"
}

func entityKey(entityType, entityID, id string) string {
	return entityPrefix(entityType, entityID) + id
}

// orderPair returns the pair in canonical order. The order is plain string
// comparison of the RAW ids, so author ids "10" and "9" order as "10" < "9".
// It is compared before escaping and the key holds the escaped ids, so the
// escaped strings need not sort the way the raw ones do (':' is 0x3A, its
// escape starts with '%' 0x25): that is harmless because every writer builds
// an edge as orderPair(raw a, raw b) and every reader, including the Load
// snapshot, unescapes the two ids from the key and calls orderPair again
// before it looks the pair up. Never compare the escaped halves of a key.
func orderPair(a, b string) (lo, hi string) {
	if a <= b {
		return a, b
	}
	return b, a
}

func edgePairPrefix(entityType, lo, hi string) string {
	return prefixEdge + escapeComponent(entityType) + ":" + escapeComponent(lo) + ":" + escapeComponent(hi) + ":"
}

func edgeKey(entityType, lo, hi, family, recordID string) string {
	return edgePairPrefix(entityType, lo, hi) + escapeComponent(family) + ":" + recordID
}

func memberPrefix(entityType, entityID string) string {
	return prefixMember + escapeComponent(entityType) + ":" + escapeComponent(entityID) + ":"
}

func memberKey(entityType, entityID, family, recordID string) string {
	return memberPrefix(entityType, entityID) + escapeComponent(family) + ":" + recordID
}

// splitFamilyRecord splits "<family>:<record id>" off a key remainder. Escaped components never hold a raw ':', so the split is
// exact.
func splitFamilyRecord(rest string) (family, recordID string, ok bool) {
	i := strings.LastIndexByte(rest, ':')
	if i < 0 {
		return "", "", false
	}
	return unescapeComponent(rest[:i]), rest[i+1:], true
}

func unescapeComponent(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
