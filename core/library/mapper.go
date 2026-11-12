// file: core/library/mapper.go
// version: 1.0.0
// guid: e4015a7b-f0d6-4365-8e34-9464ec834d26
// last-edited: 2026-11-11

package library

import (
	"fmt"
	"net/url"
	"strings"
)

// remoteKind is the syntax family of a remote path.
type remoteKind uint8

const (
	kindRelative remoteKind = iota // not absolute: never matches a mapping
	kindPosix                      // "/dl/complete"
	kindDrive                      // `C:\dir`, "C:/dir", "file:///C:/dir"
	kindUNC                        // `\\server\share\x`, "file://server/share/x"
)

// remote is a remote path split into segments. For kindDrive segs[0] is the
// drive ("C:"); for kindUNC segs[0] and segs[1] are the server and share.
type remote struct {
	kind remoteKind
	segs []string
	url  bool // written as a file URL
	back bool // a Windows form written with backslashes
}

// mapEntry is one Mapping with its Remote parsed once, at construction. ok is
// false for a Remote that cannot match (unparsable or relative); such an entry
// is kept for Mappings but never used for matching.
type mapEntry struct {
	m  Mapping
	r  remote
	ok bool
}

// pathMapper is the immutable PathMapper. It is safe for concurrent use
// because nothing mutates it after newPathMapper returns.
type pathMapper struct {
	bySource map[string][]mapEntry
	resolve  func(abs string) (Path, error)
}

// newPathMapper builds an immutable mapper. resolve maps an absolute host path
// to the Path of the most specific root containing it (the manager's Resolve);
// it may be nil, then a remote path with no mapping is ErrUnmapped.
func newPathMapper(mappings []Mapping, resolve func(abs string) (Path, error)) *pathMapper {
	pm := &pathMapper{bySource: make(map[string][]mapEntry), resolve: resolve}
	for _, m := range mappings {
		e := mapEntry{m: m}
		if r, err := parseRemote(m.Remote); err == nil && r.kind != kindRelative {
			e.r, e.ok = r, true
		}
		pm.bySource[m.Source] = append(pm.bySource[m.Source], e)
	}
	return pm
}

// isDriveSpec reports whether s begins with a drive letter and colon.
func isDriveSpec(s string) bool {
	return len(s) >= 2 && s[1] == ':' && (s[0]|0x20) >= 'a' && (s[0]|0x20) <= 'z'
}

// hasDrivePrefix reports whether s is a drive form: "C:" followed by the end
// or a separator.
func hasDrivePrefix(s string) bool {
	return isDriveSpec(s) && (len(s) == 2 || s[2] == '/' || s[2] == '\\')
}

// splitSegs splits a '/'-separated path (without its leading separator) into
// segments, dropping one trailing empty segment (a trailing separator).
func splitSegs(s string) []string {
	if s == "" {
		return nil
	}
	segs := strings.Split(s, "/")
	if segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	return segs
}

// checkSegs refuses what must never be matched or cleaned silently: empty,
// "." and ".." segments and NUL.
func checkSegs(segs []string) error {
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.IndexByte(s, 0) >= 0 {
			return fmt.Errorf("%w: segment %q", ErrBadRel, s)
		}
	}
	return nil
}

// parseRemote normalises a remote path. File URLs lose their scheme and
// authority (localhost and empty authorities are dropped; any other authority
// becomes the UNC server, per RFC 8089 appendix E.3.1, so two hosts' paths
// never collapse into one) and are percent-decoded PER SEGMENT after splitting
// on '/'. A decoded segment that contains '/' (an encoded %2F), or '\' in a
// Windows form, is REFUSED rather than kept escaped or turned into a
// separator. Backslashes become separators only for Windows forms; a POSIX
// remote keeps them as filename bytes.
func parseRemote(s string) (remote, error) {
	if len(s) >= 7 && strings.EqualFold(s[:7], "file://") {
		return parseFileURL(s[7:])
	}
	switch {
	case hasDrivePrefix(s):
		r := remote{kind: kindDrive, back: strings.Contains(s, "\\")}
		r.segs = splitSegs(strings.ReplaceAll(s, "\\", "/"))
		return r, checkSegs(r.segs)
	case strings.HasPrefix(s, "\\\\"):
		r := remote{kind: kindUNC, back: true}
		r.segs = splitSegs(strings.ReplaceAll(s[2:], "\\", "/"))
		return r, checkSegs(r.segs)
	case strings.HasPrefix(s, "/"):
		r := remote{kind: kindPosix, segs: splitSegs(s[1:])}
		return r, checkSegs(r.segs)
	}
	return remote{kind: kindRelative}, nil
}

// parseFileURL parses what follows "file://": an authority, then a path.
func parseFileURL(rest string) (remote, error) {
	auth, path := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		auth, path = rest[:i], rest[i:]
	}
	if path == "" {
		return remote{}, fmt.Errorf("%w: file URL has no path", ErrBadRel)
	}
	segs := splitSegs(path[1:])
	if auth != "" {
		segs = append([]string{auth}, segs...)
	}
	for i, s := range segs {
		d, err := url.PathUnescape(s)
		if err != nil {
			return remote{}, fmt.Errorf("%w: bad percent escape in %q", ErrBadRel, s)
		}
		if strings.Contains(d, "/") {
			return remote{}, fmt.Errorf("%w: encoded separator in %q", ErrBadRel, s)
		}
		segs[i] = d
	}
	r := remote{url: true, segs: segs}
	switch {
	case auth != "" && !strings.EqualFold(auth, "localhost"):
		r.kind = kindUNC
	case auth != "" && len(segs) > 1 && isDriveSpec(segs[1]) && len(segs[1]) == 2:
		r.kind, r.segs = kindDrive, segs[1:]
	case auth == "" && len(segs) > 0 && isDriveSpec(segs[0]) && len(segs[0]) == 2:
		r.kind = kindDrive
	case auth != "":
		r.kind, r.segs = kindPosix, segs[1:]
	default:
		r.kind = kindPosix
	}
	if r.kind != kindPosix {
		for _, s := range r.segs {
			if strings.Contains(s, "\\") {
				return remote{}, fmt.Errorf("%w: backslash in %q", ErrBadRel, s)
			}
		}
	}
	return r, checkSegs(r.segs)
}

// asciiEqualFold compares a and b folding only ASCII letters; bytes >= 0x80
// compare exactly, so no Unicode case mapping or normalisation happens.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// prefixOf reports whether e's Remote is a segment-boundary prefix of r. Only
// Windows forms of a Fold mapping match case-insensitively.
func (e mapEntry) prefixOf(r remote) bool {
	if !e.ok || e.r.kind != r.kind || len(e.r.segs) > len(r.segs) {
		return false
	}
	fold := e.m.Fold && r.kind != kindPosix
	for i, s := range e.r.segs {
		if s != r.segs[i] && !(fold && asciiEqualFold(s, r.segs[i])) {
			return false
		}
	}
	return true
}

// ToLocal implements PathMapper.
func (pm *pathMapper) ToLocal(source, rem string) (Path, error) {
	r, err := parseRemote(rem)
	if err != nil {
		return Path{}, fmt.Errorf("library: remote %q: %w", rem, err)
	}
	var best *mapEntry
	entries := pm.bySource[source]
	for i := range entries {
		e := &entries[i]
		if e.prefixOf(r) && (best == nil || len(e.r.segs) > len(best.r.segs)) {
			best = e
		}
	}
	if best == nil {
		return pm.fallback(rem, r)
	}
	return joinLocal(best.m.Local, r.segs[len(best.r.segs):])
}

// joinLocal appends the remainder segments to base without cleaning: the
// result must pass ValidateRel.
func joinLocal(base Path, rest []string) (Path, error) {
	if err := base.Validate(); err != nil {
		return Path{}, err
	}
	if len(rest) == 0 {
		return base, nil
	}
	rel := strings.Join(rest, "/")
	if base.Rel != "" {
		rel = base.Rel + "/" + rel
	}
	if err := ValidateRel(rel); err != nil {
		return Path{}, err
	}
	return Path{Root: base.Root, Rel: rel}, nil
}

// fallback resolves an absolute host path that no mapping covers by asking
// the manager. Anything else is ErrUnmapped: a guessed Path puts an import in
// the wrong place.
func (pm *pathMapper) fallback(rem string, r remote) (Path, error) {
	if pm.resolve == nil || r.kind != kindPosix || r.url {
		return Path{}, fmt.Errorf("%w: %q", ErrUnmapped, rem)
	}
	p, err := pm.resolve(rem)
	if err != nil {
		return Path{}, fmt.Errorf("%w: %q: %w", ErrUnmapped, rem, err)
	}
	if err := p.Validate(); err != nil {
		return Path{}, fmt.Errorf("library: resolved %q: %w", rem, err)
	}
	return p, nil
}

// underRel reports whether rel is prefix or under it on a separator boundary;
// the empty prefix covers everything.
func underRel(prefix, rel string) bool {
	return prefix == "" || rel == prefix || strings.HasPrefix(rel, prefix+"/")
}

// ToRemote implements PathMapper.
func (pm *pathMapper) ToRemote(source string, p Path) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	var best *mapEntry
	entries := pm.bySource[source]
	for i := range entries {
		e := &entries[i]
		if e.ok && e.m.Local.Root == p.Root && underRel(e.m.Local.Rel, p.Rel) &&
			(best == nil || len(e.m.Local.Rel) > len(best.m.Local.Rel)) {
			best = e
		}
	}
	if best == nil {
		return "", fmt.Errorf("%w: %s", ErrUnmapped, p)
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(p.Rel, best.m.Local.Rel), "/")
	return joinRemote(*best, rest), nil
}

// joinRemote appends rest to the mapping's Remote in the Remote's own style.
func joinRemote(e mapEntry, rest string) string {
	if rest == "" {
		return e.m.Remote
	}
	segs := strings.Split(rest, "/")
	switch {
	case e.r.url:
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		return trimURLPath(e.m.Remote) + "/" + strings.Join(segs, "/")
	case e.r.back:
		return strings.TrimRight(e.m.Remote, "\\/") + "\\" + strings.Join(segs, "\\")
	}
	return strings.TrimRight(e.m.Remote, "/") + "/" + strings.Join(segs, "/")
}

// trimURLPath drops trailing '/' from a file URL's path, never eating into
// the scheme and authority.
func trimURLPath(u string) string {
	rest := u[len("file://"):]
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return u
	}
	return u[:len("file://")+i] + strings.TrimRight(rest[i:], "/")
}

// Mappings implements PathMapper: a copy of the Source's mappings in input
// order, nil when there are none.
func (pm *pathMapper) Mappings(source string) []Mapping {
	entries := pm.bySource[source]
	if len(entries) == 0 {
		return nil
	}
	out := make([]Mapping, len(entries))
	for i, e := range entries {
		out[i] = e.m
	}
	return out
}

var _ PathMapper = (*pathMapper)(nil)
