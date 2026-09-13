// file: internal/itunes/smart_criteria_test.go
// version: 2.0.0
// guid: 0d8e9f7a-1b2c-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-09-13

package itunes

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

// Fixture builders.
//
// These encode the layout MEASURED over 292 real smart playlists in
// ITUNES-SMARTCRIT-PARSE (issue #2658) — big-endian, "SLst" magic, operand
// located by a u32be byte-length prefix at off-4, field u32be at off-56 and
// operator word u32be at off-52. They are synthetic, not captured ground
// truth: the real blobs live in the owner's private library and are not in
// the repo. Operand strings here are made up.

// slstHeader returns an SLst container header padded to 136 bytes.
func slstHeader() []byte {
	h := make([]byte, 136)
	copy(h, "SLst")
	return h
}

// ruleBytes encodes one string-operand rule: a 56-byte header region (field
// at 0, operator word at 4, byte-length at 52) followed by the UTF-16BE
// operand and 4 bytes of zero padding.
func ruleBytes(field SmartField, op SmartOperator, operand string, lengthOverride int) []byte {
	units := utf16.Encode([]rune(operand))
	b := make([]byte, 56+len(units)*2+4)
	binary.BigEndian.PutUint32(b[0:], uint32(field))
	binary.BigEndian.PutUint32(b[4:], uint32(op))
	n := len(units) * 2
	if lengthOverride >= 0 {
		n = lengthOverride
	}
	binary.BigEndian.PutUint32(b[52:], uint32(n))
	for i, u := range units {
		binary.BigEndian.PutUint16(b[56+2*i:], u)
	}
	return b
}

func blob(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func hasUnresolved(r *SmartCriteriaResult, substr string) bool {
	for _, u := range r.Unresolved {
		if strings.Contains(u, substr) {
			return true
		}
	}
	return false
}

func TestParseSmartCriteria_PerCriterion(t *testing.T) {
	tests := []struct {
		name         string
		field        SmartField
		op           SmartOperator
		operand      string
		wantField    string
		wantOp       string
		wantUnknownF bool
		wantUnknownO bool
	}{
		{name: "album contains", field: 3, op: 0x01000002, operand: "Example Saga", wantField: "album", wantOp: "contains"},
		{name: "artist contains", field: 4, op: 0x01000002, operand: "Jane Placeholder", wantField: "author", wantOp: "contains"},
		{name: "genre contains", field: 8, op: 0x01000002, operand: "Fantasy", wantField: "genre", wantOp: "contains"},
		{name: "album does not contain", field: 3, op: 0x03000002, operand: "Sample Box Set", wantField: "album", wantOp: "does_not_contain"},
		{name: "non-ASCII operand", field: 4, op: 0x01000002, operand: "Zoë Exämple \U0001F4DA", wantField: "author", wantOp: "contains"},
		{name: "field 71 unvalidated", field: 71, op: 0x01000002, operand: "Someone", wantField: "unknown_0x47", wantOp: "contains", wantUnknownF: true},
		{name: "field 2 unvalidated", field: 2, op: 0x01000002, operand: "Part One", wantField: "unknown_0x02", wantOp: "contains", wantUnknownF: true},
		{name: "field 14 unvalidated", field: 14, op: 0x01000002, operand: "note", wantField: "unknown_0x0E", wantOp: "contains", wantUnknownF: true},
		{name: "operator 0x01000001 unresolved", field: 3, op: 0x01000001, operand: "Example Saga", wantField: "album", wantOp: "op_0x01000001", wantUnknownO: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ParseSmartCriteria(blob(slstHeader(), ruleBytes(tc.field, tc.op, tc.operand, -1)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(res.Rules) != 1 {
				t.Fatalf("rules = %d, want 1 (%+v)", len(res.Rules), res.Rules)
			}
			r := res.Rules[0]
			if got := r.Field.String(); got != tc.wantField {
				t.Errorf("field = %q, want %q", got, tc.wantField)
			}
			if got := r.Operator.String(); got != tc.wantOp {
				t.Errorf("operator = %q, want %q", got, tc.wantOp)
			}
			if len(r.Operands) != 1 || r.Operands[0] != tc.operand {
				t.Errorf("operands = %q, want [%q]", r.Operands, tc.operand)
			}
			if r.Offset != 136+56 {
				t.Errorf("offset = %d, want %d", r.Offset, 136+56)
			}
			if got := hasUnresolved(res, "unvalidated field"); got != tc.wantUnknownF {
				t.Errorf("unvalidated-field flagged = %v, want %v (%q)", got, tc.wantUnknownF, res.Unresolved)
			}
			if got := hasUnresolved(res, "unvalidated operator"); got != tc.wantUnknownO {
				t.Errorf("unvalidated-operator flagged = %v, want %v (%q)", got, tc.wantUnknownO, res.Unresolved)
			}
			// The parser can never see numeric/date rules or map nesting, so
			// it must never present a result as complete.
			if res.Resolved() {
				t.Errorf("result claims to be resolved: %q", res.Unresolved)
			}
			if res.Conjunction != "" {
				t.Errorf("conjunction = %q, want empty (flag not located)", res.Conjunction)
			}
		})
	}
}

// The contains / does-not-contain pair differs only in the high byte of the
// operator word. Mixing them up inverts a playlist, so assert they decode to
// different operators and translate to opposite queries.
func TestParseSmartCriteria_NegationIsDistinct(t *testing.T) {
	pos, err := ParseSmartCriteria(blob(slstHeader(), ruleBytes(SmartFieldAlbum, SmartOpContains, "Example Saga", -1)))
	if err != nil {
		t.Fatal(err)
	}
	neg, err := ParseSmartCriteria(blob(slstHeader(), ruleBytes(SmartFieldAlbum, SmartOpDoesNotContain, "Example Saga", -1)))
	if err != nil {
		t.Fatal(err)
	}
	if pos.Rules[0].Operator == neg.Rules[0].Operator {
		t.Fatalf("both decoded to %v", pos.Rules[0].Operator)
	}
	p := TranslateSmartCriteria(&SmartCriteriaResult{Rules: pos.Rules})
	n := TranslateSmartCriteria(&SmartCriteriaResult{Rules: neg.Rules})
	if p != "album:*Example Saga*" || n != "-album:*Example Saga*" {
		t.Errorf("translations = %q / %q", p, n)
	}
}

func TestParseSmartCriteria_RejectsNonSLst(t *testing.T) {
	// A blob of plausible length with a valid-looking rule but no magic: the
	// old parser returned success on anything, which is the bug.
	b := blob(slstHeader(), ruleBytes(SmartFieldAlbum, SmartOpContains, "Example", -1))
	copy(b, "XXXX")
	if _, err := ParseSmartCriteria(b); err == nil {
		t.Fatal("expected error for blob without SLst magic")
	}
	if _, err := ParseSmartCriteria([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for short blob")
	}
}

func TestParseSmartCriteria_MagicOnlyHasNoRules(t *testing.T) {
	// The same 8 bytes the XML reader test feeds through.
	res, err := ParseSmartCriteria([]byte("SLst\x00\x01\x00\x01"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Rules) != 0 {
		t.Errorf("rules = %+v, want none", res.Rules)
	}
	if res.Resolved() {
		t.Error("empty result must not be resolved")
	}
	if got := TranslateSmartCriteria(res); got != "" {
		t.Errorf("translation = %q, want empty (never match-all)", got)
	}
}

func TestParseSmartCriteria_LengthPrefixMismatchRejected(t *testing.T) {
	for _, n := range []int{0, 6, 22, 1 << 20} {
		res, err := ParseSmartCriteria(blob(slstHeader(), ruleBytes(SmartFieldAlbum, SmartOpContains, "Example Saga", n)))
		if err != nil {
			t.Fatalf("n=%d: parse: %v", n, err)
		}
		if len(res.Rules) != 0 {
			t.Errorf("n=%d: accepted misaligned operand(s) %+v", n, res.Rules)
		}
	}
}

func TestParseSmartCriteria_MultipleRulesAndContainers(t *testing.T) {
	b := blob(
		slstHeader(),
		ruleBytes(SmartFieldArtist, SmartOpContains, "Jane Placeholder", -1),
		slstHeader(), // nested container
		ruleBytes(SmartFieldGenre, SmartOpDoesNotContain, "Horror", -1),
	)
	res, err := ParseSmartCriteria(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Containers != 2 {
		t.Errorf("containers = %d, want 2", res.Containers)
	}
	if len(res.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(res.Rules))
	}
	if res.Rules[0].Operands[0] != "Jane Placeholder" || res.Rules[0].Field != SmartFieldArtist {
		t.Errorf("rule 0 = %+v", res.Rules[0])
	}
	if res.Rules[1].Operands[0] != "Horror" || res.Rules[1].Operator != SmartOpDoesNotContain {
		t.Errorf("rule 1 = %+v", res.Rules[1])
	}
	if !hasUnresolved(res, "conjunction") {
		t.Errorf("multi-rule result must flag the unlocated conjunction: %q", res.Unresolved)
	}
	if got := TranslateSmartCriteria(res); got != "" {
		t.Errorf("translation = %q, want empty", got)
	}
}

func TestTranslateSmartCriteria(t *testing.T) {
	contains := SmartRule{Field: SmartFieldArtist, Operator: SmartOpContains, Operands: []string{"Jane Placeholder"}}
	excludes := SmartRule{Field: SmartFieldGenre, Operator: SmartOpDoesNotContain, Operands: []string{"Horror"}}
	tests := []struct {
		name string
		in   *SmartCriteriaResult
		want string
	}{
		{name: "nil", in: nil, want: ""},
		{name: "no rules", in: &SmartCriteriaResult{}, want: ""},
		{name: "single contains", in: &SmartCriteriaResult{Rules: []SmartRule{contains}}, want: "author:*Jane Placeholder*"},
		{name: "single does not contain", in: &SmartCriteriaResult{Rules: []SmartRule{excludes}}, want: "-genre:*Horror*"},
		{name: "AND", in: &SmartCriteriaResult{Conjunction: "AND", Rules: []SmartRule{contains, excludes}}, want: "author:*Jane Placeholder* -genre:*Horror*"},
		{name: "OR", in: &SmartCriteriaResult{Conjunction: "OR", Rules: []SmartRule{contains, excludes}}, want: "author:*Jane Placeholder* || -genre:*Horror*"},
		{name: "multi-rule without conjunction", in: &SmartCriteriaResult{Rules: []SmartRule{contains, excludes}}, want: ""},
		{name: "unresolved result", in: &SmartCriteriaResult{Rules: []SmartRule{contains}, Unresolved: []string{"x"}}, want: ""},
		{name: "unknown operator is not an equality", in: &SmartCriteriaResult{Rules: []SmartRule{{Field: SmartFieldAlbum, Operator: 0x01000001, Operands: []string{"X"}}}}, want: ""},
		{name: "unknown field is not dropped", in: &SmartCriteriaResult{Conjunction: "AND", Rules: []SmartRule{contains, {Field: 71, Operator: SmartOpContains, Operands: []string{"Y"}}}}, want: ""},
		{name: "empty operand", in: &SmartCriteriaResult{Rules: []SmartRule{{Field: SmartFieldAlbum, Operator: SmartOpContains, Operands: []string{""}}}}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TranslateSmartCriteria(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
