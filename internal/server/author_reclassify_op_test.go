// file: internal/server/author_reclassify_op_test.go
// version: 1.0.0
// guid: 23b520a4-9079-4e61-acf1-e2caab81d462
// last-edited: 2026-10-30

package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// reclassifyTestGate is a scan stand-down that records its calls and can refuse the
// acquire or lose the lease on the Nth renew.
type reclassifyTestGate struct {
	mu          sync.Mutex
	events      []string
	acquireErr  error
	loseOnRenew int // 1-based; zero never loses it
	renews      int
}

func (g *reclassifyTestGate) AcquireScanStandDown(_ context.Context, holder, reason string) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.acquireErr != nil {
		g.events = append(g.events, "acquire-refused")
		return nil, g.acquireErr
	}
	g.events = append(g.events, "acquire:"+holder+":"+reason)
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.events = append(g.events, "release")
	}, nil
}

func (g *reclassifyTestGate) RenewScanStandDown(string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.renews++
	if g.loseOnRenew > 0 && g.renews >= g.loseOnRenew {
		g.events = append(g.events, "renew-lost")
		return false
	}
	g.events = append(g.events, "renew")
	return true
}

func (g *reclassifyTestGate) snapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.events...)
}

// runReclassifyOp runs the registered op against the world's store and returns its
// error. s.scanStandDownGateOverride is set to gate when non-nil.
func runReclassifyOp(t *testing.T, s *Server, gate *reclassifyTestGate, opID string, authorID int) error {
	t.Helper()
	if gate != nil {
		s.scanStandDownGateOverride = gate
	} else {
		s.scanStandDownGateOverride = &reclassifyTestGate{}
	}
	def := relinkOpDef(t, s.RegisterAuthorReclassifyOp, entities.AuthorReclassifyOpID)
	raw, err := json.Marshal(entities.AuthorReclassifyOpParams{AuthorID: authorID})
	require.NoError(t, err)
	return def.Run(context.Background(), raw, &opReporter{id: opID})
}

func changeTypes(t *testing.T, st database.OperationChangeStore, opID string) map[string]int {
	t.Helper()
	rows, err := st.GetOperationChanges(opID)
	require.NoError(t, err)
	out := map[string]int{}
	for _, r := range rows {
		out[r.ChangeType]++
	}
	return out
}

func TestAuthorReclassifyOp_DeclaresCapabilitiesPermissionAndTheMergeKey(t *testing.T) {
	def := relinkOpDef(t, newRelinkOpServer(newRelinkOpStore(t)).RegisterAuthorReclassifyOp, entities.AuthorReclassifyOpID)
	require.Equal(t, []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite}, def.Capabilities)
	require.Equal(t, []auth.Permission{auth.PermLibraryEditMetadata}, def.Permissions,
		"the op is gated by the permission the route already required")
	require.Equal(t, "entities.author-merge", def.ConcurrencyKey,
		"it rewrites book_authors and deletes authors, so it serializes with the merge")
	require.Equal(t, opsregistry.ResumeDrop, def.ResumePolicy)
}

// The reason the route became an op: the delete and the moved credits are journaled
// under the op's id, and reverting that id brings the author and every credit back.
func TestAuthorReclassifyOp_JournalsUnderItsOpIDAndTheRevertRestoresTheAuthor(t *testing.T) {
	w := newUndoWorld(t)
	s := newRelinkOpServer(w.st)
	require.NoError(t, runReclassifyOp(t, s, nil, "op-reclass-route", w.from.ID))

	gone, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	if gone != nil {
		require.NotEqual(t, w.from.ID, gone.ID, "the author is deleted")
	}
	narrator, err := w.st.GetNarratorByName(w.from.Name)
	require.NoError(t, err)
	require.NotNil(t, narrator, "a narrator of the same name exists")

	types := changeTypes(t, w.st, "op-reclass-route")
	require.Equal(t, 3, types[string(undo.ChangeTypeAuthorReclassifyCredits)], "one credits row per book, the trashed one included")
	require.Equal(t, 1, types[string(undo.ChangeTypeAuthorReclassifyDelete)])

	pre, err := preflightUndoConflicts(w.st, "op-reclass-route")
	require.NoError(t, err)
	require.Equal(t, pre.TotalChanges, pre.Safe)
	// Through the server's revert (what POST /operations/:id/revert calls), which also
	// drops the author caches.
	s.authorsCache.Set("authors-list", nil)
	s.dedupCache.Set("author-duplicates", gin.H{"stale": true})
	res, err := s.revertOperation("op-reclass-route", audiobookspkg.RevertActor{})
	require.NoError(t, err)
	require.Zero(t, res.Failed)
	w.requireRestored(t)
	require.Zero(t, s.authorsCache.Len(), "author list cache dropped")
	require.Zero(t, s.dedupCache.Len(), "dedup cache dropped")
}

func TestAuthorReclassifyOp_HoldsTheStandDownAroundTheWritesAndRenewsPerBook(t *testing.T) {
	w := newUndoWorld(t)
	gate := &reclassifyTestGate{}
	s := newRelinkOpServer(w.st)
	require.NoError(t, runReclassifyOp(t, s, gate, "op-reclass-gate", w.from.ID))
	ev := gate.snapshot()
	require.Equal(t, "acquire:op-reclass-gate:author-reclassify", ev[0], "acquired before the first write")
	require.Equal(t, "release", ev[len(ev)-1])
	renews := 0
	for _, e := range ev {
		if e == "renew" {
			renews++
		}
	}
	require.Equal(t, 3, renews, "one renew per book (3 books credit the author)")
}

func TestAuthorReclassifyOp_AScanThatWillNotParkRefusesTheRunBeforeAnyWrite(t *testing.T) {
	w := newUndoWorld(t)
	gate := &reclassifyTestGate{acquireErr: errors.New("library.scan is running")}
	err := runReclassifyOp(t, newRelinkOpServer(w.st), gate, "op-reclass-refused", w.from.ID)
	require.ErrorContains(t, err, "library.scan is running")
	require.Equal(t, []string{"acquire-refused"}, gate.snapshot())

	still, gerr := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, gerr)
	require.Equal(t, w.from.ID, still.ID, "the author was not deleted")
	for id, want := range w.before {
		got, berr := w.st.GetBookAuthors(id)
		require.NoError(t, berr)
		require.Equal(t, want, got, "book %s untouched", id)
	}
	require.Empty(t, changeTypes(t, w.st, "op-reclass-refused"), "nothing journaled")
}

func TestAuthorReclassifyOp_WithoutAnOperationIDRefusesTheRun(t *testing.T) {
	w := newUndoWorld(t)
	gate := &reclassifyTestGate{}
	err := runReclassifyOp(t, newRelinkOpServer(w.st), gate, "", w.from.ID)
	require.ErrorContains(t, err, "without an operation id")
	require.Empty(t, gate.snapshot(), "the gate is keyed by the op id, so it is never asked")
	still, gerr := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, gerr)
	require.Equal(t, w.from.ID, still.ID)
}

// A lapsed lease resumes the scanner, so the reclassify stops writing at once and
// keeps the author (a delete would strand the books not yet moved).
func TestAuthorReclassifyOp_ALostLeaseStopsBeforeTheDeleteAndKeepsTheAuthor(t *testing.T) {
	w := newUndoWorld(t)
	gate := &reclassifyTestGate{loseOnRenew: 2}
	err := runReclassifyOp(t, newRelinkOpServer(w.st), gate, "op-reclass-lease", w.from.ID)
	require.ErrorIs(t, err, entities.ErrReclassifyLeaseLost)
	require.ErrorContains(t, err, "the author was kept")

	still, gerr := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, gerr)
	require.Equal(t, w.from.ID, still.ID, "the author is kept")
	types := changeTypes(t, w.st, "op-reclass-lease")
	require.Equal(t, 1, types[string(undo.ChangeTypeAuthorReclassifyCredits)], "only the book moved before the lease went is journaled")
	require.Zero(t, types[string(undo.ChangeTypeAuthorReclassifyDelete)])
	require.Equal(t, "release", gate.snapshot()[len(gate.snapshot())-1], "the hold is released")
}

func TestAuthorReclassifyOp_AMissingAuthorFailsWithAReadableMessage(t *testing.T) {
	w := newUndoWorld(t)
	err := runReclassifyOp(t, newRelinkOpServer(w.st), nil, "op-reclass-missing", 987654)
	require.ErrorContains(t, err, "no longer exists")
}

func TestAuthorReclassifyOp_DropsTheAuthorCaches(t *testing.T) {
	w := newUndoWorld(t)
	s := newRelinkOpServer(w.st)
	s.authorsCache.Set("authors-list", nil)
	s.dedupCache.Set("author-duplicates", gin.H{"stale": true})
	require.NoError(t, runReclassifyOp(t, s, nil, "op-reclass-cache", w.from.ID))
	require.Zero(t, s.authorsCache.Len(), "author list cache dropped")
	require.Zero(t, s.dedupCache.Len(), "dedup cache dropped")
}
