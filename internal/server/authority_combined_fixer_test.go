// file: internal/server/authority_combined_fixer_test.go
// version: 1.0.0
// guid: 0b7e3f52-9c1d-4a8e-b6f4-2d5c8a1e7f93
// last-edited: 2026-10-05

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Prod 2026-10-05: authority_evidence_enabled was turned on by PUT /config
// on a running server (Start's Prime had been a no-op, the flag was off at
// boot), and the next two plans of maintenance.repair-combined-author-credits
// were identical to the flag-off plan: every new part still read only
// `no author is named "X"; apply creates it`, and no authority snapshot was
// ever loaded. This drives the real fixer from the server's own registry,
// through the server's own store (the indexedStore decorator), with the flag
// flipped after construction exactly as the PUT does, and plans once.
func TestCombinedAuthorFixer_PlanReadsAuthorityAfterRuntimeFlagFlip(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	store := server.store
	authorcredit.ResetTitleCache()
	t.Cleanup(authorcredit.ResetTitleCache)

	_, err := store.CreateAuthor("Ann Leckie")
	require.NoError(t, err)
	book := func(title, credit, path string) string {
		a, err := store.CreateAuthor(credit)
		require.NoError(t, err)
		b, err := store.CreateBook(&database.Book{Title: title, Format: "m4b", FilePath: path, AuthorID: &a.ID})
		require.NoError(t, err)
		require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: path, Format: "m4b"}))
		require.NoError(t, store.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: a.ID, Role: "author", Position: 0}}))
		return b.ID
	}
	listed := book("Book One", "Ann Leckie, Zed Newperson", "/library/Ann Leckie/Book One/a.m4b")
	unlisted := book("Book Two", "Ann Leckie, Yan Nobody", "/library/Ann Leckie/Book Two/a.m4b")
	// Zed Newperson is an author in the authority lists (an owner override
	// is tier O); Yan Nobody is in no list.
	require.NoError(t, authority.PutPersonOverride(store, authority.PersonOverride{Name: "Zed Newperson",
		Roles: map[authority.Role]bool{authority.RoleAuthor: true}, SetAt: time.Now()}))

	require.False(t, config.AuthorityEvidenceEnabled())
	config.Mutate(func(c *config.Config) { c.AuthorityEvidenceEnabled = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.AuthorityEvidenceEnabled = false }) })

	fixer, ok := server.repairFixers.Get("maintenance.repair-combined-author-credits")
	require.True(t, ok)
	series, err := store.GetAllSeries()
	require.NoError(t, err)
	res, err := repairs.RunPlan(context.Background(), fixer, nil,
		repairs.PlanDeps{Guard: store, Series: repairs.SeriesNamesFrom(series)}, &catalogTestReporter{})
	require.NoError(t, err)
	rows := map[string]repairs.Row{}
	for _, r := range res.Rows {
		rows[r.RowID] = r
	}
	require.Contains(t, rows, listed)
	require.Contains(t, rows, unlisted)

	ev := strings.Join(rows[listed].Evidence, "\n")
	require.Contains(t, ev, `no author is named "Zed Newperson"; apply creates it`)
	require.Contains(t, ev, `the authority lists hold "Zed Newperson" as an author from the owner's library (tier O)`,
		"the plan must read the authority lists the flag turned on: %v", rows[listed].Evidence)
	require.Contains(t, strings.Join(rows[unlisted].Evidence, "\n"),
		`the authority lists hold no author entry for "Yan Nobody"`,
		"a part the lists do not know must say so, so a missing line means the lists were not read: %v", rows[unlisted].Evidence)
}
