// file: internal/books/perimeter_test.go
// version: 1.0.2
// guid: 7c2f9a14-3e8b-4d06-b5a1-9f4e2c8d6b37
// last-edited: 2026-11-08

package books

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestActivityTap_AccessLinesMakeNoEntries: core's stage 4 access records
// are printed, as gin's request log was, and never make an Activity entry.
// Stage 4 logs through core's http scope, not the process default logger, so
// the Activity tap skips it (R1-18, Record.Process); this pins that for the
// record the composed App really writes. The marker, logged after the
// request, is the first entry that arrives: the tap's queue is FIFO.
func TestActivityTap_AccessLinesMakeNoEntries(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "192.0.2.9:41000"
	c.app.Handler().ServeHTTP(httptest.NewRecorder(), req)
	slog.Info("alfred marker")

	if e := nextActivity(t, c); !strings.Contains(e.Summary, "alfred marker") {
		t.Errorf("an access record became an Activity entry: %+v", e)
	}
	var access []string
	for _, l := range strings.Split(c.newLog.String(), "\n") {
		if strings.Contains(l, `msg="http access"`) {
			access = append(access, l)
		}
	}
	if len(access) != 1 {
		t.Fatalf("the log stream got %d access records, want 1:\n%s", len(access), c.newLog.String())
	}
	if !strings.Contains(access[0], "module=core") {
		t.Errorf("the access record is not core's: %s", access[0])
	}
}
