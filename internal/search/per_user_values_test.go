// file: internal/search/per_user_values_test.go
// version: 1.1.0
// guid: c3e8a1d4-6f27-4b95-8d0a-71b2e4f9a6c3
// last-edited: 2026-10-30

package search

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCheckPerUserValues_RefusesValuesThePerUserPassCannotEvaluate(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	refused := map[string]string{
		// not a status: matches nothing, and negated matches every book
		"read_status:completed":              "unstarted, in_progress, finished, abandoned",
		"-read_status:completed":             "unstarted, in_progress, finished, abandoned",
		"read_status:started":                "read_status must be one of",
		"read_status:(finished|in-progress)": "in-progress",
		`read_status:"in progress"`:          "read_status must be one of",
		"read_status:>finished":              "not a comparison",
		// patterns are not supported on an exact field
		"read_status:fin*":      "patterns",
		"-read_status:fin*":     "patterns",
		"read_status:*ished":    "patterns",
		"read_status:finished~": "patterns",
		"progress_pct:5*":       "patterns",
		"last_played:-3d*":      "patterns",
		// a number that is not one
		"READ_STATUS:finished":     "lowercase",
		"-Progress_Pct:>50":        "lowercase",
		"Last_Played:(a|b)":        "lowercase",
		"progress_pct:>half":       "progress_pct takes a number",
		"-progress_pct:>abc":       "progress_pct takes a number",
		"progress_pct:[ten TO 90]": "progress_pct takes a number",
		"progress_pct:NaN":         "progress_pct takes a number",
		"progress_pct:(10|abc)":    "progress_pct takes a number",
		// a date that is not one
		"last_played:>30d":               "last_played",
		"last_played:(yesterday)":        "last_played",
		"title:Hush last_played:soonish": "last_played",
	}
	for q, want := range refused {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("fixture %q does not parse: %v", q, err)
		}
		err = CheckPerUserValues(ast, now)
		var ve *PerUserValueError
		if !errors.As(err, &ve) {
			t.Errorf("CheckPerUserValues(%q) = %v, want a *PerUserValueError", q, err)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("CheckPerUserValues(%q) = %q, want it to mention %q", q, err.Error(), want)
		}
		if !IsPerUserQueryError(err) {
			t.Errorf("IsPerUserQueryError(%q) = false", q)
		}
	}
}

func TestCheckPerUserValues_AcceptsTheFormsTheMatcherEvaluates(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, q := range []string{
		"read_status:finished",
		"read_status:FINISHED",
		"-read_status:unstarted",
		"read_status:(finished|in_progress|abandoned)",
		`read_status:"finished"`,
		"progress_pct:>50",
		"progress_pct:<=12.5",
		"progress_pct:50",
		"progress_pct:[10 TO 90]",
		"-progress_pct:>=100",
		"progress_pct:(10|20)",
		"last_played:*",
		"last_played:>-30d",
		"last_played:2026-01-01",
		"last_played:2026-01-01..2026-02-01",
		"title:Hush*",
		"title:Hush~ author:kyle*",
		"title:fin* OR title:Year",
	} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("fixture %q does not parse: %v", q, err)
		}
		if err := CheckPerUserValues(ast, now); err != nil {
			t.Errorf("CheckPerUserValues(%q) = %v, want nil", q, err)
		}
	}
}

func TestCheckPerUserTerms_ChecksTheValuesAndAcceptsEveryBooleanShape(t *testing.T) {
	now := time.Now()
	// An OR with a sibling is evaluated (MixedQuery), no longer refused.
	shape, _ := ParseQuery("read_status:finished OR title:Hush")
	if err := CheckPerUserTerms(shape, now); err != nil {
		t.Errorf("shape: %v", err)
	}
	value, _ := ParseQuery("read_status:completed")
	if err := CheckPerUserTerms(value, now); !IsPerUserQueryError(err) {
		t.Errorf("value: %v", err)
	}
	// ... and the value check reaches inside an OR.
	both, _ := ParseQuery("title:Hush OR read_status:completed")
	if err := CheckPerUserTerms(both, now); !IsPerUserQueryError(err) {
		t.Errorf("value under an OR: %v", err)
	}
	ok, _ := ParseQuery("read_status:finished title:Hush")
	if err := CheckPerUserTerms(ok, now); err != nil {
		t.Errorf("ok: %v", err)
	}
}
