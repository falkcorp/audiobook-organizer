// file: internal/server/handlers/audiobooks/search_per_user_test.go
// version: 1.0.2
// guid: 0d6f3a92-b814-4e57-9c20-a5e18d7b3f64
// last-edited: 2026-10-26

package audiobookshandler_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// S-D: the library search applies read_status, progress_pct and last_played as one
// flat AND after the index answers, so a query that puts one where that cannot give
// it its meaning used to be answered as something else: read_status:finished OR
// author:kyle returned the Selina Kyle books the viewer had finished, and
// read_status:completed (not a status) matched nothing, or, negated, every book.
// The list refuses it with a 400 that says what to write, as a collection or a
// smart playlist does when it is saved.
func TestListAudiobooks_SearchRefusesAPerUserTermItCannotEvaluate(t *testing.T) {
	for _, tc := range []struct{ search, want string }{
		{"read_status:finished OR author:kyle", "cannot be combined"},
		{"-(read_status:finished author:kyle)", "cannot be combined"},
		{"read_status:completed", "read_status must be one of"},
		{"-read_status:completed", "read_status must be one of"},
		{"read_status:fin*", "patterns"},
		{"progress_pct:>half", "progress_pct takes a number"},
		{"last_played:>soon", "last_played"},
		{"READ_STATUS:finished", "lowercase"},
	} {
		t.Run(tc.search, func(t *testing.T) {
			h, deps := newHandler(t)
			c, w := newCtx("GET", "/audiobooks?"+url.Values{"search": {tc.search}}.Encode(), nil, nil)
			h.ListAudiobooks(c)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "invalid search")
			assert.True(t, strings.Contains(w.Body.String(), tc.want), "want %q in %s", tc.want, w.Body.String())
			assert.False(t, deps.rec.listFiltersSeen, "refused before the list is built")
		})
	}
}

// The spellings that do work are not refused, and neither is a search with no
// per-user term at all.
func TestListAudiobooks_SearchAcceptsTheWorkingPerUserSpellings(t *testing.T) {
	for _, q := range []string{
		"read_status:finished author:kyle",
		"read_status:(finished|in_progress)",
		"-read_status:finished title:Hush",
		"progress_pct:>50",
		"last_played:>-30d",
		"last_played:*",
		"-(title:Hush author:kyle)",
		"title:Hush OR title:Hush",
		"Year One",
	} {
		t.Run(q, func(t *testing.T) {
			h, _ := newHandler(t)
			c, w := newCtx("GET", "/audiobooks?"+url.Values{"search": {q}}.Encode(), nil, nil)
			h.ListAudiobooks(c)
			assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
		})
	}
}

// B-1: the web Library search box does not send a per-user term in search=. It
// splits read_status:..., progress_pct:... and last_played:... out of the typed
// text and sends them as filters=, which the value check above never saw. So
// "-read_status:completed" (what I have not completed) answered 200 with the whole
// library, finished books included. The filters= path refuses the same values, with
// the same reason text.
func TestListAudiobooks_FiltersRefuseAPerUserValueTheMatcherCannotEvaluate(t *testing.T) {
	for _, tc := range []struct{ filters, want string }{
		{`[{"field":"read_status","value":"completed","negated":true}]`, "read_status must be one of"},
		{`[{"field":"read_status","value":"completed"}]`, "read_status must be one of"},
		{`[{"field":"read_status","value":"fin*"}]`, "patterns"},
		{`[{"field":"progress_pct","value":"[* TO 50]"}]`, "open end"},
		{`[{"field":"progress_pct","value":"!=100"}]`, "progress_pct takes a number"},
		{`[{"field":"progress_pct","value":"==50","negated":true}]`, "progress_pct takes a number"},
		{`[{"field":"progress_pct","value":"(>50|<10)"}]`, "inside ( | )"},
		{`[{"field":"last_played","value":">soon"}]`, "last_played"},
		{`[{"field":"read_status","value":"()"}]`, "lists no values"},
	} {
		t.Run(tc.filters, func(t *testing.T) {
			h, deps := newHandler(t)
			c, w := newCtx("GET", "/audiobooks?"+url.Values{"filters": {tc.filters}}.Encode(), nil, nil)
			h.ListAudiobooks(c)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "invalid filter value")
			assert.True(t, strings.Contains(w.Body.String(), tc.want), "want %q in %s", tc.want, w.Body.String())
			assert.False(t, deps.rec.listFiltersSeen, "refused before the list is built")
		})
	}
}

func TestListAudiobooks_FiltersAcceptTheWorkingPerUserSpellings(t *testing.T) {
	for _, f := range []string{
		`[{"field":"read_status","value":"finished"}]`,
		`[{"field":"read_status","value":"finished","negated":true}]`,
		`[{"field":"read_status","value":"(finished|in_progress)"}]`,
		`[{"field":"read_status","value":"(finished | in_progress)","negated":true}]`,
		`[{"field":"progress_pct","value":">50"}]`,
		`[{"field":"progress_pct","value":"[10 TO 90]"}]`,
		`[{"field":"last_played","value":"*"}]`,
	} {
		t.Run(f, func(t *testing.T) {
			h, _ := newHandler(t)
			c, w := newCtx("GET", "/audiobooks?"+url.Values{"filters": {f}}.Encode(), nil, nil)
			h.ListAudiobooks(c)
			assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
		})
	}
}

// The web search box sends a query with a per-user term and an OR or a group whole as
// search=, so what the list answers for the other terms in it is what the person
// sees. A tag: term is held by the index and is judged with the rest of the query; a
// field the index does not hold (metadata, review, has_duration) is refused with the
// search parameter's own message, which points at filters= and so cannot be acted on
// from the box; the per-user shapes are refused with their own message first.
func TestListAudiobooks_SearchSentWholeByTheWebBoxMeetsTheOtherRulesOfSearch(t *testing.T) {
	for _, tc := range []struct {
		search string
		status int
		want   string
	}{
		{"read_status:finished OR tag:noir", http.StatusBadRequest, "cannot be combined"},
		{"read_status:finished OR duration:>20m", http.StatusBadRequest, "cannot be combined"},
		{"(read_status:finished tag:noir)", http.StatusOK, ""},
		{"read_status:finished OR metadata:applied", http.StatusBadRequest, "filters parameter"},
		{"(read_status:finished metadata:applied)", http.StatusBadRequest, "filters parameter"},
	} {
		t.Run(tc.search, func(t *testing.T) {
			h, _ := newHandler(t)
			c, w := newCtx("GET", "/audiobooks?"+url.Values{"search": {tc.search}}.Encode(), nil, nil)
			h.ListAudiobooks(c)
			assert.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.want != "" {
				assert.Contains(t, w.Body.String(), tc.want)
			}
		})
	}
}
