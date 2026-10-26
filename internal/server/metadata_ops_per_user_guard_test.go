// file: internal/server/metadata_ops_per_user_guard_test.go
// version: 1.0.0
// guid: 3e7a9c15-6b24-4d80-9f13-a0c5d8e2b749
// last-edited: 2026-10-26

package server

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveFilterToBookIDs decides which books a background operation targets, with
// no viewer. A per-user term (read_status, progress_pct, last_played) in its
// FilterSpec has no listening state to be judged by, and resolving it anyway
// widened "my finished books" to the whole library: the field filters were dropped,
// and the search path ignores a per-user term when no user is present. The web
// select-all filter can carry such a term, so the resolver refuses it.
//
// The Server is the zero value on purpose: the refusal must come before anything is
// read from it, so a refused spec never reaches the service.
func TestResolveFilterToBookIDs_RefusesAPerUserTerm(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec operations.FilterSpec
	}{
		{"field filter read_status", operations.FilterSpec{
			FieldFilters: []operations.FieldFilter{{Field: "read_status", Value: "finished"}}}},
		{"negated field filter", operations.FilterSpec{
			FieldFilters: []operations.FieldFilter{{Field: "progress_pct", Value: ">50", Negated: true}}}},
		{"per-user filter among others", operations.FilterSpec{
			FieldFilters: []operations.FieldFilter{
				{Field: "author", Value: "kyle"},
				{Field: "last_played", Value: ">-30d"},
			}}},
		{"search text term", operations.FilterSpec{Search: "read_status:finished author:kyle"}},
		{"search text in a group", operations.FilterSpec{Search: "(read_status:finished tag:noir)"}},
		{"search text that does not parse", operations.FilterSpec{Search: "read_status:finished OR ("}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids, err := (&Server{}).resolveFilterToBookIDs(context.Background(), tc.spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "per-user")
			assert.Nil(t, ids)
		})
	}
}

// The guard itself, for the specs that must pass it: no per-user term, however the
// text reads. (refuseUnscopedPerUserTerms is what resolveFilterToBookIDs calls
// first; the resolver past it needs a service this test does not build.)
func TestRefuseUnscopedPerUserTerms_LetsOrdinarySpecsThrough(t *testing.T) {
	for _, spec := range []operations.FilterSpec{
		{},
		{Search: "The Long Halloween"},
		{Search: "The (Long Halloween"},
		{Search: "author:kyle tag:noir"},
		{Search: "the read_status of things"},
		{Tag: "noir", Tags: []string{"gotham"}},
		{FieldFilters: []operations.FieldFilter{{Field: "author", Value: "kyle"}}},
	} {
		assert.NoError(t, refuseUnscopedPerUserTerms(spec), "%+v", spec)
	}
}
