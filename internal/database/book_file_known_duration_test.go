// file: internal/database/book_file_known_duration_test.go
// version: 1.0.1
// guid: 2e9b6f40-7a15-4c83-b0d2-4f1a8e5c3d69
// last-edited: 2026-11-04

package database

import "testing"

func TestKnownDurationAtPath(t *testing.T) {
	const size = int64(90_000_000)
	const hush = 27000 // 7.5 h of "Batman: Hush"
	row := func(mod func(*BookFileCore)) BookFileCore {
		r := BookFileCore{FilePath: "/lib/Batman Hush.m4b", FileSize: size, FileHash: "h1", Duration: Secs(hush)}
		if mod != nil {
			mod(&r)
		}
		return r
	}
	cases := []struct {
		name string
		rows []BookFileCore
		size int64
		hash string
		want int
	}{
		{"same size and hash", []BookFileCore{row(nil)}, size, "h1", hush},
		{"hash unknown on the caller", []BookFileCore{row(nil)}, size, "", hush},
		{"hash unknown on the row", []BookFileCore{row(func(r *BookFileCore) { r.FileHash = "" })}, size, "h1", hush},
		{"hash differs", []BookFileCore{row(nil)}, size, "h2", 0},
		{"size differs", []BookFileCore{row(nil)}, size + 1, "h1", 0},
		{"unknown caller size", []BookFileCore{row(nil)}, 0, "h1", 0},
		{"missing row", []BookFileCore{row(func(r *BookFileCore) { r.Missing = true })}, size, "h1", 0},
		{"zero duration row", []BookFileCore{row(func(r *BookFileCore) { r.Duration = nil })}, size, "h1", 0},
		{"millisecond value", []BookFileCore{row(func(r *BookFileCore) { r.Duration = Secs(hush * 1000) })}, size, "h1", 0},
		{"skips unusable row for a usable one",
			[]BookFileCore{row(func(r *BookFileCore) { r.Duration = nil }), row(nil)}, size, "h1", hush},
		{"no rows", nil, size, "h1", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := KnownDurationAtPath(c.rows, c.size, c.hash); got != c.want {
				t.Fatalf("KnownDurationAtPath = %d, want %d", got, c.want)
			}
		})
	}
}
