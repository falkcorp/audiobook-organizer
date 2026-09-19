// file: internal/server/handlers/reading_failclosed_test.go
// version: 1.2.0
// guid: 0a6d2e4f-8b13-4c7e-95f2-d3b8a1c6e470
// last-edited: 2026-09-19

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

type unreadableReadingStore struct{ ReadingStore }

func (unreadableReadingStore) GetUserBookState(string, string) (*database.UserBookState, error) {
	return nil, errors.New("transient read failure")
}

// An unreadable state row is a transient failure (503), not a 500, and the
// status write it would have made is not made (readstatus fails closed).
func TestReadingStatusWrites_UnreadableStateIs503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := NewReadingHandler(unreadableReadingStore{store})
	for _, tc := range []struct {
		name   string
		method string
		fn     gin.HandlerFunc
		body   string
	}{
		{"set", http.MethodPatch, h.SetBookStatus, `{"status":"finished"}`},
		{"clear", http.MethodDelete, h.ClearBookStatus, ""},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(tc.method, "/api/v1/books/b1/status", bytes.NewBufferString(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: "b1"}}
		tc.fn(c)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status with unreadable state = %d %s, want 503", tc.name, rec.Code, rec.Body.String())
		}
	}
}

type unreadablePositionsReadingStore struct{ ReadingStore }

func (unreadablePositionsReadingStore) ListUserPositionsForBook(string, string) ([]database.UserPosition, error) {
	return nil, errors.New("transient read failure")
}

// Clearing a manual status recomputes from positions; an unreadable positions
// read is transient (503), not a 500.
func TestReadingClearStatus_UnreadablePositionsIs503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := NewReadingHandler(unreadablePositionsReadingStore{store})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/books/b1/status", nil)
	c.Params = gin.Params{{Key: "id", Value: "b1"}}
	h.ClearBookStatus(c)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("clear status with unreadable positions = %d %s, want 503", rec.Code, rec.Body.String())
	}
}

// An unreadable ubs: row made every status write for the book 503 forever;
// POST /books/:id/status/repair reports it (dry run by default) and rebuilds
// it from positions with {"apply":true}.
func TestReadingRepairStatus_RebuildsAnUnreadableRow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := filepath.Join(t.TempDir(), "db")
	store, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "B", FilePath: "/tmp/b1"}); err != nil {
		t.Fatal(err)
	}
	_ = store.SetUserPosition("_local", "b1", "s1", 300)
	store.Close()
	db, err := pebble.Open(dir, &pebble.Options{FormatMajorVersion: pebble.FormatNewest})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Set([]byte("ubs:_local:b1"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err = database.NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := NewReadingHandler(store)
	call := func(body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/books/b1/status/repair", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: "b1"}}
		h.RepairBookStatus(c)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := call(""); code != http.StatusOK {
		t.Fatalf("dry run = %d %v", code, out)
	}
	if _, err := store.GetUserBookState("_local", "b1"); err == nil {
		t.Fatal("the dry run (default) rewrote the row")
	}
	if code, out := call(`{"apply":true}`); code != http.StatusOK {
		t.Fatalf("apply = %d %v", code, out)
	}
	if st, err := store.GetUserBookState("_local", "b1"); err != nil || st == nil || st.TotalListenedSeconds != 300 {
		t.Fatalf("after apply = %+v, %v", st, err)
	}
}
