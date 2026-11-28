// file: internal/books/cutover/m3/description_test.go
// version: 0.1.0
// guid: 15f71c2e-47bb-41f5-a3fc-47b4b40f8a64
// last-edited: 2026-11-22

package m3

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// TestNormalizeDescription: each rule of the import's tidy-up on its own, the
// fixed words it reports, and that every output satisfies core's own rule
// (auth.CheckKeyDescription), so the two cannot drift apart.
func TestNormalizeDescription(t *testing.T) {
	cap := auth.MaxKeyDescriptionRunes
	for _, tc := range []struct {
		name, in, want string
		changes        []string
	}{
		{"clean", "Bat-Signal spare", "Bat-Signal spare", nil},
		{"empty", "", "", nil},
		{"non-ascii kept", "café 幽靈", "café 幽靈", nil},
		{"inner spaces kept", "a  b", "a  b", nil},
		{"a real U+FFFD is kept", "a�b", "a�b", nil},
		{"lf", "a\nb", "a b", []string{descLineBreaks}},
		{"crlf run is one space", "a\r\n\r\n\tb", "a b", []string{descLineBreaks}},
		{"two runs are two spaces", "a\nb\nc", "a b c", []string{descLineBreaks}},
		{"tab", "a\tb", "a b", []string{descLineBreaks}},
		{"other controls dropped", "a\x00b\x1bc\x7fd\u0085e", "abcde", []string{descControls}},
		{"a control between breaks splits the run", "a\n\x00\nb", "a  b", []string{descLineBreaks, descControls}},
		{"invalid bytes replaced one each", "a\xff\xfeb", "a��b", []string{descInvalidUTF8}},
		{"trimmed", "  a b  ", "a b", []string{descTrimmed}},
		{"a trailing line break is trimmed too", "a b\n", "a b", []string{descLineBreaks, descTrimmed}},
		{"only controls", "\n\x00\n", "", []string{descLineBreaks, descControls, descTrimmed}},
		{"exactly the cap", strings.Repeat("a", cap), strings.Repeat("a", cap), nil},
		{"one over", strings.Repeat("a", cap+1), strings.Repeat("a", cap), []string{descTruncated}},
		{"cut by characters not bytes", strings.Repeat("é", cap+5), strings.Repeat("é", cap), []string{descTruncated}},
		{"everything", "  x\ny\x00" + strings.Repeat("z", cap) + "\xff", "x y" + strings.Repeat("z", cap-3), []string{descInvalidUTF8, descLineBreaks, descControls, descTrimmed, descTruncated}},
	} {
		got, changes := normalizeDescription(tc.in)
		if got != tc.want || !slices.Equal(changes, tc.changes) {
			t.Errorf("%s: (%q, %v), want (%q, %v)", tc.name, got, changes, tc.want, tc.changes)
		}
		if err := auth.CheckKeyDescription(got); err != nil {
			t.Errorf("%s: the output breaks core's rule: %v", tc.name, err)
		}
		if again, more := normalizeDescription(got); tc.name != "everything" && (again != got || len(more) != 0) {
			t.Errorf("%s: normalizing the output again changes it: (%q, %v)", tc.name, again, more)
		}
	}
}

// TestMapKey_CarriesTheDescription: HEAD's description lands on the core key
// (tidied only when core's rule needs it), and a note says so without the text.
func TestMapKey_CarriesTheDescription(t *testing.T) {
	m := testMigration(t)
	scopes := []rbac.Permission{"books:view"}
	row := func(d string) *headKey {
		return &headKey{ID: "01M7ZZZKEYDESC00000000001", UserID: "01M7ZZZALFRED0000000000001", Name: "bat-signal",
			TokenHash: strings.Repeat("cd", 32), Status: "active", CreatedAt: testNow, Description: d}
	}
	k, _, notes, err := m.mapKey(row("Bat-Signal spare"), scopes)
	if err != nil || k.Description != "Bat-Signal spare" || len(notes.description) != 0 {
		t.Fatalf("clean: %+v notes %v err %v", k, notes.description, err)
	}
	k, _, notes, err = m.mapKey(row("line one\nline two"), scopes)
	if err != nil || k.Description != "line one line two" || !slices.Equal(notes.description, []string{descLineBreaks}) {
		t.Fatalf("multiline: %+v notes %v err %v", k, notes.description, err)
	}
	// Whatever the description, the mapped key passes the importer's rule.
	hash, _ := tokenHash(strings.Repeat("cd", 32))
	for _, d := range []string{"", "a\nb", strings.Repeat("x", 5000), "\x00\xff", "  "} {
		k, _, _, err := m.mapKey(row(d), scopes)
		if err != nil {
			t.Fatal(err)
		}
		if err := auth.CheckImportAPIKey(&k, hash); err != nil {
			t.Errorf("description %.20q: the importer rejects the mapped key: %v", d, err)
		}
	}
}
