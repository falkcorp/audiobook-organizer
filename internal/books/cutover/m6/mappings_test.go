// file: internal/books/cutover/m6/mappings_test.go
// version: 1.0.0
// guid: b3f0a6d2-71e4-49c8-8a15-d6e24c97b013
// last-edited: 2026-11-19

package m6_test

import (
	"encoding/json"
	"testing"
)

// TestItunesMappings_LongestRootAndWindowsFold: an iTunes path mapping lands on the
// root with the longest path containing its local side (library's Resolve), not
// on the first root that contains it, and a remote written as a Windows path
// (drive, UNC or file URL) is matched case-insensitively while a POSIX one is
// not.
func TestItunesMappings_LongestRootAndWindowsFold(t *testing.T) {
	const (
		wide   = "/batcave" // an import root that contains the library root itself
		nested = importTwo + "/Media"
	)
	type want struct {
		root string
		fold bool
	}
	cases := map[string]struct {
		to string
		want
	}{
		// The remote form decides the fold; the local side is the same each time.
		`C:`:                       {rootDir + "/a", want{"books-library", true}},
		`a:\Music`:                 {rootDir + "/b", want{"books-library", true}},
		`z:/Music`:                 {rootDir + "/c", want{"books-library", true}},
		`A:/Music`:                 {rootDir + "/d", want{"books-library", true}},
		`Z:/Music`:                 {rootDir + "/e", want{"books-library", true}},
		`\\gotham\media\Music`:     {rootDir + "/f", want{"books-library", true}},
		`file:///D:/Music`:         {rootDir + "/g", want{"books-library", true}},
		`file://localhost/E:/Muse`: {rootDir + "/h", want{"books-library", true}},
		`/Volumes/Music`:           {rootDir + "/i", want{"books-library", false}},
		`1:/Music`:                 {rootDir + "/j", want{"books-library", false}},
		`@:/Music`:                 {rootDir + "/k", want{"books-library", false}},
		"`:/Music":                 {rootDir + "/l", want{"books-library", false}},
		`[:/Music`:                 {rootDir + "/m", want{"books-library", false}},
		`{:/Music`:                 {rootDir + "/n", want{"books-library", false}},
		`ab:/Music`:                {rootDir + "/o", want{"books-library", false}},
		// Longest root wins, whatever the order the roots are planned in: the
		// nested import root beats the library root, and the library root beats the
		// wider import root that was planned after it.
		`/mnt/nested`: {nested, want{"import-2", false}},
		`/mnt/wide`:   {wide + "/other", want{"import-9", false}},
		`/mnt/lib`:    {rootDir + "/p", want{"books-library", false}},
	}
	r := newRig(t, withRows(headRows(t)), withBlob(func(tree map[string]any) {
		var ms []any
		for from, c := range cases {
			ms = append(ms, map[string]any{"from": from, "to": c.to})
		}
		tree["itunes"] = map[string]any{"path_mappings": ms}
	}), withRows(map[string][]byte{
		"import_path:9":            mustJSON(t, map[string]any{"id": 9, "path": wide, "name": "Wide", "enabled": true, "created_at": base}),
		"import_path:path:" + wide: []byte("9"),
	}))
	s, err := r.run(t)
	if err != nil {
		t.Fatalf("M6: %v\nlogs:\n%s", err, r.logs.String())
	}
	var maps []struct {
		Remote string `json:"remote"`
		Local  struct {
			Root string `json:"root"`
		} `json:"local"`
		Fold bool `json:"fold"`
	}
	if err := json.Unmarshal(s.Rows[string(ownerConfig)]["core.library.mappings"], &maps); err != nil {
		t.Fatal(err)
	}
	got := map[string]want{}
	for _, m := range maps {
		got[m.Remote] = want{m.Local.Root, m.Fold}
	}
	for from, c := range cases {
		g, ok := got[from]
		if !ok {
			t.Errorf("mapping %q was not written", from)
			continue
		}
		if g != c.want {
			t.Errorf("mapping %q = %+v, want %+v", from, g, c.want)
		}
	}
}

// TestRootsCutover_NothingToStageStagesNothing: a blob with no root, no mapping and
// no protected path stages none of the three config lists, so an install that
// never set them keeps the default and not an explicit empty list that would
// stop the default from applying.
func TestRootsCutover_NothingToStageStagesNothing(t *testing.T) {
	r := newRig(t, withBlob(func(tree map[string]any) {
		for _, k := range []string{"root_dir", "path_aliases", "protected_paths", "itunes"} {
			delete(tree, k)
		}
	}))
	s, err := r.run(t)
	if err != nil {
		t.Fatalf("M6: %v\nlogs:\n%s", err, r.logs.String())
	}
	for _, p := range []string{"core.library.roots", "core.library.mappings", "core.library.protected_paths"} {
		if v, ok := s.Rows[string(ownerConfig)][p]; ok {
			t.Errorf("%s was staged as %s with nothing to put in it", p, v)
		}
	}
}
