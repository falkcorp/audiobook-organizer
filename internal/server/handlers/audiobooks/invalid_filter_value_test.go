// file: internal/server/handlers/audiobooks/invalid_filter_value_test.go
// version: 1.0.0
// guid: 0e7b3d52-8a19-4c6f-b2d4-5f1a9c3e7d68
// last-edited: 2026-10-06

package audiobookshandler_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A filter value the matcher cannot evaluate must be a 400 naming the token,
// never a silent count:0: file_size:>20mb used to substring-match ">20mb"
// against "52428800" and answer 0; file_size:>abc must now say why it failed.
func TestListAudiobooks_InvalidFilterValue_400(t *testing.T) {
	for _, tc := range []struct{ field, value string }{
		{"file_size", ">abc"},
		{"bitrate", "<64mb"},
		{"year", "[2015 2020]"},
		{"title", "/^(?!The)/"},
		{"title", "/unterminated"},
	} {
		t.Run(tc.field+":"+tc.value, func(t *testing.T) {
			h, _ := newHandler(t)
			q := url.Values{"filters": {`[{"field":"` + tc.field + `","value":"` + tc.value + `"}]`}}
			c, w := newCtx("GET", "/audiobooks?"+q.Encode(), nil, nil)
			h.ListAudiobooks(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400 for %s:%s, got %d (%s)", tc.field, tc.value, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.field+":") {
				t.Fatalf("rejection must name the token; body was %s", w.Body.String())
			}
		})
	}
}

// The valid unit forms are accepted at the boundary.
func TestListAudiobooks_UnitFilterValues_Accepted(t *testing.T) {
	for _, tc := range []struct{ field, value string }{
		{"file_size", ">20mb"},
		{"file_size", "[5mb TO 40mb]"},
		{"bitrate", "<64k"},
		{"sample_rate", ">=44.1khz"},
		{"year", ">2020"},
		{"title", "a*"},
	} {
		t.Run(tc.field+":"+tc.value, func(t *testing.T) {
			h, d := newHandler(t)
			d.rec.listResp = map[string]any{"items": []any{}, "count": 0}
			q := url.Values{"filters": {`[{"field":"` + tc.field + `","value":"` + tc.value + `"}]`}}
			c, w := newCtx("GET", "/audiobooks?"+q.Encode(), nil, nil)
			h.ListAudiobooks(c)
			if w.Code == http.StatusBadRequest {
				t.Fatalf("%s:%s must be accepted, got 400: %s", tc.field, tc.value, w.Body.String())
			}
		})
	}
}
