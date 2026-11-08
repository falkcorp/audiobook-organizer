// file: internal/search/tag_translate_at_test.go
// version: 1.0.0
// guid: 5c1d7e42-9b3a-4f68-a0d1-2e8f6b49c7a3
// last-edited: 2026-11-08

package search

import (
	"testing"
	"time"
)

// TestTranslateAt_RefusesTagTermsDeterministically pins the behaviour behind the
// intermittent TestProp_TranslateValidQueries failure: TranslateAt is handed a
// tag: term only when the caller did not split it off, and then it always refuses
// it, for any value (including a bare number), top level or not. TranslateQuery
// splits the same term off and accepts it.
func TestTranslateAt_RefusesTagTermsDeterministically(t *testing.T) {
	for _, q := range []string{"tag:0", "tag:1", "tag:00", "tag:a", `tag:"0"`, `tag:"sci fi"`} {
		t.Run(q, func(t *testing.T) {
			ast, err := ParseQuery(q)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", q, err)
			}
			if _, _, err := TranslateAt(ast, time.Now()); !IsTagTermError(err) {
				t.Fatalf("TranslateAt(%q) err = %v, want a TagTermError", q, err)
			}
			tr, err := TranslateQuery(ast, time.Now())
			if err != nil {
				t.Fatalf("TranslateQuery(%q): %v", q, err)
			}
			if len(tr.Tags) != 1 {
				t.Fatalf("TranslateQuery(%q) tags = %v, want one", q, tr.Tags)
			}
		})
	}
}
