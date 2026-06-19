// file: internal/pathutil/abbreviate_test.go
// version: 1.0.0
// guid: 9b1f4e2a-6c83-4d07-a5e1-2f9c8b4d6071
// last-edited: 2026-06-19

package pathutil

import "testing"

func TestAbbreviate(t *testing.T) {
	// libroot is intentionally *under* books so we can prove the
	// most-specific-first ordering: a path under libroot must never render
	// as $(books)/audiobook-organizer/...
	vars := []PathVar{
		{Name: "libroot", Value: "/mnt/bigdata/books/audiobook-organizer"},
		{Name: "books", Value: "/mnt/bigdata/books"},
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "path under libroot",
			in:   "/mnt/bigdata/books/audiobook-organizer/Sanderson/Mistborn/01.m4b",
			want: "$(libroot)/Sanderson/Mistborn/01.m4b",
		},
		{
			name: "path under books but not libroot",
			in:   "/mnt/bigdata/books/itunes/iTunes Media/x.m4a",
			want: "$(books)/itunes/iTunes Media/x.m4a",
		},
		{
			name: "libroot wins over books for a libroot path",
			in:   "/mnt/bigdata/books/audiobook-organizer/Author/book.m4b",
			want: "$(libroot)/Author/book.m4b",
		},
		{
			name: "exact libroot",
			in:   "/mnt/bigdata/books/audiobook-organizer",
			want: "$(libroot)",
		},
		{
			name: "exact books",
			in:   "/mnt/bigdata/books",
			want: "$(books)",
		},
		{
			name: "unrelated path unchanged",
			in:   "/var/lib/audiobook-organizer/audiobooks.pebble",
			want: "/var/lib/audiobook-organizer/audiobooks.pebble",
		},
		{
			name: "sibling that shares a name prefix is not a match",
			in:   "/mnt/bigdata/books-archive/x.m4b",
			want: "/mnt/bigdata/books-archive/x.m4b",
		},
		{
			name: "empty path",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Abbreviate(tc.in, vars); got != tc.want {
				t.Errorf("Abbreviate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// An empty var value must never swallow every path (would turn "" into a
// prefix match for everything).
func TestAbbreviate_SkipsEmptyVarValue(t *testing.T) {
	vars := []PathVar{{Name: "libroot", Value: ""}}
	in := "/some/path/file.m4b"
	if got := Abbreviate(in, vars); got != in {
		t.Errorf("Abbreviate(%q) with empty var = %q, want unchanged", in, got)
	}
}
