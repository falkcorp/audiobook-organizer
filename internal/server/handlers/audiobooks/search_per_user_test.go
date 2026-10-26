// file: internal/server/handlers/audiobooks/search_per_user_test.go
// version: 1.0.0
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
