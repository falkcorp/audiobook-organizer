// file: internal/search/field_routing_test.go
// version: 1.3.0
// guid: 8d1f3a6c-5e27-4b90-9c14-2a7e6b0d4f83
// last-edited: 2026-10-25

package search

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// The collections handler refuses a per-user field from anyone but the
// collection's evaluator, and it decides with this helper. It must read the same
// set the translator routes on and see through every shape a term can take.
func TestQueryUsesPerUserField(t *testing.T) {
	yes := []string{
		"read_status:finished",
		"progress_pct:>5",
		"last_played:>-30d",
		"last_played:*",
		"-read_status:finished",
		"NOT read_status:unstarted",
		"title:Alpha AND read_status:in_progress",
		"title:Alpha OR (title:Bravo AND progress_pct:>=50)",
		"read_status:(finished|in_progress)",
		"((title:x))  last_played:>2026-01-01",
	}
	no := []string{
		"",
		"title:Alpha",
		"date_added:>-30d",
		"date_modified:<2026-01-01",
		"author:read_status",
		"\"read_status:finished\"",
		"title:(unclosed",
		"read_status",
	}
	for _, q := range yes {
		if !QueryUsesPerUserField(q) {
			t.Errorf("QueryUsesPerUserField(%q) = false, want true", q)
		}
	}
	for _, q := range no {
		if QueryUsesPerUserField(q) {
			t.Errorf("QueryUsesPerUserField(%q) = true, want false", q)
		}
	}
}

func TestIsPerUserField_MatchesTheRoutingSet(t *testing.T) {
	for _, n := range []string{"last_played", "progress_pct", "read_status"} {
		if !IsPerUserField(n) {
			t.Errorf("IsPerUserField(%q) = false for a per-user field", n)
		}
	}
	for _, n := range []string{"title", "date_added", "duration", ""} {
		if IsPerUserField(n) {
			t.Errorf("IsPerUserField(%q) = true, want false", n)
		}
	}
}

// QueryMayUsePerUserField is the fail-closed twin of QueryUsesPerUserField: it
// agrees with it on every query that parses, and answers true for text that does
// not parse, because that text cannot be shown to reference no per-user field.
func TestQueryMayUsePerUserField_FailsClosedOnUnparseableText(t *testing.T) {
	unparseable := []string{
		`read_status:finished "unterminated`,
		`title:"unterminated`,
		`read_status:(finished`,
	}
	for _, q := range unparseable {
		if _, err := ParseQuery(q); err == nil {
			t.Fatalf("fixture %q parses; the test needs text that does not", q)
		}
		if QueryUsesPerUserField(q) {
			t.Errorf("QueryUsesPerUserField(%q) = true; it is the evaluation question and says false for text that does not parse", q)
		}
		if !QueryMayUsePerUserField(q) {
			t.Errorf("QueryMayUsePerUserField(%q) = false, want true (fail closed)", q)
		}
	}
	for _, q := range []string{"read_status:finished", "-progress_pct:>10", "author:x (last_played:>-7d | title:y)", "author:kyle", "title:foo bar"} {
		if got, want := QueryMayUsePerUserField(q), QueryUsesPerUserField(q); got != want {
			t.Errorf("%q: QueryMayUsePerUserField = %v, QueryUsesPerUserField = %v; they must agree on text that parses", q, got, want)
		}
	}
}

// The gate is only as good as its agreement with the evaluator: a query the
// translator routes to the per-user pass (and so evaluates as the owner) must be
// one QueryUsesPerUserField reports, in every spelling the parser accepts. This
// is the differential check: for each query that parses, the translator's own
// per-user routing and the gate must give the same answer. A query that does not
// parse (or translate) is refused by EvaluateSmartPlaylist, so it never reads
// anyone's state whatever the gate says.
func TestQueryUsesPerUserField_AgreesWithTranslatorRouting(t *testing.T) {
	queries := []string{
		"read_status:finished", "READ_STATUS:finished", "Read_Status:finished",
		"read_status:\"finished\"", "read_status:'finished'", "read_status:Finished",
		"progress_pct:>5", "progress_pct:[10 TO 90]", "progress_pct:10..90", "PROGRESS_PCT:>5",
		"last_played:*", "last_played:>-30d", "last_played:-30d..now", "Last_Played:>2026-01-01",
		"-read_status:finished", "NOT read_status:finished", "NOT (read_status:finished)",
		"title:x AND (author:y OR -read_status:finished)",
		"read_status:(finished|in_progress)", "read_status:/fin.*/", "read_status:fin*",
		"title:x read_status:finished", "(((read_status:finished)))",
		"added:>-30d", "modified:<2026-01-01", "date_added:>-30d", "duration:>1h",
		"title:read_status", "read_status", "title:\"read_status:finished\"",
		"readstatus:finished", "read-status:finished", "read_status :finished", "read_status: finished",
		"user.read_status:finished", "per_user:read_status", "state:finished",
	}
	routed := 0
	for _, q := range queries {
		ast, err := ParseQuery(q)
		if err != nil {
			continue // refused by the evaluator before any state is read
		}
		_, perUser, err := Translate(ast)
		if err != nil {
			continue // likewise refused at translation
		}
		if got, want := QueryUsesPerUserField(q), len(perUser) > 0; got != want {
			t.Errorf("%q: QueryUsesPerUserField = %v but the translator routes %d per-user filter(s)", q, got, len(perUser))
		}
		if len(perUser) > 0 {
			routed++
		}
	}
	// Not vacuous: most of the list really is routed to the per-user pass.
	if routed < 15 {
		t.Errorf("only %d of the probe queries were routed per-user; the probe list no longer exercises the gate", routed)
	}
}

// The store drops a per-viewer collection's stored member list only because this
// package registers its detector with it at init. An unregistered store keeps the
// list, which is a silent fail-open, so the registration is itself tested.
func TestInit_RegistersThePerViewerClassifierWithTheStore(t *testing.T) {
	if !database.CollectionPerViewerClassifierSet() {
		t.Fatal("importing internal/search did not install the store's per-viewer classifier")
	}
}
