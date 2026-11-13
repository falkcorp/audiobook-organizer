// file: core/health/diagnostics/paths.go
// version: 1.0.0
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

// redactPaths rewrites every absolute path in s. A path is a run of non-
// delimiter characters that starts with "/" (or a Windows drive and a
// separator) at the start of the text or after a delimiter, so "a/b",
// "and/or" and the path part of a URL are not touched. A known root is
// matched as a whole, so a root whose own path contains spaces is still named;
// below the root, and for a path outside every root, a space ends the path.
func (r pathRoots) redactPaths(s string) string {
	if !strings.ContainsAny(s, "/\\") {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		if !pathStart(s, i) {
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

// pathAt returns where the path starting at s[i] ends and what replaces it.
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
	tok := trimPathTail(s[i : i+pathRun(s[i:])])
	return i + len(tok), "<path>"
}

// underRoot reports whether text starts with root and the root ends there (end
// of text, a separator or a delimiter), returning the text after the root.
func underRoot(text, root string) (string, bool) {
	if len(text) < len(root) || !pathEqual(text[:len(root)], root) {
		return "", false
	}
	rest := text[len(root):]
	if rest == "" || rest[0] == '/' || rest[0] == '\\' || isPathDelim(rest[0]) {
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

// pathRun is the length of the run of path characters at the start of s.
func pathRun(s string) int {
	n := 0
	for n < len(s) && !isPathDelim(s[n]) {
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

// slashes writes a Windows path's remainder with "/".
func slashes(s string) string { return strings.ReplaceAll(s, "\\", "/") }

// isPathDelim ends a path.
func isPathDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', ',', ';', ')', ']', '}', '<', '>', '|', 0:
		return true
	}
	return false
}

// pathStart reports whether an absolute path begins at s[i].
func pathStart(s string, i int) bool {
	if i > 0 && !pathBoundary(s, i) {
		return false
	}
	switch c := s[i]; {
	case c == '/':
		return i+1 < len(s) && s[i+1] != '/' && !isPathDelim(s[i+1])
	default:
		return winDrive(s, i)
	}
}

// pathBoundary reports whether a path may start after s[i-1].
func pathBoundary(s string, i int) bool {
	switch c := s[i-1]; {
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

// winDrive reports "X:\" or "X:/" at s[i].
func winDrive(s string, i int) bool {
	if i+2 >= len(s) {
		return false
	}
	c := s[i]
	return (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') && s[i+1] == ':' && (s[i+2] == '\\' || s[i+2] == '/')
}
