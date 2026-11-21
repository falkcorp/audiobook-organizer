// file: internal/books/cutover/passthrough.go
// version: 1.0.0
// guid: 6c1e4b97-2d83-4a05-9f7e-b3a8d05c1e42
// last-edited: 2026-11-19

package cutover

import (
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/config"
)

// ConfigPassthrough is the predicate app.Options.ConfigPassthrough takes (F3-07
// slice e): it accepts exactly the integrations.* paths the rename table sends a
// retired name to as a passthrough value or a passthrough secret, and the paths
// ConfigEnvBindings binds an env name onto. Core config owns no section for the
// integrations subtree (the integrations store is F7-08), so it places an env
// binding or a file value there only when this accepts the path; a path outside
// the table is not passthrough and boot still refuses it as an unknown key.
//
// A table path that names a family (`integrations.metadata.<source id>.credentials.<k>`,
// `integrations.notify.webhook-<n>.secret`) matches any path with the same
// segments where each `<..>` placeholder stands for one non-empty segment (or
// the non-empty tail of a segment after its literal prefix).
func ConfigPassthrough(p config.Path) bool {
	ptOnce.Do(buildPassthrough)
	s := string(p)
	if _, ok := ptExact[s]; ok {
		return true
	}
	segs := strings.Split(s, ".")
	for _, pat := range ptPatterns {
		if matchSegments(pat, segs) {
			return true
		}
	}
	return false
}

var (
	ptOnce     sync.Once
	ptExact    map[string]struct{}
	ptPatterns [][]string
)

func buildPassthrough() {
	ptExact = map[string]struct{}{}
	add := func(path string) {
		if !strings.HasPrefix(path, "integrations.") {
			return
		}
		if strings.Contains(path, "<") {
			ptPatterns = append(ptPatterns, strings.Split(path, "."))
			return
		}
		ptExact[path] = struct{}{}
	}
	for _, r := range Table().Rows {
		for _, t := range r.Targets {
			if t.Kind == TargetPassthrough || (t.Kind == TargetSecret && t.Passthrough) {
				add(t.Path)
			}
		}
	}
	for _, b := range ConfigEnvBindings() {
		add(string(b.Path))
	}
}

// matchSegments reports whether segs fits pat, segment by segment.
func matchSegments(pat, segs []string) bool {
	if len(pat) != len(segs) {
		return false
	}
	for i, ps := range pat {
		switch lit, _, hole := strings.Cut(ps, "<"); {
		case !hole:
			if ps != segs[i] {
				return false
			}
		case !strings.HasPrefix(segs[i], lit) || len(segs[i]) == len(lit):
			return false
		}
	}
	return true
}
