// file: internal/tagger/verify_test.go
// version: 1.1.0
// guid: 5b2c9e17-8a40-4d63-9f1e-d07a3c6b8e24
// last-edited: 2026-10-11

package tagger

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestValuesMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		key       string
		want, got []string
		ok        bool
	}{
		{"equal", "TITLE", []string{"a"}, []string{"a"}, true},
		{"different", "TITLE", []string{"a"}, []string{"b"}, false},
		{"missing", "TITLE", []string{"a"}, nil, false},
		{"order", "NARRATOR", []string{"a", "b"}, []string{"b", "a"}, false},
		{"second value", "NARRATOR", []string{"a", "b"}, []string{"a", "c"}, false},
		{"extra value", "NARRATOR", []string{"a"}, []string{"a", "b"}, false},
		{"delete reads absent", "ALBUM", nil, nil, true},
		{"delete reads empty", "ALBUM", []string{}, []string{""}, true},
		{"empty string is a delete", "ALBUM", []string{""}, nil, true},
		{"delete left a value", "ALBUM", []string{""}, []string{"x"}, false},
		{"no-value delete left a value", "ALBUM", nil, []string{"x"}, false},
		{"separator splits on read", "TITLE", []string{"a\vb"}, []string{"a", "b"}, true},
		{"separator kept on read", "TITLE", []string{"a\vb"}, []string{"a\vb"}, true},
		{"mp4 padded track", "TRACKNUMBER", []string{"03"}, []string{"3"}, true},
		{"mp4 padded track of total", "TRACKNUMBER", []string{"03/010"}, []string{"3/10"}, true},
		{"mp4 padded disc", "DISCNUMBER", []string{"01/02"}, []string{"1/2"}, true},
		{"mp4 bpm", "BPM", []string{"0120"}, []string{"120"}, true},
		{"mp4 compilation", "COMPILATION", []string{"01"}, []string{"1"}, true},
		{"numeric differs", "TRACKNUMBER", []string{"3"}, []string{"4"}, false},
		{"non-numeric track is text", "TRACKNUMBER", []string{"x"}, []string{"0"}, false},
		{"zero stays zero", "TRACKNUMBER", []string{"000"}, []string{"0"}, true},
		{"non-numeric key keeps zeros", "TITLE", []string{"03"}, []string{"3"}, false},
	}
	for _, c := range cases {
		if got := valuesMatch(c.key, c.want, c.got); got != c.ok {
			t.Errorf("%s: valuesMatch(%s, %q, %q) = %v; want %v", c.name, c.key, c.want, c.got, got, c.ok)
		}
	}
}

func TestFoldTagKeys(t *testing.T) {
	t.Parallel()
	in := map[string][]string{"title": {"lower"}, "TITLE": {"UPPER"}, "Album": {"x"}, "zed": {"z"}, "Zed": {"Z"}}
	for i := 0; i < 50; i++ {
		got := FoldTagKeys(in)
		if len(got) != 3 || got["TITLE"][0] != "UPPER" || got["ALBUM"][0] != "x" {
			t.Fatalf("FoldTagKeys = %v", got)
		}
		// No upper-case spelling: the first in sorted order ("Zed" < "zed") wins.
		if got["ZED"][0] != "Z" {
			t.Fatalf("ZED = %v; want the first sorted spelling's value Z", got["ZED"])
		}
	}
	if len(in) != 5 {
		t.Fatal("FoldTagKeys modified its input")
	}
}

// "The key already upper-case wins" must hold even where the upper-case
// spelling does not sort first: Georgian Mkhedruli "\u10d0" (U+10D0) upper-cases
// to Mtavruli "\u1c90" (U+1C90), which sorts after it, so the exact upper-case
// key arrives second and has to displace the lower-case spelling.
func TestFoldTagKeys_UpperCaseKeyWinsWhenItSortsAfter(t *testing.T) {
	t.Parallel()
	lower, upper := "\u10d0", "\u1c90"
	if strings.ToUpper(lower) != upper || lower >= upper {
		t.Fatalf("fixture error: want %q to upper-case to %q and sort before it", lower, upper)
	}
	got := FoldTagKeys(map[string][]string{lower: {"lower"}, upper: {"UPPER"}})
	if len(got) != 1 || len(got[upper]) != 1 || got[upper][0] != "UPPER" {
		t.Fatalf("FoldTagKeys = %v; want the upper-case spelling's value", got)
	}
}

func TestVerifyAfterWriteError_BoundedAndUnwraps(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 5000)
	want := map[string][]string{}
	for _, k := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O", "P"} {
		want[k] = []string{long}
	}
	prev := tagReader
	tagReader = func(string) (map[string][]string, error) { return map[string][]string{}, nil }
	t.Cleanup(func() { tagReader = prev })

	verr := verifyWrittenTags("/x.m4b", want)
	if verr == nil || len(verr.Keys) != 16 {
		t.Fatalf("verr = %v; want 16 keys", verr)
	}
	if d := verr.Detail(); len(d) > maxDetailBytes+200 || !strings.Contains(d, "more keys") {
		t.Errorf("Detail is %d bytes (%q...); want it bounded near %d and to name the keys left out", len(d), d[:60], maxDetailBytes)
	}
	if msg := verr.Error(); len(msg) > maxErrorBytes+300 {
		t.Errorf("Error is %d bytes; want it bounded", len(msg))
	}
	if !errors.Is(verr, ErrVerifyAfterWrite) {
		t.Error("does not unwrap to ErrVerifyAfterWrite")
	}

	tagReader = func(string) (map[string][]string, error) { return nil, os.ErrPermission }
	verr = verifyWrittenTags("/x.m4b", map[string][]string{"A": {"1"}})
	if !errors.Is(verr, ErrVerifyAfterWrite) || !errors.Is(verr, os.ErrPermission) {
		t.Errorf("read failure: %v does not unwrap to both ErrVerifyAfterWrite and the read error", verr)
	}
}
