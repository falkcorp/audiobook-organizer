// file: internal/plugins/maintenance/regroup_apply_nothing_written_test.go
// version: 1.0.0
// guid: 0b7f3e52-9a14-4d68-b3c1-6e2a85d9f470
// last-edited: 2026-10-18

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

// These pin the CLASSIFICATION the review handler's hold-or-release decision rests on:
// an apply function returns an error wrapped by database.NothingWritten only where no
// persistent write can have landed, and every other failure is unmarked, so the
// handler HOLDS the item (see database.NothingWrittenError). The handler's own tests
// use fakes and prove only the plumbing; the risk is a real apply function marking a
// failure clean after it has written, or leaving a clean one unmarked.

// stubCombiner returns err from CombineBooks without touching a store.
type stubCombiner struct{ err error }

func (c stubCombiner) CombineBooks([]string, string, *merge.CombineOverride) (*merge.CombineResult, error) {
	return nil, c.err
}

func mkApplyBooks(t *testing.T, store database.Store, folder string, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = ulid.Make().String()
		_, err := store.CreateBook(&database.Book{
			ID: ids[i], Title: "Edition", Format: "mp3",
			FilePath: folder + "/ed" + string(rune('1'+i)) + ".mp3",
		})
		require.NoError(t, err)
	}
	return ids
}

func TestApplyMultidisc_NothingWrittenClassification(t *testing.T) {
	store := newApplyTestStore(t)
	ids := mkApplyBooks(t, store, "/lib/nw-md", 2)
	item := multidiscItem(t, "/lib/nw-md", ids)

	t.Run("bad payload is nothing written", func(t *testing.T) {
		err := ApplyMultidisc(store, stubCombiner{})(context.Background(), database.ReviewItem{ID: "x", Payload: "{not json"})
		require.Error(t, err)
		assert.True(t, database.IsNothingWritten(err), "a decode failure precedes every write: %v", err)
	})
	t.Run("cancelled context is nothing written", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := ApplyMultidisc(store, stubCombiner{})(ctx, item)
		require.ErrorIs(t, err, context.Canceled)
		assert.True(t, database.IsNothingWritten(err))
	})
	t.Run("the iTunes guard refusal is nothing written", func(t *testing.T) {
		refusal := &merge.ITunesProtectedError{BookID: ids[0], Path: "/srv/media/x.m4b"}
		err := ApplyMultidisc(store, stubCombiner{err: refusal})(context.Background(), item)
		require.ErrorIs(t, err, merge.ErrITunesProtected)
		assert.True(t, database.IsNothingWritten(err), "the guard runs before CombineBooks' first write: %v", err)
	})
	t.Run("any other combine failure may have written", func(t *testing.T) {
		for name, cause := range map[string]error{
			"plain":             errors.New("move files -> survivor: disk full"),
			"book not found":    &merge.BookNotFoundError{BookID: ids[1]}, // also CombineBooks' post-move soft-delete step
			"soft-deleted":      &merge.SoftDeletedInputError{BookID: ids[1]},
			"journal refusal":   errors.New("combine refused: undo journal could not be written"),
			"context cancelled": context.Canceled,
		} {
			err := ApplyMultidisc(store, stubCombiner{err: cause})(context.Background(), item)
			require.ErrorIs(t, err, cause, name)
			assert.False(t, database.IsNothingWritten(err), "%s: an opaque CombineBooks failure must be held: %v", name, err)
		}
	})
}

func TestApplyDuplicateOf_NothingWrittenClassification(t *testing.T) {
	store := newApplyTestStore(t)
	canonical, debris := "zzz"+ulid.Make().String(), "aaa"+ulid.Make().String()
	mkDupBook(t, store, canonical, "The Real Book", "/lib/nw-dup/real.mp3")
	mkDupBook(t, store, debris, "Debris", "/lib/nw-dup/debris.mp3")
	item := duplicateOfItem(t, "/lib/nw-dup", []string{debris})
	cands := &fakeCandidates{byEntity: map[string][]database.DedupCandidate{debris: {pendingCand(debris, canonical)}}}

	t.Run("no nomination is nothing written", func(t *testing.T) {
		err := ApplyDuplicateOf(store, stubCombiner{}, &fakeCandidates{})(context.Background(), item)
		require.Error(t, err)
		assert.True(t, database.IsNothingWritten(err), "refusing to guess a survivor precedes every write: %v", err)
	})
	t.Run("a candidate lookup failure is nothing written", func(t *testing.T) {
		err := ApplyDuplicateOf(store, stubCombiner{}, &fakeCandidates{err: errors.New("store down")})(context.Background(), item)
		require.Error(t, err)
		assert.True(t, database.IsNothingWritten(err))
	})
	t.Run("the iTunes guard refusal is nothing written", func(t *testing.T) {
		refusal := &merge.ITunesProtectedError{BookID: canonical, Path: "/srv/media/x.m4b"}
		err := ApplyDuplicateOf(store, stubCombiner{err: refusal}, cands)(context.Background(), item)
		require.ErrorIs(t, err, merge.ErrITunesProtected)
		assert.True(t, database.IsNothingWritten(err))
	})
	t.Run("a merge failing after the combine began may have written", func(t *testing.T) {
		err := ApplyDuplicateOf(store, stubCombiner{err: errors.New("soft-delete absorbed: boom")}, cands)(context.Background(), item)
		require.Error(t, err)
		assert.False(t, database.IsNothingWritten(err), "a partial merge must be held: %v", err)
	})
}

// failingModifyStore lets the first failAfter ModifyBook calls through to the real
// store and fails the next one. With commitThenFail it runs the real write and THEN
// reports the failure (a write whose outcome the caller cannot know); otherwise it
// fails without running fn or writing.
type failingModifyStore struct {
	*database.PebbleStore
	calls          int
	failAfter      int
	commitThenFail bool
	armed          bool
}

func (s *failingModifyStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.calls++
	if s.armed && s.calls == s.failAfter+1 {
		if s.commitThenFail {
			if _, err := s.PebbleStore.ModifyBook(id, fn); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("synthetic store failure")
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func newPebbleForApply(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestApplyVersionGroup_NothingWrittenClassification(t *testing.T) {
	t.Run("a cross-group refusal precedes every write", func(t *testing.T) {
		store := newPebbleForApply(t)
		g1, g2 := ulid.Make().String(), ulid.Make().String()
		a, b := ulid.Make().String(), ulid.Make().String()
		for _, id := range []string{a, b} {
			_, err := store.CreateBook(&database.Book{ID: id, Title: "T", Format: "mp3", FilePath: "/lib/nw-vg/" + id + ".mp3"})
			require.NoError(t, err)
		}
		putInGroup(t, store, a, g1, true)
		putInGroup(t, store, b, g2, true)
		err := ApplyVersionGroup(store)(context.Background(), versionGroupItem(t, "/lib/nw-vg", []string{a, b}))
		require.Error(t, err)
		assert.True(t, database.IsNothingWritten(err), "%v", err)
	})
	t.Run("a bad payload precedes every write", func(t *testing.T) {
		err := ApplyVersionGroup(newPebbleForApply(t))(context.Background(), database.ReviewItem{ID: "x", Payload: "{not json"})
		require.Error(t, err)
		assert.True(t, database.IsNothingWritten(err))
	})
	t.Run("the first member's link failing before its write is nothing written", func(t *testing.T) {
		real := newPebbleForApply(t)
		ids := mkApplyBooks(t, real, "/lib/nw-vg1", 2)
		store := &failingModifyStore{PebbleStore: real, failAfter: 0, armed: true}
		err := ApplyVersionGroup(store)(context.Background(), versionGroupItem(t, "/lib/nw-vg1", ids))
		require.Error(t, err)
		assert.True(t, database.IsNothingWritten(err), "no member was written: %v", err)
		for _, id := range ids {
			b, gerr := real.GetBookByID(id)
			require.NoError(t, gerr)
			assert.Nil(t, b.VersionGroupID, "member %s must be untouched", id)
		}
	})
	t.Run("a later member's link failing after the first was written may have written", func(t *testing.T) {
		real := newPebbleForApply(t)
		ids := mkApplyBooks(t, real, "/lib/nw-vg2", 3)
		store := &failingModifyStore{PebbleStore: real, failAfter: 1, armed: true}
		err := ApplyVersionGroup(store)(context.Background(), versionGroupItem(t, "/lib/nw-vg2", ids))
		require.Error(t, err)
		assert.False(t, database.IsNothingWritten(err), "the first member is already linked: %v", err)
		linked := 0
		for _, id := range ids {
			b, gerr := real.GetBookByID(id)
			require.NoError(t, gerr)
			if b.VersionGroupID != nil {
				linked++
			}
		}
		assert.Equal(t, 1, linked, "the partial state this classification exists to hold")
	})
	t.Run("a link whose write committed but reported failure may have written", func(t *testing.T) {
		real := newPebbleForApply(t)
		ids := mkApplyBooks(t, real, "/lib/nw-vg3", 2)
		store := &failingModifyStore{PebbleStore: real, failAfter: 0, commitThenFail: true, armed: true}
		err := ApplyVersionGroup(store)(context.Background(), versionGroupItem(t, "/lib/nw-vg3", ids))
		require.Error(t, err)
		assert.False(t, database.IsNothingWritten(err), "an attempted write is held whatever it reported: %v", err)
	})
}

func reviewReq(t *testing.T, fn gin.HandlerFunc, method, url string, body any, id string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, url, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: id}}
	fn(c)
	return w
}

// 🔴 END TO END, with the real ApplyVersionGroup behind the real review handler and
// store: a version-group apply that fails after its first link write is HELD (409
// REVIEW_APPLY_PARTIAL, item still applying with the claim blanked, nothing else may
// be decided over it), and approving it again retries and finishes. A failure before
// any write is released as before.
func TestApplyVersionGroup_FailureAfterLinkIsHeldAndReapproveRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	real := newPebbleForApply(t)
	ids := mkApplyBooks(t, real, "/lib/e2e-vg", 3)
	payload, err := json.Marshal(regroupPayload{Folder: "/lib/e2e-vg", MemberBookIDs: ids, Confidence: "high"})
	require.NoError(t, err)
	item, err := real.UpsertReviewItem(database.ReviewItem{
		Kind: "regroup.version-group", DedupKey: "e2e-vg", FolderRef: "/lib/e2e-vg", Summary: "s", Payload: string(payload),
	})
	require.NoError(t, err)

	flaky := &failingModifyStore{PebbleStore: real, failAfter: 1, armed: true}
	h := reviewhandler.New(real, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionVersionGroup, ApplyVersionGroup(flaky))
	approve := func(body any) *httptest.ResponseRecorder {
		return reviewReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+item.ID+"/approve", body, item.ID)
	}

	w := approve(map[string]any{"action": itunesservice.ActionVersionGroup})
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "REVIEW_APPLY_PARTIAL")
	assert.Contains(t, w.Body.String(), "abandon-apply")
	got, err := real.GetReviewItem(item.ID)
	require.NoError(t, err)
	assert.Equal(t, database.ReviewStatusApplying, got.Status)
	assert.Empty(t, got.ApplyClaimToken)
	assert.Empty(t, got.ApplyClaimInstance)
	assert.Equal(t, itunesservice.ActionVersionGroup, got.ApplyClaimAction)

	// Nothing else may be decided over the half-linked library.
	w = reviewReq(t, h.RejectReviewItem, http.MethodPost, "/review/items/"+item.ID+"/reject", nil, item.ID)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "REVIEW_APPLY_INTERRUPTED")

	// Re-approve: the retry skips the linked member, links the rest and finishes.
	flaky.armed = false
	w = approve(nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got, err = real.GetReviewItem(item.ID)
	require.NoError(t, err)
	assert.Equal(t, database.ReviewStatusApplied, got.Status)
	var group string
	for _, id := range ids {
		b, gerr := real.GetBookByID(id)
		require.NoError(t, gerr)
		require.NotNil(t, b.VersionGroupID, "member %s linked after the retry", id)
		if group == "" {
			group = *b.VersionGroupID
		}
		assert.Equal(t, group, *b.VersionGroupID)
	}
}

func TestApplyVersionGroup_FailureBeforeAnyWriteIsReleased(t *testing.T) {
	gin.SetMode(gin.TestMode)
	real := newPebbleForApply(t)
	ids := mkApplyBooks(t, real, "/lib/e2e-vg0", 2)
	payload, err := json.Marshal(regroupPayload{Folder: "/lib/e2e-vg0", MemberBookIDs: ids, Confidence: "high"})
	require.NoError(t, err)
	item, err := real.UpsertReviewItem(database.ReviewItem{
		Kind: "regroup.version-group", DedupKey: "e2e-vg0", FolderRef: "/lib/e2e-vg0", Summary: "s", Payload: string(payload),
	})
	require.NoError(t, err)

	flaky := &failingModifyStore{PebbleStore: real, failAfter: 0, armed: true}
	h := reviewhandler.New(real, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionVersionGroup, ApplyVersionGroup(flaky))
	w := reviewReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+item.ID+"/approve",
		map[string]any{"action": itunesservice.ActionVersionGroup}, item.ID)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	got, err := real.GetReviewItem(item.ID)
	require.NoError(t, err)
	assert.Equal(t, database.ReviewStatusPending, got.Status, "a clean failure is handed back")
	assert.Empty(t, got.ApplyClaimAction)
}
