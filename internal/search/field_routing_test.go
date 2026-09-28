// file: internal/search/field_routing_test.go
// version: 1.0.0
// guid: 8d1f3a6c-5e27-4b90-9c14-2a7e6b0d4f83
// last-edited: 2026-09-27

package search

import (
	"encoding/json"
	"strings"
	"testing"
)

// duration: used to go to Bleve under its own name, which is not indexed
// (the document holds duration_seconds), so every comparison answered 0.
func TestTranslate_DurationTargetsIndexedSecondsWithUnits(t *testing.T) {
	cases := map[string][]string{
		"duration:>20m":         {`"field":"duration_seconds"`, `"min":1200`},
		"duration:<1h30m":       {`"field":"duration_seconds"`, `"max":5400`},
		"duration:>=1200":       {`"min":1200`, `"inclusive_min":true`},
		"duration:[10m TO 2h]":  {`"min":600`, `"max":7200`},
		"duration_seconds:<90s": {`"max":90`},
	}
	for q, wants := range cases {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("%s: parse: %v", q, err)
		}
		bq, _, err := Translate(ast)
		if err != nil {
			t.Fatalf("%s: translate: %v", q, err)
		}
		raw, _ := json.Marshal(bq)
		for _, w := range wants {
			if !strings.Contains(string(raw), w) {
				t.Errorf("%s: want %s in %s", q, w, raw)
			}
		}
	}
	ast, _ := ParseQuery("duration:>soon")
	if _, _, err := Translate(ast); err == nil {
		t.Error("an unparseable duration must be an error, not a silent 0")
	}
}

func TestIsIndexedField(t *testing.T) {
	for _, f := range []string{"title", "duration", "duration_seconds", "library_state", "has_cover", "read_status"} {
		if !IsIndexedField(f) {
			t.Errorf("%s should be indexed", f)
		}
	}
	for _, f := range []string{"review", "metadata", "has_duration", "has_written", "metadata_review_status"} {
		if IsIndexedField(f) {
			t.Errorf("%s is not in the Bleve document", f)
		}
	}
	ast, _ := ParseQuery(`foo -review:matched (author:x OR narrator:(a|b))`)
	got := strings.Join(FieldNames(ast), ",")
	if got != "review,author,narrator" {
		t.Errorf("FieldNames = %s", got)
	}
}
