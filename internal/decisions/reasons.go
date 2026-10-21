// file: internal/decisions/reasons.go
// version: 1.0.0
// guid: c2a65f18-d907-4e34-8b7a-3f10e6d95a82
// last-edited: 2026-10-21

package decisions

import "strings"

// Reason is one entry of the reject-reason taxonomy. The API serves the list
// so no client hard-codes it. (A Predicate for the rule-suggestion loop is a
// later PR.)
type Reason struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// ReasonOther is always allowed.
const ReasonOther = "other"

var commonReasons = []Reason{
	{"not_same_entity", "These are not the same thing"},
	{"wrong_target", "Wrong target"},
	{"bad_input_data", "The input data is wrong"},
	{"already_correct", "Already correct"},
	{ReasonOther, "Other"},
}

var splitReasons = []Reason{
	{"distinct_titles", "Distinct titles"},
	{"book_n_in_title", "\"Book N\" is part of the title"},
	{"distinct_series_positions", "Distinct series positions"},
	{"full_length_durations", "Each is a full-length book"},
	{"folder_names_range", "Folder names a range (\"Books 1-4\")"},
	{"different_authors", "Different authors"},
	{"different_narrators", "Different narrators"},
}

var authorReasons = []Reason{
	{"different_people", "Different people"},
	{"work_title_not_person", "A work title, not a person"},
	{"part_track_marker", "A part or track marker"},
	{"pen_name_keep_separate", "Pen name, keep separate"},
	{"translator_or_editor", "Translator or editor"},
}

var seriesReasons = []Reason{
	{"different_series", "Different series"},
	{"subseries_keep_separate", "Sub-series, keep separate"},
}

var metadataReasons = []Reason{
	{"wrong_book", "Wrong book"},
	{"wrong_edition", "Wrong edition"},
	{"abridged", "Abridged"},
	{"wrong_language", "Wrong language"},
	{"duration_mismatch", "Duration mismatch"},
	{"wrong_author", "Wrong author"},
}

var dedupReasons = []Reason{
	{"different_editions_keep_both", "Different editions, keep both"},
	{"part_vs_whole", "A part versus the whole"},
	{"different_books_same_title", "Different books, same title"},
}

var rewriteReasons = []Reason{
	{"current_value_correct", "The current value is correct"},
	{"proposed_value_wrong", "The proposed value is wrong"},
}

// mergeShapedRepairs are the repairs fixers that propose merging books, so
// they take the split-books taxonomy rather than the field-rewrite one.
var mergeShapedRepairs = map[string]bool{
	"repairs:dedup.split-books":       true,
	"repairs:fragment-consolidation":  true,
	"repairs:folder-books":            true,
	"repairs:duplicate-copies":        true,
	"repairs:version-twin-metadata":   true,
	"repairs:consolidation-leftovers": true,
}

// Reasons returns the taxonomy for opKind: the common codes plus the codes of
// its family. An op kind this package does not know gets the common codes
// only.
func Reasons(opKind string) []Reason {
	out := append([]Reason(nil), commonReasons...)
	var extra []Reason
	switch {
	case mergeShapedRepairs[opKind]:
		extra = splitReasons
	case strings.HasPrefix(opKind, "repairs:"):
		extra = rewriteReasons
	case strings.HasPrefix(opKind, "authors:"):
		extra = authorReasons
	case strings.HasPrefix(opKind, "series:"):
		extra = seriesReasons
	case strings.HasPrefix(opKind, "metadata:"):
		extra = metadataReasons
	case strings.HasPrefix(opKind, "dedup:"):
		extra = dedupReasons
	}
	return append(out, extra...)
}

// ReasonKnown reports whether code is in the taxonomy for opKind.
func ReasonKnown(opKind, code string) bool {
	for _, r := range Reasons(opKind) {
		if r.Code == code {
			return true
		}
	}
	return false
}
