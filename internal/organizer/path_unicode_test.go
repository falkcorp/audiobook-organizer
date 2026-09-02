// file: internal/organizer/path_unicode_test.go
// version: 1.0.1
// guid: 5c1e9a73-2d84-4f16-b0a7-9e3c25d8f401
// last-edited: 2026-09-02

// Path construction against non-ASCII metadata.
//
// The library is served to Windows clients over SMB (W:\ maps to the same tree
// Linux sees as the library root), so a component has to be legal on ext4/ZFS
// AND on NTFS. Both of those constraints are exercised here.
//
// Every case ends by CREATING THE FILE in t.TempDir(). Asserting on the string
// alone would have missed the truncation bug these tests were written to find:
// a byte-sliced CJK title is a perfectly ordinary-looking Go string right up
// until the filesystem rejects it.

package organizer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// asianTitles are real-shaped titles in scripts with multi-byte encodings,
// combining marks, and no spaces to break on.
var asianTitles = map[string]string{
	"japanese":            "宇宙兄弟",
	"japanese long":       strings.Repeat("宇宙兄弟の物語", 40), // 840 runes, 2520 bytes
	"chinese simplified":  "三体",
	"chinese traditional": "三體",
	"korean":              "해리 포터와 마법사의 돌",
	"korean long":         strings.Repeat("해리포터와마법사의돌", 30),
	"thai":                "แฮร์รี่ พอตเตอร์",
	"devanagari":          "हैरी पॉटर और पारस पत्थर",
	"arabic rtl":          "هاري بوتر وحجر الفيلسوف",
	"hebrew rtl":          "הארי פוטר ואבן החכמים",
	"emoji":               "Project 🚀 Hail Mary 📚",
	"mixed scripts":       "宇宙兄弟 - Space Brothers - 우주형제",
	"fullwidth solidus":   "第1話／第2話",              // U+FF0F, looks like "/" but is not one
	"combining marks":     "Ame\u0301lie Nothomb", // e + combining acute
	"precomposed":         "Amélie Nothomb",       // same, NFC
}

// TestSanitizePathComponent_AsianAndUnicode asserts the invariants that make a
// component safe to hand to the filesystem, then proves it by creating it.
func TestSanitizePathComponent_AsianAndUnicode(t *testing.T) {
	dir := t.TempDir()

	for name, title := range asianTitles {
		t.Run(name, func(t *testing.T) {
			got := SanitizePathComponent(title)

			if got == "" {
				t.Fatalf("SanitizePathComponent(%q) = \"\"; a non-empty title must survive sanitization", title)
			}

			// The bug this test was written for. A byte-slice truncation cuts a
			// multi-byte rune in half and leaves a string that is still a valid
			// Go value but is not valid UTF-8 and is not a legal filename.
			if !utf8.ValidString(got) {
				t.Errorf("SanitizePathComponent(%q) produced invalid UTF-8: %q (% x)", title, got, got)
			}

			if strings.ContainsAny(got, `/\`) {
				t.Errorf("SanitizePathComponent(%q) = %q; contains a path separator", title, got)
			}

			// 255 bytes is the per-component ceiling on ext4/ZFS/APFS/NTFS. The
			// implementation aims lower to leave room for ".tmp-rename".
			if len(got) > 255 {
				t.Errorf("SanitizePathComponent(%q) = %d bytes; exceeds the 255-byte component limit", title, len(got))
			}

			// The real artifact. If any of the above is subtly wrong, this is
			// where it surfaces as a syscall error rather than a string diff.
			path := filepath.Join(dir, got+".m4b")
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				t.Fatalf("could not create %q on a real filesystem: %v", got, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("created %q but cannot stat it back: %v", got, err)
			}
		})
	}
}

// TestBuildRelPath_AsianTitlesStayOneComponent runs the same inputs through the
// full builder, which is what production actually calls. A component-level
// guarantee that the builder can undo is not a guarantee.
func TestBuildRelPath_AsianTitlesStayOneComponent(t *testing.T) {
	dir := t.TempDir()

	for name, title := range asianTitles {
		t.Run(name, func(t *testing.T) {
			vars := PathVars{
				Author:      "村上春樹",
				Title:       title,
				Track:       3,
				TotalTracks: 12,
				Ext:         "m4b",
			}
			got, err := BuildRelPath(testFolderPattern, "{title} - {track:02d}", vars, BuildOpts{})
			if err != nil {
				t.Fatalf("BuildRelPath: %v", err)
			}

			if !utf8.ValidString(got) {
				t.Errorf("BuildRelPath produced invalid UTF-8: %q", got)
			}

			// testFolderPattern is "{author}/{series_prefix}{title}" and
			// BuildRelPath joins folder to stem: two separators, no more.
			if n := strings.Count(got, "/"); n != 2 {
				t.Errorf("BuildRelPath(title=%q) = %q; has %d separators, want 2", title, got, n)
			}

			for comp := range strings.SplitSeq(got, "/") {
				if comp == "" {
					t.Errorf("BuildRelPath(title=%q) = %q; has an empty component", title, got)
				}
				if len(comp) > 255 {
					t.Errorf("component %q is %d bytes; exceeds 255", comp, len(comp))
				}
			}

			// Materialize the whole relative path, directories and all.
			full := filepath.Join(dir, got+".m4b")
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", filepath.Dir(full), err)
			}
			if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
				t.Fatalf("could not create %q on a real filesystem: %v", got, err)
			}
		})
	}
}

// TestSanitizePathComponent_TruncationIsRuneAware isolates the truncation path
// with a title that is pure multi-byte and long enough to force a cut.
func TestSanitizePathComponent_TruncationIsRuneAware(t *testing.T) {
	// 3 bytes per rune. 100 runes = 300 bytes, so the cap must cut it, and the
	// cut lands mid-rune unless the implementation counts runes.
	title := strings.Repeat("宇", 100)

	got := SanitizePathComponent(title)

	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a multi-byte rune: %q is not valid UTF-8 (% x)", got, got)
	}
	if len(got) > 255 {
		t.Errorf("truncated to %d bytes; exceeds the 255-byte component limit", len(got))
	}
	// Every rune that survived must be the original one -- a mid-rune cut
	// followed by a lossy conversion would show up as U+FFFD.
	for _, r := range got {
		if r != '宇' {
			t.Errorf("truncation corrupted a rune: found %q (U+%04X) in %q", r, r, got)
			break
		}
	}
}

// TestSanitizePathComponent_InvisibleAndControlCharacters covers the characters
// that are not ASCII control codes but are just as unsafe in a filename:
// C1 controls, zero-width joiners, direction overrides, and the BOM. A
// direction override in particular can make a filename render as something
// entirely different from what it is.
func TestSanitizePathComponent_InvisibleAndControlCharacters(t *testing.T) {
	cases := map[string]string{
		"C1 control NEL":         "Book\u0085Title",
		"zero-width space":       "Book\u200bTitle",
		"left-to-right mark":     "Book\u200eTitle",
		"right-to-left override": "Book\u202eTitle",
		"first strong isolate":   "Book\u2068Title",
		"byte order mark":        "Book\ufeffTitle",
		"line separator":         "Book\u2028Title",
		"paragraph separator":    "Book\u2029Title",
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := SanitizePathComponent(in)

			if !utf8.ValidString(got) {
				t.Fatalf("SanitizePathComponent(%q) produced invalid UTF-8: %q", in, got)
			}
			for _, r := range got {
				switch {
				case r < 32 || r == 127:
					t.Errorf("ASCII control U+%04X survived in %q", r, got)
				case r >= 0x80 && r <= 0x9f:
					t.Errorf("C1 control U+%04X survived in %q", r, got)
				case r == 0x200b || r == 0xfeff:
					t.Errorf("zero-width character U+%04X survived in %q", r, got)
				case r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
					t.Errorf("bidi control U+%04X survived in %q", r, got)
				case r == 0x2028 || r == 0x2029:
					t.Errorf("line/paragraph separator U+%04X survived in %q", r, got)
				}
			}
		})
	}
}

// TestSanitizePathComponent_NormalizesToNFC pins the fix for the quietest bug
// in this file: the same title, byte-different.
//
// macOS hands out NFD, Linux and most taggers use NFC. Untreated, one book
// produces two directories that are visually identical and neither lookup finds
// the other -- and because they render the same, it reads as a duplicate-import
// bug rather than an encoding bug.
//
// Korean is the sharp case: NFD does not merely detach an accent, it
// decomposes a Hangul syllable into jamo. "해리" is 6 bytes composed and 12
// decomposed, with no visual difference whatsoever.
func TestSanitizePathComponent_NormalizesToNFC(t *testing.T) {
	// Derive both forms from ONE literal rather than writing an NFD literal and
	// an NFC literal side by side. Editors, gofmt and some filesystems silently
	// normalize source files, so a hand-written "decomposed" literal can arrive
	// already composed -- and then the test compares a string to itself and
	// passes no matter what the code does.
	//
	// This is not hypothetical: the first version of this test hard-coded both
	// forms, and the Korean and Japanese cases stayed GREEN with normalization
	// removed, because the two literals in the file were byte-identical. Only
	// the Latin cases caught the mutation. Constructing the forms at runtime is
	// what makes the test measure the thing it claims to measure.
	titles := map[string]string{
		"korean hangul jamo":    "\ud574\ub9ac \ud3ec\ud130",
		"latin combining acute": "Am\u00e9lie Nothomb",
		"japanese dakuten":      "\u304c\u304e\u3050\u3052\u3054",
		"vietnamese stacked":    "Nguy\u1ec5n Nh\u1eadt \u00c1nh",
		"thai":                  "\u0e41\u0e2e\u0e23\u0e4c\u0e23\u0e35\u0e48",
	}

	dir := t.TempDir()
	for name, title := range titles {
		t.Run(name, func(t *testing.T) {
			nfd := norm.NFD.String(title)
			nfc := norm.NFC.String(title)
			if nfd == nfc {
				t.Skipf("%q has no distinct NFD form, so there is nothing to normalize", title)
			}

			gotNFD := SanitizePathComponent(nfd)
			gotNFC := SanitizePathComponent(nfc)

			if gotNFD != gotNFC {
				t.Errorf("the same title in two encodings sanitized differently:\n  NFD -> %q (% x)\n  NFC -> %q (% x)\nThese are two directories that render identically.",
					gotNFD, gotNFD, gotNFC, gotNFC)
			}

			// Both spellings must land on ONE file, not two.
			//
			// Note this half of the assertion is weaker than it looks on macOS:
			// APFS compares names normalized, so it would collapse the two even
			// without the fix. The string comparison above is the part that is
			// filesystem-independent, and it is the one that caught the bug.
			for _, form := range []string{nfd, nfc} {
				p := filepath.Join(dir, name, SanitizePathComponent(form)+".m4b")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatalf("WriteFile(%q): %v", p, err)
				}
			}
			entries, err := os.ReadDir(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 1 {
				names := make([]string, 0, len(entries))
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("the two encodings created %d files, want 1: %q", len(entries), names)
			}
		})
	}
}

// TestSanitizePathComponent_PreservesMeaningfulJoiners is the counterweight to
// the test above, and the reason the invisible-character sweep is a deny-list
// rather than "strip everything with no visible glyph".
//
// U+200C ZERO WIDTH NON-JOINER and U+200D ZERO WIDTH JOINER are invisible, but
// they are not decoration. In Devanagari they select between a conjunct
// ligature and its separated form, which changes what the word says; the same
// applies in Persian, Thai, and Malayalam. They also bind emoji sequences, so
// stripping them turns one family emoji into three separate people.
//
// Stripping invisibles wholesale is a tempting one-liner that silently corrupts
// Hindi titles. Anything added to the deny-list needs to be meaningless, not
// merely invisible.
func TestSanitizePathComponent_PreservesMeaningfulJoiners(t *testing.T) {
	cases := map[string]string{
		"devanagari ZWNJ":  "\u0915\u200c\u0937",
		"devanagari ZWJ":   "\u0915\u200d\u0937",
		"emoji family ZWJ": "Family \U0001F468\u200d\U0001F469\u200d\U0001F466 Saga",
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := SanitizePathComponent(in)
			for _, r := range []rune{0x200c, 0x200d} {
				if strings.ContainsRune(in, r) && !strings.ContainsRune(got, r) {
					t.Errorf("SanitizePathComponent(%q) = %q; stripped U+%04X, which carries meaning in Indic scripts and binds emoji sequences", in, got, r)
				}
			}
		})
	}
}

// TestSanitizePathComponent_WindowsHostileNames covers what NTFS rejects but
// ext4 and ZFS accept. The library is reachable as W:\ over SMB, so a name that
// is legal only on Linux is a name the Windows client cannot open.
func TestSanitizePathComponent_WindowsHostileNames(t *testing.T) {
	reserved := []string{"CON", "PRN", "AUX", "NUL", "COM1", "COM9", "LPT1", "LPT9",
		"con", "Con", "nul"}

	for _, in := range reserved {
		t.Run("reserved "+in, func(t *testing.T) {
			got := SanitizePathComponent(in)
			if strings.EqualFold(got, in) {
				t.Errorf("SanitizePathComponent(%q) = %q; a Windows reserved device name passed through and cannot be created on NTFS", in, got)
			}
			if got == "" {
				t.Errorf("SanitizePathComponent(%q) = \"\"; the name should be escaped, not deleted", in)
			}
		})
	}

	trailing := map[string]string{
		"trailing dot":         "Book Title.",
		"trailing dots":        "Book Title...",
		"trailing space":       "Book Title ",
		"trailing dot + space": "Book Title. ",
	}
	for name, in := range trailing {
		t.Run(name, func(t *testing.T) {
			got := SanitizePathComponent(in)
			if strings.HasSuffix(got, ".") || strings.HasSuffix(got, " ") {
				t.Errorf("SanitizePathComponent(%q) = %q; NTFS silently strips trailing dots and spaces, so this name round-trips to something else over SMB", in, got)
			}
		})
	}
}
