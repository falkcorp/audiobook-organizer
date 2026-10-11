// file: internal/server/openlibrary_delete_close_test.go
// version: 1.0.2
// guid: 0c7f4d52-3a8e-4b19-b6d1-7e92a5c3f860
// last-edited: 2026-10-11

package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
)

// deleteOLData must never remove the data directory while the store's engine
// is still open. A Close that timed out used to nil the store, so the second
// delete skipped the close entirely and went straight to RemoveAll under the
// live engine. Now the half-closed store is kept: every delete retries the
// close and only the one that gets it through deletes.
func TestDeleteOLData_NeverRemovesDataUnderAnOpenEngine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dumpDir := filepath.Join(t.TempDir(), "ol-dumps")
	prev := config.AppConfig.OpenLibraryDumpDir
	config.AppConfig.OpenLibraryDumpDir = dumpDir
	t.Cleanup(func() { config.AppConfig.OpenLibraryDumpDir = prev })

	db, err := pebble.Open(filepath.Join(dumpDir, "oldb"), &pebble.Options{FormatMajorVersion: pebble.FormatNewest})
	if err != nil {
		t.Fatal(err)
	}
	gated, closeDB := database.NewOwnedGatedDB(db)
	it, err := gated.NewIter(&pebble.IterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	it.First()
	svc := metafetch.NewOpenLibraryServiceWithStore(openlibrary.NewOLStoreOnEngine(gated, closeDB))
	svc.CloseTimeout = 50 * time.Millisecond
	s := &Server{olService: svc}

	del := func() int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/openlibrary/data", nil)
		s.deleteOLData(c)
		return w.Code
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if code := del(); code != http.StatusInternalServerError {
			t.Fatalf("delete #%d with a lookup in flight returned %d; want 500", attempt, code)
		}
		if _, err := os.Stat(filepath.Join(dumpDir, "oldb")); err != nil {
			t.Fatalf("delete #%d removed the data while the engine was open: %v", attempt, err)
		}
	}

	if err := it.Close(); err != nil {
		t.Fatal(err)
	}
	// The 50 ms that forced the timeouts is shorter than a real fsync on a
	// loaded disk; the close with nothing left to wait for gets the default.
	svc.Mu.Lock()
	svc.CloseTimeout = 0
	svc.Mu.Unlock()
	if code := del(); code != http.StatusOK {
		t.Fatalf("delete after the lookup released returned %d; want 200", code)
	}
	if _, err := os.Stat(dumpDir); !os.IsNotExist(err) {
		t.Fatalf("data not removed after a clean close (stat error %v)", err)
	}
}
