// file: internal/server/select_all_per_user_filters_test.go
// version: 1.1.0
// guid: f7282f66-4b94-48cf-9012-922b3d4cc25d
// last-edited: 2026-10-30

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
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

// recordingReporter is an sdReporter that keeps what the op said, so a test can tell
// which fetch path ran: the whole-library path opens with a "loading books" progress
// line, a run over a resolved-to-nothing selection says so and stops.
type recordingReporter struct {
	sdReporter
	mu   sync.Mutex
	msgs []string
}

func (r *recordingReporter) UpdateProgress(_, _ int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
	return nil
}

func (r *recordingReporter) Log(_ slog.Level, msg string, _ ...slog.Attr) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
	return nil
}

func (r *recordingReporter) said(substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

// A selection that names a filter and resolves to zero books asked for something
// narrower than the library; it must fetch nothing. The op used to test
// len(bookIDs) > 0 and otherwise run the whole-library fetch, so "my finished books"
// for a user who had finished none fetched every book. Only a selection that names
// neither a filter nor books is the whole library.
func TestBulkFetchOp_ASelectionThatResolvesToNothingFetchesNothing(t *testing.T) {
	srv, _, _, _, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()
	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok, "expected a PebbleStore")
	store.WaitForWarmup()
	viewer, err := store.GetUserByUsername("viewer")
	require.NoError(t, err)

	// The viewer has finished nothing, so read_status:finished matches no book.
	now := time.Now().UTC()
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-select-none", DefID: "library.bulk-metadata-fetch", Plugin: "library", Status: "running",
		Params: `{}`, ActorUserID: &viewer.ID, QueuedAt: now,
	}))
	srv.scanStandDownGateOverride = &sdGate{}
	reg := capOpReg(t)
	require.NoError(t, srv.RegisterBulkMetadataFetchOp(reg))
	def, ok := reg.Def("library.bulk-metadata-fetch")
	require.True(t, ok)

	run := func(t *testing.T, params any) (rep *recordingReporter, runErr error) {
		t.Helper()
		body, merr := json.Marshal(params)
		require.NoError(t, merr)
		rep = &recordingReporter{sdReporter: sdReporter{id: "op-select-none"}}
		func() {
			// The whole-library path goes on to use services this harness does not
			// wire; reaching it is what is being observed, not how it ends.
			defer func() {
				if r := recover(); r != nil {
					runErr = nil
				}
			}()
			runErr = def.Run(context.Background(), body, rep)
		}()
		return rep, runErr
	}

	t.Run("a per-user filter that matches none of this user's books", func(t *testing.T) {
		rep, err := run(t, map[string]any{"selection": operations.SelectionSpec{Filter: &operations.FilterSpec{
			FieldFilters: []operations.FieldFilter{{Field: "read_status", Value: "finished"}}}}})
		require.NoError(t, err, "a selection that resolved to nothing is a finished run, not a failure")
		assert.True(t, rep.said("resolved to no books"), "msgs: %v", rep.msgs)
		assert.False(t, rep.said("loading books"), "the whole-library fetch must not start; msgs: %v", rep.msgs)
	})

	t.Run("a book-global filter that matches nothing", func(t *testing.T) {
		rep, err := run(t, map[string]any{"selection": operations.SelectionSpec{Filter: &operations.FilterSpec{
			FieldFilters: []operations.FieldFilter{{Field: "title", Value: "No Such Gotham Title"}}}}})
		require.NoError(t, err)
		assert.True(t, rep.said("resolved to no books"), "msgs: %v", rep.msgs)
		assert.False(t, rep.said("loading books"), "msgs: %v", rep.msgs)
	})

	t.Run("an empty selection is still the whole library", func(t *testing.T) {
		rep, _ := run(t, map[string]any{})
		assert.True(t, rep.said("loading books"), "the whole-library fetch must start; msgs: %v", rep.msgs)
		assert.False(t, rep.said("resolved to no books"), "msgs: %v", rep.msgs)
	})
}

func TestBulkFetchSelectionIsWholeLibrary(t *testing.T) {
	assert.True(t, bulkFetchSelectionIsWholeLibrary(operations.SelectionSpec{}))
	assert.False(t, bulkFetchSelectionIsWholeLibrary(operations.SelectionSpec{BookIDs: []string{"b1"}}))
	assert.False(t, bulkFetchSelectionIsWholeLibrary(operations.SelectionSpec{Filter: &operations.FilterSpec{}}),
		"even an empty filter is a filter: it was given")
}

// The op judges a per-user selection by the rows of the user who enqueued it, so a
// byte-identical request from another user must not be folded into the first user's
// active run (it would be judged by their rows). The def opts into DedupeSameActorOnly;
// the same user asking twice still joins their own run.
func TestBulkFetchOp_IdenticalPerUserSelectionsFromTwoUsersGetTheirOwnRuns(t *testing.T) {
	srv, _, _, _, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()
	store, ok := database.GetGlobalStore().(*database.PebbleStore)
	require.True(t, ok, "expected a PebbleStore")
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	viewer, err := store.GetUserByUsername("viewer")
	require.NoError(t, err)

	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, srv.RegisterBulkMetadataFetchOp(reg))
	def, ok := reg.Def("library.bulk-metadata-fetch")
	require.True(t, ok)
	require.True(t, def.DedupeSameActorOnly)

	params := map[string]any{"selection": operations.SelectionSpec{Filter: &operations.FilterSpec{
		FieldFilters: []operations.FieldFilter{{Field: "read_status", Value: "unstarted"}}}}}
	as := func(userID string) []opsregistry.EnqueueOption {
		return []opsregistry.EnqueueOption{opsregistry.WithActor(userID), opsregistry.WithActorPermissions([]string{"library.edit_metadata"})}
	}
	enqueue := func(userID string) string {
		t.Helper()
		id, eerr := reg.EnqueueOp(context.Background(), "library.bulk-metadata-fetch", params, as(userID)...)
		require.NoError(t, eerr)
		return id
	}

	first := enqueue(admin.ID)
	second := enqueue(viewer.ID)
	assert.NotEqual(t, first, second, "the second user's selection was folded into the first user's run")
	for id, want := range map[string]string{first: admin.ID, second: viewer.ID} {
		row, gerr := store.GetOperationV2(id)
		require.NoError(t, gerr)
		require.NotNil(t, row.ActorUserID)
		assert.Equal(t, want, *row.ActorUserID)
	}
	assert.Equal(t, first, enqueue(admin.ID), "the same user asking again still joins their own run")
}
