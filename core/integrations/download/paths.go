// file: core/integrations/download/paths.go
// version: 1.0.0
// guid: 0d2b7e94-3c58-4a16-9e0f-b41c7a8d5f23
// last-edited: 2026-11-11

package download

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/integrations"
)

// Path mapping (05-download §4.4): what a download client reports is a path on
// ITS host, and this host sees the same bytes under another path. A Mapping
// says how the two correspond; the Pool applies them, longest prefix first,
// on a separator boundary, never by string replacement in the middle of a
// segment.
//
// 05-download §4.4 has the Pool apply library.PathMapper through Env.Paths.
// Neither exists yet: both land with F7-06 (Env.Paths is omitted by F7-01 for
// that reason). Until then the Pool maps through this package's PathMapper
// and Mappings, which carry the same semantics (longest prefix, separator
// boundary, Fold for Windows remotes, no mapping means unmapped), and F7-09
// feeds Mappings from core.library.mappings (Source "download:<instance>")
// or swaps the PathMapper for library's. This is an F7-04 seam recorded in
// seams.d/F7-04.md.

// LocalPath is a path on this host: an item's RemotePath mapped through a
// Mapping. The zero value means "unmapped". It is its own type so that a
// path the client reports and a path this host can open are never mixed up;
// F7-06 or F7-09 re-types it as library.Path.
type LocalPath string

// ErrUnmapped says no Mapping covers a path. The Pool leaves LocalPath zero
// for an unmapped item and refuses an unmapped Relocate.
var ErrUnmapped = errors.New("download: path matches no mapping")

// Mapping is one row of core.library.mappings with Source "download:<id>"
// (05-library §5L.3.6, 05-download §4.4).
type Mapping struct {
	// Remote is the prefix as the client writes it: "/downloads/complete", a
	// Windows drive form "D:\dl" or "D:/dl", or a file URL of either
	// ("file:///D:/dl"). A UNC path is not supported and never matches.
	Remote string
	// Local is the absolute host directory Remote corresponds to.
	Local LocalPath
	// Fold matches Remote case-insensitively (Windows remotes only).
	Fold bool
}

// Source is the mapping source of a download instance: "download:<id>".
func Source(id integrations.InstanceID) string { return "download:" + string(id) }

// PathMapper maps between a client's paths and this host's. source is
// Source(instance). The Pool depends on this interface only.
type PathMapper interface {
	// ToLocal maps a path as source's client reports it to this host. A path
	// no mapping covers is ErrUnmapped.
	ToLocal(source, remote string) (LocalPath, error)
	// ToRemote maps a host path to the path the client of source sees. A path
	// no mapping covers is ErrUnmapped.
	ToRemote(source string, local LocalPath) (string, error)
}

// Mappings returns the mappings of one source. It is read on every call, so a
// live configuration source takes effect without a rebuild.
type Mappings func(source string) []Mapping

// NewMapper is the PathMapper over m: longest prefix on a separator boundary,
// byte-exact except for a Mapping's Fold.
func NewMapper(m Mappings) PathMapper { return mapper{m} }

type mapper struct{ mappings Mappings }

// remotePath is a client path split into segments.
type remotePath struct {
	windows bool     // a drive-letter path: its separators are backslash or slash
	segs    []string // no empty, "." or ".." segment; segs[0] is "D:" for a drive path
}

// parseRemote splits a client path. It refuses (false) a relative path, a UNC
// path, a file URL with a host, a segment that decodes to one holding a
// separator, and any ".." segment: a traversal must never be mapped.
func parseRemote(s string) (remotePath, bool) {
	if s == "" {
		return remotePath{}, false
	}
	if len(s) >= 7 && strings.EqualFold(s[:7], "file://") {
		rest := s[7:]
		switch {
		case strings.HasPrefix(rest, "/"):
		case len(rest) >= 10 && strings.EqualFold(rest[:10], "localhost/"):
			rest = rest[9:]
		default:
			return remotePath{}, false
		}
		parts := strings.Split(rest, "/")
		for i, p := range parts {
			d, err := url.PathUnescape(p)
			if err != nil || strings.ContainsAny(d, `/\`) {
				return remotePath{}, false
			}
			parts[i] = d
		}
		rest = strings.Join(parts, "/")
		if len(rest) >= 3 && rest[0] == '/' && isDriveLetter(rest[1]) && rest[2] == ':' {
			rest = rest[1:] // "/D:/dl": a drive path as a URL spells it
		}
		s = rest
	}
	win := isWindowsPath(s)
	if win {
		s = strings.ReplaceAll(s, `\`, "/")
	}
	switch {
	case !win && !strings.HasPrefix(s, "/"):
		return remotePath{}, false
	case strings.HasPrefix(s, "//") || strings.HasPrefix(s, `\\`):
		return remotePath{}, false // UNC
	}
	var segs []string
	for seg := range strings.SplitSeq(s, "/") {
		switch seg {
		case "", ".":
		case "..":
			return remotePath{}, false
		default:
			if strings.ContainsRune(seg, 0) {
				return remotePath{}, false
			}
			segs = append(segs, seg)
		}
	}
	return remotePath{windows: win, segs: segs}, true
}

func isDriveLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// isWindowsPath reports a drive-letter path ("D:", "D:\x", "D:/x"). A path
// that starts with "/" is POSIX and its backslashes are ordinary characters.
func isWindowsPath(s string) bool {
	return len(s) >= 2 && isDriveLetter(s[0]) && s[1] == ':' && (len(s) == 2 || s[2] == '\\' || s[2] == '/')
}

// String renders p in the client's own style: backslashes for a drive path,
// slashes otherwise.
func (p remotePath) String() string {
	if p.windows {
		if len(p.segs) == 1 {
			return p.segs[0] + `\` // "D:" alone is the drive's current directory
		}
		return strings.Join(p.segs, `\`)
	}
	return "/" + strings.Join(p.segs, "/")
}

// hasPrefix reports whether prefix's segments start p's, and how many that is.
func (p remotePath) hasPrefix(prefix remotePath, fold bool) bool {
	if p.windows != prefix.windows || len(prefix.segs) > len(p.segs) {
		return false
	}
	for i, seg := range prefix.segs {
		if seg == p.segs[i] || fold && strings.EqualFold(seg, p.segs[i]) {
			continue
		}
		return false
	}
	return true
}

// ToLocal implements PathMapper.
func (m mapper) ToLocal(source, remote string) (LocalPath, error) {
	p, ok := parseRemote(remote)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnmapped, remote)
	}
	best, bestLen := -1, -1
	var bestRoot remotePath
	rows := m.mappings(source)
	for i, row := range rows {
		root, ok := parseRemote(row.Remote)
		if !ok || !validLocal(row.Local) || !p.hasPrefix(root, row.Fold) {
			continue
		}
		if len(root.segs) > bestLen {
			best, bestLen, bestRoot = i, len(root.segs), root
		}
	}
	if best < 0 {
		return "", fmt.Errorf("%w: %q", ErrUnmapped, remote)
	}
	rest := p.segs[len(bestRoot.segs):]
	parts := append([]string{string(rows[best].Local)}, rest...)
	return LocalPath(filepath.Join(parts...)), nil
}

// validLocal reports a usable Mapping.Local: an absolute host path.
func validLocal(l LocalPath) bool { return l != "" && filepath.IsAbs(string(l)) }

// ToRemote implements PathMapper.
func (m mapper) ToRemote(source string, local LocalPath) (string, error) {
	if !filepath.IsAbs(string(local)) {
		return "", fmt.Errorf("%w: %q is not an absolute host path", ErrUnmapped, string(local))
	}
	lp := localSegments(string(local))
	rows := m.mappings(source)
	best, bestLen := -1, -1
	for i, row := range rows {
		if !validLocal(row.Local) {
			continue
		}
		if _, ok := parseRemote(row.Remote); !ok {
			continue
		}
		root := localSegments(string(row.Local))
		if len(root) <= len(lp) && slices.Equal(root, lp[:len(root)]) && len(root) > bestLen {
			best, bestLen = i, len(root)
		}
	}
	if best < 0 {
		return "", fmt.Errorf("%w: no mapping covers %s", ErrUnmapped, string(local))
	}
	remote, _ := parseRemote(rows[best].Remote)
	remote.segs = append(slices.Clone(remote.segs), lp[bestLen:]...)
	return remote.String(), nil
}

// localSegments splits an absolute host path, cleaned, into its volume and
// segments, so a prefix compare is on separator boundaries.
func localSegments(p string) []string {
	p = filepath.Clean(p)
	vol := filepath.VolumeName(p)
	p = strings.TrimPrefix(p, vol)
	var segs []string
	if vol != "" {
		segs = append(segs, vol)
	}
	for seg := range strings.SplitSeq(filepath.ToSlash(p), "/") {
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	return segs
}
