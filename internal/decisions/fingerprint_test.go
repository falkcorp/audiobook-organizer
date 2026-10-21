// file: internal/decisions/fingerprint_test.go
// version: 1.0.0
// guid: 8d41f6a3-0c92-47be-b5e8-2a6710d3c9f4
// last-edited: 2026-10-21

package decisions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The golden vectors pin the byte encoding. They were also reproduced with an
// independent implementation (a few lines of Python: uvarint length-prefixed
// fields under the domain tag, sha256, hex). If one of these fails, a stored
// reject would no longer be found: do not update the constant, fix the code.
func TestSubjectKey_GoldenVectors(t *testing.T) {
	require.Equal(t, "1cdf20f9cd52f1b193aae7762e986a4b225dceed51b4cb39cc27df535183dc36",
		SubjectKey("repairs:dedup.split-books", "book", []string{"b2", "b1"}, ""))
	require.Equal(t, "5c0a598b100ca2a410e692b7f9721c0986f8f566816c416c3e356df13c44f30d",
		SubjectKey("authors:group-merge", "author", []string{"12", "9", "12"}, "s"))
	require.Equal(t, "9906b625b37dce2a9740901b2d9718872046f06e4e7be765835e60e654c39eca",
		SubjectKey("metadata:candidate", "book", []string{"b1"}, "audible|B000"))
}

func TestMaterialHash_GoldenVectors(t *testing.T) {
	require.Equal(t, "d3e65547e31d4eb2dba2c828f36db370ac38b44e245899f0bb5629088b96936d",
		MaterialHash(map[string]string{"series": "X", "position": "2"}))
	require.Equal(t, "6639d97a0a47125de11d5f22d9c61b66c3168cc8a41f6572797e351c73979136",
		MaterialHash(map[string]string{"a": ""}))
}

func TestSubjectKey_IgnoresIDOrderAndDuplicates(t *testing.T) {
	a := SubjectKey("k", "book", []string{"x", "y", "z"}, "s")
	require.Equal(t, a, SubjectKey("k", "book", []string{"z", "x", "y"}, "s"))
	require.Equal(t, a, SubjectKey("k", "book", []string{"y", "x", "z", "x", "y"}, "s"))
}

func TestSubjectKey_FieldsCannotBleedIntoEachOther(t *testing.T) {
	seen := map[string]string{}
	cases := map[string]string{
		"ids ab,c":      SubjectKey("k", "t", []string{"ab", "c"}, ""),
		"ids a,bc":      SubjectKey("k", "t", []string{"a", "bc"}, ""),
		"ids abc":       SubjectKey("k", "t", []string{"abc"}, ""),
		"kind k:t":      SubjectKey("k:t", "", nil, ""),
		"kind k type t": SubjectKey("k", "t", nil, ""),
		"scope s":       SubjectKey("k", "t", []string{"a"}, "s"),
		"id as":         SubjectKey("k", "t", []string{"as"}, ""),
		"other kind":    SubjectKey("k2", "t", []string{"a"}, "s"),
		"other type":    SubjectKey("k", "t2", []string{"a"}, "s"),
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
	require.NotEqual(t, SubjectKey("a", "b", nil, ""), MaterialHash(map[string]string{"a": "b"}))
}
