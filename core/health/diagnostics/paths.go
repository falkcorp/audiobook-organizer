// file: core/health/diagnostics/paths.go
// version: 1.0.2
// guid: 5b1d7c34-9e02-4a68-b3f5-2c8e6d0a7f19
// last-edited: 2026-11-13

package diagnostics

import (
	"path/filepath"
	"slices"
	"strings"
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

// redactPaths rewrites every absolute path in free text s. A path is a run of
// non-delimiter characters that starts with "/" (a Windows drive and a
// separator, or a UNC "\\\\host") at the start of the text or after a
// delimiter, so "a/b", "and/or", the path part of a URL and a closing tag
// ("</div>") are not touched. A known root is matched as a whole, so a root
// whose own path contains spaces is still named; below the root, and for a
// path outside every root, a space ends the path. That cut is the documented
// limit of free text: a path with spaces outside every root leaks its tail
// here. Structured values go through redactValue, which has the whole string.
//
// The rewrite is a fixed point: the ">" that ends "<root:NAME>" is not a
// boundary (pathBoundary) and the rule never writes a path after "<path>", so a
// second pass leaves both alone.
func (r pathRoots) redactPaths(s string) string {
	if !strings.ContainsAny(s, "/\\") {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		if !r.pathStart(s, i) {
			i++
			continue
		}
		end, repl := r.pathAt(s, i)
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

// pathAt returns where the path starting at s[i] ends and what replaces it. A
// known root names the path first; then a URL route shape (routePrefixes) is
// left as written; anything else is "<path>".
func (r pathRoots) pathAt(s string, i int) (end int, repl string) {
	for _, root := range r {
		rest, ok := underRoot(s[i:], root.Path)
		if !ok {
			continue
		}
		tok := trimPathTail(s[i : i+len(root.Path)+pathRun(rest)])
		if len(tok) < len(root.Path) {
			tok = s[i : i+len(root.Path)]
		}
		return i + len(tok), "<root:" + root.Name + ">" + slashes(tok[len(root.Path):])
	}
	run := s[i : i+pathRun(s[i:])]
	if isRoute(run) {
		return i + len(run), run
	}
	tok := trimPathTail(run)
	return i + len(tok), "<path>"
}

// routePrefixes are the HTTP subtrees the router mounts: a request line or a
// metric label ("GET /api/v1/books/{id}", route="/api/core/v1/auth/status")
// names a route, not a file, and is the same text on every host. The cost is
// that a real directory called /api at the filesystem root is not redacted.
var routePrefixes = []string{"/api/", "/public/", "/livez", "/readyz", "/metrics"}

// isRoute reports whether run (a path-shaped token) is a router-mounted route.
// An exact endpoint ("/livez") must end there or at a query or a further
// segment, so "/livez-backup" is still a path.
func isRoute(run string) bool {
	for _, p := range routePrefixes {
		rest, ok := strings.CutPrefix(run, p)
		if !ok {
			continue
		}
		if strings.HasSuffix(p, "/") || rest == "" || rest[0] == '/' || rest[0] == '?' {
			return true
		}
	}
	return false
}

// redactValue rewrites a string that is a whole value (a JSON string, as
// opposed to a line of prose). When the entire value is one clean absolute path
// it is replaced as one, spaces and all, so "/Users/bruce/Library/Application
// Support/app/db" does not leak its tail. A value that is more than that (a
// second path later in the text, ": " and a message, a quoted string, a path
// filepath.Clean would change) is prose that starts with a path, and goes to
// redactPaths so every path in it is rewritten and none of its text is lost to
// the replacement. A newline, a value that is not path-shaped, and a route
// take the same road.
func (r pathRoots) redactValue(s string) string {
	if s == "" || strings.ContainsAny(s, "\n\r") || !absolutePath(s) || !r.singlePath(s) {
		return r.redactPaths(s)
	}
	for _, root := range r {
		if rest, ok := underRoot(s, root.Path); ok {
			if rest != "" && !isPathSep(rest[0]) {
				return r.redactPaths(s) // punctuation after the root: prose
			}
			return "<root:" + root.Name + ">" + slashes(rest)
		}
	}
	if isRoute(s) {
		return r.redactPaths(s)
	}
	return "<path>"
}

// singlePath reports whether s, which starts as an absolute path, is nothing
// but that path: no later path start, no ": " (a message after the path), no
// double quote or backtick, and filepath.Clean leaves it as it is. An
// apostrophe is a character of names ("/Music/Bruce's Mix"); a quoted second
// path is caught by the later-path-start rule.
func (r pathRoots) singlePath(s string) bool {
	if strings.Contains(s, ": ") || strings.ContainsAny(s, "\"`") || filepath.Clean(s) != s {
		return false
	}
	for i := 1; i < len(s); i++ {
		if r.pathStart(s, i) {
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

func isPathSep(c byte) bool { return c == '/' || c == '\\' }

// underRoot reports whether text starts with root and the root ends there (end
// of text, a separator or a delimiter), returning the text after the root.
func underRoot(text, root string) (string, bool) {
	if len(text) < len(root) || !pathEqual(text[:len(root)], root) {
		return "", false
	}
	rest := text[len(root):]
	if rest == "" || isPathSep(rest[0]) || isPathDelim(rest[0]) {
		return rest, true
	}
	// Sentence punctuation right after the root ("see /srv/lib.").
	if run := rest[:pathRun(rest)]; strings.Trim(run, ".:") == "" {
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
// the escape stays with the text around the path.
func pathRun(s string) int {
	n := 0
	for n < len(s) && !isPathDelim(s[n]) {
		if s[n] == '\\' && n+1 < len(s) && (s[n+1] == '"' || s[n+1] == '\'') {
			break
		}
		n++
	}
	return n
}

// trimPathTail drops sentence punctuation that follows a path in prose
// ("open /x/y: no such file", "see /x/y.").
func trimPathTail(tok string) string {
	for len(tok) > 1 && (strings.HasSuffix(tok, ":") || strings.HasSuffix(tok, ".")) {
		tok = tok[:len(tok)-1]
	}
	return tok
}

// slashes writes a Windows path's remainder with "/", one for each run of
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
func (r pathRoots) pathStart(s string, i int) bool {
	if i > 0 && !r.pathBoundary(s, i) {
		return false
	}
	return pathStartAt(s, i)
}

// pathStartAt reports whether s[i:] begins like an absolute path: "/x", a
// drive ("C:\") or a UNC share ("\\\\host\\share").
func pathStartAt(s string, i int) bool {
	switch c := s[i]; {
	case c == '/':
		return i+1 < len(s) && s[i+1] != '/' && !isPathDelim(s[i+1])
	case c == '\\':
		return i+2 < len(s) && s[i+1] == '\\' && s[i+2] != '\\' && !isPathDelim(s[i+2])
	default:
		return winDrive(s, i)
	}
}

// pathBoundary reports whether a path may start after s[i-1]. A "<" and a ">"
// are boundaries like any delimiter ("<b>/home/bruce/x" has a path in it), with
// two narrow exceptions that keep the rewrite a fixed point and leave markup
// alone: a ">" that ends the placeholder of a configured root ("<root:NAME>"),
// whose tail is already rewritten, and the "/" of a closing tag
// ("</div>": "/", tag-name characters, then ">").
func (r pathRoots) pathBoundary(s string, i int) bool {
	switch c := s[i-1]; {
	case c == '>' && r.endsRootPlaceholder(s, i):
		return false
	case c == '<' && closingTag(s, i):
		return false
	case isPathDelim(c):
		return true
	case c == '(' || c == '[' || c == '{' || c == '=' || c == ':':
		return true
	case c == '/':
		// The path of file:///x, whose third slash starts it.
		return i >= 3 && s[i-2] == '/' && s[i-3] == ':'
	}
	return false
}

// endsRootPlaceholder reports whether the ">" at s[i-1] closes "<root:NAME>"
// for the name of a configured root: the redactor's own output, whose tail is
// already rewritten. "<path>" is not skipped: the redactor never writes a
// path right after it, so "<path>/home/bruce/x" is a path to redact. A name no
// root has is text someone wrote, not output of this rule.
func (r pathRoots) endsRootPlaceholder(s string, i int) bool {
	open := strings.LastIndexByte(s[:i-1], '<')
	if open < 0 {
		return false
	}
	name, ok := strings.CutPrefix(s[open:i], "<root:")
	if !ok {
		return false
	}
	name, ok = strings.CutSuffix(name, ">")
	return ok && slices.ContainsFunc(r, func(root PathRoot) bool { return root.Name == name })
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
