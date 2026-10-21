// file: internal/decisions/fingerprint_test.go
// version: 1.1.0
// guid: 8d41f6a3-0c92-47be-b5e8-2a6710d3c9f4
// last-edited: 2026-10-21

package decisions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The golden vectors pin the byte encoding. They were also reproduced with an
// independent implementation (a few lines of Python: uvarint length-prefixed
// fields under the domain tag, sha256, hex). If one of these fails, a stored
// reject would no longer be found: do not update the constant, fix the code.
func TestSubjectKey_GoldenVectors(t *testing.T) {
	for _, g := range []struct {
		want, kind, typ, scope string
		ids                    []string
	}{
		{"1cdf20f9cd52f1b193aae7762e986a4b225dceed51b4cb39cc27df535183dc36", "repairs:dedup.split-books", "book", "", []string{"b2", "b1"}},
		{"5c0a598b100ca2a410e692b7f9721c0986f8f566816c416c3e356df13c44f30d", "authors:group-merge", "author", "s", []string{"12", "9", "12"}},
		{"9906b625b37dce2a9740901b2d9718872046f06e4e7be765835e60e654c39eca", "metadata:candidate", "book", "audible|B000", []string{"b1"}},
	} {
		// The checked entry point and the raw encoding agree on canonical input.
		require.Equal(t, g.want, subjectHash(g.kind, g.typ, g.ids, g.scope), g.kind)
		require.Equal(t, g.want, mustSubjectKey(t, g.kind, g.typ, g.ids, g.scope), g.kind)
	}
}

// ULID ids are upper-cased before hashing, so the golden vector holds for the
// canonical spelling and every other casing lands on the same key.
func TestSubjectKey_CanonicalisesULIDCase(t *testing.T) {
	const up = "01JB8Y3G5XK0S1M2N3P4Q5R6T7"
	const up2 = "01JB8Y3G5XK0S1M2N3P4Q5R6T8"
	lo, lo2 := strings.ToLower(up), strings.ToLower(up2)
	want := subjectHash("k", "book", []string{up, up2}, "")
	require.Equal(t, want, mustSubjectKey(t, "k", "book", []string{up, up2}, ""))
	require.Equal(t, want, mustSubjectKey(t, "k", "book", []string{lo, lo2}, ""))
	require.Equal(t, want, mustSubjectKey(t, "k", "book", []string{lo, up2, up}, ""), "two spellings of one id are one id")
	// A 26-character id that is not a ULID is left alone (case is meaningful).
	require.Equal(t, subjectHash("k", "t", []string{"abcdefghijklmnopqrstuvwxyz"}, ""),
		mustSubjectKey(t, "k", "t", []string{"abcdefghijklmnopqrstuvwxyz"}, ""))
}

func TestSubjectKey_RefusesNonCanonicalIDs(t *testing.T) {
	for name, ids := range map[string][]string{
		"leading space":  {" b1"},
		"trailing space": {"b1 "},
		"trailing tab":   {"b1	"},
		"only space":     {"  "},
		"empty":          {""},
		"control":        {"b\x001"},
		"none":           nil,
		"one bad of two": {"b1", "b2 "},
	} {
		_, err := SubjectKey("k", "book", ids, "")
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	_, err := SubjectKey("", "book", []string{"b1"}, "")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = SubjectKey("k", "", []string{"b1"}, "")
	require.ErrorIs(t, err, ErrInvalid)
}

func TestMaterialHash_GoldenVectors(t *testing.T) {
	require.Equal(t, "d3e65547e31d4eb2dba2c828f36db370ac38b44e245899f0bb5629088b96936d",
		MaterialHash(map[string]string{"series": "X", "position": "2"}))
	require.Equal(t, "6639d97a0a47125de11d5f22d9c61b66c3168cc8a41f6572797e351c73979136",
		MaterialHash(map[string]string{"a": ""}))
}

func TestSubjectKey_IgnoresIDOrderAndDuplicates(t *testing.T) {
	a := mustSubjectKey(t, "k", "book", []string{"x", "y", "z"}, "s")
	require.Equal(t, a, mustSubjectKey(t, "k", "book", []string{"z", "x", "y"}, "s"))
	require.Equal(t, a, mustSubjectKey(t, "k", "book", []string{"y", "x", "z", "x", "y"}, "s"))
}

func TestSubjectKey_FieldsCannotBleedIntoEachOther(t *testing.T) {
	seen := map[string]string{}
	cases := map[string]string{
		"ids ab,c":      subjectHash("k", "t", []string{"ab", "c"}, ""),
		"ids a,bc":      subjectHash("k", "t", []string{"a", "bc"}, ""),
		"ids abc":       subjectHash("k", "t", []string{"abc"}, ""),
		"kind k:t":      subjectHash("k:t", "", nil, ""),
		"kind k type t": subjectHash("k", "t", nil, ""),
		"scope s":       subjectHash("k", "t", []string{"a"}, "s"),
		"id as":         subjectHash("k", "t", []string{"as"}, ""),
		"other kind":    subjectHash("k2", "t", []string{"a"}, "s"),
		"other type":    subjectHash("k", "t2", []string{"a"}, "s"),
	}
	for name, h := range cases {
		require.Len(t, h, 64, name)
		if prev, dup := seen[h]; dup {
			t.Errorf("%q and %q hash alike", name, prev)
		}
		seen[h] = name
	}
}

func TestMaterialHash_MapOrderAndEmpty(t *testing.T) {
	a := MaterialHash(map[string]string{"a": "1", "b": "2", "c": "3"})
	for range 20 { // map iteration order is random; the hash must not be
		require.Equal(t, a, MaterialHash(map[string]string{"c": "3", "a": "1", "b": "2"}))
	}
	require.Equal(t, "", MaterialHash(nil))
	require.Equal(t, "", MaterialHash(map[string]string{}))
	require.NotEqual(t, MaterialHash(map[string]string{"a": "bc"}), MaterialHash(map[string]string{"ab": "c"}))
	require.NotEqual(t, MaterialHash(map[string]string{"a": ""}), MaterialHash(map[string]string{"": "a"}))
}

func TestSubjectAndMaterialDomainsDiffer(t *testing.T) {
	require.NotEqual(t, subjectHash("a", "b", nil, ""), MaterialHash(map[string]string{"a": "b"}))
}
