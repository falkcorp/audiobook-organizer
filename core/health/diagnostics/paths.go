// file: core/health/diagnostics/paths.go
// version: 1.2.0
// guid: 5b1d7c34-9e02-4a68-b3f5-2c8e6d0a7f19
// last-edited: 2026-11-14

package diagnostics

import (
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PathRoot is a known filesystem root, by the name a reader of the bundle
// sees. An absolute path inside Path is written "<root:NAME>/rest"; any other
// absolute path is written "<path>" (05-health 5H.3.5 layer 2, "absolute
// filesystem paths outside known roots").
type PathRoot struct {
	// Name is the root's name (05-library root names): no "<", ">" or "/".
	Name string
	// Path is the root's absolute path.
	Path string
}

// pathRoots is a bundle's roots, longest path first so the most specific root
// names a path.
type pathRoots []PathRoot

// newPathRoots cleans the roots, drops one with no name or no absolute path,
// and orders them longest first.
func newPathRoots(in []PathRoot) pathRoots {
	var out pathRoots
	for _, r := range in {
		p := filepath.Clean(r.Path)
		if r.Name == "" || strings.ContainsAny(r.Name, "<>/\\") || !filepath.IsAbs(p) || p == string(filepath.Separator) {
			continue
		}
		out = append(out, PathRoot{Name: r.Name, Path: p})
	}
	slices.SortStableFunc(out, func(a, b PathRoot) int { return len(b.Path) - len(a.Path) })
	return out
}

// maxNest bounds how deep the rule recurses into a token it has already
// matched: the tail of a path under a root, the part of a route before its
// query, a query value that is itself a route. Each level re-reads only the
// text inside the token above it, so the work is linear until the bound; past
// it a text that still holds a path start is written "<path>" (fail closed), so
// a line built to nest cannot make the rule quadratic.
const maxNest = 3

// redactPaths rewrites every absolute path in free text s. A path is a run of
// non-delimiter characters that starts with "/" (a Windows drive and a
// separator, or a UNC "\\\\host") at the start of the text or after a
// boundary (pathBoundary: any byte that is not part of a name), so "a/b",
// "and/or", the path part of a URL and a closing tag ("</div>") are not
// touched. A known root is matched as a whole, so a root whose own path
// contains spaces is still named; below the root, and for a path outside every
// root, a space ends the path. That cut is the documented limit of free text:
// a path with spaces outside every root leaks its tail here. Structured values
// go through redactValue, which has the whole string.
//
// The rewrite is a fixed point: the ">" that ends "<root:NAME>" is not a
// boundary (pathBoundary) and the rule never writes a path after "<path>", so a
// second pass leaves both alone.
func (r pathRoots) redactPaths(s string) string { return r.redactPathsAt(s, 0, scanText) }

// scanMode says what the string being scanned is.
type scanMode uint8

const (
	// scanText is free text, or a value judged as text.
	scanText scanMode = iota
	// scanRoutePath is the path part of a route: there a doubled slash is the
	// start of a path ("/api/v1/x//Users/bruce", which is what the access log
	// writes for "/api/v1/x/%2FUsers%2Fbruce"), and only a scheme's "//" is not.
	scanRoutePath
)

func (r pathRoots) redactPathsAt(s string, depth int, mode scanMode) string {
	if !strings.ContainsAny(s, "/\\") {
		return s
	}
	if depth > maxNest {
		if r.hasPathStart(s, mode) {
			return "<path>"
		}
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		if !r.pathStart(s, i, mode) {
			i++
			continue
		}
		end, repl := r.pathAt(s, i, depth, mode)
		b.WriteString(s[last:i])
		b.WriteString(repl)
		last, i = end, end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// hasPathStart reports whether any absolute path begins in s.
func (r pathRoots) hasPathStart(s string, mode scanMode) bool {
	for i := 0; i < len(s); i++ {
		if r.pathStart(s, i, mode) {
			return true
		}
	}
	return false
}

// pathAt returns where the path starting at s[i] ends and what replaces it. A
// known root locates the extent of the path (a root with spaces in its own
// name is matched whole); then name decides what the path is called, from its
// cleaned form. A URL route shape (isRoute) is left as written except for its
// inside, which is judged (routeText); anything else is "<path>".
//
// A token that starts like a route is read to the end of its chunk (routeExtent),
// not to the first path delimiter: a query is not cut at "'", ")" or ",", which
// a browse request carries raw. If the chunk is not a route (a bad escape, a
// ".." out of the subtree, "/livez/x") the whole chunk is "<path>", so a
// route-shaped token that does not parse never leaves a tail behind.
func (r pathRoots) pathAt(s string, i, depth int, mode scanMode) (end int, repl string) {
	if mode == scanRoutePath {
		// A path that starts inside a route's path part runs to the end of the
		// part: the part is not prose, so no delimiter ends the path early.
		return len(s), r.name(s[i:], depth)
	}
	for _, root := range r {
		rest, ok := underRoot(s[i:], root.Path)
		if !ok {
			continue
		}
		tok := trimPathTail(s[i : i+len(root.Path)+pathRun(rest, isWinPath(root.Path))])
		if len(tok) < len(root.Path) {
			tok = s[i : i+len(root.Path)]
		}
		return i + len(tok), r.name(tok, depth)
	}
	if routeStart(s[i:]) {
		tok := trimRouteTail(s[i : i+routeExtent(s[i:])])
		if isRoute(tok) {
			return i + len(tok), r.routeText(tok, depth)
		}
		return i + len(tok), "<path>"
	}
	run := s[i : i+pathRun(s[i:], isWinPath(s[i:]))]
	tok := trimPathTail(run)
	return i + len(tok), r.name(tok, depth)
}

// name is what a path is called in the bundle: "<root:NAME>/rest" when the
// path, cleaned, is still under a known root, "<path>" otherwise. The raw text
// never decides: "/srv/lib/../../home/x" starts with a root and is not under
// it, and "/srv/lib/../vault/x" is under another. Only the cleaned tail is
// written, so no ".." survives into the output.
//
// A backslash is a separator only in a Windows path. Under a POSIX root it is a
// character of a name, so the text written is the POSIX-cleaned text with its
// backslashes intact. A reader may still take that backslash for a separator,
// so the path is named only when the backslash reading is under the same root
// too: "/srv/lib/x\..\..\home\bruce" is "<path>", not a name that carries a
// ".." out of the root for a reader who splits on "\".
//
// The tail below a root is scanned again for a path that starts inside it
// ("/srv/lib/a@/home/bruce/x"): a token is one run of name characters, so a
// path that begins after "@" or "=" would otherwise ride out in the tail.
func (r pathRoots) name(tok string, depth int) string {
	win := isWinPath(tok)
	clean := cleanPath(tok, win)
	alt := ""
	if !win && strings.Contains(tok, "\\") {
		alt = cleanPath(tok, true)
	}
	for _, root := range r {
		if isWinPath(root.Path) != win {
			continue
		}
		rp := root.Path
		if win {
			rp = slashes(rp)
		}
		rest, ok := cleanUnder(clean, rp)
		if !ok {
			continue
		}
		if alt != "" {
			if _, ok := cleanUnder(alt, rp); !ok {
				return "<path>"
			}
		}
		return "<root:" + root.Name + ">" + r.tailText(rest, depth)
	}
	return "<path>"
}

// tailText is what follows a root's placeholder: rest is "" or starts with "/"
// (cleanUnder), and the text after that "/" is read again for a path that
// starts inside it.
func (r pathRoots) tailText(rest string, depth int) string {
	if len(rest) <= 1 {
		return rest
	}
	return "/" + r.redactPathsAt(rest[1:], depth+1, scanText)
}

// cleanPath is the cleaned, forward-slash form of a path. A Windows path
// (drive or UNC) has its backslashes read as separators first; forcing it for
// a POSIX path gives the reading of a reader who splits on "\".
func cleanPath(tok string, backslashSeparators bool) string {
	if backslashSeparators {
		tok = slashes(tok)
	}
	return path.Clean(tok)
}

// cleanUnder reports whether the cleaned path is root itself or below it,
// returning what follows the root ("" or starting with "/"). Both are in the
// forward-slash form cleanPath writes.
func cleanUnder(clean, root string) (string, bool) {
	if len(clean) < len(root) || !pathEqual(clean[:len(root)], root) {
		return "", false
	}
	rest := clean[len(root):]
	if rest == "" || rest[0] == '/' {
		return rest, true
	}
	return "", false
}

// routeShape reports whether p, the path part of a request target, is a
// router-mounted route: the "/api/" and "/public/" subtrees and the exact
// probe endpoints. A request line or a metric label ("GET /api/v1/books/{id}",
// route="/api/core/v1/auth/status") names a route, not a file, and is the same
// text on every host. The cost is that a real directory called /api at the
// filesystem root is not redacted. An exact endpoint ends there: "/livez/x" is
// a path. cleaned says p already went through path.Clean, which turns the
// subtree's own root ("/api/") into "/api".
func routeShape(p string, cleaned bool) bool {
	switch p {
	case "/livez", "/readyz", "/metrics":
		return true
	case "/api", "/public":
		return cleaned
	}
	return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/public/")
}

// isRoute reports whether run (a path-shaped token) is a router-mounted route.
// Only the part before a "?" or "#" is judged. It is judged as written, and
// again percent-decoded and cleaned (a "%2e%2e" is a ".." to the router), and
// cleaned with a backslash read as a separator, so no spelling of ".." carries
// it out of the route's subtree. A percent escape that does not decode is not a
// route.
func isRoute(run string) bool {
	p := run
	if i := strings.IndexAny(run, "?#"); i >= 0 {
		p = run[:i]
	}
	if !routeShape(p, false) {
		return false
	}
	dec := p
	for range 3 {
		next, err := url.PathUnescape(dec)
		if err != nil {
			return false
		}
		if next == dec {
			break
		}
		dec = next
	}
	if next, err := url.PathUnescape(dec); err != nil || next != dec {
		return false // nested more deeply than we decode: fail closed
	}
	return routeShape(path.Clean(dec), true) && routeShape(path.Clean(slashes(dec)), true)
}

// routeStart reports whether s begins like a route: the "/api/" and "/public/"
// subtrees and the exact probe endpoints. It is the cheap test before the
// extent of a route is read; isRoute decides.
func routeStart(s string) bool {
	return strings.HasPrefix(s, "/api/") || strings.HasPrefix(s, "/public/") ||
		strings.HasPrefix(s, "/livez") || strings.HasPrefix(s, "/readyz") || strings.HasPrefix(s, "/metrics")
}

// routeExtent is the length of the chunk a route-shaped token spans in free
// text: to whitespace, a control byte, a double quote, a backtick or a `\"`
// escape. Every other byte is part of it, including the bytes that end a
// filesystem path ("'" ")" "," "]" ";" "|" "}" "<" ">"), because a query value
// may hold any of them raw.
func routeExtent(s string) int {
	n := 0
	for n < len(s) {
		c := s[n]
		if c <= ' ' || c == 0x7f || c == '"' || c == '`' {
			break
		}
		if c == '\\' && n+1 < len(s) && s[n+1] == '"' {
			break
		}
		n++
	}
	return n
}

// trimRouteTail drops the punctuation that closes a route in prose
// ("(GET /api/x)", "'/api/x?a=b'", "GET /api/x, then") so it stays outside the
// token. One backward pass. A final segment of dots ("/api/x/..") is a name, not
// punctuation, as in trimPathTail.
func trimRouteTail(tok string) string {
	cut := len(tok)
	for cut > 0 && strings.IndexByte("'),];|}!.:", tok[cut-1]) >= 0 {
		cut--
	}
	if cut > 0 && (tok[cut-1] == '/' || tok[cut-1] == '\\') {
		for cut < len(tok) && tok[cut] == '.' {
			cut++
		}
	}
	return tok[:cut]
}

// routeText is a route as written, except that its inside is judged: the path
// part is decoded and read again for a path that starts inside it (routePath),
// the query is values (redactQuery), and the fragment is one more value.
func (r pathRoots) routeText(tok string, depth int) string {
	i := strings.IndexAny(tok, "?#")
	pathPart, rest := tok, ""
	if i >= 0 {
		pathPart, rest = tok[:i], tok[i:]
	}
	out := "/" + r.routePath(pathPart[1:], depth)
	switch {
	case rest == "":
		return out
	case rest[0] == '#':
		return out + "#" + r.queryPart(rest[1:], depth, false)
	}
	q, frag, hasFrag := strings.Cut(rest[1:], "#")
	out += "?" + r.redactQuery(q, depth)
	if hasFrag {
		out += "#" + r.queryPart(frag, depth, false)
	}
	return out
}

// routePath is a route's path part (without its first "/"): percent-decoded to a
// fixed point and read as text in scanRoutePath mode, so
// "/api/v1/x/%2FUsers%2Fbruce" and "/api/v1/x//Users/bruce" are judged alike. A
// part with no path in it is kept as written; one with a path is written
// re-encoded if it had to be decoded.
func (r pathRoots) routePath(pp string, depth int) string {
	dec, exhausted := lenientDecode(pp, false)
	if exhausted {
		return "<value>"
	}
	out := r.redactPathsAt(dec, depth+1, scanRoutePath)
	switch {
	case out == dec:
		return pp
	case dec == pp:
		return out
	}
	return encodeRedacted(out)
}

// redactQuery judges a query string as values: each name and value is
// percent-decoded and judged (queryPart); the rest is kept exactly as written. A
// path in a query is not a route ("?path=/Users/bruce/x" and "?path=%2FUsers%2Fbruce"
// are what filesystem/browse logs). Past maxNest a query is "<query>".
func (r pathRoots) redactQuery(q string, depth int) string {
	if q == "" {
		return q
	}
	if depth+1 >= maxNest {
		return "<query>"
	}
	pairs := strings.Split(q, "&")
	for i, p := range pairs {
		k, v, hasV := strings.Cut(p, "=")
		out := r.queryPart(k, depth, true)
		if hasV {
			out += "=" + r.queryPart(v, depth, true)
		}
		pairs[i] = out
	}
	return strings.Join(pairs, "&")
}

// queryPart is one name, value or fragment of a route: left as written unless
// its decoded form holds a path. Decoding is lenient (a valid %XX decodes, a bad
// one stays, "+" is a space when plus) and runs to a fixed point within
// maxDecode rounds; a part that is still changing after that is "<value>". A
// part with a path is written as the rewritten text if it was not decoded, and
// percent-encoded (encodeRedacted) if it was, so a decoded control byte, "&",
// "#" or space never reaches the output.
func (r pathRoots) queryPart(raw string, depth int, plus bool) string {
	dec, exhausted := lenientDecode(raw, plus)
	if exhausted {
		return "<value>"
	}
	out := r.queryValue(dec, depth+1)
	switch {
	case out == dec:
		return raw
	case dec == raw:
		return out
	}
	return encodeRedacted(out)
}

// queryValue judges a decoded query part. A value has no prose: one that is a
// route recurses, and one that starts as an absolute path is that path whole,
// spaces, quotes and brackets included ("/Users/bruce/Harry Potter (old)/x" is
// one path). Anything else is text.
func (r pathRoots) queryValue(dec string, depth int) string {
	if dec == "" || strings.ContainsAny(dec, "\n\r") || !absolutePath(dec) {
		return r.redactPathsAt(dec, depth, scanText)
	}
	if r.wholeRoute(dec) {
		return r.routeText(dec, depth)
	}
	return r.name(dec, depth)
}

// wholeRoute reports whether s, a whole value that starts as an absolute path, is
// one route (and not a path under a configured root, which names the root).
func (r pathRoots) wholeRoute(s string) bool {
	if !routeStart(s) || !isRoute(s) {
		return false
	}
	for _, root := range r {
		if _, ok := underRoot(s, root.Path); ok {
			return false
		}
	}
	return true
}

// maxDecode bounds the percent-decoding rounds of one part. A browser encodes at
// most twice; a part still changing after five is built to hide something.
const maxDecode = 5

// lenientDecode percent-decodes s to a fixed point, at most maxDecode rounds,
// and reports whether it was still changing at the end (fail closed: the caller
// writes a placeholder). Linear: a fixed number of linear passes.
func lenientDecode(s string, plus bool) (dec string, exhausted bool) {
	for range maxDecode {
		next := unescapeLenient(s, plus)
		if next == s {
			return s, false
		}
		s = next
	}
	return s, unescapeLenient(s, plus) != s
}

// unescapeLenient decodes every valid %XX (and "+" as a space when plus) and
// leaves everything else, so "50%" and "%zz" stay as they are instead of
// failing the way url.QueryUnescape does.
func unescapeLenient(s string, plus bool) string {
	if !strings.Contains(s, "%") && !(plus && strings.Contains(s, "+")) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
		case c == '+' && plus:
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

// encodeRedacted percent-encodes rewritten text for a query: letters, digits
// "-._~" and the bytes of the rule's own placeholders ("/", ":", "<", ">") stay,
// every other byte is %XX. Nothing decoded survives as a control byte, a line
// break, a separator or a quote, and a second pass over the output decodes it
// back to the same text, so the rewrite is a fixed point.
func encodeRedacted(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) + len(s)/2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' || c == '/' || c == ':' || c == '<' || c == '>' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

// redactValue rewrites a string that is a whole value (a JSON string, as
// opposed to a line of prose). When the entire value is one clean absolute path
// it is replaced as one, spaces and all, so "/Users/bruce/Library/Application
// Support/app/db" does not leak its tail. A value that is more than that (a
// second path later in the text, ": " and a message, a quoted string) is prose
// that starts with a path, and goes to redactPaths so every path in it is
// rewritten and none of its text is lost to the replacement. A newline, a value
// that is not path-shaped, and a route take the same road. A single path is
// named from its cleaned form (name), so ".." cannot carry it out of a root.
func (r pathRoots) redactValue(s string) string { return r.redactValueAt(s, 0) }

// redactValueAt: a value that starts as one route is that route whole, whatever
// follows it (a quote, ": " and a message, a later path: the query's values and
// the path part are judged, so none of that text is a way around the rule); a
// value that is one clean path is that path; any other value is text.
func (r pathRoots) redactValueAt(s string, depth int) string {
	if s == "" || strings.ContainsAny(s, "\n\r") || !absolutePath(s) {
		return r.redactPathsAt(s, depth, scanText)
	}
	if r.wholeRoute(s) {
		return r.routeText(s, depth)
	}
	if !r.singlePath(s) {
		return r.redactPathsAt(s, depth, scanText)
	}
	for _, root := range r {
		if rest, ok := underRoot(s, root.Path); ok && rest != "" && !isSepFor(isWinPath(root.Path), rest[0]) {
			return r.redactPathsAt(s, depth, scanText) // punctuation after the root: prose
		}
	}
	return r.name(s, depth)
}

// singlePath reports whether s, which starts as an absolute path, is nothing
// but that path: no later path start, no ": " (a message after the path) and no
// double quote or backtick. How the path is spelled does not matter: a trailing
// or doubled separator or a "/./" segment is still one path, and a space inside
// it is part of a name, so a non-canonical value is not prose. An apostrophe is
// a character of names ("/Music/Bruce's Mix"); a quoted second path is caught
// by the later-path-start rule.
func (r pathRoots) singlePath(s string) bool {
	if strings.Contains(s, ": ") || strings.ContainsAny(s, "\"`") {
		return false
	}
	for i := 1; i < len(s); i++ {
		if r.pathStart(s, i, scanText) {
			return false
		}
	}
	return true
}

// absolutePath reports a value that starts as an absolute path: "/x", a
// Windows drive or a UNC share.
func absolutePath(s string) bool {
	return pathStartAt(s, 0)
}

// isWinPath reports a Windows path: a drive and a separator, or a UNC share.
func isWinPath(s string) bool {
	return winDrive(s, 0) || strings.HasPrefix(s, `\\`)
}

// isSepFor reports whether c separates path segments in a path of that kind: a
// Windows path takes both, a POSIX path only "/". A backslash in a POSIX path
// is a character of a name.
func isSepFor(win bool, c byte) bool { return c == '/' || win && c == '\\' }

// underRoot reports whether text starts with root and the root ends there (end
// of text, a separator or a delimiter), returning the text after the root.
func underRoot(text, root string) (string, bool) {
	if len(text) < len(root) || !pathEqual(text[:len(root)], root) {
		return "", false
	}
	win := isWinPath(root)
	rest := text[len(root):]
	if rest == "" || isSepFor(win, rest[0]) || isPathDelim(rest[0]) {
		return rest, true
	}
	// Sentence punctuation right after the root ("see /srv/lib.").
	if run := rest[:pathRun(rest, win)]; strings.Trim(run, ".:") == "" {
		return rest, true
	}
	return "", false
}

// pathEqual compares path text; a Windows path (a drive letter) ignores case.
func pathEqual(a, b string) bool {
	if winDrive(b, 0) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// pathRun is the length of the run of path characters at the start of s. A
// backslash that escapes a quote ("\\"" in a quoted string) ends the run, so
// the escape stays with the text around the path. In a POSIX path (win false)
// so does a backslash escape of a control character or a code point ("\n",
// "\t", "\r", "\u2028": what a quoted log value writes), which is not a name:
// it is where the next thing starts. A Windows path keeps those, because "\n"
// there is a separator and the first letter of "new".
func pathRun(s string, win bool) int {
	n := 0
	for n < len(s) && !isPathDelim(s[n]) {
		if s[n] == '\\' && n+1 < len(s) {
			if s[n+1] == '"' || s[n+1] == '\'' {
				break
			}
			if !win && escapeAt(s, n) {
				break
			}
		}
		n++
	}
	return n
}

// escapeAt reports whether s[i:] is a backslash escape of a control character
// ("\n", "\t", "\r") or of a code point ("\u" and four hex digits).
func escapeAt(s string, i int) bool {
	if i+1 >= len(s) || s[i] != '\\' {
		return false
	}
	switch s[i+1] {
	case 'n', 't', 'r':
		return true
	case 'u':
		return i+6 <= len(s) && isHex(s[i+2]) && isHex(s[i+3]) && isHex(s[i+4]) && isHex(s[i+5])
	}
	return false
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// trimPathTail drops sentence punctuation that follows a path in prose
// ("open /x/y: no such file", "see /x/y."). A final segment made only of dots
// ("/x/.", "/x/..", "/x/...") is a name, not punctuation: stripping part of it
// would misname it or leave a ".." outside the replacement, where it undoes the
// cleaning. One pass: the last separator is found once and the run of "." and
// ":" after it is cut in one step.
func trimPathTail(tok string) string {
	seg := strings.LastIndexAny(tok, "/\\") + 1
	last := tok[seg:]
	if dots := strings.TrimRight(last, ":"); dots != "" && strings.Trim(dots, ".") == "" {
		return tok[:seg+len(dots)]
	}
	cut := seg + len(strings.TrimRight(last, ".:"))
	if cut == 0 {
		return tok
	}
	return tok[:cut]
}

// slashes writes a Windows path's backslashes as "/", one for each run of
// backslashes.
func slashes(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		for i+1 < len(s) && s[i+1] == '\\' {
			i++
		}
		b.WriteByte('/')
	}
	return b.String()
}

// isPathDelim ends a path.
func isPathDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', ',', ';', ')', ']', '}', '<', '>', '|', 0:
		return true
	}
	return false
}

// pathStart reports whether an absolute path begins at s[i].
func (r pathRoots) pathStart(s string, i int, mode scanMode) bool {
	if i > 0 && !r.pathBoundary(s, i, mode) {
		return false
	}
	return pathStartAt(s, i)
}

// pathStartAt reports whether s[i:] begins like an absolute path: "/x" (or a
// doubled "//x", a join that did not collapse its slash), a drive ("C:\") or a
// UNC share ("\\\\host\\share"). A "/" with a single segment after it is a path
// only if that segment is a name (shapedSegment), so "*/15", "*/*", "+/-",
// "50%/50%" and "(read)/(write)" are not.
func pathStartAt(s string, i int) bool {
	switch c := s[i]; {
	case c == '/':
		j := i
		for j < len(s) && s[j] == '/' {
			j++
		}
		return j < len(s) && !isPathDelim(s[j]) && shapedSegment(s, j)
	case c == '\\':
		return i+2 < len(s) && s[i+1] == '\\' && s[i+2] != '\\' && !isPathDelim(s[i+2])
	default:
		return winDrive(s, i)
	}
}

// shapedSegment reports whether the path whose first segment starts at s[j]
// looks like a path: it has a second segment, or its one segment starts with a
// letter, a digit, "_", "." or "~" and holds a letter. The scan stops at the
// next "/" or delimiter, so the cost is the segment, and segments do not
// overlap.
func shapedSegment(s string, j int) bool {
	k := j
	for k < len(s) && s[k] != '/' && !isPathDelim(s[k]) {
		k++
	}
	if k < len(s) && s[k] == '/' {
		return true
	}
	seg := s[j:k]
	first, _ := utf8.DecodeRuneInString(seg)
	if !(unicode.IsLetter(first) || unicode.IsDigit(first) || first == '_' || first == '.' || first == '~') {
		return false
	}
	for _, c := range seg {
		if unicode.IsLetter(c) {
			return true
		}
	}
	return false
}

// pathBoundary reports whether a path may start after s[i-1]. Any byte that is
// not part of a name is a boundary (the set of bytes that cannot be one is
// closed, the set of separators is not: "@", "+", "#", "!", "&", "*", "=",
// "(", a quote, U+2028 and the rest all begin a path), so a path is found after
// whatever precedes it. A name byte is not: letters, digits and "._~-", and
// any letter, digit or mark of another script. The exceptions keep the rewrite
// a fixed point and leave markup and URLs alone:
//
//   - the ">" that ends the placeholder of a configured root ("<root:NAME>"),
//     whose tail is already rewritten;
//   - the "/" of a closing tag ("</div>");
//   - a "/" after a "/" (the path is the first of the run), except the third
//     slash of "file:///x";
//   - the "//" after a scheme ("https://host"), though the "/" after the host
//     of a "file://host/x" does start a path;
//   - a backslash escape that is itself a boundary ("\n", "\t", "\r",
//     "\uXXXX": the quoted text "l1\n/home/bruce/x" has a path in it).
func (r pathRoots) pathBoundary(s string, i int, mode scanMode) bool {
	c := s[i-1]
	switch {
	case c == '>' && r.endsRootPlaceholder(s, i):
		return false
	case c == '<' && closingTag(s, i):
		return false
	case c == '/':
		if i >= 3 && s[i-2] == '/' && s[i-3] == ':' {
			return true // the third slash of "file:///x"
		}
		// In a route's path part the second slash of a run starts a path, unless
		// the run follows a scheme's colon or is longer (the first slash of the
		// run is judged once, so a run of slashes is not rescanned).
		return mode == scanRoutePath && (i < 2 || s[i-2] != '/' && s[i-2] != ':')
	case c == ':' && s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
		return false
	case isNameByte(c):
		return afterEscape(s, i) || fileURLHost(s, i)
	case c >= utf8.RuneSelf:
		rn, _ := utf8.DecodeLastRuneInString(s[:i])
		return !(unicode.IsLetter(rn) || unicode.IsDigit(rn) || unicode.IsMark(rn))
	}
	return true
}

// isNameByte reports a byte that belongs to a name: letters, digits and "._~-".
func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-'
}

// afterEscape reports whether s[i] follows a backslash escape ("\n", "\t",
// "\r" or "\uXXXX") that ends at i.
func afterEscape(s string, i int) bool {
	return i >= 2 && escapeAt(s, i-2) && s[i-1] != 'u' || i >= 6 && escapeAt(s, i-6)
}

// fileURLHost reports whether the "/" at s[i] follows "file://HOST": the path
// of a file URL with a host ("file://localhost/home/bruce/x").
func fileURLHost(s string, i int) bool {
	if s[i] != '/' {
		return false
	}
	j := i
	for j > 0 && i-j <= 253 && (isNameByte(s[j-1]) && s[j-1] != '~') {
		j--
	}
	return j < i && j >= 7 && strings.EqualFold(s[j-7:j], "file://")
}

// endsRootPlaceholder reports whether the ">" at s[i-1] closes "<root:NAME>"
// for the name of a configured root: the redactor's own output, whose tail is
// already rewritten. "<path>" is not skipped: the redactor never writes a
// path right after it, so "<path>/home/bruce/x" is a path to redact. A name no
// root has is text someone wrote, not output of this rule.
func (r pathRoots) endsRootPlaceholder(s string, i int) bool {
	// Compare the text before the ">" with each root's "<root:NAME" in place:
	// no search back through the line, so a line of ">" costs O(roots) per
	// character, not O(line).
	const open = "<root:"
	for _, root := range r {
		start := i - (len(open) + len(root.Name) + 1)
		if start >= 0 && s[start:start+len(open)] == open && s[start+len(open):i-1] == root.Name {
			return true
		}
	}
	return false
}

// closingTag reports whether s[i] is the "/" of "</name>": tag-name characters
// follow it and then ">".
func closingTag(s string, i int) bool {
	if s[i] != '/' {
		return false
	}
	j := i + 1
	for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
		j++
	}
	return j > i+1 && j < len(s) && s[j] == '>'
}

// winDrive reports "X:\" or "X:/" at s[i].
func winDrive(s string, i int) bool {
	if i+2 >= len(s) {
		return false
	}
	c := s[i]
	return (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') && s[i+1] == ':' && (s[i+2] == '\\' || s[i+2] == '/')
}
