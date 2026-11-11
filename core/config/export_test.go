// file: core/config/export_test.go
// version: 1.0.2
// guid: d2bf2cbd-335b-424b-854c-53a0c4529b22
// last-edited: 2026-11-10

package config

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// TestSource is an operator layer for tests: the file and env grammar is
// F3-02's, so tests hand values in canonical paths through the seam it
// fills. By names a value's origin ("file gotham.yaml", "env GOTHAM_…").
type TestSource struct {
	Vals       map[Path]json.RawMessage
	By         func(Path) string
	Unfiltered bool
}

// read returns the values q can place, as F3-02's sources do: a registered
// struct key, an entry under a map section, or a passthrough path. A path no
// registered section owns yet is skipped (a module key at Boot, before its
// section registers) unless Unfiltered, which hands it to the store to refuse,
// or unless q.want names it.
func (t *TestSource) read(q sourceQuery) ([]sourced, error) {
	out := make([]sourced, 0, len(t.Vals))
	for _, p := range slices.Sorted(maps.Keys(t.Vals)) {
		// A wanted path (Store.Operator) is returned even when no section
		// owns it yet: step 0 reads core.paths.state_dir before core.paths
		// registers.
		switch {
		case q.want != nil:
			if !q.want[p] {
				continue
			}
		case !t.Unfiltered && !placeable(q, p):
			continue
		}
		out = append(out, sourced{path: p, raw: t.Vals[p], by: t.By(p)})
	}
	return out, nil
}

func placeable(q sourceQuery, p Path) bool {
	if slices.Contains(q.paths, p) || q.passthrough(p) || q.entrySecret(p) {
		return true
	}
	for _, m := range q.maps {
		if strings.HasPrefix(string(p), string(m)+".") {
			return true
		}
	}
	return false
}

func (t *TestSource) holds(_ sourceQuery, drop func(Path) bool) (bool, error) {
	for p := range t.Vals {
		if drop(p) {
			return true, nil
		}
	}
	return false, nil
}

func (t *TestSource) redacted(_ sourceQuery, drop func(Path) bool) ([]byte, error) {
	kept := map[Path]json.RawMessage{}
	for p, v := range t.Vals {
		if !drop(p) {
			kept[p] = v
		}
	}
	return json.Marshal(kept)
}

// SetOperatorSources installs file and env as s's operator layers (either
// may be nil).
func SetOperatorSources(s *Store, file, env *TestSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if file != nil {
		s.file = file
	} else {
		s.file = nil
	}
	if env != nil {
		s.env = env
	} else {
		s.env = nil
	}
}

// SetBootRecordRenameHook runs fn between the boot record's temp-file fsync
// and its rename (a crash there must keep the previous record).
func SetBootRecordRenameHook(s *Store, fn func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bootIO.renameHook = fn
}
