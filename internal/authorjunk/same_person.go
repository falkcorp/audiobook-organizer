// file: internal/authorjunk/same_person.go
// version: 1.0.0
// guid: c0f5485b-cc4e-48f6-b64d-fff72775e96e
// last-edited: 2026-09-29

package authorjunk

import "strings"

// SamePersonName reports whether two spellings name the same person: equal
// under FoldKey (case, diacritics and their Unicode form, punctuation and
// spacing folded: "J.R.R. Tolkien" / "J. R. R. Tolkien", "José" / "Jose"),
// with a single "Last, First" on either side also read as "First Last"
// ("Roiphe, Anne" / "Anne Roiphe"). Empty names match nothing.
//
// It is the one comparison for "is this folder the author's?": the metadata
// search's heading check and the transcribed-identity gate both ask it, and
// disagreeing there sent the same book down two paths.
func SamePersonName(a, b string) bool {
	ka, kb := personKeys(a), personKeys(b)
	for _, x := range ka {
		for _, y := range kb {
			if x == y {
				return true
			}
		}
	}
	return false
}

// personKeys is FoldKey(name), plus FoldKey of the "First Last" reading when
// name is exactly one "Last, First".
func personKeys(name string) []string {
	k := FoldKey(name)
	if k == "" {
		return nil
	}
	keys := []string{k}
	if last, first, ok := strings.Cut(name, ","); ok && !strings.Contains(first, ",") {
		if f := FoldKey(strings.TrimSpace(first) + " " + strings.TrimSpace(last)); f != "" && f != k {
			keys = append(keys, f)
		}
	}
	return keys
}
