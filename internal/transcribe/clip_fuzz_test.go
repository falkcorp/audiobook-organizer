// file: internal/transcribe/clip_fuzz_test.go
// version: 1.0.0
// guid: 3e7b1c58-9d24-4a60-8f13-b5c2a0e96d47
// last-edited: 2026-11-17

package transcribe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// maxClipNumber bounds every number ParseClip returns: digit numbers are capped
// at maxDigitChapter (3) digits and a spelled number cannot exceed 999.
const maxClipNumber = 999

// FuzzParseClip pins what the hot path needs from ParseClip: it runs on every new
// transcription, so a panic would take down a transcription worker. For any
// input it must not panic, must answer deterministically, must name a kind it
// can store, and must not return more text than it was given (a case fold can
// lengthen a string, never by more than half again) or a number outside what the
// grammar accepts.
//
// The seeds are invented text (Batman titles and characters) plus the hostile
// shapes a transcript can take: megabyte-scale repeats, no spaces, long digit
// runs after "chapter", and multibyte case folds. Run it for a while with
//
//	go test ./internal/transcribe -run '^$' -fuzz FuzzParseClip -fuzztime 60s
func FuzzParseClip(f *testing.F) {
	seeds := []string{
		"",
		" ",
		"Gotham Audio presents, The Killing Joke, written by Alfred Pennyworth, read by Selina Kyle",
		"The Long Halloween. Written by Lucius Fox. Narrated by Selina Kyle. Translated by Harvey Dent.",
		"Chapter 12. The Court of Owls. Bruce Wayne walked down the long corridor of the old manor while the rain kept falling.",
		"Chapter twenty three. Hush.",
		"Part two. Year One.",
		"Book one of the Dark Knight collection, books one through six.",
		"Prologue. Epilogue. Foreword. Afterword.",
		"Chapter XIV: Arkham",
		"(music)",
		"[BLANK_AUDIO]",
		"Thank you for watching. Thanks for watching.",
		"Batman Batman Batman Batman Batman Batman Batman Batman Batman Batman Batman Batman",
		"Cover art by Barbara Gordon. Published by Wayne Enterprises. A production of Gotham Audio.",
		"Chapter 99999999999999999999999999999999999999999999999999. The Joker.",
		"chapter " + strings.Repeat("9", 4096),
		"chapter " + strings.Repeat("one ", 2048),
		strings.Repeat("a", 1<<16),
		strings.Repeat("Chapter 1. ", 512),
		strings.Repeat("written by ", 512) + "Dick Grayson",
		strings.Repeat("İ", 2048),
		"Chapter İ K Kelvin ſ long s",
		"\xff\xfe\x00 not utf-8 \xc3\x28",
		"♪♫♬",
		"Selina Kyle presents Gotham Audio presents Alfred Pennyworth presents",
		"Part Ⅰ. Book Ⅹ.",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p := ParseClip(s)
		if q := ParseClip(s); q != p {
			t.Fatalf("ParseClip is not deterministic for %q", s)
		}
		if p.Kind != ClipKindNone {
			ok := false
			for _, k := range AllClipKinds {
				if p.Kind == k {
					ok = true
				}
			}
			if !ok {
				t.Fatalf("kind %q is not storable (input %q)", p.Kind, s)
			}
		}
		limit := len(s) + len(s)/2 + 8
		for name, v := range map[string]string{
			"ChapterTitle": p.ChapterTitle, "Marker": p.Marker, "Title": p.Title, "Author": p.Author,
			"Narrator": p.Narrator, "Translator": p.Translator, "CoverArtist": p.CoverArtist, "Publisher": p.Publisher,
		} {
			if len(v) > limit {
				t.Fatalf("%s is %d bytes for a %d-byte input", name, len(v), len(s))
			}
			if utf8.ValidString(s) && !utf8.ValidString(v) {
				t.Fatalf("%s is not valid UTF-8 for a valid input %q", name, s)
			}
		}
		for name, n := range map[string]int{
			"ChapterNumber": p.ChapterNumber, "PartNumber": p.PartNumber, "CollectionFrom": p.CollectionFrom, "CollectionTo": p.CollectionTo,
		} {
			if n < 0 || n > maxClipNumber {
				t.Fatalf("%s = %d is outside 0..%d (input %q)", name, n, maxClipNumber, s)
			}
		}
	})
}
