// file: internal/server/handlers/audiobooks/handler_scoped_facets_test.go
// version: 1.0.0
// guid: 2a6e9f3c-5d1b-4c78-8e04-b7f1d3a9c625
// last-edited: 2026-10-06

package audiobookshandler_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// scoped=1 must evaluate the facets over EXACTLY the predicate GET /audiobooks
// would apply to the same query string: search, tags[], library_state,
// is_primary_version, the filters JSON, and the quarantine default. The list
// handler and this one share parseListRequest; this pins that the facets
// handler hands the service the same ListFilters the list would.
func TestAudiobookFacets_ScopedUsesListPredicate(t *testing.T) {
	h, d := newHandler(t)
	q := url.Values{}
	q.Set("scoped", "1")
	q.Set("search", "dune")
	q.Add("tags[]", "fantasy")
	q.Add("tags[]", "metadata:language:en")
	q.Set("library_state", "organized")
	q.Set("is_primary_version", "true")
	q.Set("filters", `[{"field":"genre","value":"scifi"}]`)
	q.Set("sort_by", "title")

	d.svc.EXPECT().
		ScopedTagFacets(mock.Anything, "dune", (*int)(nil), (*int)(nil), mock.MatchedBy(func(f audiobookspkg.ListFilters) bool {
			return len(f.Tags) == 2 && f.Tags[0] == "fantasy" && f.Tags[1] == "metadata:language:en" &&
				f.LibraryState == "organized" &&
				f.IsPrimaryVersion != nil && *f.IsPrimaryVersion &&
				len(f.FieldFilters) == 1 && f.FieldFilters[0].Field == "genre" && f.FieldFilters[0].Value == "scifi" &&
				f.ExcludeQuarantined // list default: quarantined books are not listed
		})).
		Return(audiobookspkg.ScopedTagFacets{
			Tags:  []database.TagWithCount{{Tag: "fantasy", Count: 3}, {Tag: "metadata:language:en", Count: 3}, {Tag: "epic", Count: 1}},
			Total: 3,
		}, nil).Once()

	c, w := newCtx("GET", "/audiobooks/facets?"+q.Encode(), nil, nil)
	h.AudiobookFacets(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Genres      []string                `json:"genres"`
			ScopedTags  []database.TagWithCount `json:"scoped_tags"`
			ScopedTotal int                     `json:"scoped_total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, 3, resp.Data.ScopedTotal)
	require.Len(t, resp.Data.ScopedTags, 3)
	require.NotNil(t, resp.Data.Genres, "library-wide keys are still present")
}

// show_quarantined=true lifts the quarantine exclusion, as it does on the list.
func TestAudiobookFacets_ScopedHonoursShowQuarantined(t *testing.T) {
	h, d := newHandler(t)
	d.svc.EXPECT().
		ScopedTagFacets(mock.Anything, "", (*int)(nil), (*int)(nil), mock.MatchedBy(func(f audiobookspkg.ListFilters) bool {
			return !f.ExcludeQuarantined
		})).
		Return(audiobookspkg.ScopedTagFacets{Tags: []database.TagWithCount{}}, nil).Once()
	c, w := newCtx("GET", "/audiobooks/facets?scoped=1&show_quarantined=true", nil, nil)
	h.AudiobookFacets(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// The list's request validation applies: a bad filters value is a 400, never
// a facet computed over an unfiltered library.
func TestAudiobookFacets_ScopedRejectsWhatTheListRejects(t *testing.T) {
	for name, target := range map[string]string{
		"empty filter value": "/audiobooks/facets?scoped=1&filters=" + url.QueryEscape(`[{"field":"title","value":""}]`),
		"unknown field":      "/audiobooks/facets?scoped=1&filters=" + url.QueryEscape(`[{"field":"zzz_not_a_field","value":"x"}]`),
		"bare filter param":  "/audiobooks/facets?scoped=1&title=Dune",
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newHandler(t) // mock service: any ScopedTagFacets call fails the test
			c, w := newCtx("GET", target, nil, nil)
			h.AudiobookFacets(c)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
}

// Without scoped=1 the endpoint is unchanged: no scoped keys, no service call.
func TestAudiobookFacets_UnscopedUnchanged(t *testing.T) {
	h, _ := newHandler(t)
	c, w := newCtx("GET", "/audiobooks/facets", nil, nil)
	h.AudiobookFacets(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "scoped_tags")
}
