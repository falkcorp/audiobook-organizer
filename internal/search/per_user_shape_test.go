// file: internal/search/per_user_shape_test.go
// version: 2.0.0
// guid: c7a2e5f8-31d6-4b94-8e07-6f1b3a9d5c42
// last-edited: 2026-10-30

package search

import (
	"errors"
	"testing"
	"time"
)

// The queries whose per-user terms the flat AND list gets wrong are the ones that
// need the tree; everything else keeps the flat path.
func TestPerUserNeedsTree_NamesTheShapesTheFlatPassGetsWrong(t *testing.T) {
	tree := []string{
		// a per-user term under an OR with a sibling, whatever the sibling is
		"read_status:finished OR title:Hush",
		"title:Hush OR read_status:finished",
		"read_status:finished OR read_status:unstarted",
		"title:Hush OR -read_status:finished",
		"title:Hush OR read_status:(finished|in_progress)",
		"title:Hush OR (title:Knightfall AND progress_pct:>10)",
		"title:Hush OR (title:Knightfall (title:Year OR last_played:*))",
		"author:kyle || progress_pct:>50",
		"(title:Alpha OR read_status:finished) AND author:Wayne",
		// a per-user term under a NOT over a group
		"-(read_status:finished title:Hush)",
		"NOT (read_status:finished OR title:Hush)",
		"NOT (title:Hush (title:Knightfall OR last_played:*))",
		"title:Hush -(progress_pct:>1 title:Knightfall)",
		"NOT NOT NOT (read_status:finished title:Hush)",
	}
	for _, q := range tree {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("fixture %q does not parse: %v", q, err)
		}
		if !perUserNeedsTree(ast) {
			t.Errorf("perUserNeedsTree(%q) = false, want true", q)
		}
	}
}

func TestPerUserNeedsTree_LeavesTheShapesTheFlatPassGetsRightOnTheFlatPath(t *testing.T) {
	flat := []string{
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
	for _, q := range flat {
		ast, err := ParseQuery(q)
		if err != nil {
			continue // an empty or unparseable query is refused elsewhere
		}
		if perUserNeedsTree(ast) {
			t.Errorf("perUserNeedsTree(%q) = true, want false (the flat path answers it)", q)
		}
	}
	if perUserNeedsTree(nil) {
		t.Error("perUserNeedsTree(nil) = true, want false")
	}
}

// TranslateAt hands back a flat list that is ANDed; for a tree shape that is the
// wrong boolean, so it refuses instead of returning it, and TranslateQuery takes
// the viewer's rows route.
func TestTranslateAt_RefusesATreeShapeAndTranslateQueryRoutesIt(t *testing.T) {
	for _, q := range []string{
		"read_status:finished OR title:Hush",
		"-(read_status:finished title:Hush)",
	} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := TranslateAt(ast, time.Now()); !errors.Is(err, errPerUserNeedsRows) {
			t.Errorf("TranslateAt(%q) err = %v, want errPerUserNeedsRows", q, err)
		}
		tr, err := TranslateQuery(ast, time.Now())
		if err != nil {
			t.Fatalf("TranslateQuery(%q): %v", q, err)
		}
		if tr.Mixed == nil || tr.Query != nil || len(tr.PerUser) != 0 {
			t.Errorf("TranslateQuery(%q) = %+v, want Mixed only", q, tr)
		}
	}
	// the flat path is unchanged for a plain conjunction
	ast, _ := ParseQuery("read_status:finished title:Hush")
	tr, err := TranslateQuery(ast, time.Now())
	if err != nil || tr.Mixed != nil || tr.Query == nil || len(tr.PerUser) != 1 {
		t.Errorf("flat query: %+v, err %v", tr, err)
	}
}

// A term the translator refuses (a date without its sign) is refused at translation
// time on the tree route too, not at the first evaluation.
func TestTranslateQuery_TreeRouteRefusesAMalformedDateUpFront(t *testing.T) {
	ast, err := ParseQuery("title:Hush OR last_played:>30d")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TranslateQuery(ast, time.Now()); err == nil {
		t.Error("TranslateQuery accepted last_played:>30d under an OR")
	}
}
