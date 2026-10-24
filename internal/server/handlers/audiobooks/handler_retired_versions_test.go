// file: internal/server/handlers/audiobooks/handler_retired_versions_test.go
// version: 1.0.0
// guid: 3f0b9d52-6c1e-4a87-b2d4-9e5a17c80f36
// last-edited: 2026-10-23

// Tests for the retired-versions read and restore endpoints. The store is the
// generated AudiobooksStore mock wrapped in an in-memory fake that adds the
// optional capabilities (retired reader and writer, version reader and
// deleter) the way the production store does.

package audiobookshandler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	audiobooksmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks/mocks"
)

// fakeRetiredStore is a tiny retired-versions backend: versions by ID, retired
// rows per holder, books by ID. UnretireBookFiles records its arguments and
// removes the rows it moved, the way the real primitive does.
type fakeRetiredStore struct {
	audiobookshandler.AudiobooksStore
	books    map[string]*database.Book
	versions map[string]*database.BookVersion
	rows     map[string][]database.BookFile // holder ID -> retired rows

	unretireErr   error
	unretireCalls []unretireCall
	deleted       []string
	deleteErr     error
}

type unretireCall struct {
	moves      []database.BookFileMove
	backTo     string
	versionID  string
	versionIDs map[string]string
}

func (s *fakeRetiredStore) GetBookByID(id string) (*database.Book, error) { return s.books[id], nil }

func (s *fakeRetiredStore) GetBookVersion(id string) (*database.BookVersion, error) {
	return s.versions[id], nil
}

func (s *fakeRetiredStore) GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error) {
	var out []database.BookVersion
	for _, v := range s.versions {
		if v.BookID == bookID {
			out = append(out, *v)
		}
	}
	return out, nil
}

func (s *fakeRetiredStore) DeleteBookVersion(id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = append(s.deleted, id)
	delete(s.versions, id)
	return nil
}

func (s *fakeRetiredStore) GetBookFilesIncludingRetired(string) ([]database.BookFile, error) {
	return nil, nil
}
func (s *fakeRetiredStore) GetRetiredBookFiles(holder string) ([]database.BookFile, error) {
	return s.rows[holder], nil
}
func (s *fakeRetiredStore) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return nil, nil
}
func (s *fakeRetiredStore) GetRetiredVersionsFromBook(string) ([]database.BookVersion, error) {
	return nil, nil
}
func (s *fakeRetiredStore) GetBookFileByPIDIncludingRetired(string) (*database.BookFile, error) {
	return nil, nil
}
func (s *fakeRetiredStore) GetBookFileByPathIncludingRetired(string) (*database.BookFile, error) {
	return nil, nil
}

func (s *fakeRetiredStore) RetireBookFiles([]database.BookFileMove, string, string) error {
	return errors.New("not used")
}
func (s *fakeRetiredStore) RehomeRetiredVersion(string, string) error {
	return errors.New("not used")
}
func (s *fakeRetiredStore) UnretireBookFiles(moves []database.BookFileMove, backTo, versionID string, before map[string]string) error {
	s.unretireCalls = append(s.unretireCalls, unretireCall{moves, backTo, versionID, before})
	if s.unretireErr != nil {
		return s.unretireErr
	}
	moved := map[string]bool{}
	for _, mv := range moves {
		for _, id := range mv.FileIDs {
			moved[id] = true
		}
	}
	var keep []database.BookFile
	for _, f := range s.rows[moves[0].SourceBookID] {
		if !moved[f.ID] {
			keep = append(keep, f)
		}
	}
	s.rows[moves[0].SourceBookID] = keep
	return nil
}

func boolPtr(b bool) *bool { return &b }

// newRetiredFixture: holder "h1" with retired version "v-new" (two rows) and
// "v-old" (one row), both retired out of the trashed shell "shell1"; "v-gone"
// retired out of the purged shell "shell-purged"; "v-empty" is retired with no
// rows; "v-alt" is an ordinary alt version.
func newRetiredFixture() *fakeRetiredStore {
	t1 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	return &fakeRetiredStore{
		books: map[string]*database.Book{
			"h1":     {ID: "h1", Title: "Holder"},
			"shell1": {ID: "shell1", Title: "Shell Title", MarkedForDeletion: boolPtr(true)},
			"live2":  {ID: "live2", Title: "Live Shell"},
		},
		versions: map[string]*database.BookVersion{
			"v-new":   {ID: "v-new", BookID: "h1", Status: database.BookVersionStatusRetired, RetiredFromBookID: "shell1", RetireOpID: "op1", RetiredAt: &t2, Source: "retired_book", SourceOriginalPath: "/lib/shell"},
			"v-old":   {ID: "v-old", BookID: "h1", Status: database.BookVersionStatusRetired, RetiredFromBookID: "shell1", RetiredAt: &t1},
			"v-gone":  {ID: "v-gone", BookID: "h1", Status: database.BookVersionStatusRetired, RetiredFromBookID: "shell-purged"},
			"v-live":  {ID: "v-live", BookID: "h1", Status: database.BookVersionStatusRetired, RetiredFromBookID: "live2"},
			"v-empty": {ID: "v-empty", BookID: "h1", Status: database.BookVersionStatusRetired, RetiredFromBookID: "shell1"},
			"v-alt":   {ID: "v-alt", BookID: "h1", Status: database.BookVersionStatusAlt},
			"v-other": {ID: "v-other", BookID: "h2", Status: database.BookVersionStatusRetired, RetiredFromBookID: "shell1"},
		},
		rows: map[string][]database.BookFile{
			"h1": {
				{ID: "f1", BookID: "h1", FilePath: "/lib/shell/01.m4b", Retired: true, VersionID: "v-new", FileSize: 100, Duration: 60, TrackNumber: 1},
				{ID: "f2", BookID: "h1", FilePath: "/lib/shell/02.m4b", Retired: true, VersionID: "v-new", FileSize: 200, Duration: 90, TrackNumber: 2, Missing: true},
				{ID: "f3", BookID: "h1", FilePath: "/lib/old/01.m4b", Retired: true, VersionID: "v-old", FileSize: 50, Duration: 30},
				{ID: "f4", BookID: "h1", FilePath: "/lib/purged/01.m4b", Retired: true, VersionID: "v-gone", FileSize: 5},
				{ID: "f5", BookID: "h1", FilePath: "/lib/live2/01.m4b", Retired: true, VersionID: "v-live", FileSize: 5},
			},
		},
	}
}

func retiredHandler(t *testing.T, fake *fakeRetiredStore) *audiobookshandler.Handler {
	t.Helper()
	h, _ := newHandlerWithStore(t, func(m *audiobooksmocks.MockAudiobooksStore) audiobookshandler.AudiobooksStore {
		fake.AudiobooksStore = m
		return fake
	})
	return h
}

func versionParams(id, vid string) gin.Params {
	return gin.Params{{Key: "id", Value: id}, {Key: "vid", Value: vid}}
}

func TestListRetiredVersions(t *testing.T) {
	type respT struct {
		Data struct {
			Count    int `json:"count"`
			Versions []struct {
				ID                string `json:"id"`
				RetiredFromBookID string `json:"retired_from_book_id"`
				RetiredFromTitle  string `json:"retired_from_title"`
				ShellExists       bool   `json:"shell_exists"`
				FileCount         int    `json:"file_count"`
				TotalSize         int64  `json:"total_size"`
				TotalDuration     int    `json:"total_duration"`
				Files             []struct {
					ID      string `json:"id"`
					Missing bool   `json:"missing"`
				} `json:"files"`
			} `json:"versions"`
		} `json:"data"`
	}

	t.Run("lists retired versions with their rows, newest first", func(t *testing.T) {
		h := retiredHandler(t, newRetiredFixture())
		c, w := newCtx("GET", "/audiobooks/h1/retired-versions", nil, p("id", "h1"))
		h.ListRetiredVersions(c)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
		}
		var env respT
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// v-empty has no rows, v-alt is not retired, v-other belongs to h2.
		var ids []string
		for _, v := range env.Data.Versions {
			ids = append(ids, v.ID)
		}
		want := map[string]bool{"v-new": true, "v-old": true, "v-gone": true, "v-live": true}
		if env.Data.Count != 4 || len(ids) != 4 {
			t.Fatalf("want 4 versions, got %v (%s)", ids, w.Body.String())
		}
		for _, id := range ids {
			if !want[id] {
				t.Fatalf("unexpected version %q in %v", id, ids)
			}
		}
		if ids[0] != "v-new" || ids[1] != "v-old" {
			t.Fatalf("want newest retirement first (v-new, v-old, ...), got %v", ids)
		}
		first := env.Data.Versions[0]
		if first.FileCount != 2 || first.TotalSize != 300 || first.TotalDuration != 150 || len(first.Files) != 2 {
			t.Fatalf("v-new aggregates wrong: %+v", first)
		}
		if !first.Files[1].Missing {
			t.Fatalf("missing flag not carried: %+v", first.Files)
		}
		if first.RetiredFromBookID != "shell1" || first.RetiredFromTitle != "Shell Title" || !first.ShellExists {
			t.Fatalf("shell fields wrong: %+v", first)
		}
		for _, v := range env.Data.Versions {
			if v.ID == "v-gone" && (v.ShellExists || v.RetiredFromTitle != "") {
				t.Fatalf("purged shell must report shell_exists=false: %+v", v)
			}
		}
	})

	t.Run("a book with none answers an empty list, not null", func(t *testing.T) {
		fake := newRetiredFixture()
		fake.books["h3"] = &database.Book{ID: "h3"}
		h := retiredHandler(t, fake)
		c, w := newCtx("GET", "/audiobooks/h3/retired-versions", nil, p("id", "h3"))
		h.ListRetiredVersions(c)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", w.Code)
		}
		var raw struct {
			Data struct {
				Versions json.RawMessage `json:"versions"`
				Count    int             `json:"count"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if string(raw.Data.Versions) != "[]" || raw.Data.Count != 0 {
			t.Fatalf("want [] and 0, got %s / %d", raw.Data.Versions, raw.Data.Count)
		}
	})

	t.Run("unknown book is 404", func(t *testing.T) {
		h := retiredHandler(t, newRetiredFixture())
		c, w := newCtx("GET", "/audiobooks/nope/retired-versions", nil, p("id", "nope"))
		h.ListRetiredVersions(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d", w.Code)
		}
	})

	t.Run("a store without the capabilities fails closed", func(t *testing.T) {
		h, _ := newHandlerWithStore(t, nil)
		c, w := newCtx("GET", "/audiobooks/h1/retired-versions", nil, p("id", "h1"))
		h.ListRetiredVersions(c)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("want 500 (never an empty list), got %d (%s)", w.Code, w.Body.String())
		}
	})
}

func TestRestoreRetiredVersion(t *testing.T) {
	t.Run("moves only that version's rows back to the shell and deletes the version", func(t *testing.T) {
		fake := newRetiredFixture()
		h := retiredHandler(t, fake)
		c, w := newCtx("POST", "/audiobooks/h1/retired-versions/v-new/restore", nil, versionParams("h1", "v-new"))
		h.RestoreRetiredVersion(c)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
		}
		if len(fake.unretireCalls) != 1 {
			t.Fatalf("want one UnretireBookFiles call, got %d", len(fake.unretireCalls))
		}
		call := fake.unretireCalls[0]
		if call.backTo != "shell1" || call.versionID != "v-new" {
			t.Fatalf("wrong destination or version: %+v", call)
		}
		if len(call.moves) != 1 || call.moves[0].SourceBookID != "h1" ||
			len(call.moves[0].FileIDs) != 2 || call.moves[0].FileIDs[0] != "f1" || call.moves[0].FileIDs[1] != "f2" {
			t.Fatalf("must move exactly v-new's rows f1,f2 out of the holder, got %+v", call.moves)
		}
		if len(fake.deleted) != 1 || fake.deleted[0] != "v-new" {
			t.Fatalf("want v-new deleted, got %v", fake.deleted)
		}
		var env struct {
			Data struct {
				RestoredFiles int `json:"restored_files"`
			} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if env.Data.RestoredFiles != 2 {
			t.Fatalf("want restored_files=2, got %s", w.Body.String())
		}
		// The other versions' rows stay retired.
		if len(fake.rows["h1"]) != 3 {
			t.Fatalf("other versions' rows must stay retired, got %d rows", len(fake.rows["h1"]))
		}
	})

	t.Run("a retry after a failed delete finishes the job without a second move", func(t *testing.T) {
		fake := newRetiredFixture()
		fake.deleteErr = errors.New("boom")
		h := retiredHandler(t, fake)
		c, w := newCtx("POST", "/x", nil, versionParams("h1", "v-new"))
		h.RestoreRetiredVersion(c)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("want 500 when the delete fails, got %d", w.Code)
		}
		fake.deleteErr = nil
		c, w = newCtx("POST", "/x", nil, versionParams("h1", "v-new"))
		h.RestoreRetiredVersion(c)
		if w.Code != http.StatusOK {
			t.Fatalf("retry: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		if len(fake.unretireCalls) != 1 {
			t.Fatalf("the retry must not move rows again, got %d calls", len(fake.unretireCalls))
		}
		if len(fake.deleted) != 1 {
			t.Fatalf("retry must delete the empty version, got %v", fake.deleted)
		}
	})

	refusals := []struct {
		name   string
		holder string
		vid    string
		want   int
	}{
		{"unknown version", "h1", "nope", http.StatusNotFound},
		{"version of another book", "h1", "v-other", http.StatusBadRequest},
		{"not a retired version", "h1", "v-alt", http.StatusBadRequest},
		{"shell was purged", "h1", "v-gone", http.StatusConflict},
		{"shell is live, not in the trash", "h1", "v-live", http.StatusConflict},
	}
	for _, tc := range refusals {
		t.Run("refuses: "+tc.name, func(t *testing.T) {
			fake := newRetiredFixture()
			h := retiredHandler(t, fake)
			c, w := newCtx("POST", "/x", nil, versionParams(tc.holder, tc.vid))
			h.RestoreRetiredVersion(c)
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d (%s)", tc.want, w.Code, w.Body.String())
			}
			if len(fake.unretireCalls) != 0 || len(fake.deleted) != 0 {
				t.Fatalf("a refused restore must write nothing: %+v deleted=%v", fake.unretireCalls, fake.deleted)
			}
		})
	}

	t.Run("a store conflict maps to 409 and keeps the version", func(t *testing.T) {
		fake := newRetiredFixture()
		fake.unretireErr = database.ErrBookFileNotRetired
		h := retiredHandler(t, fake)
		c, w := newCtx("POST", "/x", nil, versionParams("h1", "v-new"))
		h.RestoreRetiredVersion(c)
		if w.Code != http.StatusConflict {
			t.Fatalf("want 409, got %d (%s)", w.Code, w.Body.String())
		}
		if len(fake.deleted) != 0 {
			t.Fatalf("version must survive a failed restore, deleted %v", fake.deleted)
		}
	})

	t.Run("a store without the capabilities fails closed", func(t *testing.T) {
		h, _ := newHandlerWithStore(t, nil)
		c, w := newCtx("POST", "/x", nil, versionParams("h1", "v-new"))
		h.RestoreRetiredVersion(c)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("want 500, got %d", w.Code)
		}
	})
}
