// file: core/health/diagnostics/sanitize_test.go
// version: 1.2.0
// guid: 36bfade5-be50-496e-b763-33a05bf55746
// last-edited: 2026-11-16

package diagnostics

import (
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"unicode"
)

// The fixtures are Batman-universe: people, titles and places only. "Hush" is
// the planted marker (a comic title) in the last segment of a location; the
// usernames and directory names are characters.

func testSanitizer() Sanitizer {
	return NewSanitizer([]PathRoot{{Name: "lib", Path: "/srv/lib"}, {Name: "vault", Path: "/srv/vault"}})
}

// leakCase is one input of the review's leak classes, the exact text the
// sanitizer must write, and the words that must be gone from it.
type leakCase struct {
	name string
	in   string
	want string
	gone []string
}

func leakCases() []leakCase {
	return []leakCase{
		// M1: whitespace or a delimiter inside a directory name.
		{"M1 mac volume with a space", `open /Volumes/Macintosh HD/Users/bruce/Audiobooks/Hush.m4b: permission denied`,
			`open <path>: permission denied`, []string{"Macintosh", "HD", "bruce", "Hush"}},
		{"M1 windows profile with a space", `open C:\Users\Bruce Wayne\Music\Hush.m4b: The system cannot find the file specified.`,
			`open <path>: The system cannot find the file specified.`, []string{"Bruce", "Wayne", "Hush"}},
		{"M1 quoted path with a space", `open "/Users/bruce/My Books/Hush.m4b": permission denied`,
			`open "<path>: permission denied`, []string{"My", "Books", "Hush"}},
		{"M1 name with angle brackets and a comma", `scan /Users/bruce/<Last>, <First>/Hush failed`,
			`scan <path>`, []string{"Last", "First", "Hush", "failed"}},
		{"M1 apostrophe", `/Users/bruce/O'Brien/Hush`, `<path>`, []string{"Brien", "Hush"}},
		{"M1 tab in a name", "/Users/bruce/a\tb/Hush", `<path>`, []string{"Hush"}},
		{"M1 encoded separators and a space", `%2FUsers%2Fbruce%2Fa b%2FHush`, `<path>`, []string{"bruce", "Hush"}},
		{"M1 path inside JSON text", `payload={"path":"/Users/bruce/My Books/Hush"}`, `<path>`, []string{"bruce", "Books", "Hush"}},
		{"M1 wrapped error, first path has a space", `scan /Volumes/My Books: open /Volumes/My Books/Hush.m4b: no such file or directory`,
			`scan <path>: no such file or directory`, []string{"Books", "Hush"}},
		{"M1 wrapped error, profile with a space", `scan /Users/Bruce Wayne: open /Users/Bruce Wayne/x: permission denied`,
			`scan <path>: permission denied`, []string{"Wayne"}},
		{"M1 two-path error", `rename /a b /c d: file exists`, `rename <path>: file exists`, []string{"b", "d"}},
		{"M1 operation the list does not know", `renameat2 /Users/Bruce Wayne /Users/Selina Kyle: permission denied`,
			`renameat2 <path>: permission denied`, []string{"Wayne", "Kyle"}},
		{"M1 windows API error", `CreateFile C:\Users\Bruce Wayne: Access is denied.`, `CreateFile <path>: Access is denied.`, []string{"Wayne"}},
		{"M1 relative name with a space", `open My Books/Hush.m4b: no such file or directory`, `<path>: no such file or directory`, []string{"My", "Hush", "open"}},
		{"M1 exec error", `fork/exec /Users/bruce/my bin/Hush: no such file or directory`, `fork/exec <path>: no such file or directory`, []string{"bin", "Hush"}},
		{"M1 exec lookup error", `exec: "/Users/bruce/my bin/Hush": executable file not found in $PATH`,
			`exec: "<path>: executable file not found in $PATH`, []string{"bruce", "bin", "Hush"}},
		{"M1 line break between two errors", "a /x Selina\nopen /c Kyle: no such file or directory", "a <path>\nopen <path>: no such file or directory", []string{"Selina", "Kyle"}},
		{"M1 errno text inside the first path", `open /Users/a: file exists b/Hush: no such file or directory`, `open <path>: no such file or directory`, []string{"exists", "Hush"}},
		// U+00A0 and U+2028 separate words (unicode.IsSpace), which only wipes more.
		{"M1 no-break space inside a name", "open /Users/bruce\u00a0Wayne/Hush: permission denied", `open <path>: permission denied`, []string{"Wayne", "Hush"}},
		{"M1 line separator inside a name", "open /Users/bruce\u2028Wayne/Hush: permission denied", `open <path>: permission denied`, []string{"Wayne", "Hush"}},
		// A template belongs to its own line: the words between two lines'
		// locations are not a name that spans them.
		{"M1 each line has its own template", "open /Users/bruce: permission denied\nnote Selina Kyle ok /Users/alfred/Hush: no such file or directory",
			"open <path>: permission denied\nnote Selina Kyle ok <path>: no such file or directory", []string{"bruce", "alfred", "Hush"}},
		// An errno text opens a template only after a word that ends in ":"; the
		// same words in running prose are not an error message.
		{"M1 errno words without a colon are prose", `see /srv/a Selina no such file or directory`, `see <path>`, []string{"Selina", "such"}},
		{"M1 errno words after a colon end a template", `see /srv/a Selina: no such file or directory`, `see <path>: no such file or directory`, []string{"Selina"}},

		// M2: a location that starts glued to a name byte.
		{"M2 compiler flags", `CGO_CFLAGS=-I/Users/bruce/include -L/Users/bruce/lib`, `<path>`, []string{"bruce", "include"}},
		{"M2 CJK before the root", `文件/Users/bruce/Hush`, `<path>`, []string{"bruce", "Hush"}},
		{"M2 accented letter before the root", `é/Users/bruce/Hush`, `<path>`, []string{"bruce", "Hush"}},
		{"M2 encoded start", `HD%2FUsers%2Fbruce`, `<path>`, []string{"bruce"}},
		{"M2 key and value", `path=/Users/bruce/Hush`, `<path>`, []string{"Hush"}},

		// M3: the other forms of a start.
		{"M3 rooted backslash path", `\Users\bruce\Hush`, `<path>`, []string{"bruce", "Hush"}},
		{"M3 drive-relative", `C:Users\bruce\Hush`, `<path>`, []string{"bruce", "Hush"}},
		{"M3 tilde user", `~bruce/Hush`, `<path>`, []string{"bruce", "Hush"}},
		{"M3 escaped separators", `x \\u002FUsers\\u002Fbruce\\u002FHush`, `x <path>`, []string{"bruce", "Hush"}},
		{"M3 decimal entities", `&#47;Users&#47;bruce&#47;Hush`, `<path>`, []string{"bruce", "Hush"}},
		{"M3 hex entities", `&#x2F;Users&#x2F;bruce`, `<path>`, []string{"bruce"}},
		{"M3 double-encoded entities", `&amp;#47;Users&amp;#47;bruce`, `<path>`, []string{"bruce"}},
		{"M3 double percent-encoding", `%252FUsers%252Fbruce`, `<path>`, []string{"bruce"}},

		// M4: paths inside URLs.
		{"M4 smb", `smb://nas.example/home/bruce/Hush`, `<path>`, []string{"bruce", "Hush"}},
		{"M4 nfs", `nfs://nas.example/export/home/bruce/Hush`, `<path>`, []string{"bruce", "Hush"}},
		{"M4 sftp with userinfo", `sftp://bruce:pw@host.example/home/bruce/Hush`, `<path>`, []string{"bruce", "pw", "Hush"}},
		{"M4 file URL", `file:///Users/bruce/Hush`, `<path>`, []string{"bruce", "Hush"}},
		{"M4 file URL, windows drive and a space", `file:///C:/Users/Bruce Wayne/Hush`, `<path>`, []string{"Wayne", "Hush"}},
		{"M4 route spelling a filesystem path", `/api/v1/files/Users/bruce/Hush`, `<path>`, []string{"bruce", "Hush"}},

		// Stack frames carry a file path.
		{"go stack frame", "goroutine 1 [running]:\nmain.main()\n\t/Users/bruce/src/app/main.go:12 +0x1d",
			"goroutine 1 [running]:\nmain.main()\n\t<path>", []string{"bruce", "main.go"}},

		// m3: a location under a root names the root and nothing after it.
		{"m3 under a root", `/srv/lib/Jim Gordon/Hush`, `<root:lib>`, []string{"Jim", "Gordon", "Hush"}},
		{"m3 under a root, in an error", `open /srv/lib/Jim Gordon/Hush.m4b: permission denied`, `open <root:lib>: permission denied`, []string{"Jim", "Gordon", "Hush"}},
		{"m3 two locations are not under the first root", `copy /srv/lib/a to /Users/bruce/x done`, `copy <path>`, []string{"bruce", "done"}},
		{"m3 second root", `scan /srv/vault/Bruce Wayne/Hush failed`, `scan <root:vault>`, []string{"Wayne", "Hush", "failed"}},
	}
}

func TestSanitize_LeakTable(t *testing.T) {
	s := testSanitizer()
	for _, c := range leakCases() {
		t.Run(c.name, func(t *testing.T) {
			got := s.Text(c.in)
			if got != c.want {
				t.Errorf("Text(%q)\n got  %q\n want %q", c.in, got, c.want)
			}
			for _, g := range c.gone {
				if strings.Contains(got, g) {
					t.Errorf("Text(%q) = %q still holds %q", c.in, got, g)
				}
			}
			if again := s.Text(got); again != got {
				t.Errorf("not idempotent: Text(%q) = %q, then %q", c.in, got, again)
			}
		})
	}
}

// TestSanitize_WhatSurvivesAndWhatIsLost pins both sides. What survives: the
// words before the first location of a line, a bare user or host name, a name
// with no separator, a time of day. What is lost with a location: everything
// after it on its line, unless the line ends in a text the sanitizer knows, and
// every word of an inline route, MIME type or URL. No word of a path segment
// survives in any of these.
func TestSanitize_WhatSurvivesAndWhatIsLost(t *testing.T) {
	s := testSanitizer()
	for _, c := range []struct{ in, want string }{
		// The prose after a location is lost with it (it may be the rest of a name).
		{`user dir /Users/Bruce Wayne is full`, `user dir <path>`},
		{`see /Users/bruce/Bruce Wayne`, `see <path>`},
		{`see /Users/bruce/Bruce Wayne.`, `see <path>`},
		{`/Users/bruce/Brucie (1)`, `<path>`},
		{`open /Users/bruce/Brucie (1): permission denied`, `open <path>: permission denied`},
		{`GET /api/v1/books/{id} 200`, `GET <path>`},
		{`content type text/plain`, `<path>`},
		{`fetch http://nas.example`, `fetch <path>`},
		{`organizing /srv/lib/Jim Gordon/The Long Halloween now`, `organizing <root:lib>`},
		{`copied /srv/lib/Alfred Pennyworth/Batman Hush.m4b to the shelf`, `copied <root:lib>`},
		{`cache dir C:\Users\Bruce Wayne`, `cache dir <path>`},
		// A known error text keeps standing after the location; any other text goes.
		{`ffprobe /srv/lib/Jim Gordon/The Long Halloween.m4b: exit status 1`, `ffprobe <root:lib>: exit status 1`},
		{`walk /Users/Bruce Wayne: context canceled`, `walk <path>: context canceled`},
		{`walk C:\Users\Bruce Wayne: context canceled`, `walk <path>: context canceled`},
		{`write tags /srv/lib/Alfred Pennyworth/Batman Hush.m4b: unsupported format`, `write tags <root:lib>`},
		{`open /Users/bruce/x: permission denied, retrying in 5s`, `open <path>`},
		{`ffprobe chapters /srv/lib/Alfred Pennyworth/Batman Hush.m4b: exit status 1: /srv/lib/Alfred Pennyworth/Batman Hush.m4b: Invalid data found when processing input`,
			`ffprobe chapters <path>: Invalid data found when processing input`},
		// A name that holds ": ", a quote or a backtick is wiped whole.
		{`/srv/lib/Alfred Pennyworth/Batman: Hush.m4b`, `<root:lib>`},
		{`/srv/downloads/Batman: Hush/01 Batman Hush.m4b`, `<path>`},
		{`/srv/lib/Alfred Pennyworth/The "Long" Halloween`, `<root:lib>`},
		{`level=INFO msg=organized path="/srv/lib/Alfred Pennyworth/Batman Hush.m4b"`, `level=INFO msg=organized <root:lib>`},
		{`path="C:\Users\Bruce Wayne" size=3`, `<path>`},
		{`payload={"path":"/Users/Bruce Wayne","n":1}`, `<path>`},
		{`["/Users/Bruce Wayne",3]`, `["<path>`},
		{`import "/srv/lib/Alfred Pennyworth/Batman Hush" failed: unsupported codec`, `import "<root:lib>`},
		{"open /srv/lib/Alfred Pennyworth/It `Rains`/x y: permission denied", `open <root:lib>: permission denied`},
		// A relative name has no known start: the run begins at the line's first word.
		{`Jim Gordon/The Long Halloween`, `<path>`},
		{`planned Alfred Pennyworth/Batman Hush/Part 1.m4b`, `<path>`},
		{`openat Alfred Pennyworth/Batman Hush.m4b: path escapes from parent`, `<path>: path escapes from parent`},
		{`level=INFO msg=reading Alfred Pennyworth/x`, `<path>`},
		// Each line is its own run: a location on one line leaves the next alone.
		{"copy /Users/bruce/x y z\nnext line is fine\nthen /srv/lib/a b", "copy <path>\nnext line is fine\nthen <root:lib>"},
		// Not locations, so not touched.
		{`user bruce logged in`, `user bruce logged in`},
		{`dial nas.example refused`, `dial nas.example refused`},
		{`at 12:30:45 done`, `at 12:30:45 done`},
		{`open Alfred: permission denied`, `open Alfred: permission denied`},
		{``, ``},
		{`plain words only`, `plain words only`},
	} {
		if got := s.Text(c.in); got != c.want {
			t.Errorf("Text(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

// TestSanitize_OverlongStringIsRedactedWhole: a string over maxTextBytes that has
// a trigger byte is replaced whole (it is never tokenized); one without a trigger
// byte is returned as is, at any length; one at the limit is sanitized.
func TestSanitize_OverlongStringIsRedactedWhole(t *testing.T) {
	s := testSanitizer()
	over := "open /Users/bruce/x " + strings.Repeat("a/ ", maxTextBytes/3)
	if len(over) <= maxTextBytes {
		t.Fatalf("test input is %d bytes, want over %d", len(over), maxTextBytes)
	}
	if got := s.Text(over); got != "<redacted:line_too_long>" {
		t.Errorf("an overlong triggered string: got %d bytes, starting %.40q", len(got), got)
	}
	plain := strings.Repeat("a", maxTextBytes+1)
	if got := s.Text(plain); got != plain {
		t.Errorf("an overlong string with no trigger byte changed (%d bytes)", len(got))
	}
	atLimit := "see /Users/bruce/x" + strings.Repeat(" ", maxTextBytes-len("see /Users/bruce/x"))
	if len(atLimit) != maxTextBytes {
		t.Fatalf("test input is %d bytes", len(atLimit))
	}
	if got := s.Text(atLimit); !strings.HasPrefix(got, "see <path>") {
		t.Errorf("a string at the limit is sanitized: got %.40q", got)
	}
	if got := s.Text(tooLong); got != tooLong {
		t.Errorf("the replacement is not stable: %q", got)
	}
}

// TestSanitize_MixedRunIsLabelledPath: a run that holds a second location start,
// in any spelling (a bare word, a "key=" form, a quoted one, another drive), is
// not named after the root of its first word.
func TestSanitize_MixedRunIsLabelledPath(t *testing.T) {
	s := testSanitizer()
	for _, c := range []struct{ in, want string }{
		{`copy /srv/lib/a /Users/bruce/x`, `copy <path>`},
		{`copy /srv/lib/a key=/Users/bruce/x`, `copy <path>`},
		{`copy /srv/lib/a "/Users/bruce/x"`, `copy <path>`},
		{`copy /srv/lib/a C:\Users\bruce`, `copy <path>`},
		{`copy /srv/lib/a smb://nas.example/x`, `copy <path>`},
		{`copy /srv/lib/a ~/x`, `copy <path>`},
		{`copy /srv/lib/Alfred Pennyworth/Batman Hush`, `copy <root:lib>`},
		{`copy /srv/vault/a`, `copy <root:vault>`},
	} {
		if got := s.Text(c.in); got != c.want {
			t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitize_ZeroSanitizerFailsClosed: the zero value has no roots and knows no
// error text, and still wipes every location (to the end of its line).
func TestSanitize_ZeroSanitizerFailsClosed(t *testing.T) {
	var z Sanitizer
	for _, c := range leakCases() {
		got := z.Text(c.in)
		for _, g := range c.gone {
			if strings.Contains(got, g) {
				t.Errorf("zero Sanitizer: Text(%q) = %q holds %q", c.in, got, g)
			}
		}
	}
	if got := z.Text("/srv/lib/x"); got != "<path>" {
		t.Errorf("the zero Sanitizer has no roots, got %q", got)
	}
	if got := z.Text("open /Users/bruce/x: permission denied"); got != "open <path>" {
		t.Errorf("the zero Sanitizer knows no error text, got %q", got)
	}
}

// TestSanitize_RootNamesAreSafe: a root name with a trigger byte is written
// with "_" so the placeholder is stable under a second pass.
func TestSanitize_RootNamesAreSafe(t *testing.T) {
	s := NewSanitizer([]PathRoot{{Name: "a/b c:d%e", Path: "/srv/odd"}, {Name: "", Path: "/srv/none"}, {Name: "rel", Path: "relative/dir"}})
	got := s.Text("open /srv/odd/x y: permission denied")
	if got != "open <root:a_b c_d_e>: permission denied" {
		t.Errorf("got %q", got)
	}
	if again := s.Text(got); again != got {
		t.Errorf("not idempotent: %q then %q", got, again)
	}
	if got := s.Text("/srv/none/x"); got != "<path>" {
		t.Errorf("a root with no name must be dropped, got %q", got)
	}
	if got := s.Text("/relative/dir/x"); got != "<path>" {
		t.Errorf("a root that is not absolute must be dropped, got %q", got)
	}
}

// TestSanitize_DotDotCannotClimbOutOfARoot: a location is judged by its cleaned
// form, so ".." does not make a path "under" a root it leaves.
func TestSanitize_DotDotCannotClimbOutOfARoot(t *testing.T) {
	s := testSanitizer()
	for _, in := range []string{
		`/srv/lib/../../Users/bruce/Hush`,
		`/srv/lib/a/../../../Users/bruce/Hush`,
		`/srv/lib\..\..\Users\bruce\Hush`,
		// under the root when read with "/" only, outside it when "\" separates:
		`/srv/lib/a\..\..\..\Users\bruce\Hush`,
		`/srv/library/Hush`,
	} {
		if got := s.Text(in); got != "<path>" {
			t.Errorf("Text(%q) = %q, want <path>", in, got)
		}
	}
}

// plantForm is a way of starting a location and the separator its segments are
// joined by.
type plantForm struct{ start, sep string }

func plantForms() []plantForm {
	return []plantForm{
		{"/Users/", "/"},
		{"-I/Users/", "/"},
		{"文件/Users/", "/"},
		{"é/Users/", "/"},
		{"~bruce/", "/"},
		{"~/", "/"},
		{`C:\Users\`, `\`},
		{`C:Users\`, `\`},
		{`\Users\`, `\`},
		{`\\nas.example\share\`, `\`},
		{`\\u002FUsers\\u002F`, `\\u002F`},
		{`\/Users\/`, `\/`},
		{`&#47;Users&#47;`, `&#47;`},
		{`&#x2F;Users&#x2F;`, `&#x2F;`},
		{`&amp;#47;Users&amp;#47;`, `&amp;#47;`},
		{`%2FUsers%2F`, `%2F`},
		{`%252FUsers%252F`, `%252F`},
		{`HD%2FUsers%2F`, `%2F`},
		{"smb://nas.example/home/", "/"},
		{"nfs://nas.example/export/home/", "/"},
		{"sftp://bruce:pw@host.example/home/", "/"},
		{"file:///Users/", "/"},
		{"p=/Users/", "/"},
		{`"path":"/Users/`, "/"},
		{"/Volumes/Macintosh HD/Users/", "/"},
		// Relative forms: no known start, the run begins at the line's first word.
		{"", "/"},
		{"./", "/"},
		{"../", "/"},
		{"", `\`},
		// A location embedded in logfmt and in a JSON object text.
		{`path="/Users/`, "/"},
		{`level=INFO path=/Users/`, "/"},
		{`{"path":"/Users/`, "/"},
		{`{\"path\":\"/Users/`, "/"},
	}
}

// plantNames are directory names that hold the characters the old scanner
// stopped at: spaces, tabs, quotes, commas, brackets, "&#=;?%", a backtick,
// a colon, Unicode, and the ": " that makes a name look like an error wrap.
func plantNames() []string {
	return []string{
		"selina", "Bruce Wayne", "My Books", "O'Brien", "a\tb", `a "b c`, "a,b, c", "<Last>, <First>",
		"a&b #c=d;e?f%g", "a`b c", "HD ", " lead", "x:y z", "Zoë Ünï", "Harleen Quinzel", "a\u00a0b", "a\u2028b",
		"Batman: Year One", `The "Long" Halloween`, "Jason `Robin` Todd", "a: b: c d", "Brucie (1)",
		"Dick Grayson: Nightwing", `Cass "Orphan" Cain`,
	}
}

// plantFinals are the last segment: the marker, with and without whitespace
// around it, so a spaced final segment is planted exactly like a glued one.
func plantFinals() []string {
	return []string{
		"Hush", "Hush.m4b", "Hush!", "1-Hush", "Hush,",
		"Batman Hush", "Batman Hush.m4b", "Part 1 Hush", "Hush (Unabridged)", "Hush: Part 2", `"Hush" Part 2`,
	}
}

func plantPrefixes() []string {
	return []string{"", "open ", "scan: open ", "dial ", "CGO_CFLAGS=", "error: ", "see ", "err=\"open ", "ffprobe chapters "}
}

// plantSuffixes are what follows the location: a known error text (kept), prose,
// and a tail that looks like an error text but is not one (lost).
func plantSuffixes() []string {
	return []string{
		"", ": permission denied", ": no such file or directory", " failed", ", retrying.", `"`, ": input/output error",
		": exit status 1", ": context canceled", ": unsupported format", ": exit status", " is Selina's now",
		`" size=3`, `","n":1}`, ": no such file or directory, retrying in 5s",
	}
}

// keptSuffix reports a suffix that is a known error text: it ends the line and stays.
func keptSuffix(suf string) bool {
	switch suf {
	case ": permission denied", ": no such file or directory", ": input/output error", ": exit status 1", ": context canceled":
		return true
	}
	return false
}

// suffixWords are the words of a suffix that is lost.
func suffixWords(suf string) []string {
	return []string{"failed", "retrying", "unsupported", "Selina", "size"}
}

// plantedNameWords are words of plantNames that no prefix or suffix spells, so
// one in the output is a leak of the name and not of the prose.
func plantedNameWords() []string {
	return []string{
		"selina", "Bruce", "Wayne", "Books", "Brien", "Zoë", "Ünï", "lead", "Last", "First", "Harleen", "Quinzel", "Macintosh",
		"Year", "Halloween", "Todd", "Brucie", "Grayson", "Nightwing", "Orphan", "Cain", "Batman", "Part", "Unabridged",
	}
}

// plant builds one input: prefix, a start, the names joined by the form's
// separator, the final segment (it may hold whitespace) and a suffix.
func plant(pre string, f plantForm, names []string, final, suf string) string {
	var b strings.Builder
	b.WriteString(pre)
	b.WriteString(f.start)
	for _, n := range names {
		b.WriteString(n)
		b.WriteString(f.sep)
	}
	b.WriteString(final)
	b.WriteString(suf)
	return b.String()
}

// checkPlanted is the invariant: the planted marker, and every word of the
// planted names, is gone from the output; the output is idempotent; and it
// satisfies the structural property.
func checkPlanted(t testing.TB, s Sanitizer, in, marker string, nameWords []string) {
	t.Helper()
	got := s.Text(in)
	if strings.Contains(got, marker) {
		t.Fatalf("Text(%q) = %q still holds the planted marker %q", in, got, marker)
	}
	for _, w := range nameWords {
		if strings.Contains(in, w) && strings.Contains(got, w) {
			t.Fatalf("Text(%q) = %q still holds the planted name word %q", in, got, w)
		}
	}
	if again := s.Text(got); again != got {
		t.Fatalf("not idempotent: Text(%q) = %q, then %q", in, got, again)
	}
	checkStructure(t, in, got)
}

// checkStructure is the structural property, written without reading the
// sanitizer: no word of the output holds a separator, "%", "~", an entity or
// starts with a drive, except the two fixed words of an errno text, "input/output"
// and "i/o".
func checkStructure(t testing.TB, in, got string) {
	t.Helper()
	entity := regexp.MustCompile(`&(#|[A-Za-z0-9]{1,32};)`) // 32 is the sanitizer's entity-name bound (the longest HTML name is 31)
	drive := regexp.MustCompile(`^["'(\[{<]*[A-Za-z]:`)
	for _, w := range strings.FieldsFunc(got, unicode.IsSpace) {
		switch strings.ToLower(w) { // the errno texts match ignoring ASCII case
		case "input/output", "i/o", "input/output,", "fork/exec":
			continue
		}
		if strings.ContainsAny(w, `/\%~`) || entity.MatchString(w) || drive.MatchString(w) {
			t.Fatalf("Text(%q) = %q: the word %q can still be a location", in, got, w)
		}
	}
}

// checkSuffix: a known error text at the end of the line stays, and any other
// suffix is lost with the location.
func checkSuffix(t testing.TB, s Sanitizer, in, suf string) {
	t.Helper()
	got := s.Text(in)
	if keptSuffix(suf) {
		if !strings.HasSuffix(got, suf) {
			t.Fatalf("Text(%q) = %q lost the error text %q", in, got, suf)
		}
		return
	}
	for _, w := range suffixWords(suf) {
		if strings.Contains(suf, w) && strings.Contains(got, w) {
			t.Fatalf("Text(%q) = %q kept the suffix word %q: only a known error text may follow a location", in, got, w)
		}
	}
}

// TestSanitize_PlantedMarkerNeverAppears is the invariant over a cross product
// of forms, names, finals, prefixes and suffixes, and over a seeded random
// sample of one- to three-name locations.
func TestSanitize_PlantedMarkerNeverAppears(t *testing.T) {
	s := testSanitizer()
	words := plantedNameWords()
	n := 0
	for _, f := range plantForms() {
		for _, name := range plantNames() {
			for _, fin := range plantFinals() {
				for _, pre := range plantPrefixes() {
					for _, suf := range plantSuffixes() {
						in := plant(pre, f, []string{name}, fin, suf)
						checkPlanted(t, s, in, "Hush", words)
						checkSuffix(t, s, in, suf)
						n++
					}
				}
			}
		}
	}
	rng := rand.New(rand.NewPCG(2026, 11))
	forms, names, finals, pres, sufs := plantForms(), plantNames(), plantFinals(), plantPrefixes(), plantSuffixes()
	for range 60_000 {
		k := 1 + rng.IntN(3)
		var ns []string
		for range k {
			ns = append(ns, names[rng.IntN(len(names))])
		}
		suf := sufs[rng.IntN(len(sufs))]
		in := plant(pres[rng.IntN(len(pres))], forms[rng.IntN(len(forms))], ns, finals[rng.IntN(len(finals))], suf)
		checkPlanted(t, s, in, "Hush", words)
		checkSuffix(t, s, in, suf)
		n++
	}
	t.Logf("%d planted inputs", n)
}

// FuzzSanitizerText: the planted marker never appears, and no word of the two
// directory names does. The fuzzer chooses the prefix, the two directory names
// and the final segment (any bytes, whitespace included) and the suffix; the
// form is chosen by a byte. The output must also be idempotent and satisfy the
// structural property (checkStructure).
func FuzzSanitizerText(f *testing.F) {
	forms := plantForms()
	for i := range forms {
		for _, n := range plantNames()[:6] {
			f.Add("open ", n, "Bruce Wayne", "Hush", ": permission denied", uint8(i))
		}
	}
	f.Add("", "a b", "c d", "Hush", "", uint8(0))
	f.Add("CGO_CFLAGS=", "a", "b", "Hush", ` -L/Users/bruce/lib`, uint8(1))
	f.Add("", `a" b`, "c\td", "Hush", `","q":"a?b","p":"/Users%2F@z%2F`, uint8(15))
	f.Add("open ", "Batman: Year One", `The "Long" Halloween`, "Batman Hush", ": no such file or directory", uint8(0))
	s := testSanitizer()
	f.Fuzz(func(t *testing.T, pre, n1, n2, final, suf string, form uint8) {
		const marker = "Hush"
		for _, x := range []string{pre, n1, n2, suf} {
			if strings.Contains(x, marker) {
				t.Skip("the fuzzer wrote the marker itself")
			}
		}
		for _, x := range []string{n1, n2, final} {
			if strings.Contains(x, "\n") {
				// A name with a line break in it is wiped one line at a time: the
				// words on the next line have no trigger (a known limit, see the
				// Sanitizer doc and seams F8-03).
				t.Skip("a name with a line break")
			}
		}
		if final == "" {
			final = marker
		} else {
			final += marker
		}
		pf := forms[int(form)%len(forms)]
		in := plant(pre, pf, []string{n1, n2}, final, suf)
		got := s.Text(in)
		if strings.Contains(got, marker) {
			t.Fatalf("the marker survived: Text(%q) = %q", in, got)
		}
		if again := s.Text(got); again != got {
			t.Fatalf("not idempotent: %q then %q", got, again)
		}
		checkStructure(t, in, got)
		// A word of a directory name is a word of the output only when the
		// prefix or the suffix spells it (a known error text, or the prose
		// before the location).
		outWords := map[string]bool{}
		for _, w := range strings.FieldsFunc(got, unicode.IsSpace) {
			outWords[w] = true
		}
		fixed := map[string]bool{}
		for _, w := range strings.FieldsFunc(pre+" "+suf, unicode.IsSpace) {
			fixed[w] = true
		}
		// With no start of its own the first name is the first word of the line,
		// and "000 /x" is spelled like the word "000" before an absolute path: the
		// first name is not checked then (a known limit, see seams F8-03).
		checked := []string{n1, n2}
		if pf.start == "" {
			checked = []string{n2}
			for _, w := range strings.FieldsFunc(n1, unicode.IsSpace) {
				fixed[w] = true // the same word in n2 cannot be told from the first name's
			}
		}
		for _, n := range checked {
			for _, w := range strings.FieldsFunc(n, unicode.IsSpace) {
				if len(w) >= 3 && outWords[w] && !fixed[w] && !strings.ContainsAny(w, "<>") {
					t.Fatalf("the name word %q survived: Text(%q) = %q", w, in, got)
				}
			}
		}
	})
}

// FuzzSanitizerArbitrary is the brief's item 4a: any input at all. The output is
// idempotent, satisfies the structural property, and a line that had no byte of
// "/\\%~&:" is returned unchanged.
func FuzzSanitizerArbitrary(f *testing.F) {
	for _, c := range leakCases() {
		f.Add(c.in)
	}
	for _, in := range []string{
		"", "plain words", "a: b", "x:y", "C:", `C:\`, "%2", "&#", "&amp;#47;", "\x00/\xff", "/\n/\n/", "open /a: no such file or directory",
		"open /a b: exit status 1", "a/ b: input/output error", "\u00a0/\u00a0", "~", "~~/", "fork/exec x: no such file or directory", ": inpuT/output error",
		"&" + strings.Repeat("0", 33) + ";", // longer than any entity name: not an entity
	} {
		f.Add(in)
	}
	s := testSanitizer()
	f.Fuzz(func(t *testing.T, in string) {
		got := s.Text(in)
		if again := s.Text(got); again != got {
			t.Fatalf("not idempotent: Text(%q) = %q, then %q", in, got, again)
		}
		checkStructure(t, in, got)
		if !strings.ContainsAny(in, "/\\%~&:") && got != in {
			t.Fatalf("a string with no trigger byte changed: %q -> %q", in, got)
		}
	})
}

// TestSanitize_IdempotentOnTheTable: Text(Text(x)) == Text(x) for the table and
// for text with no location.
func TestSanitize_IdempotentOnTheTable(t *testing.T) {
	s := testSanitizer()
	for _, c := range leakCases() {
		once := s.Text(c.in)
		if twice := s.Text(once); twice != once {
			t.Errorf("Text is not idempotent on %q: %q then %q", c.in, once, twice)
		}
		checkStructure(t, c.in, once)
	}
}

// TestSanitize_TemplatesFromRealOSErrors builds the file errors from the os
// package on temp directories whose names hold spaces, quotes, commas, tabs,
// brackets, an errno text and Unicode, and checks that the text the sanitizer
// writes is the operation, the placeholder and the errno text, and holds no
// byte of any directory name.
func TestSanitize_TemplatesFromRealOSErrors(t *testing.T) {
	s := testSanitizer()
	names := []string{
		"Bruce Wayne", `quo"te`, "com,ma", "ta\tb", "Zoë Ünï", "Harvey 'Two-Face' Dent", "<Last>, <First>",
		"x: permission denied y", "a%2Fb c", "semi;colon&amp", "back`tick (1)", "  lead and trail  ",
		"Batman: Hush", `The "Long" Halloween`,
	}
	for _, name := range names {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Logf("the file system does not take the name %q: %v", name, err)
			continue // one name the file system refuses must not skip the others
		}
		missing := filepath.Join(dir, "Hush.m4b")
		file := filepath.Join(dir, "plain.txt")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, openErr := os.Open(missing)
		_, statErr := os.Stat(missing)
		_, readDirErr := os.ReadDir(missing)
		removeErr := os.Remove(missing)
		mkdirErr := os.Mkdir(dir, 0o755)
		notDirErr := os.Chdir(file)
		renameErr := os.Rename(missing, filepath.Join(dir, "Joker.m4b"))
		linkErr := os.Symlink("target", file)
		_, readFileErr := os.ReadFile(filepath.Join(file, "inside"))
		for _, c := range []struct {
			what string
			err  error
		}{
			{"open", openErr}, {"stat", statErr}, {"readdir", readDirErr}, {"remove", removeErr}, {"mkdir", mkdirErr},
			{"chdir", notDirErr}, {"rename", renameErr}, {"symlink", linkErr}, {"readfile", readFileErr},
		} {
			if c.err == nil {
				t.Fatalf("%q %s: no error", name, c.what)
			}
			var inner error
			var op string
			switch e := c.err.(type) {
			case *fs.PathError:
				op, inner = e.Op, e.Err
			case *os.LinkError:
				op, inner = e.Op, e.Err
				if e.Op == "symlink" {
					op += " " + e.Old // the link target is a relative text the caller chose, not a location
				}
			default:
				t.Fatalf("%q %s: %T", name, c.what, c.err)
			}
			for _, wrapped := range []struct{ text, want string }{
				{c.err.Error(), op + " <path>: " + inner.Error()},
				{fmt.Sprintf("scan %s: %v", dir, c.err), "scan <path>: " + inner.Error()},
				{fmt.Sprintf("import %q failed: %v", dir, c.err), `import "<path>: ` + inner.Error()},
			} {
				got := s.Text(wrapped.text)
				if got != wrapped.want {
					t.Errorf("%q %s:\n in   %q\n got  %q\n want %q", name, c.what, wrapped.text, got, wrapped.want)
				}
				for _, frag := range strings.FieldsFunc(name+" "+filepath.Base(filepath.Dir(dir)), func(r rune) bool { return r == ' ' || r == '\t' }) {
					if len(frag) >= 3 && strings.Contains(got, frag) && !strings.Contains(inner.Error(), frag) && !strings.HasPrefix(frag, "scan") {
						t.Errorf("%q %s: %q still holds %q", name, c.what, got, frag)
					}
				}
				if strings.Contains(got, "Hush") || strings.Contains(got, "Joker") || strings.Contains(got, "plain.txt") {
					t.Errorf("%q %s: %q holds a file name", name, c.what, got)
				}
				if again := s.Text(got); again != got {
					t.Errorf("%q %s: not idempotent: %q then %q", name, c.what, got, again)
				}
			}
		}
	}
}

// TestSanitize_ErrnoTextsCoverTheHost: every errno text of this host is known,
// so the template keeps the error text. A text that is missing makes the line
// wipe to its end, text included, so the failure names the text to add to
// errnoTexts. The final segment has a space, so nothing but the text being
// known can produce the want.
func TestSanitize_ErrnoTextsCoverTheHost(t *testing.T) {
	s := testSanitizer()
	check := func(text string) {
		in := "open /Users/bruce/Bruce Wayne: " + text
		want := "open <path>: " + text
		if got := s.Text(in); got != want {
			t.Errorf("%q: got %q, want %q (add %q to errnoTexts)", text, got, want, strings.ToLower(strings.TrimRight(text, ".")))
		}
	}
	for n := 1; n < 140; n++ {
		if text := syscall.Errno(n).Error(); !strings.HasPrefix(text, "errno ") {
			check(text)
		}
	}
	for _, e := range []error{fs.ErrNotExist, fs.ErrExist, fs.ErrPermission, fs.ErrClosed, fs.ErrInvalid, os.ErrDeadlineExceeded} {
		check(e.Error())
	}
}

// TestSanitize_ErrorTextsOfOtherHosts pins the texts the host table cannot
// check here: Linux errno texts that macOS words differently, the Windows API
// texts, and the other texts that end an error in this program (os.Root's
// escape, the context errors, an exec exit, ffprobe's complaint).
func TestSanitize_ErrorTextsOfOtherHosts(t *testing.T) {
	s := testSanitizer()
	for _, text := range []string{
		"no medium found", "wrong medium type", "software caused connection abort", "required key not available",
		"channel number out of range", "cannot assign requested address", "transport endpoint is already connected",
		"too many references: cannot splice", "owner died", "invalid or incomplete multibyte or wide character",
		"The system cannot find the path specified.", "Access is denied.", "The filename, directory name, or volume label syntax is incorrect.",
		"path escapes from parent", "context canceled", "context deadline exceeded", "unexpected EOF", "EOF", "exit status 1",
		"exit status 127", "Invalid data found when processing input",
	} {
		in := "walk /srv/lib/Alfred Pennyworth/Bruce Wayne: " + text
		if got, want := s.Text(in), "walk <root:lib>: "+text; got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
	// Not a known text: the line is wiped to its end, text included.
	for _, text := range []string{"exit status", "exit status x", "exit status 1234567", "context canceled by Alfred", "no such file or directory (os error 2)"} {
		in := "walk /srv/lib/Alfred Pennyworth/Bruce Wayne: " + text
		if got, want := s.Text(in), "walk <root:lib>"; got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

// opsOf is the sanitizer's own count of the steps it took for in.
func opsOf(s Sanitizer, in string) int {
	_, n := s.text(in)
	return n
}

// TestSanitize_OpsAreLinear: no clock. The sanitizer counts the bytes it
// inspects, the words it compares and the bytes it cleans, and the test bounds
// that count at 1 MiB over every shape that has been a quadratic for a scanner
// (all separators, many short segments, a hugely long word, many ": " words,
// operation words with no error, an error text on every line, Unicode
// spaces), and compares N with 4N, so a super-linear step shows as a ratio and
// not as a time. The count is self-reported; TestSanitize_OpsAreLinearInTheCode
// below also counts a step that is not self-reported.
func TestSanitize_OpsAreLinear(t *testing.T) {
	s := testSanitizer()
	const mib = 1 << 20
	shapes := map[string]func(n int) string{
		"all slashes":              func(n int) string { return strings.Repeat("/", n) },
		"short segments":           func(n int) string { return strings.Repeat("a/ ", n/3) },
		"one huge word":            func(n int) string { return "/" + strings.Repeat("a", n) },
		"ops without an error":     func(n int) string { return strings.Repeat("open ", n/5) + "/x" },
		"colon words":              func(n int) string { return strings.Repeat("x: ", n/3) },
		"error on every line":      func(n int) string { return strings.Repeat("open /a b: permission denied\n", n/29) },
		"errors on one line":       func(n int) string { return strings.Repeat("open /a b: permission denied ", n/29) },
		"errno text repeated":      func(n int) string { return "/a " + strings.Repeat("x: no such file or directory ", n/29) },
		"unicode spaces":           func(n int) string { return strings.Repeat("a/\u00a0", n/4) },
		"percent runs":             func(n int) string { return strings.Repeat("%", n) },
		"entities":                 func(n int) string { return strings.Repeat("&#47;", n/5) },
		"two locations and a root": func(n int) string { return "/srv/lib/a " + strings.Repeat("b ", n/2) + "/Users/bruce" },
		"windows drives":           func(n int) string { return strings.Repeat(`C:\a `, n/5) },
		"newlines":                 func(n int) string { return strings.Repeat("/a\n", n/3) },
		// The shape the review named: errno candidates ("the" starts 13 Windows
		// texts) against a word that is as long as the input, and against many
		// words just over and just under the longest word of a text.
		"errno candidates, one huge word": func(n int) string { return "x: the " + strings.Repeat("a", n) },
		"errno candidates, long words": func(n int) string {
			return strings.Repeat("x: the "+strings.Repeat("a", maxErrnoWord+1)+" ", n/(maxErrnoWord+9))
		},
		"errno candidates, borderline": func(n int) string {
			return strings.Repeat("x: the "+strings.Repeat("a", maxErrnoWord)+" ", n/(maxErrnoWord+8))
		},
		"errno candidates, upper case":     func(n int) string { return strings.Repeat("x: THE SYSTEM CANNOT FIND THE ", n/30) },
		"errno candidates, trailing marks": func(n int) string { return "x: the " + strings.Repeat("!", n) },
	}
	// tight is the shapes that put errno candidates against long words: the work
	// per byte is a small constant, so a pass over the long word for each of the
	// 13 candidates (what a missing length check costs) shows as a factor over it.
	tight := map[string]bool{
		"errno candidates, one huge word": true, "errno candidates, trailing marks": true, "errno candidates, long words": true,
	}
	for name, mk := range shapes {
		in := mk(mib)
		got := opsOf(s, in)
		// Each step is a small multiple of the input; the multiple is the same
		// at every size, which the ratio below checks.
		bound := 64 * len(in)
		if tight[name] {
			bound = 8 * len(in)
		}
		if got > bound {
			t.Errorf("%s: %d steps for %d bytes, want at most %d", name, got, len(in), bound)
		}
		small, big := opsOf(s, mk(mib/4)), opsOf(s, mk(mib))
		if small == 0 || float64(big) > 4.4*float64(small) {
			t.Errorf("%s: %d steps at 256 KiB and %d at 1 MiB: the cost is not linear in the input", name, small, big)
		}
	}
}

// TestSanitize_OpsAreLinearInTheCode guards the counter itself: a shape that
// is made of more work per byte must cost more steps, so a counter that stopped
// counting would not pass for a linear sanitizer.
func TestSanitize_OpsAreLinearInTheCode(t *testing.T) {
	s := testSanitizer()
	cheap := opsOf(s, strings.Repeat("a", 1<<16))
	dear := opsOf(s, strings.Repeat("/a b: permission denied\n", 1<<12))
	if dear <= cheap {
		t.Errorf("the counter does not see the work: %d steps for text with errors, %d for plain text", dear, cheap)
	}
}

// TestSanitize_CollidingKeysAreLinear: 50,000 object keys that all become
// "<path>" are named with at most two lookups each, and the sanitizer's step
// count over all of them is linear in their total size.
func TestSanitize_CollidingKeysAreLinear(t *testing.T) {
	const n = 50_000
	x := make(map[string]any, n)
	total := 0
	for i := range n {
		k := fmt.Sprintf("/Users/bruce/My Books/%05d.m4b", i)
		x[k] = i
		total += opsOf(testSanitizer(), k)
	}
	if total > 64*n*40 {
		t.Errorf("%d steps to sanitize %d keys", total, n)
	}
	st := keysBuild()
	out := st.walkObject(x, 0)
	if len(out) != n {
		t.Fatalf("%d members, want %d (a member was lost)", len(out), n)
	}
	if st.keyProbes != 2*n-1 {
		t.Errorf("%d lookups to name %d colliding keys, want %d", st.keyProbes, n, 2*n-1)
	}
	for k := range out {
		if strings.Contains(k, "bruce") || strings.Contains(k, "Books") {
			t.Fatalf("key %q names a location", k)
		}
	}
}

// TestSafeKeys_Negatives: the allowlist validators refuse what is not their
// grammar, so a value that would carry a location is wiped by Text instead.
func TestSafeKeys_Negatives(t *testing.T) {
	long := strings.Repeat("a", 65)
	bad := map[string][]string{
		"route": {
			"/Users/bruce/Hush", "GET /Users/bruce/x", "/api/../etc", "/api/%2FUsers/bruce", "/api/v1//x", "/api", "FETCH /api/x",
			"/api/v1/a b", "/api/{}", "/api/{a-b}", "/public", "/api/./x", "/api/x?p=/Users/bruce", "/api/x#f", "GET  /api/x",
			"/api/v1/{id", "/api/v1/id}", `/api/a\b`, "api/v1/x", "", "GET", "GET ", "/api/" + strings.Repeat("a/", 20),
			"/api/" + strings.Repeat("a", 300), "/livez/x", "/metrics/Users", "/api/~bruce", "/api/Users bruce",
			"/api/v1/files/C:/Users", "/api/&#47;x",
		},
		"method":    {"get", "FETCH", "GET ", "", "G/ET"},
		"level":     {"", "info!", "/Users", "LOUD"},
		"reason":    {"", "No", "a b", "a/b", long, "a%2Fb", `a\b`},
		"component": {"Core", "core/x", "core x"},
		"run_id":    {"", "xyz", "/Users/bruce/x", "01HZ0000000000000000000I00", "81HZ0000000000000000000000", "8d1e0a7c-1111-2222-3333-44445555666", "8d1e0a7c_1111_2222_3333_444455556666"},
		"version":   {"", "1.2", "/Users/bruce", "v1.2.3/x", "abcdefg\\x", "ABCDEFGH", "1.2.3-" + strings.Repeat("a", 200), "v.1.2"},
		"time":      {"", "yesterday", "2026-11-16", "/Users/bruce"},
	}
	for key, vals := range bad {
		valid, ok := safeKey(key)
		if !ok {
			t.Fatalf("%s is not an allowlisted key", key)
		}
		for _, v := range vals {
			if valid(v) {
				t.Errorf("%s accepts %q", key, v)
			}
		}
	}
	good := map[string][]string{
		"route":     {"GET /api/v1/books/{id}", "/api/v1/files/{path...}", "/metrics", "/livez", "/readyz", "POST /api/v1/x/", "/public/health", "DELETE /api/v1/a/{$}"},
		"method":    {"GET", "DELETE"},
		"level":     {"INFO", "warn", "ERROR"},
		"reason":    {"no_roots:read", "a-b.c", "x"},
		"run_id":    {"01HZ0000000000000000000000", "8d1e0a7c-1111-2222-3333-444455556666"},
		"version":   {"v1.2.3", "1.2.3-rc.1+build.5", "abcdef0", "v0.0.0-20261116101010-abcdef012345"},
		"time":      {"2026-11-16T10:10:10Z", "2026-11-16T10:10:10.123456789+02:00"},
		"component": {"core.health"},
	}
	for key, vals := range good {
		valid, _ := safeKey(key)
		for _, v := range vals {
			if !valid(v) {
				t.Errorf("%s refuses %q", key, v)
			}
		}
	}
	for _, k := range []string{"path", "msg", "err", "error", "detail", "file", "dir", "url", "Route", "ROUTE", "", "pattern ", "name"} {
		if _, ok := safeKey(k); ok {
			t.Errorf("%q must not be an allowlisted key", k)
		}
	}
}

// TestScrub_SafeKeyValuesAreKeptAndOthersWiped: the allowlist applies to the
// value only when it validates; a route that is not a template is wiped.
func TestScrub_SafeKeyValuesAreKeptAndOthersWiped(t *testing.T) {
	st := keysBuild()
	for _, c := range []struct{ key, in, want string }{
		{"route", "GET /api/v1/books/{id}", "GET /api/v1/books/{id}"},
		{"route", "/Users/bruce/Hush", "<path>"},
		{"route", "GET /Users/bruce/My Books/Hush", "GET <path>"},
		{"pattern", "/api/v1/files/{path...}", "/api/v1/files/{path...}"},
		{"reason", "no_roots:read", "no_roots:read"},
		{"reason", "/Users/bruce/Hush", "<path>"},
		{"msg", "GET /api/v1/books/{id}", "GET <path>"},
		{"path", "/api/v1/books/{id}", "<path>"},
		{"version", "v1.2.3", "v1.2.3"},
		{"version", "/Users/bruce/v1.2.3", "<path>"},
	} {
		if got, _ := st.scrub(c.key, c.in); got != c.want {
			t.Errorf("scrub(%q, %q) = %q, want %q", c.key, c.in, got, c.want)
		}
	}
}
