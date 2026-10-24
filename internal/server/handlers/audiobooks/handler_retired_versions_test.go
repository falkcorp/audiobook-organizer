// file: internal/server/handlers/audiobooks/handler_retired_versions_test.go
// version: 1.1.0
// guid: 3f0b9d52-6c1e-4a87-b2d4-9e5a17c80f36
// last-edited: 2026-10-23

// Tests for the retired-versions read endpoint and the restore stub (501). The store is the
// generated AudiobooksStore mock wrapped in an in-memory fake that adds the
// optional capabilities (retired reader and writer, version reader and
// deleter) the way the production store does.

package audiobookshandler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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

	unretireCalls []unretireCall
	deleted       []string
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
				ShellInTrash      bool   `json:"shell_in_trash"`
				FileCount         int    `json:"file_count"`
				TotalSize         int64  `json:"total_size"`
				TotalDuration     int    `json:"total_duration"`
				Files             []struct {
					ID       string `json:"id"`
					FilePath string `json:"file_path"`
					Missing  bool   `json:"missing"`
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
			if v.ID == "v-gone" && (v.ShellExists || v.ShellInTrash || v.RetiredFromTitle != "") {
				t.Fatalf("purged shell must report shell_exists=false and shell_in_trash=false: %+v", v)
			}
		}
	})

	t.Run("reports whether the shell is still in the trash", func(t *testing.T) {
		h := retiredHandler(t, newRetiredFixture())
		c, w := newCtx("GET", "/audiobooks/h1/retired-versions", nil, p("id", "h1"))
		h.ListRetiredVersions(c)
		var env respT
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := map[string][2]bool{}
		for _, v := range env.Data.Versions {
			got[v.ID] = [2]bool{v.ShellExists, v.ShellInTrash}
		}
		// shell1 exists and is trashed; live2 exists and is not; shell-purged is gone.
		want := map[string][2]bool{
			"v-new":  {true, true},
			"v-old":  {true, true},
			"v-live": {true, false},
			"v-gone": {false, false},
		}
		for id, w := range want {
			if got[id] != w {
				t.Errorf("version %s: (shell_exists, shell_in_trash) = %v, want %v", id, got[id], w)
			}
		}
	})

	t.Run("lists a version's files in disc, track, path order", func(t *testing.T) {
		fake := newRetiredFixture()
		fake.books["h4"] = &database.Book{ID: "h4"}
		fake.versions["v-ord"] = &database.BookVersion{ID: "v-ord", BookID: "h4", Status: database.BookVersionStatusRetired, RetiredFromBookID: "shell1"}
		// Stored (and returned by the fake) out of order.
		fake.rows["h4"] = []database.BookFile{
			{ID: "r1", BookID: "h4", FilePath: "/x/d2t1.mp3", Retired: true, VersionID: "v-ord", DiscNumber: 2, TrackNumber: 1},
			{ID: "r2", BookID: "h4", FilePath: "/x/d1t3.mp3", Retired: true, VersionID: "v-ord", DiscNumber: 1, TrackNumber: 3},
			{ID: "r3", BookID: "h4", FilePath: "/x/b.mp3", Retired: true, VersionID: "v-ord", DiscNumber: 1, TrackNumber: 2},
			{ID: "r4", BookID: "h4", FilePath: "/x/a.mp3", Retired: true, VersionID: "v-ord", DiscNumber: 1, TrackNumber: 2},
		}
		h := retiredHandler(t, fake)
		c, w := newCtx("GET", "/audiobooks/h4/retired-versions", nil, p("id", "h4"))
		h.ListRetiredVersions(c)
		var env respT
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || len(env.Data.Versions) != 1 {
			t.Fatalf("decode/count: %v (%s)", err, w.Body.String())
		}
		var got []string
		for _, f := range env.Data.Versions[0].Files {
			got = append(got, f.ID)
		}
		if strings.Join(got, ",") != "r4,r3,r2,r1" {
			t.Fatalf("want r4,r3,r2,r1 (d1t2 a, d1t2 b, d1t3, d2t1), got %v", got)
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

// The restore route answers 501 Not Implemented and changes nothing, whatever
// it is asked: a version that could be restored, one that could not, an unknown
// one, and a store with no retired-row capabilities (the stub reads nothing). A
// direct restore would clear every row's VersionID and delete the version row
// with no journal entry, so it must not run until restores are journaled.
func TestRestoreRetiredVersion_IsNotImplementedAndWritesNothing(t *testing.T) {
	cases := []struct {
		name, holder, vid string
	}{
		{"a restorable version", "h1", "v-new"},
		{"a version whose shell was purged", "h1", "v-gone"},
		{"a version of another book", "h1", "v-other"},
		{"an unknown version", "h1", "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newRetiredFixture()
			rowsBefore := len(fake.rows["h1"])
			h := retiredHandler(t, fake)
			c, w := newCtx("POST", "/x", nil, versionParams(tc.holder, tc.vid))
			h.RestoreRetiredVersion(c)
			if w.Code != http.StatusNotImplemented {
				t.Fatalf("want 501, got %d (%s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "journaled") {
				t.Errorf("the 501 must say why: %s", w.Body.String())
			}
			if len(fake.unretireCalls) != 0 || len(fake.deleted) != 0 {
				t.Fatalf("the stub must write nothing: unretire=%+v deleted=%v", fake.unretireCalls, fake.deleted)
			}
			if len(fake.rows["h1"]) != rowsBefore || len(fake.versions) != 7 {
				t.Fatalf("the stub changed the store: %d rows, %d versions", len(fake.rows["h1"]), len(fake.versions))
			}
		})
	}

	t.Run("a store without the capabilities still answers 501, not 500", func(t *testing.T) {
		h, _ := newHandlerWithStore(t, nil)
		c, w := newCtx("POST", "/x", nil, versionParams("h1", "v-new"))
		h.RestoreRetiredVersion(c)
		if w.Code != http.StatusNotImplemented {
			t.Fatalf("want 501, got %d", w.Code)
		}
	})
}
