// file: core/health/diagnostics/paths_adversarial_test.go
// version: 1.2.0
// guid: 9d4b7e21-3a6c-4f58-8e0d-5b1c2a97f634
// last-edited: 2026-11-14

package diagnostics

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/log"
)

// The grammar these tests hold the path rule to is written in
// docs/agent-tasks/platform-core/seams.d/F8-03.md ("Review round 5"). The
// round-1 to round-4 fixes each handled the string a review reported; this file
// plants a marker directory name in every position the grammar covers and
// asserts it never survives, so the next byte over fails here first.

// marker is the planted directory name. It starts with a letter that is not a
// hex digit, so "%" followed by it is never a valid escape, and it appears
// nowhere in a placeholder.
const marker = "Gordonx"

func advRoots() pathRoots { return scalingRoots() }

// road is one way text reaches the path rule in a real bundle.
type road struct {
	name string
	run  func(in string) string
	json bool // the output is a JSON line
}

// roadsFor returns the four roads: free text (a non-JSON log line), a whole JSON
// string value, and a whole JSON log line in two spellings (json.Marshal, and
// every byte \uXXXX or \/ escaped).
func roadsFor(roots pathRoots) []road {
	w := &sectionWriter{st: &build{red: log.NewRedactor(nil), roots: roots, written: map[string]bool{}}}
	line := func(quoted string) string { return w.redactLine([]byte(`{"msg":"request","path":` + quoted + `}`)) }
	return []road{
		{"text", roots.redactPaths, false},
		{"value", roots.redactValue, false},
		{"jsonline", func(in string) string { b, _ := json.Marshal(in); return line(string(b)) }, true},
		{"jsonescaped", func(in string) string { return line(escapeAllJSON(in)) }, true},
	}
}

// escapeAllJSON writes in as a JSON string whose every byte but a letter or
// digit is escaped ("\/" for a slash, "\u00XX" otherwise; a multi-byte rune as
// one \uXXXX or a surrogate pair), so the decoder, not the text rule, sees the
// path.
func escapeAllJSON(in string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range in {
		switch {
		case r == '/':
			b.WriteString(`\/`)
		case r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	b.WriteByte('"')
	return b.String()
}

// decodedForms are the texts a reader of the output could recover the marker
// from: the output as written, and percent-decoded to a fixed point (so an
// encoded leak does not pass).
func decodedForms(out string) []string {
	forms := []string{out}
	dec := out
	for range 8 {
		next := decodeOnce(dec)
		if next == dec {
			break
		}
		dec = next
		forms = append(forms, dec)
	}
	return forms
}

// decodeOnce is the oracle's own lenient percent-decoder (a valid %XX decodes, a
// bad one stays, "+" is a space), independent of the code under test.
func decodeOnce(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b.WriteByte(byte(v))
			i += 2
		case s[i] == '+':
			b.WriteByte(' ')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// invariants checks what holds for every input on every road: the marker is not
// in the output (raw, decoded, or in any string of a JSON output), a line stays
// one line, and a JSON line stays valid JSON.
func invariants(t *testing.T, rd road, in, out string, wantMarkerGone bool) {
	t.Helper()
	if rd.json {
		if strings.ContainsAny(out, "\n\r") || !json.Valid([]byte(out)) {
			t.Errorf("[%s] %q: output is not one valid JSON line: %q", rd.name, in, out)
			return
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err == nil {
			if s, ok := m["path"].(string); ok {
				out = out + "\n" + s // judge the decoded string too
			}
		}
	} else if strings.Count(out, "\n") != strings.Count(in, "\n") || strings.Count(out, "\r") != strings.Count(in, "\r") {
		t.Errorf("[%s] %q: line breaks changed: %q", rd.name, in, out)
	}
	if !wantMarkerGone {
		return
	}
	for _, f := range decodedForms(out) {
		if strings.Contains(f, marker) {
			t.Errorf("[%s] marker survived\n in:  %q\n out: %q", rd.name, in, out)
			return
		}
	}
}

// ---- the planted-marker generator (table and fuzz share it) ----

// A name is one directory name above the marker. Plain names are what free text
// can carry; odd names hold a byte that ends a path run in text (an apostrophe,
// a bracket, a comma, a semicolon, a pipe, a space): the marker follows it, so
// a rule that cut the path there would leak the marker.
var plainNames = []string{"bat", "bat-x_.~"}

var oddNames = []string{"x'" + marker, "x(" + marker + ")", "x)" + marker, "x," + marker, "x]" + marker, "x;" + marker, "x|" + marker, "x}" + marker, "x " + marker, "x>" + marker, "x%" + marker,
	// A raw query delimiter in a name (G8): a route's query is one placeholder,
	// so none of these may split it and leave the part after it.
	"x&" + marker, "x#" + marker, "x=" + marker, "x?" + marker, "x&y=" + marker, "x#y&" + marker}

// quoteNames hold a double quote, a backtick or a JSON-escaped quote (a backslash
// and a quote: what an unparsed JSON line writes for a quote in a name). They end
// a path in free text and make a whole value prose, but not a route or a URL
// (G9): a route's query runs to the next whitespace whatever it holds, and so
// does its path part, so a name with a quote in it is carried whole there.
var quoteNames = []string{`x"` + marker, "x`" + marker, `x\"` + marker, `x""` + marker}

// allNames is the fuzz's name table.
func allNames() []string {
	return append(append(append([]string{}, plainNames...), oddNames...), quoteNames...)
}

func isPlain(name string) bool {
	return slices.Contains(plainNames, name)
}

// pathOf is the planted path: the marker is a segment of its own, below a name.
func pathOf(name string) string { return "/Users/" + name + "/" + marker + "/Books" }

func backslashes(p string) string { return strings.ReplaceAll(p, "/", `\`) }

// pct escapes every byte that is not unreserved, as encodeURIComponent would
// and more (a slash is %2F).
func pct(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// form is one spelling of the planted path.
type form struct {
	name    string
	encoded bool // percent-encoded: only a route's query or path part decodes it
	fn      func(name string) string
	// rawName is set when the directory name is written as is inside an encoded
	// form (only the slashes are escaped), so a space or quote in it is a raw one.
	rawName bool
}

var forms = []form{
	{"raw", false, pathOf, false},
	{"windows", false, func(n string) string { return "C:" + backslashes(pathOf(n)) }, false},
	{"mixed", false, func(n string) string { return `C:\Users/` + n + `\` + marker + `/Books` }, false},
	{"unc", false, func(n string) string { return `\\vault\share\` + n + `\` + marker }, false},
	{"dotdot", false, func(n string) string { return "/srv/lib/../" + n + "/" + marker }, false},
	{"pct", true, func(n string) string { return pct(pathOf(n)) }, false},
	{"pct-lower", true, func(n string) string { return strings.NewReplacer("%2F", "%2f", "%5C", "%5c").Replace(pct(pathOf(n))) }, false},
	{"pct-twice", true, func(n string) string { return pct(pct(pathOf(n))) }, false},
	{"pct-thrice", true, func(n string) string { return pct(pct(pct(pathOf(n)))) }, false},
	{"pct-slashes-only", true, func(n string) string { return "/Users" + "%2F" + n + "%2F" + marker + "%2FBooks" }, true},
	{"pct-windows", true, func(n string) string { return pct("C:" + backslashes(pathOf(n))) }, false},
	{"pct-windows-fwd", true, func(n string) string { return pct("C:/Users/" + n + "/" + marker) }, false},
	{"pct-unc", true, func(n string) string { return pct(`\\vault\share\` + n + `\` + marker) }, false},
	{"pct-dotdot", true, func(n string) string { return pct("/srv/lib/../" + n + "/" + marker) }, false},
	{"pct-bare-percent", true, func(n string) string { return pct(pathOf(n)) + "%" }, false},
	{"pct-bad-escape", true, func(n string) string { return pct(pathOf(n)) + "%zz" }, false},
}

// ctx is a place the planted text sits.
type ctx struct {
	name string
	tmpl string // %s is the planted text
	// kind: "text" (outside any route), "query" (a route's query value),
	// "path" (a route's path part).
	kind string
}

var ctxs = []ctx{
	{"bare", "%s", "text"}, {"prose", "open %s now", "text"}, {"dquote", `"%s"`, "text"}, {"squote", "'%s'", "text"},
	{"paren", "(%s)", "text"}, {"bracket", "[%s]", "text"}, {"brace", "{%s}", "text"}, {"eq", "x=%s", "text"},
	{"comma", "x,%s", "text"}, {"semi", "x;%s", "text"}, {"pipe", "x|%s", "text"}, {"tag", "<b>%s</b>", "text"},
	{"at", "a@%s", "text"}, {"plus", "a+%s", "text"}, {"star", "a*%s", "text"}, {"bang", "a!%s", "text"},
	{"hash", "k#%s", "text"}, {"amp", "x&%s", "text"}, {"colon", "x:%s", "text"}, {"escnl", `l1\n%s`, "text"},
	{"esctab", `l1\t%s`, "text"},

	{"browse", "/api/v1/filesystem/browse?path=%s", "query"},
	{"second", "/api/v1/x?a=1&path=%s", "query"},
	{"apos", "/api/v1/x?sort=a'b&path=%s", "query"},
	{"rparen", "/api/v1/x?sort=a)b&path=%s", "query"},
	{"qcomma", "/api/v1/x?sort=a,b&path=%s", "query"},
	{"rbracket", "/api/v1/x?sort=a]b&path=%s", "query"},
	{"qsemi", "/api/v1/x?sort=a;b&path=%s", "query"},
	{"qpipe", "/api/v1/x?sort=a|b&path=%s", "query"},
	{"rbrace", "/api/v1/x?sort=a}b&path=%s", "query"},
	{"authors", "/api/v1/authors/O'Brien/books?path=%s", "query"},
	{"key", "/api/v1/x?%s=1", "query"},
	{"fragment", "/api/v1/x?q=1#%s", "query"},
	{"frag-only", "/api/v1/x#%s", "query"},
	{"url-query", "https://abs.example/api/x?path=%s", "query"},
	{"url-fragment", "https://abs.example/items#%s", "query"},
	{"url-second", "https://abs.example:8080/x?a=1&path=%s", "query"},
	{"url-bare-query", "https://abs.example?path=%s", "query"},
	{"livez", "/livez?x=%s", "query"},
	{"metrics", "/metrics?p=%s", "query"},
	{"public", "/public/x?p=%s", "query"},
	{"nested", "/api/v1/x?next=/api/v1/y?p=%s", "query"},
	{"multi", "/api/v1/x?a=%s&b=%s", "query"},

	{"pathpart", "/api/v1/x/%s", "path"},
	{"url-path", "https://abs.example/items/%s", "path"},
}

// wrappers put the target in a line.
var wrappers = []string{"%s", "GET %s 200", `"%s"`, "'%s'", "(%s)", "[%s]", "<a href=%s>", `msg="GET %s done"`}

// suffixes follow the target inside its wrapper.
var suffixes = []string{"", " 200", ")", "'", ",", ".", `;x`, ` "ok"`}

// planted is one generated input and the roads it is in the grammar for.
type planted struct {
	in           string
	text, whole  bool // the text road / the three whole-value roads apply
	desc         string
	routeWhole   bool
	ctxKind      string
	wrap, suffix string
}

// plant builds the input for the indexes and says which roads the grammar
// covers it on. ok is false for a combination that is not meaningful.
func plant(ci, fi int, name string, wi int, si int) (p planted, ok bool) {
	c, f := ctxs[ci%len(ctxs)], forms[fi%len(forms)]
	w, suffix := wrappers[wi%len(wrappers)], suffixes[si%len(suffixes)]
	plantedText := f.fn(name)
	if c.kind == "path" && !(f.encoded || f.name == "raw") {
		return p, false
	}
	var target string
	if strings.Count(c.tmpl, "%s") == 2 {
		target = fmt.Sprintf(c.tmpl, plantedText, plantedText)
	} else {
		target = fmt.Sprintf(c.tmpl, plantedText)
	}
	in := fmt.Sprintf(w, target+suffix)
	p = planted{in: in, ctxKind: c.kind, wrap: w, suffix: suffix,
		desc: fmt.Sprintf("%s/%s/%q/%q/%q", c.name, f.name, name, w, suffix)}
	routeKind := c.kind != "text"
	// A route that is the whole value is judged as one route (G4): its query
	// runs to the end of the string, so any name and any suffix is covered.
	p.routeWhole = routeKind && w == "%s"
	plain := isPlain(name)
	quote := strings.ContainsAny(name, "\"`")
	space := strings.Contains(name, " ")
	// The text road. A route's token ends at whitespace only (G9), so a raw
	// space in a name ends it; an encoded name has none. Outside a route a
	// path run ends at every delimiter (G2), so only a plain name is carried
	// whole.
	switch {
	case !routeKind && f.encoded:
		// G7: an encoded path outside a route is one token, written "<path>"
		// whole; only a raw space or quote in the name ends the token.
		p.text = !(f.rawName && (space || quote))
	case routeKind && f.encoded && !f.rawName:
		p.text = true
	case routeKind:
		// G9: a route or URL token runs to whitespace and no other byte ends
		// it, in its query and in its path part alike, so a quote in the name
		// is carried whole.
		p.text = !space
	default:
		p.text = plain
	}
	// A wrapper that adds a quote ends the token the same way: `"%s"` is fine
	// (the quote is outside) but a quote byte in the name is the name's.
	// The whole-value roads read a route that is the whole value whole; a
	// path that is the whole value is one path unless a quote or a later
	// ": " makes it prose.
	switch {
	case c.name == "url-path" && space:
		// A URL's path part with a raw space and no query to open an opaque
		// tail is cut there like any path in free text (G2); only a route's
		// path part is read to the end of a whole value.
		p.whole = false
	case p.routeWhole:
		p.whole = true
	case !routeKind && f.encoded:
		p.whole = p.text
	case routeKind:
		p.whole = p.text // prose around a route is text
	case c.tmpl == "%s" && w == "%s" && suffix == "":
		p.whole = !quote
	default:
		p.whole = plain
	}
	return p, true
}

// TestRedactPaths_PlantedMarkerNeverSurvives plants the marker in every context
// and spelling the grammar covers, on every road, and checks the invariants.
func TestRedactPaths_PlantedMarkerNeverSurvives(t *testing.T) {
	var count atomic.Int64
	t.Run("group", func(t *testing.T) {
		for ci := range ctxs {
			t.Run(ctxs[ci].name, func(t *testing.T) {
				t.Parallel()
				// Each subtest owns its roads: the JSON roads hold a build
				// with a written-set that is not safe to share.
				roadList := roadsFor(advRoots())
				for fi := range forms {
					for _, name := range allNames() {
						for _, ws := range wrapSuffixPairs() {
							p, ok := plant(ci, fi, name, ws[0], ws[1])
							if !ok {
								continue
							}
							for _, rd := range roadList {
								run := (rd.name == "text" && p.text) || (rd.name != "text" && p.whole)
								if !run {
									// Outside the grammar's promise, the shape
									// invariants still hold.
									invariants(t, rd, p.in, safeRun(rd, p.in), false)
									continue
								}
								out := safeRun(rd, p.in)
								invariants(t, rd, p.in, out, true)
								count.Add(1)
							}
						}
					}
				}
			})
		}
	})
	if n := count.Load(); n < 10_000 {
		t.Fatalf("only %d planted cases ran; the generator shrank", n)
	}
}

// wrapSuffixPairs varies the wrapper and the suffix one at a time, plus the
// diagonal, instead of crossing them: the crossed product multiplies the run
// time (113 s under -race) without reaching a state the one-at-a-time pairs do
// not, because the wrapper only chooses the bytes before the target and the
// suffix the bytes after it.
func wrapSuffixPairs() [][2]int {
	var out [][2]int
	for wi := range wrappers {
		out = append(out, [2]int{wi, 0})
	}
	for si := 1; si < len(suffixes); si++ {
		out = append(out, [2]int{0, si})
	}
	for i := 1; i < len(wrappers) && i < len(suffixes); i++ {
		out = append(out, [2]int{i, i})
	}
	return out
}

// safeRun runs a road and turns a panic into a test-visible string.
func safeRun(rd road, in string) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("PANIC: %v", r)
		}
	}()
	return rd.run(in)
}

// FuzzRedactPaths holds the planted-marker invariant against names the table
// does not list. The name is filtered to the bytes a directory name can hold
// (never a slash, newline or control byte) and the marker is its own segment,
// so no name can hide it. CI runs the seed corpus only; run the fuzzer with
// -fuzz=FuzzRedactPaths.
func FuzzRedactPaths(f *testing.F) {
	for ci := range ctxs {
		for _, fi := range []int{0, 1, 5, 7, 9} {
			f.Add(uint8(ci), uint8(fi), "x'"+marker, uint8(0), uint8(0), "")
			f.Add(uint8(ci), uint8(fi), "x&#=;?%"+marker, uint8(0), uint8(0), "")
			f.Add(uint8(ci), uint8(fi), "x\"'`)]>,"+marker, uint8(0), uint8(0), "")
			f.Add(uint8(ci), uint8(fi), `x\"`+marker, uint8(0), uint8(0), "")
		}
	}
	f.Add(uint8(30), uint8(5), "bat", uint8(1), uint8(1), " later /Users/x")
	f.Add(uint8(0), uint8(0), "bat", uint8(0), uint8(0), "")
	roots := advRoots()
	roadList := roadsFor(roots)
	f.Fuzz(func(t *testing.T, ci, fi uint8, rawName string, wi, si uint8, tail string) {
		name := cleanName(rawName)
		p, ok := plant(int(ci), int(fi), name, int(wi), int(si))
		if !ok {
			t.Skip()
		}
		in := p.in
		if isPlain(name) || p.routeWhole {
			in += tail // an arbitrary tail only where the whole target is covered
		}
		for _, rd := range roadList {
			run := (rd.name == "text" && p.text) || (rd.name != "text" && p.whole)
			// A tail can end or open a token; only the shape invariants hold
			// then.
			wantGone := run && tail == ""
			start := time.Now()
			out := safeRun(rd, in)
			// Linearity is a hard rule: a slow input is a finding, whatever the
			// output is. (The bound is far above a loaded runner's noise.)
			if d := time.Since(start); d > scalingBound {
				t.Errorf("[%s] %q (%d bytes) took %s, want under %s", rd.name, in, len(in), d, scalingBound)
			}
			invariants(t, rd, in, out, wantGone)
		}
	})
}

// cleanName keeps the bytes a directory name can hold: letters, digits and the
// punctuation of the odd names, one space at most, never a slash, backslash,
// control byte or colon (": " makes a value prose), and always the marker as a
// separate segment (pathOf adds it).
func cleanName(raw string) string {
	var b strings.Builder
	rs := []rune(raw)
	for i, r := range rs {
		if b.Len() >= 40 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune("-_.~'(),;|}]>%@+*!#&=?\"` ", r):
			b.WriteRune(r)
		case r == '\\' && i+1 < len(rs) && rs[i+1] == '"':
			b.WriteRune(r) // the JSON escape of a quote; the quote follows
		}
	}
	s := b.String()
	if strings.Count(s, " ") > 1 {
		s = strings.ReplaceAll(s, " ", "")
	}
	if s == "" || strings.Trim(s, ".") == "" {
		return "bat"
	}
	return s + "z"
}

// ---- exact outputs ----

type pc struct{ in, text, value string }

func same(in, want string) pc { return pc{in, want, want} }

func runExact(t *testing.T, cases []pc) {
	t.Helper()
	roots := advRoots()
	for _, c := range cases {
		if c.text != "" {
			if got := roots.redactPaths(c.in); got != c.text {
				t.Errorf("text  %q\n got  %q\n want %q", c.in, got, c.text)
			}
		}
		if c.value != "" {
			if got := roots.redactValue(c.in); got != c.value {
				t.Errorf("value %q\n got  %q\n want %q", c.in, got, c.value)
			}
		}
	}
}

// nestEncoded is p percent-encoded n times.
func nestEncoded(p string, n int) string {
	for range n {
		p = url.QueryEscape(p)
	}
	return p
}

// TestRedactPaths_RouteQueryIsOpaque (N-1, G8): once a token is a route its
// query and fragment are one placeholder, however many apostrophes, brackets,
// commas, semicolons, pipes, "&", "=" or "#" they hold, and the punctuation that
// closes a route in a line stays outside it. The path part is not cut either
// ("O'Brien").
func TestRedactPaths_RouteQueryIsOpaque(t *testing.T) {
	browse := "/api/v1/filesystem/browse?path="
	runExact(t, []pc{
		same(browse+"%2FUsers%2Fo'brien%2FBooks", "/api/v1/filesystem/browse?<query>"),
		same(browse+"%2FUsers%2Fbruce%2FAudiobooks%20(old)%2FHarry%20Potter", "/api/v1/filesystem/browse?<query>"),
		same(browse+"%2FVolumes%2FBruce's%20Drive%2Fx", "/api/v1/filesystem/browse?<query>"),
		same("/api/v1/books?sort=a,b&path=%2FUsers%2Fbruce", "/api/v1/books?<query>"),
		same("/api/v1/books?sort=a]b&path=%2FUsers%2Fbruce", "/api/v1/books?<query>"),
		same("/api/v1/books?sort=a;b&path=%2FUsers%2Fbruce", "/api/v1/books?<query>"),
		same("/api/v1/books?sort=a|b&path=%2FUsers%2Fbruce", "/api/v1/books?<query>"),
		same("/api/v1/books?sort=a}b&path=%2FUsers%2Fbruce", "/api/v1/books?<query>"),
		same("/api/v1/books?sort=a)b&path=%2FUsers%2Fbruce", "/api/v1/books?<query>"),
		same("/api/v1/authors/O'Brien/books?path=%2FUsers%2Fbruce", "/api/v1/authors/O'Brien/books?<query>"),
		same("/api/v1/books/1,2,3?path=%2FUsers%2Fbruce", "/api/v1/books/1,2,3?<query>"),
		// A route in a line: the query runs to the next whitespace (G9), so the
		// bracket, quote or punctuation glued to its end goes with it. That is the
		// accepted cost of reading no delimiter as the end of a query.
		same("(GET /api/v1/x?p=%2FUsers%2Fbruce)", "(GET /api/v1/x?<query>"),
		same("[GET /api/v1/x?p=%2FUsers%2Fbruce]", "[GET /api/v1/x?<query>"),
		same("'/api/v1/x?p=%2FUsers%2Fbruce'", "'/api/v1/x?<query>"),
		same(`"/api/v1/x?p=%2FUsers%2Fbruce"`, `"/api/v1/x?<query>`),
		{in: "/api/v1/x?p=%2FUsers%2Fbruce, then", text: "/api/v1/x?<query> then", value: "/api/v1/x?<query>"},
		same("GET /api/v1/x?p=%2FUsers%2Fbruce: denied", "GET /api/v1/x?<query> denied"),
		// A query with no path is as opaque as one with a path: there is one rule.
		same("/api/v1/books?sort=title,asc&page=2", "/api/v1/books?<query>"),
		same("GET /api/v1/books?ids=1,2,3 200", "GET /api/v1/books?<query> 200"),
		// A fragment; a "#" after a "?" is part of the query.
		same("/api/v1/books#top", "/api/v1/books#<fragment>"),
		same("/api/v1/books?a=1#top", "/api/v1/books?<query>"),
		// A "..": an escape of one after a delimiter does not hide it.
		same("/api/v1/x'/%2e%2e/%2e%2e/home/bruce", "/api/v1/x%27<path>"),
	})
}

// TestRedactPaths_UndecodableQueryIsOpaque (N-2): a query with a bad or
// truncated escape, or one nested past any decoding depth, is the same
// placeholder: nothing is decoded, so nothing can fail to decode open.
func TestRedactPaths_UndecodableQueryIsOpaque(t *testing.T) {
	browse := "/api/v1/filesystem/browse?path="
	runExact(t, []pc{
		same(browse+"%2FUsers%2Fbruce%2F50%", "/api/v1/filesystem/browse?<query>"),
		same(browse+"%2FUsers%2Fbruce%zz", "/api/v1/filesystem/browse?<query>"),
		same("/api/v1/x?a=50%&path=%2FUsers%2Fbruce", "/api/v1/x?<query>"),
		same("GET "+browse+"%2FUsers%2Fbruce%2F50% 200", "GET /api/v1/filesystem/browse?<query> 200"),
		same("/api/v1/x?path="+nestEncoded("/Users/bruce", 8), "/api/v1/x?<query>"),
		same("/api/v1/x?a=%&b=%zz&c=%2", "/api/v1/x?<query>"),
		same("/api/v1/x?"+nestEncoded("/Users/bruce", 8)+"=1", "/api/v1/x?<query>"),
	})
}

// TestRedactPaths_QueryNeverWritesALineBreak (N-3): the query is a placeholder,
// so a "%0A" in it, decoded or not, cannot split a line, on every road; and the
// output is a fixed point.
func TestRedactPaths_QueryNeverWritesALineBreak(t *testing.T) {
	browse := "/api/v1/filesystem/browse?path="
	runExact(t, []pc{
		same("GET "+browse+"%2FUsers%2Fbruce%0Aforged-line 200", "GET /api/v1/filesystem/browse?<query> 200"),
		same("/api/v1/x?msg=see%20%2FUsers%2Fbruce%20now", "/api/v1/x?<query>"),
		same("/api/v1/x?m=%E2%80%A8%2FUsers%2Fbruce", "/api/v1/x?<query>"),
	})
	roots := advRoots()
	for _, in := range []string{
		"GET " + browse + "%2FUsers%2Fbruce%0Aforged-line 200",
		"GET " + browse + "%2FUsers%2Fbruce%0D%0Aforged 200",
		"GET " + browse + "see%20%2FUsers%2Fbruce%0Aforged",
		"GET https://abs.example/x?p=%0A%2FUsers%2Fbruce 200",
		"GET path=%0A%2FUsers%2Fbruce 200",
	} {
		out := roots.redactPaths(in)
		if strings.ContainsAny(out, "\n\r") {
			t.Errorf("%q wrote a line break: %q", in, out)
		}
		if again := roots.redactPaths(out); again != out {
			t.Errorf("%q is not a fixed point: %q then %q", in, out, again)
		}
	}
}

// TestRedactPaths_SingleSegmentSymbolsAreNotPaths (N-4): "/" glued to a symbol
// with one segment that is not a name is not a path ("*/15 * * * *", "*/*",
// "+/-", "50%/50%", "(read)/(write)"); two segments, or a letter, still are.
func TestRedactPaths_SingleSegmentSymbolsAreNotPaths(t *testing.T) {
	runExact(t, []pc{
		same("*/15 * * * *", "*/15 * * * *"),
		same("0 */6 * * *", "0 */6 * * *"),
		same("text/plain,*/*;q=0.8", "text/plain,*/*;q=0.8"),
		same("*/*", "*/*"),
		same("+/-", "+/-"),
		same("50%/50%", "50%/50%"),
		same("(read)/(write)", "(read)/(write)"),
		same("a (b)/(c) d", "a (b)/(c) d"),
		// Still paths.
		same("a*/home/bruce/x", "a*<path>"),
		same("a+/home/bruce/x", "a+<path>"),
		same("*/15/home/bruce/x", "*<path>"),
		same("cron */15 * * * * /home/bruce/job", "cron */15 * * * * <path>"),
		same("(read)/home/bruce/x", "(read)<path>"),
		same("x /srv", "x <path>"),
		same("see /x", "see <path>"),
		same("x=/Users", "x=<path>"),
	})
}

// TestRedactPaths_DoubledSlashInARoutePath (N-5): the access log writes the
// decoded path, so "/api/v1/x/%2FUsers%2Fbruce" arrives as "/api/v1/x//Users/bruce";
// a "//" inside a route's path part starts a path (the redaction replaces from
// the second slash, so one slash survives), and a scheme's "//" does not.
func TestRedactPaths_DoubledSlashInARoutePath(t *testing.T) {
	runExact(t, []pc{
		same("/api/v1/x//Users/bruce", "/api/v1/x/<path>"),
		same("/api//Users/bruce", "/api/<path>"),
		same("/api/v1/x/%2FUsers%2Fbruce", "/api/v1/x/<path>"),
		same("/api/v1/x/%252FUsers%252Fbruce", "/api/v1/x/<path>"),
		same("/api/v1/x/https://example.com/y", "/api/v1/x/https://example.com/y"),
		same("/api/v1/x/file:///home/bruce/y", "/api/v1/x/file://<path>"),
		same("/api/v1/books//1", "/api/v1/books//1"), // a doubled slash that is not a path
		same("/api/v1/x?next=/api/v1/y//Users/bruce", "/api/v1/x?<query>"),
	})
}

// TestRedactPaths_EncodedPathOutsideARouteIsRedacted (G7): a token that is not a
// route or a URL and that carries a percent-encoded separator is decoded (one
// layer at a time, leniently) and written "<path>" whole when the decoded text
// holds an absolute-path start. Bare "path=%2F..." in prose, a "url.query" member,
// a JSON string holding a pre-encoded target: none can carry a path out.
func TestRedactPaths_EncodedPathOutsideARouteIsRedacted(t *testing.T) {
	runExact(t, []pc{
		same("path=%2FUsers%2Fbruce", "<path>"),
		same("path=%2fUsers%2fbruce", "<path>"),
		same(`path=%5C%5Chost%5Cshare%5Cbruce`, "<path>"),
		same("path=C%3A%5CUsers%5Cbruce", "<path>"),
		same("path=%252FUsers%252Fbruce", "<path>"),
		same("path="+nestEncoded("/Users/bruce", 5), "<path>"),
		same("path="+nestEncoded("/Users/bruce", 8), "<path>"),
		same("path=%2FUsers%2Fa'b%2FGordonx%2FBooks", "<path>"),
		same("path=%2FUsers%2Fa;b%2FGordonx%2FBooks", "<path>"),
		same("path=%2FUsers%2Fa&b%2FGordonx%2FBooks", "<path>"),
		same("path=%2FUsers%2Fa%zzb%2FGordonx%2FBooks", "<path>"),
		same("path=%2FUsers%2Fa%b%2FGordonx%2FBooks", "<path>"),
		same("open (path=%2FUsers%2Fbruce) failed", "open (<path>) failed"),
		same("'path=%2FUsers%2Fbruce' failed", "'<path>' failed"),
		same("x=a p=%2FUsers%2Fbruce y", "x=a <path> y"),
		// Not judged: no encoded separator, or no absolute path once decoded.
		same("see 50%25 done", "see 50%25 done"),
		same("application%2Fjson", "application%2Fjson"),
		same("a%2Fb%2Fc", "a%2Fb%2Fc"),
		same("rate=50%25%2F100", "rate=50%25%2F100"),
		same("name=Bruce%20Wayne", "name=Bruce%20Wayne"),
		// A URL: its query and fragment are the same placeholder as a route's.
		same("Get \"https://abs.example/api/x?path=%2FUsers%2Fbruce\"", "Get \"https://abs.example/api/x?<query>"),
		same("https://abs.example/items?p=/Users/a&b/Gordonx/Books", "https://abs.example/items?<query>"),
		same("url=https://abs.example/items?p=/Users/a;b/Gordonx", "url=https://abs.example/items?<query>"),
		same("https://abs.example/items/%2FUsers%2Fbruce", "https://abs.example/items/<path>"),
		same("https://abs.example/a/b", "https://abs.example/a/b"),
		same("https://abs.example", "https://abs.example"),
		same("ws://abs.example:8080/x#top", "ws://abs.example:8080/x#<fragment>"),
		// A URL's path part still decoding after the bound is not shown.
		same("https://abs.example/items/"+nestEncoded("/Users/bruce", 8), "https://abs.example/<value>"),
		// "%XX" prose is not judged whole: only a token that carries an encoded
		// separator is, so a raw path next to a "%25" loses just the path.
		same("rate=50%25,/Users/bruce/Books", "rate=50%25,<path>"),
	})
}

// TestRedactPaths_EncodedPathNestedInTailsFailsClosed: a path under a root is
// read again from inside its tail, and that nests; past maxNest the text that
// still holds a percent-encoded path is "<path>", as one that holds a raw path
// is.
func TestRedactPaths_EncodedPathNestedInTailsFailsClosed(t *testing.T) {
	roots := advRoots()
	// A bare token that carries the encoded path is judged whole at the top, so
	// the nested read is reached through a route's or a URL's path part.
	for _, prefix := range []string{"", "/api/x//", "https://h/x//"} {
		for n := 1; n <= 8; n++ {
			in := prefix + strings.Repeat("/srv/lib/a@", n) + "%2FUsers%2Fbruce"
			for name, run := range map[string]func(string) string{"text": roots.redactPaths, "value": roots.redactValue} {
				if out := run(in); strings.Contains(out, "bruce") {
					t.Errorf("[%s] %d nested tails: %q -> %q", name, n, in, out)
				}
			}
		}
	}
}

// TestRedactPaths_RawQueryDelimiterInANameIsRedacted (G8): a raw "&", "#", "=",
// ";", "?" or "%" inside a directory name used to split a route's query, and the
// part after the delimiter was a relative path the rule left. Nothing in the
// query is shown now, so no delimiter can split it.
func TestRedactPaths_RawQueryDelimiterInANameIsRedacted(t *testing.T) {
	runExact(t, []pc{
		same("/api/v1/x?sort=a}b&path=/Users%2F#z%2FGordonx%2FBooks", "/api/v1/x?<query>"),
		same("/api/v1/x?path=/Users/a&b/Gordonx/Books", "/api/v1/x?<query>"),
		same("/api/v1/x?/Users/a=b/Gordonx/Books=1", "/api/v1/x?<query>"),
		same("/api/v1/x?path=/Users/a;b/Gordonx/Books", "/api/v1/x?<query>"),
		same("/api/v1/x?path=/Users/a?b/Gordonx/Books", "/api/v1/x?<query>"),
		same("/api/v1/x?path=/Users/a%b/Gordonx/Books", "/api/v1/x?<query>"),
		same("/api/v1/x?path=/Users/a#b/Gordonx/Books", "/api/v1/x?<query>"),
		same("/api/v1/x#/Users/a&b/Gordonx/Books", "/api/v1/x#<fragment>"),
		same("/api/v1/x?path=/Users/a%23b/Gordonx/Books", "/api/v1/x?<query>"),
		// A raw space in a query: a value that is one URL (or one route) is read
		// whole, so the words after the space are part of the query. Free text
		// ends the token at the space, as it does for a route.
		{in: "https://abs.example/x?path=/Users/Bruce Wayne/Gordonx/Books", value: "https://abs.example/x?<query>"},
		{in: "/api/v1/x?path=/Users/Bruce Wayne/Gordonx/Books", value: "/api/v1/x?<query>"},
		same("/api/v1/x?path=/Users/a%26b/Gordonx/Books", "/api/v1/x?<query>"),
	})
}

// TestRedactPaths_QueryRunsToWhitespaceWhateverItHolds (G9): once a route or URL
// has a "?" or "#", the opaque query runs to the next whitespace in every
// context, free text included. A raw quote, backtick, apostrophe, bracket, angle
// bracket or comma in a directory name does not end it, and none of what the
// token held is left behind. The cost is the other direction: the quote or bracket
// that closes a route in prose, and punctuation glued to its end, goes with the
// query.
func TestRedactPaths_QueryRunsToWhitespaceWhateverItHolds(t *testing.T) {
	q := "/Users/a%sb/" + marker + "/Books"
	var cases []pc
	for _, c := range []string{`"`, "`", "'", ")", "]", ">", ",", "}", "|", ";", `\"`, `""`, `")`, "`)"} {
		raw := fmt.Sprintf(q, c)
		cases = append(cases,
			same("GET /api/v1/x?p="+raw+" 200", "GET /api/v1/x?<query> 200"),
			same("GET /api/v1/x#"+raw+" 200", "GET /api/v1/x#<fragment> 200"),
			same("GET /api/v1/x?a=1#"+raw+" 200", "GET /api/v1/x?<query> 200"),
			same("GET https://abs.example/x?p="+raw+" 200", "GET https://abs.example/x?<query> 200"),
			same("GET https://abs.example/x#"+raw+" 200", "GET https://abs.example/x#<fragment> 200"),
			same("see /api/v1/x?"+raw+", then", "see /api/v1/x?<query> then"),
		)
	}
	cases = append(cases,
		// The documented cost: a route quoted or bracketed in prose loses its
		// closer and any punctuation glued to it.
		same(`"/api/x?a=1" failed`, `"/api/x?<query> failed`),
		same("`/api/x?a=1` failed", "`/api/x?<query> failed"),
		same("(GET /api/x?a=1)", "(GET /api/x?<query>"),
		same(`GET /api/x?a=1, 200`, `GET /api/x?<query> 200`),
		// A route with no query still gives its closers back.
		same(`"/api/v1/books/1" failed`, `"/api/v1/books/1" failed`),
		same("`/api/v1/books/1` failed", "`/api/v1/books/1` failed"),
		same("(GET /api/v1/books/1)", "(GET /api/v1/books/1)"),
		same(`GET /api/v1/books/1, 200`, `GET /api/v1/books/1, 200`),
		same(`path="/api/v1/books/1"`, `path="/api/v1/books/1"`),
	)
	runExact(t, cases)
}

// TestRedactPaths_QuoteInARoutePathIsNotAnEnd (G9, neighbour a): the path part of
// a route or URL before any "?" is one chunk to the next whitespace as well. It
// ended at a raw quote or backtick, and a directory name that held one
// ("/api/v1/files//Users/a"b/Gordonx/Books") left "b/Gordonx/Books" in the clear
// after the "<path>". A path that starts inside the part (a doubled slash, or an
// encoded one) now runs to the end of the chunk. A route's own segments
// ("/api/v1/files/Users/a"b/Gordonx") are not a filesystem path (no absolute path
// starts in them), so they are shown as written, as they are without the quote.
func TestRedactPaths_QuoteInARoutePathIsNotAnEnd(t *testing.T) {
	var cases []pc
	for _, c := range []string{`"`, "`", `\"`, "'", ")", ">"} {
		name := "a" + c + "b"
		cases = append(cases,
			same("GET /api/v1/files//Users/"+name+"/"+marker+"/Books 200", "GET /api/v1/files/<path> 200"),
			same("GET /api/v1/files/%2FUsers%2F"+name+"/"+marker+"/Books 200", "GET /api/v1/files/<path> 200"),
			same("GET https://abs.example/files//Users/"+name+"/"+marker+"/Books 200", "GET https://abs.example/files/<path> 200"),
			same("GET https://abs.example/files/%2FUsers%2F"+name+"/"+marker+"/Books 200", "GET https://abs.example/files/<path> 200"),
		)
	}
	cases = append(cases,
		// Shown as written: not a filesystem path, and the same text without the
		// quote.
		same(`GET /api/v1/files/Users/a"b/Gordonx/Books 200`, `GET /api/v1/files/Users/a"b/Gordonx/Books 200`),
		same(`GET /api/v1/files/Users/a/Gordonx/Books 200`, `GET /api/v1/files/Users/a/Gordonx/Books 200`),
		same(`GET https://abs.example/files/Users/a"b/Gordonx/Books 200`, `GET https://abs.example/files/Users/a"b/Gordonx/Books 200`),
		// A quoted route with a path inside it gives the closing quote back.
		same(`path="/api/v1/x//Users/bruce"`, `path="/api/v1/x/<path>"`),
		same("path=`/api/v1/x//Users/bruce`", "path=`/api/v1/x/<path>`"),
		same(`msg=\"/api/v1/x//Users/bruce\"`, `msg=\"/api/v1/x/<path>\"`),
	)
	runExact(t, cases)
}

// TestRedactPaths_WhitespaceInANameEndsTheToken (G9, neighbour b): a raw space,
// tab or line break ends a route's token in free text, in the query as in the
// path part, as it ends any token (G2). The part of the name after it is the next
// token, and a bare "Gordonx/Books" is not a path. A whole value that starts as a
// route or URL reads a space and a tab to its end (a line break is kept, so the
// redactor never changes the number of lines); free text does not. This is the
// limit the redactor still has and is listed for the owner in the seams note.
func TestRedactPaths_WhitespaceInANameEndsTheToken(t *testing.T) {
	roots := advRoots()
	for _, ws := range []string{" ", "\t", "\n", "\r"} {
		in := "GET /api/v1/x?p=/Users/a" + ws + "b/" + marker + "/Books 200"
		want := "GET /api/v1/x?<query>" + ws + "b/" + marker + "/Books 200"
		if got := roots.redactPaths(in); got != want {
			t.Errorf("query, %q\n got  %q\n want %q", ws, got, want)
		}
		in = "GET /api/v1/x//Users/a" + ws + "b/" + marker + "/Books 200"
		want = "GET /api/v1/x/<path>" + ws + "b/" + marker + "/Books 200"
		if got := roots.redactPaths(in); got != want {
			t.Errorf("path part, %q\n got  %q\n want %q", ws, got, want)
		}
	}
	// A whole value that is one route reads a space or a tab through.
	for _, ws := range []string{" ", "\t"} {
		if got := roots.redactValue("/api/v1/x?p=/Users/a" + ws + "b/" + marker + "/Books"); got != "/api/v1/x?<query>" {
			t.Errorf("whole value, %q: %q", ws, got)
		}
	}
}

// TestRedactPaths_EscapedQuoteInAJSONStringDoesNotEndTheQuery (G9, neighbour c): a
// query that starts in one JSON string value and holds an escaped quote ("\\\"")
// is one query on every road. In the decoded string it is a real quote; in the
// raw line (a line that is not valid JSON, or text around one) it is a backslash
// and a quote. Neither ends the token.
func TestRedactPaths_EscapedQuoteInAJSONStringDoesNotEndTheQuery(t *testing.T) {
	roots := advRoots()
	w := &sectionWriter{st: &build{red: log.NewRedactor(nil), roots: roots, written: map[string]bool{}}}
	// Valid JSON lines: the decoder gives the string a real quote.
	for _, line := range []string{
		`{"msg":"request","path":"/api/v1/x?p=/Users/a\"b/Gordonx/Books"}`,
		`{"msg":"request","path":"GET /api/v1/x?p=/Users/a\"b/Gordonx/Books 200"}`,
		`{"msg":"request","path":"GET https://abs.example/x?p=/Users/a\"b/Gordonx/Books 200"}`,
		`{"msg":"request","path":"GET /api/v1/files//Users/a\"b/Gordonx/Books 200"}`,
		`{"msg":"request","path":"/api/v1/x?p=/Users/a\u0022b/Gordonx/Books"}`,
		`{"msg":"request","a":"/api/v1/x?p=/Users/a","path":"/api/v1/x?p=/Users/a\"b/Gordonx/Books"}`,
	} {
		out := w.redactLine([]byte(line))
		if strings.Contains(out, marker) || !json.Valid([]byte(out)) {
			t.Errorf("JSON line\n in:  %s\n out: %s", line, out)
		}
	}
	// Raw text: the escape is a backslash and a quote.
	runExact(t, []pc{
		same(`{"msg":"GET /api/v1/x?p=/Users/a\"b/Gordonx/Books 200"}`, `{"msg":"GET /api/v1/x?<query> 200"}`),
		same(`{"msg":"GET /api/v1/x//Users/a\"b/Gordonx/Books 200"}`, `{"msg":"GET /api/v1/x/<path> 200"}`),
		same(`/api/v1/x?p=/Users/a\"b/Gordonx/Books`, `/api/v1/x?<query>`),
		same(`GET https://abs.example/x?p=/Users/a\"b/Gordonx/Books 200`, `GET https://abs.example/x?<query> 200`),
	})
}

// TestRedactPaths_PathStartPastTheEndIsNotAPath: an empty string is not an
// absolute path. absolutePath indexed the first byte of "" and panicked (found by
// FuzzRedactPaths when the query was split into parts).
func TestRedactPaths_PathStartPastTheEndIsNotAPath(t *testing.T) {
	if absolutePath("") || pathStartAt("/x", 2) {
		t.Fatal("a position past the end is not a path start")
	}
	runExact(t, []pc{
		same("/api/v1/x?", "/api/v1/x?<query>"),
		same("/api/v1/x#", "/api/v1/x#<fragment>"),
		same("/api/v1/x?=#", "/api/v1/x?<query>"),
	})
}

// TestRedactPaths_OutputIsAFixedPoint: re-redacting what the rule wrote changes
// nothing, on the exact cases above and on every planted input.
func TestRedactPaths_OutputIsAFixedPoint(t *testing.T) {
	roots := advRoots()
	n := 0
	for ci := range ctxs {
		for fi := range forms {
			for _, name := range plainNames {
				p, ok := plant(ci, fi, name, 0, 0)
				if !ok {
					continue
				}
				out := roots.redactPaths(p.in)
				if again := roots.redactPaths(out); again != out {
					t.Errorf("not a fixed point (text) %q\n once  %q\n twice %q", p.in, out, again)
				}
				outv := roots.redactValue(p.in)
				if again := roots.redactValue(outv); again != outv {
					t.Errorf("not a fixed point (value) %q\n once  %q\n twice %q", p.in, outv, again)
				}
				n++
			}
		}
	}
	if n < 100 {
		t.Fatalf("only %d inputs", n)
	}
}
