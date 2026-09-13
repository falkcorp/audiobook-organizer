// file: internal/itunes/smart_criteria_translator.go
// version: 2.0.0
// guid: 9c7d8e6f-0a1b-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-09-13
//
// Translates parsed iTunes Smart Criteria rules into our DSL query
// string. Used during the one-time iTunes dynamic playlist migration
// (spec 3.4 task 5).

package itunes

import (
	"fmt"
	"strings"
)

// TranslateSmartCriteria converts a parsed SmartCriteriaResult into a DSL
// query string suitable for creating a smart UserPlaylist.
//
// It returns "" — never a partial or match-all query — whenever the result
// cannot be translated faithfully: a nil or unresolved result, no rules, a
// rule with an unvalidated field or operator, or more than one rule without a
// known conjunction. Dropping a restricting rule broadens the playlist and an
// unmapped operator can invert it (0x03000002 is the exact inverse of
// 0x01000002), so a query is produced only when every rule is understood.
// The importer treats "" as "not importable" (ITUNES-SMARTCRIT-PARSE).
func TranslateSmartCriteria(parsed *SmartCriteriaResult) string {
	if !parsed.Resolved() || len(parsed.Rules) == 0 {
		return ""
	}

	joiner := ""
	switch {
	case len(parsed.Rules) == 1:
	case parsed.Conjunction == "AND":
		joiner = " "
	case parsed.Conjunction == "OR":
		joiner = " || "
	default:
		return ""
	}

	parts := make([]string, 0, len(parsed.Rules))
	for _, rule := range parsed.Rules {
		part := translateRule(rule)
		if part == "" {
			return ""
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, joiner)
}

// translateRule returns "" for any rule it cannot express exactly.
func translateRule(rule SmartRule) string {
	if !rule.Field.Known() || len(rule.Operands) != 1 || rule.Operands[0] == "" {
		return ""
	}
	field, operand := rule.Field.String(), rule.Operands[0]

	switch rule.Operator {
	case SmartOpContains:
		return fmt.Sprintf("%s:*%s*", field, operand)
	case SmartOpDoesNotContain:
		return fmt.Sprintf("-%s:*%s*", field, operand)
	default:
		return ""
	}
}
