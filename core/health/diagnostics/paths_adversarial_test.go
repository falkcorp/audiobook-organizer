// file: core/health/diagnostics/paths_adversarial_test.go
// version: 1.0.1
// guid: 9d4b7e21-3a6c-4f58-8e0d-5b1c2a97f634
// last-edited: 2026-11-14

package diagnostics

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
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

var oddNames = []string{"x'" + marker, "x(" + marker + ")", "x)" + marker, "x," + marker, "x]" + marker, "x;" + marker, "x|" + marker, "x}" + marker, "x " + marker, "x>" + marker, "x%" + marker}

// quoteNames hold a double quote or backtick, which ends a text token and makes
// a whole value prose; they are planted only where the grammar reads a value
// whole (inside a route query, encoded).
var quoteNames = []string{`x"` + marker, "x`" + marker}

// allNames is the fuzz's name table.
func allNames() []string {
	return append(append(append([]string{}, plainNames...), oddNames...), quoteNames...)
}

func isPlain(name string) bool {
	for _, n := range plainNames {
		if n == name {
			return true
		}
	}
	return false
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
	{"livez", "/livez?x=%s", "query"},
	{"metrics", "/metrics?p=%s", "query"},
	{"public", "/public/x?p=%s", "query"},
	{"nested", "/api/v1/x?next=/api/v1/y?p=%s", "query"},
	{"multi", "/api/v1/x?a=%s&b=%s", "query"},

	{"pathpart", "/api/v1/x/%s", "path"},
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
	if c.kind == "text" && f.encoded {
		return p, false // G7: a percent-encoded path outside a route is not decoded
	}
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
	// The text road. A route's token ends at whitespace or a quote (G4), so
	// those bytes in a raw name end it; an encoded name has none. Outside a
	// route a path run ends at every delimiter (G2), so only a plain name is
	// carried whole.
	switch {
	case routeKind && f.encoded && !f.rawName:
		p.text = true
	case routeKind:
		p.text = !space && !quote
	default:
		p.text = plain
	}
	// A wrapper that adds a quote ends the token the same way: `"%s"` is fine
	// (the quote is outside) but a quote byte in the name is the name's.
	// The whole-value roads read a route that is the whole value whole; a
	// path that is the whole value is one path unless a quote or a later
	// ": " makes it prose.
	switch {
	case p.routeWhole:
		p.whole = true
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
			invariants(t, rd, in, safeRun(rd, in), wantGone)
		}
	})
}

// cleanName keeps the bytes a directory name can hold: letters, digits and the
// punctuation of the odd names, one space at most, never a slash, backslash,
// control byte or colon (": " makes a value prose), and always the marker as a
// separate segment (pathOf adds it).
func cleanName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if b.Len() >= 40 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune("-_.~'(),;|}]>%@+*!#&=\"` ", r):
			b.WriteRune(r)
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

// TestRedactPaths_RouteQueryIsNotCutAtDelimiters (N-1): once a token is a route
// its query is not cut at an apostrophe, bracket, comma, semicolon or pipe,
// which a filesystem/browse request carries raw (encodeURIComponent leaves ' ( )
// raw), nor is its path part ("O'Brien").
func TestRedactPaths_RouteQueryIsNotCutAtDelimiters(t *testing.T) {
	browse := "/api/v1/filesystem/browse?path="
	runExact(t, []pc{
		same(browse+"%2FUsers%2Fo'brien%2FBooks", browse+"<path>"),
		same(browse+"%2FUsers%2Fbruce%2FAudiobooks%20(old)%2FHarry%20Potter", browse+"<path>"),
		same(browse+"%2FVolumes%2FBruce's%20Drive%2Fx", browse+"<path>"),
		same("/api/v1/books?sort=a,b&path=%2FUsers%2Fbruce", "/api/v1/books?sort=a,b&path=<path>"),
		same("/api/v1/books?sort=a]b&path=%2FUsers%2Fbruce", "/api/v1/books?sort=a]b&path=<path>"),
		same("/api/v1/books?sort=a;b&path=%2FUsers%2Fbruce", "/api/v1/books?sort=a;b&path=<path>"),
		same("/api/v1/books?sort=a|b&path=%2FUsers%2Fbruce", "/api/v1/books?sort=a|b&path=<path>"),
		same("/api/v1/books?sort=a}b&path=%2FUsers%2Fbruce", "/api/v1/books?sort=a}b&path=<path>"),
		same("/api/v1/books?sort=a)b&path=%2FUsers%2Fbruce", "/api/v1/books?sort=a)b&path=<path>"),
		same("/api/v1/authors/O'Brien/books?path=%2FUsers%2Fbruce", "/api/v1/authors/O'Brien/books?path=<path>"),
		same("/api/v1/books/1,2,3?path=%2FUsers%2Fbruce", "/api/v1/books/1,2,3?path=<path>"),
		// A route in a line: the punctuation that closes it stays outside.
		same("(GET /api/v1/x?p=%2FUsers%2Fbruce)", "(GET /api/v1/x?p=<path>)"),
		same("[GET /api/v1/x?p=%2FUsers%2Fbruce]", "[GET /api/v1/x?p=<path>]"),
		same("'/api/v1/x?p=%2FUsers%2Fbruce'", "'/api/v1/x?p=<path>'"),
		same(`"/api/v1/x?p=%2FUsers%2Fbruce"`, `"/api/v1/x?p=<path>"`),
		{in: "/api/v1/x?p=%2FUsers%2Fbruce, then", text: "/api/v1/x?p=<path>, then", value: "/api/v1/x?p=<path>"},
		{in: "GET /api/v1/x?p=%2FUsers%2Fbruce: denied", text: "GET /api/v1/x?p=<path>: denied", value: "GET /api/v1/x?p=<path>: denied"},
		// No path, no change.
		same("/api/v1/books?sort=title,asc&page=2", "/api/v1/books?sort=title,asc&page=2"),
		same("GET /api/v1/books?ids=1,2,3 200", "GET /api/v1/books?ids=1,2,3 200"),
		// A "..": an escape of one after a delimiter does not hide it.
		same("/api/v1/x'/%2e%2e/%2e%2e/home/bruce", "/api/v1/x%27<path>"),
	})
}

// TestRedactPaths_QueryDecodeFailsClosed (N-2): a part that does not decode is
// judged leniently and, if it still has not settled after five rounds,
// replaced.
func TestRedactPaths_QueryDecodeFailsClosed(t *testing.T) {
	browse := "/api/v1/filesystem/browse?path="
	runExact(t, []pc{
		same(browse+"%2FUsers%2Fbruce%2F50%", browse+"<path>"),
		same(browse+"%2FUsers%2Fbruce%zz", browse+"<path>"),
		same("/api/v1/x?a=50%&path=%2FUsers%2Fbruce", "/api/v1/x?a=50%&path=<path>"),
		same("GET "+browse+"%2FUsers%2Fbruce%2F50% 200", "GET "+browse+"<path> 200"),
		same("/api/v1/x?path="+nestEncoded("/Users/bruce", 8), "/api/v1/x?path=<value>"),
		same("/api/v1/x?a=%&b=%zz&c=%2", "/api/v1/x?a=%&b=%zz&c=%2"), // text, no path: kept as written
		same("/api/v1/x?"+nestEncoded("/Users/bruce", 8)+"=1", "/api/v1/x?<value>=1"),
	})
}

// TestRedactPaths_RewrittenQueryValueIsReencoded (N-3): a value is written
// percent-encoded, so a decoded newline cannot split the line and a decoded
// "&", "#" or space cannot change the shape of the query.
func TestRedactPaths_RewrittenQueryValueIsReencoded(t *testing.T) {
	browse := "/api/v1/filesystem/browse?path="
	runExact(t, []pc{
		same("GET "+browse+"%2FUsers%2Fbruce%0Aforged-line 200", "GET "+browse+"<path>%0Aforged-line 200"),
		same("/api/v1/x?msg=see%20%2FUsers%2Fbruce%20now", "/api/v1/x?msg=see%20<path>%20now"),
		same("/api/v1/x?msg=a%26b%20%2FUsers%2Fbruce", "/api/v1/x?msg=a%26b%20<path>"),
		same("/api/v1/x?m=%23%2FUsers%2Fbruce", "/api/v1/x?m=%23<path>"),
		same("/api/v1/x?m=%22%2FUsers%2Fbruce%22", "/api/v1/x?m=%22<path>%22"),
		same("/api/v1/x?m=a+b%20%2FUsers%2Fbruce", "/api/v1/x?m=a%20b%20<path>"),
		same("/api/v1/x?m=%E2%80%A8%2FUsers%2Fbruce", "/api/v1/x?m=%E2%80%A8<path>"),
	})
	// One line in, one line out, through the whole line road, and the output is a
	// fixed point.
	roots := advRoots()
	for _, in := range []string{
		"GET " + browse + "%2FUsers%2Fbruce%0Aforged-line 200",
		"GET " + browse + "%2FUsers%2Fbruce%0D%0Aforged 200",
		"GET " + browse + "see%20%2FUsers%2Fbruce%0Aforged",
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
// a "//" inside a route's path part starts a path (the redaction replaces from the second slash, so one slash survives), and a scheme's "//" does not.
func TestRedactPaths_DoubledSlashInARoutePath(t *testing.T) {
	runExact(t, []pc{
		same("/api/v1/x//Users/bruce", "/api/v1/x/<path>"),
		same("/api//Users/bruce", "/api/<path>"),
		same("/api/v1/x/%2FUsers%2Fbruce", "/api/v1/x/<path>"),
		same("/api/v1/x/%252FUsers%252Fbruce", "/api/v1/x/<path>"),
		same("/api/v1/x/https://example.com/y", "/api/v1/x/https://example.com/y"),
		same("/api/v1/x/file:///home/bruce/y", "/api/v1/x/file://<path>"),
		same("/api/v1/books//1", "/api/v1/books//1"), // a doubled slash that is not a path
		same("/api/v1/x?next=/api/v1/y//Users/bruce", "/api/v1/x?next=/api/v1/y/<path>"),
	})
}

// TestRedactPaths_QueryDepthCap (N-6): the "<query>" cap at maxNest is part of
// the rule's output, so a query nested past it is replaced, and one within it
// is not.
func TestRedactPaths_QueryDepthCap(t *testing.T) {
	runExact(t, []pc{
		same("/api/a?q=/api/b?q=/api/c?q=zzz", "/api/a?q=/api/b?q=/api/c?<query>"),
		same("/api/a?q=/api/b?q=zzz", "/api/a?q=/api/b?q=zzz"),
		same("/api/a?q=zzz", "/api/a?q=zzz"),
	})
}

// TestRedactPaths_EncodedPathOutsideARouteIsAKnownLimit pins G7: a
// percent-encoded absolute path that is not inside a route token is not
// decoded. No producer feeds a bundle source with such text; the day one does,
// this test changes with the fix, deliberately.
func TestRedactPaths_EncodedPathOutsideARouteIsAKnownLimit(t *testing.T) {
	roots := advRoots()
	for _, in := range []string{
		"path=%2FUsers%2Fbruce",
		"Get \"https://abs.example/api/x?path=%2FUsers%2Fbruce\"",
	} {
		if got := roots.redactPaths(in); got != in {
			t.Errorf("%q changed to %q: the known limit moved; update seams G7 and this test", in, got)
		}
	}
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
