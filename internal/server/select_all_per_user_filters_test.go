// file: internal/server/select_all_per_user_filters_test.go
// version: 1.0.0
// guid: f7282f66-4b94-48cf-9012-922b3d4cc25d
// last-edited: 2026-10-30

package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A select-all on a view filtered by a per-user term resolves against the
// REQUESTING user's listening rows, as the Library list does for the same filter.
// Before 2026-10-30 the resolver refused a per-user term outright, so the select-all
// could not be used on such a view at all; before 2026-10-26 it dropped the term and
// ran the op over the whole view. Titles and names are synthetic.
func TestSelectAllResolvesPerUserFiltersAgainstTheRequestingUser(t *testing.T) {
	srv, _, _, seedID, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()

	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok, "expected a PebbleStore")
	store.WaitForWarmup()
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	viewer, err := store.GetUserByUsername("viewer")
	require.NoError(t, err)

	newBook := func(title string) string {
		b, cerr := store.CreateBook(&database.Book{Title: title, FilePath: "/synthetic/" + title + ".m4b"})
		require.NoError(t, cerr)
		return b.ID
	}
	dive := newBook("Year One")
	longHalloween := newBook("The Long Halloween")
	darkVictory := newBook("Dark Victory")
	hush := newBook("Hush")
	setState := func(user, book, status string, pct int) {
		require.NoError(t, store.SetUserBookState(&database.UserBookState{
			UserID: user, BookID: book, Status: status, ProgressPct: pct, LastActivityAt: time.Now().UTC()}))
	}
	// The admin has started two of the new books; the viewer has started two others
	// (one shared). The harness's own book is untouched by both.
	setState(admin.ID, longHalloween, database.UserBookStatusInProgress, 40)
	setState(admin.ID, darkVictory, database.UserBookStatusFinished, 100)
	setState(viewer.ID, hush, database.UserBookStatusFinished, 100)
	setState(viewer.ID, darkVictory, database.UserBookStatusInProgress, 10)

	unstarted := operations.FilterSpec{FieldFilters: []operations.FieldFilter{{Field: "read_status", Value: "unstarted"}}}
	resolve := func(spec operations.FilterSpec, user string) ([]string, error) {
		return srv.resolveFilterToBookIDs(context.Background(), spec, user)
	}

	t.Run("read_status:unstarted is the requesting user's unstarted books", func(t *testing.T) {
		got, err := resolve(unstarted, admin.ID)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{seedID, dive, hush}, got)

		got, err = resolve(unstarted, viewer.ID)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{seedID, dive, longHalloween}, got,
			"the other user's rows must not decide this user's selection")
	})

	t.Run("a negated per-user filter and a book-global filter combine", func(t *testing.T) {
		spec := operations.FilterSpec{FieldFilters: []operations.FieldFilter{
			{Field: "read_status", Value: "finished", Negated: true},
			{Field: "title", Value: "Halloween"},
		}}
		got, err := resolve(spec, viewer.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{longHalloween}, got)

		got, err = resolve(spec, admin.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{longHalloween}, got)
	})

	t.Run("a per-user term in the search text is judged by the same user", func(t *testing.T) {
		// The search text goes through the Bleve index, which the harness does not
		// open: index every book so the DSL path runs instead of the substring
		// fallback (which cannot read a listening row).
		idx, ierr := search.Open(filepath.Join(t.TempDir(), "bleve"))
		require.NoError(t, ierr)
		t.Cleanup(func() { _ = idx.Close() })
		require.NoError(t, idx.MarkRebuilt())
		for id, title := range map[string]string{
			seedID: "Authz Test Book", dive: "Year One", longHalloween: "The Long Halloween",
			darkVictory: "Dark Victory", hush: "Hush",
		} {
			require.NoError(t, idx.IndexBook(search.BookDocument{BookID: id, Title: title}))
		}
		srv.audiobookService.SetSearchIndex(idx)
		t.Cleanup(func() { srv.audiobookService.SetSearchIndex(nil) })

		got, err := resolve(operations.FilterSpec{Search: "read_status:finished"}, viewer.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{hush}, got)

		got, err = resolve(operations.FilterSpec{Search: "read_status:finished"}, admin.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{darkVictory}, got)
	})

	t.Run("no user id refuses and names the filter instead of widening", func(t *testing.T) {
		got, err := resolve(unstarted, "")
		require.Error(t, err)
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), `"read_status"`)
		assert.Contains(t, err.Error(), "per-user")
	})

	t.Run("a spec with no per-user term is unchanged by who asks", func(t *testing.T) {
		spec := operations.FilterSpec{FieldFilters: []operations.FieldFilter{{Field: "title", Value: "Halloween"}}}
		anon, err := resolve(spec, "")
		require.NoError(t, err)
		asAdmin, err := resolve(spec, admin.ID)
		require.NoError(t, err)
		asViewer, err := resolve(spec, viewer.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{longHalloween}, anon)
		assert.Equal(t, anon, asAdmin)
		assert.Equal(t, anon, asViewer)

		every, err := resolve(operations.FilterSpec{}, "")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{seedID, dive, longHalloween, darkVictory, hush}, every)
	})
}

// The bulk fetch op learns who it acts for from its own operation row, which the
// registry stamped from the verified request, and hands the same identity to the
// continuation it queues (which re-resolves the selection and so needs it again).
func TestOpActorReadsTheRowAndReplaysItOnASuccessor(t *testing.T) {
	srv, _, _, _, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()
	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok, "expected a PebbleStore")

	actor := "synthetic-user-id"
	now := time.Now().UTC()
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-with-actor", DefID: "library.bulk-metadata-fetch", Plugin: "library", Status: "running",
		Params: `{}`, ActorUserID: &actor, ActorAuthMethod: "session",
		ActorPermissions: []string{"library.view"}, QueuedAt: now,
	}))
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-no-actor", DefID: "library.bulk-metadata-fetch", Plugin: "library", Status: "running",
		Params: `{}`, QueuedAt: now,
	}))

	got, err := srv.opActor("op-with-actor")
	require.NoError(t, err)
	assert.Equal(t, actor, got.userID)

	var replay opsregistry.EnqueueOptions
	for _, o := range got.options() {
		o(&replay)
	}
	assert.Equal(t, actor, replay.ActorUserID)
	assert.Equal(t, "session", replay.ActorAuthMethod)
	assert.Equal(t, []string{"library.view"}, replay.ActorPermissions)

	none, err := srv.opActor("op-no-actor")
	require.NoError(t, err)
	assert.Empty(t, none.userID)
	assert.Empty(t, none.options(), "an op nobody enqueued must not invent an actor for its successor")

	unknown, err := srv.opActor("")
	require.NoError(t, err)
	assert.Empty(t, unknown.userID)
}
