// file: core/health/diagnostics/sanitize_test.go
// version: 1.1.0
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
			`open "<path>": permission denied`, []string{"My", "Books", "Hush"}},
		{"M1 name with angle brackets and a comma", `scan /Users/bruce/<Last>, <First>/Hush failed`,
			`scan <path> failed`, []string{"Last", "First", "Hush"}},
		{"M1 apostrophe", `/Users/bruce/O'Brien/Hush`, `<path>`, []string{"Brien", "Hush"}},
		{"M1 tab in a name", "/Users/bruce/a\tb/Hush", `<path>`, []string{"Hush"}},
		{"M1 encoded separators and a space", `%2FUsers%2Fbruce%2Fa b%2FHush`, `<path>`, []string{"bruce", "Hush"}},
		{"M1 path inside JSON text", `payload={"path":"/Users/bruce/My Books/Hush"}`, `<path>"}`, []string{"bruce", "Books", "Hush"}},
		{"M1 wrapped error, first path has a space", `scan /Volumes/My Books: open /Volumes/My Books/Hush.m4b: no such file or directory`,
			`scan <path>: no such file or directory`, []string{"Books", "Hush"}},
		{"M1 wrapped error, profile with a space", `scan /Users/Bruce Wayne: open /Users/Bruce Wayne/x: permission denied`,
			`scan <path>: permission denied`, []string{"Wayne"}},
		{"M1 two-path error", `rename /a b /c d: file exists`, `rename <path>: file exists`, []string{"b", "d"}},
		{"M1 operation the list does not know", `renameat2 /Users/Bruce Wayne /Users/Selina Kyle: permission denied`,
			`renameat2 <path>: permission denied`, []string{"Wayne", "Kyle"}},
		{"M1 windows API error", `CreateFile C:\Users\Bruce Wayne: Access is denied.`, `CreateFile <path>: Access is denied.`, []string{"Wayne"}},
		{"M1 relative name with a space", `open My Books/Hush.m4b: no such file or directory`, `open <path>: no such file or directory`, []string{"My", "Hush"}},
		{"M1 exec error", `fork/exec /Users/bruce/my bin/Hush: no such file or directory`, `fork/exec <path>: no such file or directory`, []string{"bin", "Hush"}},
		{"M1 exec lookup error", `exec: "/Users/bruce/my bin/Hush": executable file not found in $PATH`,
			`exec: "<path>": executable file not found in $PATH`, []string{"bruce", "bin", "Hush"}},
		{"M1 line break between two errors", "a /x Selina\nopen /c Kyle: no such file or directory", "a <path>: no such file or directory", []string{"Selina", "Kyle"}},
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
		{"M1 errno words without a colon are prose", `see /srv/a Selina no such file or directory`, `see <path> Selina no such file or directory`, nil},
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
			"goroutine 1 [running]:\nmain.main()\n\t<path> +0x1d", []string{"bruce", "main.go"}},

		// m3: a location under a root names the root and nothing after it.
		{"m3 under a root", `/srv/lib/Frank Miller/Hush`, `<root:lib>`, []string{"Frank", "Miller", "Hush"}},
		{"m3 under a root, in an error", `open /srv/lib/Frank Miller/Hush.m4b: permission denied`, `open <root:lib>: permission denied`, []string{"Frank", "Hush"}},
		{"m3 two locations are not under the first root", `copy /srv/lib/a to /Users/bruce/x done`, `copy <path> done`, []string{"bruce"}},
		{"m3 second root", `scan /srv/vault/Bruce Wayne/Hush failed`, `scan <root:vault> failed`, []string{"Wayne", "Hush"}},
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

// TestSanitize_ResidualIsExactlyWhatTheOwnerAccepted pins what survives, so the
// residual cannot grow without a test saying so. Only the last words of a
// space-containing final segment of a location in text that does not start as
// one and is not a Go file error survive; so do bare usernames and host names
// outside a location, and the time of day. What is lost is as pinned as what
// is kept: an inline route, a MIME type and a URL are wiped.
func TestSanitize_ResidualIsExactlyWhatTheOwnerAccepted(t *testing.T) {
	s := testSanitizer()
	for _, c := range []struct{ in, want string }{
		{`user dir /Users/Bruce Wayne is full`, `user dir <path> Wayne is full`},
		{`see /Users/bruce/Bruce Wayne`, `see <path> Wayne`},
		{`see /Users/bruce/Bruce Wayne.`, `see <path> Wayne.`},
		{`/Users/bruce/Brucie (1)`, `<path>)`},
		{`open /Users/bruce/Brucie (1): permission denied`, `open <path>): permission denied`},
		{`user bruce logged in`, `user bruce logged in`},
		{`dial nas.example refused`, `dial nas.example refused`},
		{`at 12:30:45 done`, `at 12:30:45 done`},
		{`GET /api/v1/books/{id} 200`, `GET <path>} 200`},
		{`content type text/plain`, `content type <path>`},
		{`fetch http://nas.example`, `fetch <path>`},
		{``, ``},
		{`plain words only`, `plain words only`},
	} {
		if got := s.Text(c.in); got != c.want {
			t.Errorf("Text(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

// TestSanitize_ZeroSanitizerFailsClosed: the zero value has no roots and no
// errno index, and still wipes every location.
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
	}
}

// plantNames are directory names that hold the characters the old scanner
// stopped at: spaces, tabs, quotes, commas, brackets, "&#=;?%", a backtick,
// a colon, Unicode.
func plantNames() []string {
	return []string{
		"selina", "Bruce Wayne", "My Books", "O'Brien", "a\tb", `a "b c`, "a,b, c", "<Last>, <First>",
		"a&b #c=d;e?f%g", "a`b c", "HD ", " lead", "x:y z", "Zoë Ünï", "Harleen Quinzel", "a\u00a0b", "a\u2028b",
	}
}

func plantFinals() []string { return []string{"Hush", "Hush.m4b", "Hush!", "1-Hush", "Hush,"} }

func plantPrefixes() []string {
	return []string{"", "open ", "scan: open ", "dial ", "CGO_CFLAGS=", "error: ", "see ", "err=\"open "}
}

func plantSuffixes() []string {
	return []string{"", ": permission denied", ": no such file or directory", " failed", ", retrying.", `"`, ": input/output error"}
}

// plantedNameWords are words of plantNames that no prefix or suffix spells, so
// one in the output is a leak of the name and not of the prose.
func plantedNameWords() []string {
	return []string{"selina", "Bruce", "Wayne", "Books", "Brien", "Zoë", "Ünï", "lead", "Last", "First", "Harleen", "Quinzel", "Macintosh"}
}

// plant builds one input: prefix, a start, the names joined by the form's
// separator, the final segment (no whitespace, so the owner-accepted residual
// of a spaced final segment is not what is being tested) and a suffix.
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
	entity := regexp.MustCompile(`&(#|[A-Za-z0-9]+;)`)
	drive := regexp.MustCompile(`^["'(\[{<]*[A-Za-z]:`)
	for _, w := range strings.FieldsFunc(got, unicode.IsSpace) {
		if w == "input/output" || w == "i/o" || w == "input/output," || w == "fork/exec" {
			continue
		}
		if strings.ContainsAny(w, `/\%~`) || entity.MatchString(w) || drive.MatchString(w) {
			t.Fatalf("Text(%q) = %q: the word %q can still be a location", in, got, w)
		}
	}
}

// TestSanitize_PlantedMarkerNeverAppears is the invariant over a cross product
// of forms, names, finals, prefixes and suffixes, and over a seeded random
// sample of two-name locations.
func TestSanitize_PlantedMarkerNeverAppears(t *testing.T) {
	s := testSanitizer()
	words := plantedNameWords()
	n := 0
	for _, f := range plantForms() {
		for _, name := range plantNames() {
			for _, fin := range plantFinals() {
				for _, pre := range plantPrefixes() {
					for _, suf := range plantSuffixes() {
						checkPlanted(t, s, plant(pre, f, []string{name}, fin, suf), "Hush", words)
						n++
					}
				}
			}
		}
	}
	rng := rand.New(rand.NewPCG(2026, 11))
	forms, names, finals, pres, sufs := plantForms(), plantNames(), plantFinals(), plantPrefixes(), plantSuffixes()
	for range 40_000 {
		k := 1 + rng.IntN(3)
		var ns []string
		for range k {
			ns = append(ns, names[rng.IntN(len(names))])
		}
		checkPlanted(t, s, plant(pres[rng.IntN(len(pres))], forms[rng.IntN(len(forms))], ns, finals[rng.IntN(len(finals))], sufs[rng.IntN(len(sufs))]), "Hush", words)
		n++
	}
	t.Logf("%d planted inputs", n)
}

// FuzzSanitizerText: the planted marker never appears. The fuzzer chooses the
// prefix, the two directory names (any bytes, whitespace included), the final
// segment (its whitespace is dropped: a spaced final segment is the
// owner-accepted residual) and the suffix; the form is chosen by a byte.
func FuzzSanitizerText(f *testing.F) {
	forms := plantForms()
	for i := range forms {
		for _, n := range plantNames()[:6] {
			f.Add("open ", n, "Bruce Wayne", "Hush", ": permission denied", uint8(i))
		}
	}
	f.Add("", "a b", "c d", "Hush", "", uint8(0))
	f.Add(`CGO_CFLAGS=`, "a", "b", "Hush", ` -L/Users/bruce/lib`, uint8(1))
	f.Add("", `a" b`, "c\td", "Hush", `","q":"a?b","p":"/Users%2F@z%2F`, uint8(15))
	s := testSanitizer()
	f.Fuzz(func(t *testing.T, pre, n1, n2, final, suf string, form uint8) {
		final = strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, final)
		const marker = "Hush"
		for _, x := range []string{pre, n1, n2, suf} {
			if strings.Contains(x, marker) {
				t.Skip("the fuzzer wrote the marker itself")
			}
		}
		if final == "" {
			final = marker
		} else {
			final += marker
		}
		pf := forms[int(form)%len(forms)]
		got := s.Text(plant(pre, pf, []string{n1, n2}, final, suf))
		if strings.Contains(got, marker) {
			t.Fatalf("the marker survived: Text(%q) = %q", plant(pre, pf, []string{n1, n2}, final, suf), got)
		}
		if again := s.Text(got); again != got {
			t.Fatalf("not idempotent: %q then %q", got, again)
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
	}
	for _, name := range names {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Skipf("the file system does not take the name %q: %v", name, err)
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
			var op, last string
			switch e := c.err.(type) {
			case *fs.PathError:
				op, inner, last = e.Op, e.Err, e.Path
			case *os.LinkError:
				op, inner, last = e.Op, e.Err, e.New
			default:
				t.Fatalf("%q %s: %T", name, c.what, c.err)
			}
			// The punctuation that ends the last word of a location stays
			// outside the placeholder (so a quoted path keeps its quote): a
			// path that ends in one shows that one byte.
			tail := last[len(strings.TrimRight(last, ":,;.)]}\"'")):]
			for _, wrapped := range []struct{ text, want string }{
				{c.err.Error(), op + " <path>" + tail + ": " + inner.Error()},
				{fmt.Sprintf("scan %s: %v", dir, c.err), "scan <path>" + tail + ": " + inner.Error()},
				{fmt.Sprintf("import %q failed: %v", dir, c.err), ""},
			} {
				got := s.Text(wrapped.text)
				if wrapped.want != "" && got != wrapped.want {
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
// so the template ends where the error text begins. A text that is missing
// makes the span end at the last triggered word, which leaves a word of a name
// with spaces; the failure names the text to add to errnoTexts.
func TestSanitize_ErrnoTextsCoverTheHost(t *testing.T) {
	s := testSanitizer()
	for n := 1; n < 140; n++ {
		text := syscall.Errno(n).Error()
		if strings.HasPrefix(text, "errno ") {
			continue
		}
		in := "open /Users/Bruce Wayne/Hush: " + text
		want := "open <path>: " + text
		if got := s.Text(in); got != want {
			t.Errorf("errno %d, %q: got %q, want %q (add %q to errnoTexts)", n, text, got, want, strings.ToLower(strings.TrimRight(text, ".")))
		}
	}
	for _, e := range []error{fs.ErrNotExist, fs.ErrExist, fs.ErrPermission, fs.ErrClosed, fs.ErrInvalid, os.ErrDeadlineExceeded, os.ErrProcessDone} {
		in := "open /Users/Bruce Wayne/Hush: " + e.Error()
		if e == os.ErrProcessDone {
			continue // not a file error text
		}
		if got := s.Text(in); strings.Contains(got, "Wayne") {
			t.Errorf("%v: %q", e, got)
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
	}
	for name, mk := range shapes {
		in := mk(mib)
		got := opsOf(s, in)
		// Each step is a small multiple of the input; the multiple is the same
		// at every size, which the ratio below checks.
		if bound := 64 * len(in); got > bound {
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
		{"msg", "GET /api/v1/books/{id}", "GET <path>}"},
		{"path", "/api/v1/books/{id}", "<path>}"},
		{"version", "v1.2.3", "v1.2.3"},
		{"version", "/Users/bruce/v1.2.3", "<path>"},
	} {
		if got, _ := st.scrub(c.key, c.in); got != c.want {
			t.Errorf("scrub(%q, %q) = %q, want %q", c.key, c.in, got, c.want)
		}
	}
}
