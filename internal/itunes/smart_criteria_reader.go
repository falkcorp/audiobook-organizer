// file: internal/itunes/smart_criteria_reader.go
// version: 2.0.0
// guid: 8b6c7d5e-9f0a-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-09-13
//
// Reader for the iTunes Smart Criteria binary blob (ITUNES-SMARTCRIT-PARSE).
//
// What is known about the format comes from a measurement over 292 real
// smart playlists (see TODO.md ITUNES-SMARTCRIT-PARSE / issue #2658), not from
// a captured fixture — the owner's library is private and not in the repo:
//
//   - The blob starts with the magic "SLst" (0x534c7374).
//   - Every integer is BIG-endian; string operands are UTF-16BE.
//   - It is NOT a flat header + fixed-stride rule array. It is a nested tree of
//     SLst containers whose nesting is still unmapped. The previous parser
//     assumed a 136-byte little-endian stride and returned empty rules for
//     every real blob while reporting success.
//   - String operands can be located structurally: a UTF-16BE run at offset
//     `off` whose preceding u32be (at off-4) equals its byte length. For those
//     operands the rule header sits at fixed negative offsets:
//     field = u32be(off-56), operator word = u32be(off-52).
//     358 real operands satisfied the length-prefix check, which is what
//     proves the alignment.
//
// Only the field codes and operator words that were validated against real
// playlist membership are named. Everything else is surfaced as unknown and
// recorded in SmartCriteriaResult.Unresolved, so TranslateSmartCriteria
// refuses to produce a query from it rather than inventing one.

package itunes

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode"
	"unicode/utf16"
)

// smartCriteriaMagic is the 4-byte tag at offset 0 of every SLst container.
var smartCriteriaMagic = []byte("SLst")

const (
	// smartRuleHeaderSpan is the distance from a rule's field word to its
	// string operand: field at off-56, operator at off-52, length at off-4.
	smartRuleHeaderSpan = 56
	smartOperatorOffset = 52
	smartLengthOffset   = 4
)

// SmartRule represents one string-operand rule recovered from a blob.
type SmartRule struct {
	Field    SmartField
	Operator SmartOperator
	Operands []string
	// Offset is the byte offset of the operand inside the blob, kept so a
	// reviewer can locate the rule in a hexdump.
	Offset int
}

// SmartField identifies which track attribute the rule checks (u32be).
type SmartField uint32

// Only field codes validated against real playlist membership are named.
// Codes 2, 14 and 71 also occur in real blobs but their meaning has not been
// validated, so they are deliberately left unnamed and render as unknown.
const (
	SmartFieldAlbum  SmartField = 3
	SmartFieldArtist SmartField = 4
	SmartFieldGenre  SmartField = 8
)

// String returns our DSL field name for a SmartField, or unknown_0xNN.
func (f SmartField) String() string {
	switch f {
	case SmartFieldAlbum:
		return "album"
	case SmartFieldArtist:
		return "author"
	case SmartFieldGenre:
		return "genre"
	default:
		return fmt.Sprintf("unknown_0x%02X", uint32(f))
	}
}

// Known reports whether the field code has a validated meaning.
func (f SmartField) Known() bool {
	switch f {
	case SmartFieldAlbum, SmartFieldArtist, SmartFieldGenre:
		return true
	}
	return false
}

// SmartOperator is the u32be operator word that follows the field word.
type SmartOperator uint32

// Only operator words validated against real playlist membership are named:
// 0x01000002 held for 4017/4017 members (contains) and 0x03000002 for
// 0/23700 (a perfect inversion — does not contain). 0x01000001 occurs 22
// times but fits neither meaning (18.2%), so it stays unnamed.
const (
	SmartOpContains       SmartOperator = 0x01000002
	SmartOpDoesNotContain SmartOperator = 0x03000002
)

func (o SmartOperator) String() string {
	switch o {
	case SmartOpContains:
		return "contains"
	case SmartOpDoesNotContain:
		return "does_not_contain"
	default:
		return fmt.Sprintf("op_0x%08X", uint32(o))
	}
}

// Known reports whether the operator word has a validated meaning.
func (o SmartOperator) Known() bool {
	return o == SmartOpContains || o == SmartOpDoesNotContain
}

// SmartCriteriaResult is the parsed output of a Smart Criteria blob.
type SmartCriteriaResult struct {
	// Conjunction is "AND" or "OR" when known. The AND/OR flag has not been
	// located in the format, so the parser always leaves this empty; it is
	// set only by callers that know it from elsewhere.
	Conjunction string
	// Rules holds every string-operand rule whose length prefix matched.
	Rules []SmartRule
	// Containers is the number of SLst magics in the blob (1 = no nesting).
	Containers int
	// Unresolved lists every reason the rule list may not be a faithful
	// representation of the playlist. TranslateSmartCriteria returns ""
	// whenever it is non-empty.
	Unresolved []string
	RawLength  int
}

// Resolved reports whether the result can be translated without guessing.
func (r *SmartCriteriaResult) Resolved() bool {
	return r != nil && len(r.Unresolved) == 0
}

// unresolvedCompleteness is always recorded by ParseSmartCriteria: the
// operand scan only sees string rules, so numeric/date/media-kind rules and
// the grouping implied by nested SLst containers are invisible to it.
const unresolvedCompleteness = "rule set completeness unverified: only UTF-16 string operands are decoded; " +
	"numeric/date rules and SLst container nesting are not mapped"

// ParseSmartCriteria extracts string-operand rules from an iTunes Smart
// Criteria blob. It returns an error when the blob is not an SLst blob. On
// success the result's Unresolved list says what could not be decoded; a
// result is never presented as complete while the format is only partially
// mapped.
func ParseSmartCriteria(data []byte) (*SmartCriteriaResult, error) {
	if len(data) < len(smartCriteriaMagic) {
		return nil, fmt.Errorf("smart criteria too short: %d bytes", len(data))
	}
	if !bytes.Equal(data[:len(smartCriteriaMagic)], smartCriteriaMagic) {
		return nil, fmt.Errorf("smart criteria: missing SLst magic (got % x)", data[:len(smartCriteriaMagic)])
	}

	result := &SmartCriteriaResult{
		RawLength:  len(data),
		Containers: bytes.Count(data, smartCriteriaMagic),
		Unresolved: []string{unresolvedCompleteness},
	}

	for off := smartRuleHeaderSpan; off+2 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[off-smartLengthOffset : off]))
		s, ok := stringOperandAt(data, off, n)
		if !ok {
			off++
			continue
		}
		rule := SmartRule{
			Field:    SmartField(binary.BigEndian.Uint32(data[off-smartRuleHeaderSpan : off-smartOperatorOffset])),
			Operator: SmartOperator(binary.BigEndian.Uint32(data[off-smartOperatorOffset : off-smartOperatorOffset+4])),
			Operands: []string{s},
			Offset:   off,
		}
		// Header sanity check: a length-prefixed run with no operator word
		// in front of it is not a rule. The case this guards against is a
		// small field code (e.g. 2) read as a length prefix over the
		// operator word that follows it. This check can only drop
		// candidates, never invent one.
		if rule.Operator == 0 {
			off++
			continue
		}
		if !rule.Field.Known() {
			result.Unresolved = append(result.Unresolved,
				fmt.Sprintf("rule at offset %d: unvalidated field code %d", off, uint32(rule.Field)))
		}
		if !rule.Operator.Known() {
			result.Unresolved = append(result.Unresolved,
				fmt.Sprintf("rule at offset %d: unvalidated operator word 0x%08X", off, uint32(rule.Operator)))
		}
		result.Rules = append(result.Rules, rule)
		off += n
	}

	if len(result.Rules) > 1 {
		result.Unresolved = append(result.Unresolved,
			"AND/OR conjunction flag not located in the format")
	}
	return result, nil
}

// stringOperandAt reports whether data[off:off+n] is a length-prefixed
// UTF-16BE operand: n is a positive even byte count that fits, every code
// unit is printable (valid surrogate pairs allowed), and the run is maximal —
// the unit after it is not a printable continuation. The maximality check is
// what makes the length prefix a real cross-check rather than a coincidence.
func stringOperandAt(data []byte, off, n int) (string, bool) {
	if n <= 0 || n%2 != 0 || n > len(data)-off {
		return "", false
	}
	runes := make([]rune, 0, n/2)
	for i := off; i < off+n; i += 2 {
		u := rune(binary.BigEndian.Uint16(data[i:]))
		if utf16.IsSurrogate(u) {
			// A surrogate must be the high half of a pair inside the run.
			if i+4 > off+n {
				return "", false
			}
			r := utf16.DecodeRune(u, rune(binary.BigEndian.Uint16(data[i+2:])))
			if r == unicode.ReplacementChar {
				return "", false
			}
			u = r
			i += 2
		}
		if !unicode.IsPrint(u) {
			return "", false
		}
		runes = append(runes, u)
	}
	if end := off + n; end+2 <= len(data) {
		next := rune(binary.BigEndian.Uint16(data[end:]))
		if !utf16.IsSurrogate(next) && unicode.IsPrint(next) {
			return "", false
		}
	}
	return string(runes), true
}
