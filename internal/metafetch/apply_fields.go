// file: internal/metafetch/apply_fields.go
// version: 1.0.0
// guid: e3dc3bd1-0410-4e52-9d69-c900a119c5ea
// last-edited: 2026-09-12

package metafetch

import (
	"log/slog"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// ApplyField is one user-selectable field of a metadata apply.
//
// ApplyFields is the ONE list three things are derived from:
//
//   - the apply allowlist (FilterApplyFields): a field not selected is zeroed
//     before the apply, so it is never written;
//   - fetched_value provenance (FetchedProvenance): every field the apply can
//     write records what the provider said;
//   - the web apply dialogs, whose shared list
//     (web/src/config/metadataApplyFields.ts) must name exactly these keys --
//     TestApplyFieldsMatchWebList reads that file and fails on any difference.
//
// The allowlist and the provenance list used to be two hand-written lists in
// service_apply.go, and they had drifted from the apply and from each other:
// the UI sent "series_position" and the server had no branch for it, unchecking
// "isbn" still wrote ISBN10/ISBN13, subtitle/abridged/page count/secondary
// series/runtime were written whatever was selected, and provenance was
// recorded for eight fields out of the twenty-odd the apply writes.
type ApplyField struct {
	// Key is the allowlist name the apply request carries in "fields". It is
	// the MetadataCandidate JSON key of the field, which is how the web dialogs
	// look the value up on a candidate.
	Key string
	// clear zeroes every BookMetadata member this field controls.
	clear func(*metadata.BookMetadata)
	// provenance adds this field's fetched_value rows to out, keyed by the
	// field-state vocabulary (database.UserLockableFields and the UI's
	// FIELD_STATE_KEYS: author_name, series_name, isbn10...), NOT by Key. Only
	// non-empty values are added: saveMetadataState deletes rows missing from
	// the map it is given, so an empty value must mean "no row", not "blank".
	provenance func(metadata.BookMetadata, map[string]any)
}

func putString(out map[string]any, key, v string) {
	if v != "" {
		out[key] = v
	}
}

// ApplyFields is every field a metadata apply can write, in UI display order.
// Category tags are deliberately absent: they are additive book_tags, not a
// Book column, and are applied whatever the selection (see ApplyMetadataCandidate).
var ApplyFields = []ApplyField{
	{Key: "title",
		clear:      func(m *metadata.BookMetadata) { m.Title = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "title", m.Title) }},
	{Key: "author",
		clear:      func(m *metadata.BookMetadata) { m.Author = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "author_name", m.Author) }},
	{Key: "narrator",
		clear:      func(m *metadata.BookMetadata) { m.Narrator = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "narrator", m.Narrator) }},
	{Key: "series",
		clear:      func(m *metadata.BookMetadata) { m.Series = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "series_name", m.Series) }},
	{Key: "series_position",
		clear: func(m *metadata.BookMetadata) { m.SeriesPosition = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) {
			putString(out, "series_position", m.SeriesPosition)
		}},
	{Key: "series_secondary",
		clear: func(m *metadata.BookMetadata) {
			m.SeriesSecondary = ""
			m.SeriesSecondaryPosition = ""
		},
		provenance: func(m metadata.BookMetadata, out map[string]any) {
			putString(out, "series_secondary", m.SeriesSecondary)
			putString(out, "series_secondary_position", m.SeriesSecondaryPosition)
		}},
	{Key: "year",
		clear: func(m *metadata.BookMetadata) { m.PublishYear = 0 },
		provenance: func(m metadata.BookMetadata, out map[string]any) {
			if m.PublishYear == 0 {
				return
			}
			// Keyed by the column the year routes to (see applyMetadataUnguarded).
			if m.PublishYearIsAudiobookRelease {
				out["audiobook_release_year"] = m.PublishYear
			} else {
				out["print_year"] = m.PublishYear
			}
		}},
	{Key: "publisher",
		clear:      func(m *metadata.BookMetadata) { m.Publisher = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "publisher", m.Publisher) }},
	{Key: "isbn",
		// One UI checkbox for all three ISBN members: unchecking "isbn" used to
		// clear only ISBN and leave ISBN10/ISBN13 to be written.
		clear: func(m *metadata.BookMetadata) {
			m.ISBN = ""
			m.ISBN10 = ""
			m.ISBN13 = ""
		},
		provenance: func(m metadata.BookMetadata, out map[string]any) {
			// Same resolution as applyMetadataUnguarded, so the fetched_value is
			// the value the column could have received.
			putString(out, "isbn13", firstNonEmpty(m.ISBN13, isbnOfLen(m.ISBN, 13)))
			putString(out, "isbn10", firstNonEmpty(m.ISBN10, isbnOfLen(m.ISBN, 10)))
		}},
	{Key: "asin",
		clear:      func(m *metadata.BookMetadata) { m.ASIN = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "asin", m.ASIN) }},
	{Key: "cover_url",
		clear:      func(m *metadata.BookMetadata) { m.CoverURL = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "cover_url", m.CoverURL) }},
	{Key: "description",
		clear: func(m *metadata.BookMetadata) { m.Description = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) {
			putString(out, "description", m.Description)
		}},
	{Key: "language",
		clear:      func(m *metadata.BookMetadata) { m.Language = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "language", m.Language) }},
	{Key: "genre",
		clear:      func(m *metadata.BookMetadata) { m.Genre = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "genre", m.Genre) }},
	{Key: "subtitle",
		clear:      func(m *metadata.BookMetadata) { m.Subtitle = "" },
		provenance: func(m metadata.BookMetadata, out map[string]any) { putString(out, "subtitle", m.Subtitle) }},
	{Key: "abridged",
		clear: func(m *metadata.BookMetadata) { m.Abridged = nil },
		provenance: func(m metadata.BookMetadata, out map[string]any) {
			if m.Abridged != nil {
				out["abridged"] = *m.Abridged
			}
		}},
	{Key: "page_count",
		clear: func(m *metadata.BookMetadata) { m.PageCount = 0 },
		provenance: func(m metadata.BookMetadata, out map[string]any) {
			if m.PageCount > 0 {
				out["page_count"] = m.PageCount
			}
		}},
	{Key: "duration_sec",
		clear: func(m *metadata.BookMetadata) { m.DurationSec = 0 },
		provenance: func(m metadata.BookMetadata, out map[string]any) {
			// The apply stores the provider runtime as AudibleRuntimeMin.
			if m.DurationSec > 0 {
				out["audible_runtime_min"] = m.DurationSec / 60
			}
		}},
}

// ApplyFieldKeys returns the allowlist names in ApplyFields order.
func ApplyFieldKeys() []string {
	keys := make([]string, 0, len(ApplyFields))
	for _, f := range ApplyFields {
		keys = append(keys, f.Key)
	}
	return keys
}

// FilterApplyFields zeroes every field of meta whose key is not in fields. An
// empty fields list means "apply everything" (the batch paths pass nil). A
// name that is not an ApplyFields key is logged: it means a client and the
// server disagree on the vocabulary, which is how "series_position" went
// unhonored without anyone noticing.
func FilterApplyFields(meta metadata.BookMetadata, fields []string) metadata.BookMetadata {
	if len(fields) == 0 {
		return meta
	}
	allowed := make(map[string]bool, len(fields))
	for _, f := range fields {
		allowed[f] = true
	}
	for _, f := range ApplyFields {
		if allowed[f.Key] {
			delete(allowed, f.Key)
			continue
		}
		f.clear(&meta)
	}
	if len(allowed) > 0 {
		unknown := make([]string, 0, len(allowed))
		for k := range allowed {
			unknown = append(unknown, k)
		}
		sort.Strings(unknown)
		slog.Warn("metadata apply: ignoring unknown field names in the allowlist",
			"unknown", unknown, "known", ApplyFieldKeys())
	}
	return meta
}

// FetchedProvenance returns the fetched_value rows for meta, one entry per
// non-empty field-state key, from ApplyFields.
func FetchedProvenance(meta metadata.BookMetadata) map[string]any {
	out := map[string]any{}
	for _, f := range ApplyFields {
		f.provenance(meta, out)
	}
	return out
}
