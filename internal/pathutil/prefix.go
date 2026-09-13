// file: internal/pathutil/prefix.go
// version: 1.0.1
// guid: 2f81aaa2-4529-41f2-9f2c-6649fc5b1ee9
// last-edited: 2026-09-12

package pathutil

import "strings"

// isPathSep reports whether c is a path separator in either the POSIX or the
// Windows form. Both count because the path remappers handle iTunes locations
// written on Windows ("C:\Music\iTunes") as well as local POSIX paths.
func isPathSep(c byte) bool {
	return c == '/' || c == '\\'
}

// CutPathPrefix is strings.CutPrefix restricted to a path-component boundary:
// it reports whether p is prefix itself or lies under it, and returns the rest
// of p after prefix. A bare strings.HasPrefix treats "/lib" as a prefix of
// "/lib2/a.m4b"; this does not.
//
// The boundary holds when any of these is true:
//   - p == prefix (rest is "");
//   - prefix already ends in a separator ("/lib/" or `C:\Music\`);
//   - the byte of p right after prefix is a separator (rest starts with it).
//
// Both '/' and '\' count as separators. rest is always p[len(prefix):], so a
// caller writing `to + rest` gets exactly the bytes a bare HasPrefix match
// would have produced for every path the boundary rule still accepts. An empty
// prefix never matches: callers treat an empty mapping as "not configured".
func CutPathPrefix(p, prefix string) (rest string, ok bool) {
	if prefix == "" || !strings.HasPrefix(p, prefix) {
		return "", false
	}
	return boundaryRest(p, prefix)
}

// IsWithin reports whether path is root itself or lies under root, on a
// path-component boundary. It is the predicate form of CutPathPrefix and is the
// replacement for a bare strings.HasPrefix(path, root) containment check, which
// wrongly treats "/lib2/a.m4b" as inside "/lib".
//
// For every path a bare HasPrefix accepts, IsWithin agrees except when root
// lacks a trailing separator AND the byte of path right after root is not a
// separator (the sibling case). In particular:
//   - root "/lib/" (trailing separator) matches exactly what HasPrefix matched;
//   - root "/" matches every absolute path, as before;
//   - path == root matches, as before;
//   - an empty root matches nothing (HasPrefix matched everything), so a
//     caller that relied on "" meaning "everything" must say so itself.
//
// Both '/' and '\' count as separators, so Windows drive roots such as
// `C:\lib` and "C:/lib" work without normalising. The comparison is
// case-sensitive and does no cleaning: "/lib/../x" is "within" "/lib" here
// exactly as it was under HasPrefix.
func IsWithin(path, root string) bool {
	_, ok := CutPathPrefix(path, root)
	return ok
}

// CutPathPrefixFold is CutPathPrefix with an ASCII/Unicode case-insensitive
// comparison of the prefix (strings.EqualFold), for Windows drive paths whose
// case is not significant. The comparison covers exactly len(prefix) bytes of
// p, and rest is sliced from the ORIGINAL p at that offset, so the remainder
// keeps its original case and is byte-identical to what CutPathPrefix returns
// when the cases already agree.
func CutPathPrefixFold(p, prefix string) (rest string, ok bool) {
	if prefix == "" || len(p) < len(prefix) || !strings.EqualFold(p[:len(prefix)], prefix) {
		return "", false
	}
	return boundaryRest(p, prefix)
}

// boundaryRest applies the separator-boundary rule once p is known to start
// with prefix (in whichever case sense the caller checked).
func boundaryRest(p, prefix string) (string, bool) {
	rest := p[len(prefix):]
	if rest == "" || isPathSep(prefix[len(prefix)-1]) || isPathSep(rest[0]) {
		return rest, true
	}
	return "", false
}
