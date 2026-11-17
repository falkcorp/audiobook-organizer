// file: core/health/diagnostics/roots.go
// version: 2.0.0
// guid: 5b1d7c34-9e02-4a68-b3f5-2c8e6d0a7f19
// last-edited: 2026-11-16

package diagnostics

import (
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// PathRoot is a known filesystem root, by the name a reader of the bundle
// sees. Text under Path is written "<root:NAME>" with nothing after it (the
// library-relative tail names authors and titles, so it is hidden too); any
// other absolute location is written "<path>" (05-health 5H.3.5 layer 2,
// "absolute filesystem paths outside known roots").
type PathRoot struct {
	// Name is the root's name (05-library root names). A byte of it that the
	// sanitizer would itself rewrite (a separator, "<", ">", "%", "~", "&", ":"
	// or a control character) is written "_" in the placeholder, so the
	// placeholder is stable under a second pass.
	Name string
	// Path is the root's absolute path.
	Path string
}

// pathRoots is a bundle's roots, longest path first so the most specific root
// names a location.
type pathRoots []PathRoot

// newPathRoots cleans the roots, drops one with no name or no absolute path,
// writes the unsafe bytes of a name as "_", and orders them longest first.
func newPathRoots(in []PathRoot) pathRoots {
	var out pathRoots
	for _, r := range in {
		p := filepath.Clean(r.Path)
		name := safeRootName(r.Name)
		if name == "" || !filepath.IsAbs(p) || p == string(filepath.Separator) {
			continue
		}
		out = append(out, PathRoot{Name: name, Path: p})
	}
	slices.SortStableFunc(out, func(a, b PathRoot) int { return len(b.Path) - len(a.Path) })
	return out
}

// safeRootName is name with every byte the sanitizer reacts to written "_".
// The placeholder "<root:NAME>" holds no trigger byte, which is what makes
// Text idempotent. An empty name stays empty (the root is dropped).
func safeRootName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == '<' || r == '>' || r == '%' || r == '~' || r == '&' || r == ':':
			return '_'
		case unicode.IsControl(r), unicode.IsSpace(r) && r != ' ':
			return '_'
		}
		return r
	}, name)
}

// rootOf names the configured root that body, the text of one wiped run,
// starts under. It reads the text under both separator readings (a POSIX path
// with backslashes in it is read as written and as a Windows path would be, and
// both must agree, so "/srv/lib\..\..\Users" is not "under" /srv/lib) and
// matches a drive letter without regard to case.
func (r pathRoots) rootOf(body string) (name string, ok bool) {
	if len(r) == 0 {
		return "", false
	}
	win := isWinPath(body)
	clean := cleanPath(body, win)
	alt := ""
	if !win && strings.Contains(body, "\\") {
		alt = cleanPath(body, true)
	}
	for _, root := range r {
		if isWinPath(root.Path) != win {
			continue
		}
		rp := root.Path
		if win {
			rp = slashes(rp)
		}
		if _, under := cleanUnder(clean, rp); !under {
			continue
		}
		if alt != "" {
			if _, under := cleanUnder(alt, rp); !under {
				return "", false
			}
		}
		return root.Name, true
	}
	return "", false
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

// isWinPath reports a Windows path: a drive and a separator, or a UNC share.
func isWinPath(s string) bool {
	return winDrive(s, 0) || strings.HasPrefix(s, `\\`)
}

// pathEqual compares path text; a Windows path (a drive letter) ignores case.
func pathEqual(a, b string) bool {
	if winDrive(b, 0) {
		return strings.EqualFold(a, b)
	}
	return a == b
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

// winDrive reports "X:\" or "X:/" at s[i].
func winDrive(s string, i int) bool {
	if i+2 >= len(s) {
		return false
	}
	c := s[i]
	return (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') && s[i+1] == ':' && (s[i+2] == '\\' || s[i+2] == '/')
}
