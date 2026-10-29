// file: internal/server/version_lifecycle_resume_test.go
// version: 1.1.0
// guid: 6d71c936-4d00-48ec-b188-ad49586bbd4e
// last-edited: 2026-10-29

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// swapFaultStore fails the writes that leave a real swap half done: the
// book_file write of one row, or the status write of one version.
type swapFaultStore struct {
	database.Store
	failFileID  string
	failVersion string
	failStatus  string
	// failPathLookup: BookFilesAtPath cannot be completed.
	failPathLookup bool
}

func (s *swapFaultStore) BookFilesAtPath(path string) ([]database.BookFile, error) {
	if s.failPathLookup {
		return nil, database.ErrBookFilesAtPathUnavailable
	}
	return s.Store.BookFilesAtPath(path)
}

func (s *swapFaultStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if s.failFileID != "" && fileID == s.failFileID {
		return nil, errors.New("injected: book_file write failed")
	}
	return s.Store.ModifyBookFile(bookID, fileID, fn)
}

func (s *swapFaultStore) UpdateBookVersion(v *database.BookVersion) error {
	if s.failVersion != "" && v.ID == s.failVersion && v.Status == s.failStatus {
		return errors.New("injected: version status write failed")
	}
	return s.Store.UpdateBookVersion(v)
}

func (f *swapTrashFixture) rowOf(versionID string) string {
	f.t.Helper()
	rows, err := f.store.GetBookFiles(f.book.ID)
	require.NoError(f.t, err)
	for _, r := range rows {
		if r.VersionID == versionID {
			return r.ID
		}
	}
	f.t.Fatalf("no book_file row for version %s", versionID)
	return ""
}

// stuckAfterRowFailure trashes the active version while the entering alt's row
// cannot be written: the files moved and the swap is left in
// swapping_out/swapping_in. The store is healthy again on return.
func (f *swapTrashFixture) stuckAfterRowFailure() (alt *database.BookVersion) {
	f.t.Helper()
	alt, _ = f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	healthy := f.srv.store
	f.srv.store = &swapFaultStore{Store: healthy, failFileID: f.rowOf(alt.ID)}
	w := f.trash(f.active.ID)
	require.Equal(f.t, http.StatusInternalServerError, w.Code, w.Body.String())
	f.srv.store = healthy
	require.Equal(f.t, database.BookVersionStatusSwappingOut, f.version(f.active.ID).Status)
	require.Equal(f.t, database.BookVersionStatusSwappingIn, f.version(alt.ID).Status)
	return alt
}

func (f *swapTrashFixture) do(method, path string) *httptest.ResponseRecorder {
	f.t.Helper()
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func (f *swapTrashFixture) resume() *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(http.MethodPost, "/api/v1/audiobooks/"+f.book.ID+"/versions/resume-swap")
}

type swapIncompleteListing struct {
	Data struct {
		Books []struct {
			BookID        string `json:"book_id"`
			Title         string `json:"title"`
			BookPath      string `json:"book_path"`
			Resume        string `json:"resume"`
			ResumeRefusal string `json:"resume_refusal"`
			Versions      []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Files  []struct {
					Path string `json:"path"`
				} `json:"files"`
			} `json:"versions"`
		} `json:"books"`
		Count int `json:"count"`
	} `json:"data"`
}

func (f *swapTrashFixture) listUnfinished() swapIncompleteListing {
	f.t.Helper()
	w := f.do(http.MethodGet, "/api/v1/audiobooks/swap-incomplete")
	require.Equal(f.t, http.StatusOK, w.Code, w.Body.String())
	var out swapIncompleteListing
	require.NoError(f.t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	return out
}

// The swap moved its files and could not write a row. The unfinished-swap list
// names the book with its path and files and says a resume finishes it; the
// resume settles every version (the entering one is active, the leaving one is
// alt), the list empties, and the version that left can then be trashed.
func TestHandleResumeVersionSwap_FinishesAStuckSwap(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt := f.stuckAfterRowFailure()

	listing := f.listUnfinished()
	require.Equal(t, 1, listing.Data.Count)
	require.Len(t, listing.Data.Books, 1)
	b := listing.Data.Books[0]
	assert.Equal(t, f.book.ID, b.BookID)
	assert.Equal(t, "The Long Halloween", b.Title)
	assert.Equal(t, "finish", b.Resume, b.ResumeRefusal)
	statuses := map[string]string{}
	for _, v := range b.Versions {
		statuses[v.ID] = v.Status
	}
	assert.Equal(t, database.BookVersionStatusSwappingOut, statuses[f.active.ID])
	assert.Equal(t, database.BookVersionStatusSwappingIn, statuses[alt.ID])

	w := f.resume()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data struct {
			Action            string `json:"action"`
			ActiveVersionID   string `json:"active_version_id"`
			LeavingVersionID  string `json:"leaving_version_id"`
			EnteringVersionID string `json:"entering_version_id"`
			FilesMoved        int    `json:"files_moved"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "finish", body.Data.Action)
	assert.Equal(t, alt.ID, body.Data.ActiveVersionID)
	assert.Equal(t, f.active.ID, body.Data.LeavingVersionID)
	assert.Equal(t, alt.ID, body.Data.EnteringVersionID)
	assert.Zero(t, body.Data.FilesMoved, "the files had already moved")

	assert.Equal(t, database.BookVersionStatusActive, f.version(alt.ID).Status)
	assert.Equal(t, database.BookVersionStatusAlt, f.version(f.active.ID).Status)
	assert.Zero(t, f.listUnfinished().Data.Count, "the book is still listed after its swap was resumed")

	// The old active version is an ordinary alt now: trashing it works.
	w = f.trash(f.active.ID)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusTrash, f.version(f.active.ID).Status)
	assert.Equal(t, database.BookVersionStatusActive, f.version(alt.ID).Status)
}

// Only the last status write failed: the files and rows are settled, the
// leaving version reads alt and the entering one still swapping_in. Trashing the
// alt that left is refused (409 VERSION_SWAPPING, nothing written) while the
// book has a swap in flight; the resume then settles it and the trash goes
// through.
func TestHandleResumeVersionSwap_StatusOnlyLeftoverBlocksTrashUntilResumed(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt, _ := f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	healthy := f.srv.store
	f.srv.store = &swapFaultStore{Store: healthy, failVersion: alt.ID, failStatus: database.BookVersionStatusActive}
	w := f.trash(f.active.ID)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	f.srv.store = healthy
	require.Equal(t, database.BookVersionStatusAlt, f.version(f.active.ID).Status)
	require.Equal(t, database.BookVersionStatusSwappingIn, f.version(alt.ID).Status)

	before := f.snapshot()
	disk := diskTree(t, f.dir)
	w = f.trash(f.active.ID)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, codeVersionSwapping, errorCodeOf(t, w))
	assert.Equal(t, before, f.snapshot(), "a refused trash wrote a version")
	assert.Equal(t, disk, diskTree(t, f.dir))

	listing := f.listUnfinished()
	require.Equal(t, 1, listing.Data.Count)
	assert.Equal(t, "finish", listing.Data.Books[0].Resume, listing.Data.Books[0].ResumeRefusal)

	w = f.resume()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusActive, f.version(alt.ID).Status)
	assert.Zero(t, f.listUnfinished().Data.Count)

	w = f.trash(f.active.ID)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusTrash, f.version(f.active.ID).Status)
}

// A second version became active while the swap was stuck: the resume cannot
// know which one the owner wants, answers 409 SWAP_RESUME_REFUSED with the
// reason and changes nothing, in the database or on disk.
func TestHandleResumeVersionSwap_RefusesWhenAnotherVersionIsActive(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.stuckAfterRowFailure()
	other, _ := f.add(database.BookVersionStatusActive, 2*time.Hour, "Dark Victory.mp3")

	before := f.snapshot()
	disk := diskTree(t, f.dir)
	w := f.resume()
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, codeSwapResumeRefused, errorCodeOf(t, w))
	assert.Contains(t, w.Body.String(), other.ID, "the refusal does not name the other active version")
	assert.Equal(t, before, f.snapshot())
	assert.Equal(t, disk, diskTree(t, f.dir))

	listing := f.listUnfinished()
	require.Equal(t, 1, listing.Data.Count)
	assert.Empty(t, listing.Data.Books[0].Resume)
	assert.NotEmpty(t, listing.Data.Books[0].ResumeRefusal)
}

func TestHandleResumeVersionSwap_NothingToResumeAndUnknownBook(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	before := f.snapshot()

	w := f.resume()
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, codeNoSwapToResume, errorCodeOf(t, w))
	assert.Equal(t, before, f.snapshot())

	w = f.do(http.MethodPost, "/api/v1/audiobooks/no-such-book/versions/resume-swap")
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Zero(t, f.listUnfinished().Data.Count)
}

// A running merge or combine holds the lock: the resume answers 409, moves
// nothing and writes nothing; once the merge is done the same call succeeds.
func TestHandleResumeVersionSwap_MergeBusyWritesNothing(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt := f.stuckAfterRowFailure()
	before := f.snapshot()
	disk := diskTree(t, f.dir)

	prev := merge.InteractiveMergeLockWait
	merge.InteractiveMergeLockWait = 30 * time.Millisecond
	t.Cleanup(func() { merge.InteractiveMergeLockWait = prev })
	merge.LockMergeRMW()
	w := f.resume()
	merge.UnlockMergeRMW()

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, before, f.snapshot())
	assert.Equal(t, disk, diskTree(t, f.dir))

	w = f.resume()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusActive, f.version(alt.ID).Status)
}

// The static resume-swap and swap-incomplete segments register next to the
// :id / :vid parameters (gin panics at registration when they cannot), and a
// request for them reaches the resume handlers, not the parameter routes.
func TestVersionLifecycleRoutes_ResumeRoutesRegisteredNextToParameterRoutes(t *testing.T) {
	f := newSwapTrashFixture(t)
	have := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		have[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"POST /api/v1/audiobooks/:id/versions/resume-swap",
		"GET /api/v1/audiobooks/swap-incomplete",
		"POST /api/v1/audiobooks/:id/versions/:vid/restore",
		"DELETE /api/v1/audiobooks/:id/versions/:vid",
	} {
		assert.True(t, have[want], "route %q is not registered", want)
	}
	// resume-swap must not be taken for a version id by the restore/delete routes.
	w := f.do(http.MethodDelete, "/api/v1/audiobooks/"+f.book.ID+"/versions/resume-swap")
	assert.NotContains(t, w.Body.String(), "NO_SWAP_TO_RESUME")
	w = f.resume()
	assert.Equal(t, codeNoSwapToResume, errorCodeOf(t, w), strings.TrimSpace(w.Body.String()))
}

// GET needs library.view; the POST needs library.organize and not the weaker
// view or the unrelated delete permission.
func TestVersionLifecycleRoutes_ResumeSwapPermissions(t *testing.T) {
	f := setupCredGuardServer(t)
	_, viewerTok := f.viewerGoneSession(t)
	onlyOrganize := f.exactPermGoneSession(t, "resume-organize", auth.PermLibraryOrganize)
	onlyDelete := f.exactPermGoneSession(t, "resume-delete", auth.PermLibraryDelete)
	onlyView := f.exactPermGoneSession(t, "resume-view", auth.PermLibraryView)

	const list = "/api/v1/audiobooks/swap-incomplete"
	const resume = "/api/v1/audiobooks/synthetic-book/versions/resume-swap"

	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, list, nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, resume, nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	assert.Equal(t, http.StatusOK, f.do(t, onlyView, http.MethodGet, list, nil).Code)
	assert.Equal(t, http.StatusOK, f.do(t, viewerTok, http.MethodGet, list, nil).Code)
	assert.Equal(t, http.StatusForbidden, f.do(t, onlyDelete, http.MethodGet, list, nil).Code)

	// The unknown book is a 404 for a caller that holds library.organize (it
	// reached the handler) and a 403 for every other caller.
	assert.Equal(t, http.StatusNotFound, f.do(t, onlyOrganize, http.MethodPost, resume, nil).Code)
	assert.Equal(t, http.StatusForbidden, f.do(t, onlyView, http.MethodPost, resume, nil).Code)
	assert.Equal(t, http.StatusForbidden, f.do(t, onlyDelete, http.MethodPost, resume, nil).Code)
	assert.Equal(t, http.StatusForbidden, f.do(t, viewerTok, http.MethodPost, resume, nil).Code)
}

// delugeCall is one call of the Deluge follow a resume made, and whether the
// answer had already been written when it was made.
type delugeCall struct {
	from, to *database.BookVersion
	path     string
	answered bool
}

// resumeWatchingDeluge resumes the stuck swap with the Deluge follow replaced
// by a recorder.
func (f *swapTrashFixture) resumeWatchingDeluge() (*httptest.ResponseRecorder, []delugeCall) {
	f.t.Helper()
	var calls []delugeCall
	w := httptest.NewRecorder()
	prev := notifyDelugeAfterSwapResume
	notifyDelugeAfterSwapResume = func(_ interface {
		GetBookByID(id string) (*database.Book, error)
	}, from, to *database.BookVersion, path string) {
		calls = append(calls, delugeCall{from: from, to: to, path: path, answered: w.Body.Len() > 0})
	}
	f.t.Cleanup(func() { notifyDelugeAfterSwapResume = prev })
	f.srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+f.book.ID+"/versions/resume-swap", nil))
	notifyDelugeAfterSwapResume = prev
	return w, calls
}

// A finished swap tells Deluge, after the answer is written: both versions
// when both were left swapping_*.
func TestHandleResumeVersionSwap_FinishTellsDelugeAfterTheAnswer(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt := f.stuckAfterRowFailure()

	w, calls := f.resumeWatchingDeluge()

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].from)
	require.NotNil(t, calls[0].to)
	assert.Equal(t, f.active.ID, calls[0].from.ID)
	assert.Equal(t, alt.ID, calls[0].to.ID)
	assert.True(t, calls[0].answered, "Deluge was called before the answer was written")
}

// The swap got as far as the status writes with only the entering version left
// swapping_in: Deluge is still told to follow it (the entering version's
// torrent moves to the book's file), with no leaving version.
func TestHandleResumeVersionSwap_StatusOnlyFinishTellsDelugeTheEnteringVersion(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt, _ := f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	healthy := f.srv.store
	f.srv.store = &swapFaultStore{Store: healthy, failVersion: alt.ID, failStatus: database.BookVersionStatusActive}
	require.Equal(t, http.StatusInternalServerError, f.trash(f.active.ID).Code)
	f.srv.store = healthy
	require.Equal(t, database.BookVersionStatusSwappingIn, f.version(alt.ID).Status)

	w, calls := f.resumeWatchingDeluge()

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, calls, 1, "a status-only finish did not tell Deluge")
	assert.Nil(t, calls[0].from)
	require.NotNil(t, calls[0].to)
	assert.Equal(t, alt.ID, calls[0].to.ID)
	book, err := f.store.GetBookByID(f.book.ID)
	require.NoError(t, err)
	assert.Equal(t, book.FilePath, calls[0].path)
	assert.True(t, calls[0].answered)
}

// An undo puts the swap back: the files are where Deluge already has them.
func TestHandleResumeVersionSwap_UndoDoesNotTellDeluge(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusSwappingIn, time.Hour, "Year One.mp3")

	w, calls := f.resumeWatchingDeluge()

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, calls)
}

// A path lookup the plan could not complete is a fault of the library's, not a
// statement about the book: 500, not the 409 of a refusal, and nothing written.
func TestHandleResumeVersionSwap_APathLookupThatCannotBeCompletedAnswers500(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt := f.stuckAfterRowFailure()
	healthy := f.srv.store
	before := f.snapshot()
	disk := diskTree(t, f.dir)

	f.srv.store = &swapFaultStore{Store: healthy, failPathLookup: true}
	w := f.resume()
	f.srv.store = healthy

	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.NotEqual(t, codeSwapResumeRefused, errorCodeOf(t, w))
	assert.Equal(t, before, f.snapshot())
	assert.Equal(t, disk, diskTree(t, f.dir))

	w = f.resume()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, database.BookVersionStatusActive, f.version(alt.ID).Status)
}
