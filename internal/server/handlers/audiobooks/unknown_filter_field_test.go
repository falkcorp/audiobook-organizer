// file: internal/server/handlers/audiobooks/unknown_filter_field_test.go
// version: 1.0.0
// guid: 13ea27a2-ccad-49a7-a164-0bc1ca711898
// last-edited: 2026-08-14

package audiobookshandler_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A filter naming a field the backend cannot filter on used to be answered with
// count:0 — the same answer as a truthful "no books match". That made a typo, a
// renamed field, and a name the UI offered but the backend never implemented
// all indistinguishable from a fact about the library. Measured on production
// 2026-08-14: the nonsense field zzz_not_a_real_field returned count:0, exactly
// as marked_for_deletion did while 3,953 books qualified.
func TestListAudiobooks_UnknownFilterField_400(t *testing.T) {
	for _, field := range []string{"zzz_not_a_real_field", "titel", "version_group_id"} {
		t.Run(field, func(t *testing.T) {
			h, _ := newHandler(t)
			q := url.Values{"filters": {`[{"field":"` + field + `","value":"x"}]`}}
			c, w := newCtx("GET", "/audiobooks?"+q.Encode(), nil, nil)
			h.ListAudiobooks(c)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400 for unknown filter field %q, got %d (%s)",
					field, w.Code, w.Body.String())
			}
			// The message has to name the offending field, or the caller is
			// told "something is wrong" and has to guess which filter.
			if !strings.Contains(w.Body.String(), field) {
				t.Fatalf("rejection must name the field %q; body was %s", field, w.Body.String())
			}
			// ...and it must suggest the alternatives, which is the half that
			// turns an error into a fix.
			if !strings.Contains(w.Body.String(), "title") {
				t.Fatalf("rejection must list valid fields; body was %s", w.Body.String())
			}
		})
	}
}

// The mirror case. Rejecting unknown names is only safe if every name the UI
// can actually emit is accepted — otherwise this guard turns thirteen silent
// zeros into thirteen hard errors, which is a worse regression than the bug.
// internal/audiobooks.TestUISearchFields_AreAllFilterableByTheBackend enforces
// that across the whole UI list; this checks the ones that were broken.
func TestListAudiobooks_PreviouslyBrokenFields_NotRejected(t *testing.T) {
	for _, field := range []string{
		"year", "series_number", "isbn10", "isbn13", "work_id", "channels",
		"bit_depth", "created_at", "updated_at", "duration", "file_size",
		"bitrate", "sample_rate", "marked_for_deletion",
	} {
		t.Run(field, func(t *testing.T) {
			h, d := newHandler(t)
			d.rec.listResp = map[string]any{"items": []any{}, "count": 0}
			q := url.Values{"filters": {`[{"field":"` + field + `","value":"1"}]`}}
			c, w := newCtx("GET", "/audiobooks?"+q.Encode(), nil, nil)
			h.ListAudiobooks(c)

			if w.Code == http.StatusBadRequest {
				var body map[string]any
				_ = json.Unmarshal(w.Body.Bytes(), &body)
				t.Fatalf("%q must be an accepted filter field, but the handler rejected it: %s",
					field, w.Body.String())
			}
		})
	}
}
