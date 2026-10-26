// file: internal/search/per_user_shape_test.go
// version: 1.0.2
// guid: c7a2e5f8-31d6-4b94-8e07-6f1b3a9d5c42
// last-edited: 2026-10-26

package search

import (
	"errors"
	"testing"
	"time"
)

func TestCheckPerUserBoolean_RefusesTheShapesTheFlatPassGetsWrong(t *testing.T) {
	refused := []string{
		// a per-user term under an OR with a sibling, whatever the sibling is
		"read_status:finished OR title:Hush",
		"title:Hush OR read_status:finished",
		"read_status:finished OR read_status:unstarted",
		"title:Hush OR -read_status:finished",
		"title:Hush OR read_status:(finished|in_progress)",
		"title:Hush OR (title:Knightfall AND progress_pct:>10)",
		"title:Hush OR (title:Knightfall (title:Year OR last_played:*))",
		"author:kyle || progress_pct:>50",
		// a per-user term under a NOT over a group
		"-(read_status:finished title:Hush)",
		"NOT (read_status:finished OR title:Hush)",
		"NOT (title:Hush (title:Knightfall OR last_played:*))",
		"title:Hush -(progress_pct:>1 title:Knightfall)",
		"NOT NOT NOT (read_status:finished title:Hush)",
	}
	for _, q := range refused {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("fixture %q does not parse: %v", q, err)
		}
		if err := CheckPerUserBoolean(ast); !errors.Is(err, ErrPerUserBoolean) {
			t.Errorf("CheckPerUserBoolean(%q) = %v, want ErrPerUserBoolean", q, err)
		}
	}
}

func TestCheckPerUserBoolean_AcceptsTheShapesTheFlatPassGetsRight(t *testing.T) {
	accepted := []string{
		"read_status:finished",
		"-read_status:finished",
		"NOT read_status:unstarted",
		"NOT NOT read_status:finished",
		// two negations of a group cancel in the translator, so the group is plain
		"NOT NOT (read_status:finished title:Hush)",
		"-(-(read_status:finished title:Hush))",
		"read_status:finished title:Hush",
		"read_status:finished AND -progress_pct:>50 AND title:Hush",
		"(title:Hush OR title:Knightfall) read_status:finished",
		"read_status:(finished|in_progress)",
		"-read_status:(finished|in_progress)",
		"title:Hush -read_status:(finished|in_progress)",
		// no per-user term: the same shapes are plain boolean queries
		"title:Hush OR title:Knightfall",
		"-(title:Hush title:Knightfall)",
		"NOT (title:Hush OR author:kyle)",
		"date_added:>-30d OR title:Hush",
		"",
	}
	for _, q := range accepted {
		ast, err := ParseQuery(q)
		if err != nil {
			continue // an empty or unparseable query is refused elsewhere
		}
		if err := CheckPerUserBoolean(ast); err != nil {
			t.Errorf("CheckPerUserBoolean(%q) = %v, want nil", q, err)
		}
	}
	if err := CheckPerUserBoolean(nil); err != nil {
		t.Errorf("CheckPerUserBoolean(nil) = %v, want nil", err)
	}
}

// The check is only meaningful while it names the shapes the translator really
// flattens: a refused query must be one whose per-user filters the translator pulls
// out of the tree (otherwise the refusal is spurious), and a query it accepts must
// translate to exactly one flat AND list.
func TestCheckPerUserBoolean_RefusedQueriesAreTheOnesTranslatedWithPerUserFilters(t *testing.T) {
	for _, q := range []string{
		"read_status:finished OR title:Hush",
		"-(read_status:finished title:Hush)",
	} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		_, perUser, err := TranslateAt(ast, time.Now())
		if err != nil || len(perUser) == 0 {
			t.Errorf("%q: translate = %d per-user filters, err %v; the refusal guards a shape the translator does flatten", q, len(perUser), err)
		}
	}
}
