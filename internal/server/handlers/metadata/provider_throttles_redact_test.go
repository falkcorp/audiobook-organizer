// file: internal/server/handlers/metadata/provider_throttles_redact_test.go
// version: 1.0.0
// guid: ee4330fb-fd4d-4ee7-9082-c3192500d1f6
// last-edited: 2026-10-30

package metadatahandler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	metadatapkg "github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// A hold persisted before RecordFailure redacted can still carry a credential;
// the view must scrub it on the way out regardless of how it got into the store.
func TestToThrottleView_RedactsDetail(t *testing.T) {
	now := time.Now()
	view := toThrottleView(metadatapkg.ProviderThrottle{
		ProviderID: "google-books",
		Reason:     metadatapkg.ThrottleTransport,
		Detail:     `Get "https://books.example.test/v1?key=LEAKED&q=batman": EOF`,
		Until:      now.Add(time.Hour),
		SetAt:      now,
	}, now)
	if strings.Contains(view.Detail, "LEAKED") || !strings.Contains(view.Detail, "key=***") {
		t.Fatalf("detail not redacted: %q", view.Detail)
	}
}

type rawThrottleStore struct{ rows map[string][]byte }

func (s rawThrottleStore) LoadProviderThrottles() (map[string][]byte, error) { return s.rows, nil }
func (s rawThrottleStore) SaveProviderThrottle(string, []byte) error         { return nil }
func (s rawThrottleStore) DeleteProviderThrottle(string) error               { return nil }

func TestListProviderThrottles_ServesRedactedDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	metadatapkg.ResetThrottlesForTesting()
	t.Cleanup(metadatapkg.ResetThrottlesForTesting)

	// The row goes in through a persisted store, as a pre-redaction hold would.
	// AttachStore scrubs on load and the view scrubs again on the way out; the
	// served body must be clean either way.
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	row := `{"provider_id":"google-books","reason":"transport","detail":"Get \"https://books.example.test/v1?key=LEAKED\": EOF","until":"` + until + `","set_at":"` + until + `"}`
	store := rawThrottleStore{rows: map[string][]byte{"google-books": []byte(row)}}
	if _, err := metadatapkg.DefaultThrottleRegistry().AttachStore(store); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/metadata/providers/throttles", nil)
	(&Handler{}).listProviderThrottlesImpl(c)

	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "google-books") {
		t.Fatalf("status %d body %s", w.Code, body)
	}
	if strings.Contains(body, "LEAKED") || !strings.Contains(body, "key=***") {
		t.Fatalf("served body leaks or lacks redaction: %s", body)
	}
}
