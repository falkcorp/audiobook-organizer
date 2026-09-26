// file: internal/server/handlers/activity_type_alias_test.go
// version: 1.0.0
// guid: 2c7e9b53-8a14-4f6d-b3e0-5d91a7c4e826
// last-edited: 2026-09-25

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// filterCapture records the filter ListActivity hands the store.
type filterCapture struct {
	*handlersmocks.MockActivityService
	got database.ActivityFilter
}

func (f *filterCapture) QueryWithPartial(_ context.Context, filter database.ActivityFilter) (database.ActivityQueryResult, error) {
	f.got = filter
	return database.ActivityQueryResult{}, nil
}

// fakeOpDefs models the registry's Def: canonical and former IDs both resolve
// to the canonical def. It also counts deprecated-ID uses, as the registry's
// NoteDeprecatedDefIDUse does.
type fakeOpDefs struct {
	defs  []opsregistry.OperationDef
	noted []string // "<given>|<entry>"
}

func (f *fakeOpDefs) Def(id string) (opsregistry.OperationDef, bool) {
	for _, d := range f.defs {
		if d.ID == id {
			return d, true
		}
		for _, old := range d.FormerIDs {
			if old == id {
				return d, true
			}
		}
	}
	return opsregistry.OperationDef{}, false
}

func (f *fakeOpDefs) NoteDeprecatedDefIDUse(given, entry string) {
	f.noted = append(f.noted, given+"|"+entry)
}

// TestListActivity_TypeFilterIsAliasAware: an op's activity rows carry the def
// ID they were recorded under, so a renamed op's history is split across its
// former and canonical IDs. ?type= by EITHER spelling must ask the store for
// both, as GET /operations/timeline?def_id= does; a type that names no op is
// passed through literally.
func TestListActivity_TypeFilterIsAliasAware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	defs := &fakeOpDefs{defs: []opsregistry.OperationDef{
		{ID: "maintenance.library-optimize", FormerIDs: []string{"library.optimize"}},
	}}

	cases := []struct {
		name, query   string
		wantType      string
		wantAliases   []string
		wantNotedUses []string
	}{
		{"former ID", "library.optimize", "maintenance.library-optimize", []string{"library.optimize"},
			[]string{"library.optimize|" + opsregistry.AliasEntryActivityFilter}},
		{"canonical ID", "maintenance.library-optimize", "maintenance.library-optimize", []string{"library.optimize"}, nil},
		{"not an op", "daily_digest", "daily_digest", nil, nil},
		{"absent", "", "", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defs.noted = nil
			svc := &filterCapture{MockActivityService: handlersmocks.NewMockActivityService(t)}
			h := handlers.NewActivityHandler(svc, nil, handlers.WithActivityOpDefs(defs))
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/activity?type="+tc.query, nil)
			h.ListActivity(c)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tc.wantType, svc.got.Type)
			assert.Equal(t, tc.wantAliases, svc.got.TypeAliases)
			assert.Equal(t, tc.wantNotedUses, defs.noted)
		})
	}

	// Without the option the filter stays literal.
	svc := &filterCapture{MockActivityService: handlersmocks.NewMockActivityService(t)}
	h := handlers.NewActivityHandler(svc, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/activity?type=library.optimize", nil)
	h.ListActivity(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "library.optimize", svc.got.Type)
	assert.Nil(t, svc.got.TypeAliases)
}
