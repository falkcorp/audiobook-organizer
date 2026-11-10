// file: core/config/path.go
// version: 1.0.0
// guid: 47cb7370-51cc-4bc5-8993-13977e2120c1
// last-edited: 2026-11-10

package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Path is a dotted key path: "core.log.level", "modules.books.scan.on_startup".
// Grammar: seg = [a-z][a-z0-9_]{0,47}; path = seg ("." seg)*; the first segment
// is "core" or "modules". The keys of a map-typed value are data, not path
// segments: they follow the map's own key type (a task kind such as
// "books.dedup.full-scan" inside the map key "core.tasks.kinds"), and env, file
// and patches set the whole map as one JSON value (05-config §1.3.6). Config
// never encodes '.' or '-' into a segment (COR3-11).
type Path string

const pathSep = "."

// segmentRE is the grammar of one path segment (05-config §1.3.6).
var segmentRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// Validate reports whether p is a path of the grammar above. It names the
// first offending segment.
func (p Path) Validate() error {
	if p == "" {
		return fmt.Errorf("config: path is empty")
	}
	segs := strings.Split(string(p), pathSep)
	if segs[0] != "core" && segs[0] != "modules" {
		return fmt.Errorf("config: path %q: first segment %q must be \"core\" or \"modules\"", string(p), segs[0])
	}
	for _, seg := range segs {
		if !segmentRE.MatchString(seg) {
			return fmt.Errorf("config: path %q: segment %q does not match [a-z][a-z0-9_]{0,47}", string(p), seg)
		}
	}
	return nil
}

// Parent returns p without its last segment, or "" when p has one segment.
func (p Path) Parent() Path {
	i := strings.LastIndex(string(p), pathSep)
	if i < 0 {
		return ""
	}
	return p[:i]
}

// Join returns p extended by seg. It does not validate: a caller that builds
// a path validates the result. Joining onto the empty path yields seg.
func (p Path) Join(seg string) Path {
	if p == "" {
		return Path(seg)
	}
	return p + pathSep + Path(seg)
}
