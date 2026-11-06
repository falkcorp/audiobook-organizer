// file: core/storage/keys/keys_test.go
// version: 1.0.0
// guid: d2ed0bc1-a73a-42fa-b272-e36ec0586416
// last-edited: 2026-11-05

package keys

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// randomSegs draws a segment list biased toward the bytes that matter to the
// encoding (0x00, 0x01, 0xFF) and toward shared prefixes, so the property
// checks hit the escape and terminator cases, not just random noise.
func randomSegs(r *rand.Rand) []Seg {
	alphabet := []byte{0x00, 0x01, 0x02, 'a', 'b', '/', ' ', 0xFE, 0xFF}
	n := r.IntN(4)
	segs := make([]Seg, n)
	for i := range segs {
		s := make(Seg, r.IntN(4))
		for j := range s {
			s[j] = alphabet[r.IntN(len(alphabet))]
		}
		segs[i] = s
	}
	return segs
}

// compareLists is the lexicographic order of segment lists (segments compared
// bytewise, a shorter list that is a prefix of a longer one first).
func compareLists(a, b []Seg) int {
	return slices.CompareFunc(a, b, func(x, y Seg) int { return bytes.Compare(x, y) })
}

func TestJoinOrderMatchesSegmentOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		a, b := randomSegs(r), randomSegs(r)
		want := compareLists(a, b)
		got := bytes.Compare(Join(a...), Join(b...))
		if got != want {
			t.Fatalf("order mismatch: lists %q vs %q compare %d, keys %q vs %q compare %d",
				a, b, want, Join(a...), Join(b...), got)
		}
		// Every key that extends a lies in [Prefix(a), PrefixEnd(Prefix(a))).
		ext := Join(append(slices.Clone(a), b...)...)
		lo, hi := Prefix(a...), PrefixEnd(Prefix(a...))
		if bytes.Compare(ext, lo) < 0 || (hi != nil && bytes.Compare(ext, hi) >= 0) {
			t.Fatalf("extension %q of %q is outside [%q, %q)", ext, a, lo, hi)
		}
	}
}

func TestSplitInvertsJoin(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 5000 {
		segs := randomSegs(r)
		got, err := Split(Join(segs...))
		if err != nil {
			t.Fatalf("Split(Join(%q)): %v", segs, err)
		}
		if compareLists(got, segs) != 0 || len(got) != len(segs) {
			t.Fatalf("Split(Join(%q)) = %q", segs, got)
		}
	}
	for _, bad := range [][]byte{
		{'a'},             // no terminator
		{'a', 0x00},       // escape at end
		{'a', 0x00, 0x02}, // unknown escape code
	} {
		if _, err := Split(bad); !errors.Is(err, ErrMalformed) {
			t.Errorf("Split(%q) error = %v, want ErrMalformed", bad, err)
		}
	}
	if segs, err := Split(nil); err != nil || len(segs) != 0 {
		t.Errorf("Split(nil) = %q, %v; want no segments", segs, err)
	}
}

func TestPrefixEnd(t *testing.T) {
	cases := []struct {
		in, want []byte
	}{
		{[]byte("a"), []byte("b")},
		{[]byte{'a', 0xFF}, []byte("b")},
		{[]byte{'a', 0xFE, 0xFF, 0xFF}, []byte{'a', 0xFF}},
		{[]byte{0xFF, 0xFF}, nil},
		{nil, nil},
	}
	for _, tc := range cases {
		if got := PrefixEnd(tc.in); !bytes.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
			t.Errorf("PrefixEnd(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	in := []byte("ab")
	_ = PrefixEnd(in)
	if string(in) != "ab" {
		t.Errorf("PrefixEnd modified its argument: %q", in)
	}
}

func TestNumericSegmentsSortNumerically(t *testing.T) {
	if bytes.Compare(Join(U64(2)), Join(U64(10))) >= 0 {
		t.Error("U64(2) must sort before U64(10)")
	}
	before := time.Unix(-5, 0)
	after := time.Unix(5, 0)
	if bytes.Compare(Join(Time(before)), Join(Time(after))) >= 0 {
		t.Error("a time before 1970 must sort before one after it")
	}
	b := []byte{1, 2}
	s := Bytes(b)
	b[0] = 9
	if s[0] != 1 {
		t.Error("Bytes must copy its argument")
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"a", "tv", "series_2", "abcdefghijklmnopqrstuvwxyz012345"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "A", "a-b", "a/b", "a b", "abcdefghijklmnopqrstuvwxyz0123456"} {
		if err := ValidName(bad); err == nil {
			t.Errorf("ValidName(%q) = nil, want an error", bad)
		}
	}
}
