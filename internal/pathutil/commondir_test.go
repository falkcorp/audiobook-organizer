// file: internal/pathutil/commondir_test.go
// version: 1.0.0
// guid: 1e6c9a07-2d54-4b38-8f01-7a2e5c9d4b63
// last-edited: 2026-06-19

package pathutil

import "testing"

func TestCommonDir(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{
			name: "single file returns its dir",
			in:   []string{"/mnt/books/Author/Book/01.m4b"},
			want: "/mnt/books/Author/Book",
		},
		{
			name: "files in same dir",
			in:   []string{"/mnt/books/Author/Book/01.m4b", "/mnt/books/Author/Book/02.m4b"},
			want: "/mnt/books/Author/Book",
		},
		{
			name: "files in nested subdirs share the ancestor",
			in:   []string{"/mnt/books/Author/Book/cd1/01.m4b", "/mnt/books/Author/Book/cd2/01.m4b"},
			want: "/mnt/books/Author/Book",
		},
		{
			name: "no shared dir falls back to root",
			in:   []string{"/a/x.m4b", "/b/y.m4b"},
			want: "/",
		},
		{
			name: "empty input",
			in:   nil,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CommonDir(tc.in); got != tc.want {
				t.Errorf("CommonDir(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
