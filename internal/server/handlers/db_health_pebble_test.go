// file: internal/server/handlers/db_health_pebble_test.go
// version: 1.0.0
// guid: 8d3e51b7-0c94-4a26-b8f1-53a70e6d29cc
// last-edited: 2026-08-19

package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The Pebble section of /db-health is gated on a CAPABILITY, not on an error
// path, and that makes its absence invisible: a store that cannot report key
// counts produces a 200 with a healthy-looking payload that is silently missing
// a section. These tests pin which of the three outcomes each input produces.

// dbHealthStoreStub supplies only what GetDBHealth actually calls on the store.
// The embedded diagnosticsStore is deliberately nil: if the handler ever starts
// calling something else, this panics loudly instead of quietly returning a zero
// value and letting the assertions below pass for the wrong reason.
type dbHealthStoreStub struct {
	diagnosticsStore
}

func (dbHealthStoreStub) CountPrefix(string) (int64, error)            { return 0, nil }
func (dbHealthStoreStub) ScanPrefix(string) ([]database.KVPair, error) { return nil, nil }

// dbHealthStoreWithKeyCount carries the capability; dbHealthStoreStub does not.
// The difference between the two types IS the test fixture.
type dbHealthStoreWithKeyCount struct {
	dbHealthStoreStub
	*mockKeyCounter
}

// pebbleSection returns the "pebble" object from the response, and whether it
// was present at all. Presence is the assertion, so it is reported separately
// rather than collapsed into a zero value.
func pebbleSection(t *testing.T, body []byte) (map[string]any, bool) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("response is not JSON: %v — body %s", err, body)
	}
	payload := envelope
	if data, ok := envelope["data"].(map[string]any); ok {
		payload = data
	}
	sec, ok := payload["pebble"].(map[string]any)
	return sec, ok
}

func callDBHealth(t *testing.T, store diagnosticsStore) []byte {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/db-health", nil)

	NewDiagnosticsHandler(store, nil, nil, nil, nil).GetDBHealth(c)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — body %s", w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// TestGetDBHealth_ReportsPebbleSectionWhenTheStoreCarriesKeyCount is the happy
// path: the numbers reaching the payload are the ones KeyCount returned.
func TestGetDBHealth_ReportsPebbleSectionWhenTheStoreCarriesKeyCount(t *testing.T) {
	kc := newMockKeyCounter(t)
	kc.EXPECT().KeyCount().Return(4242, 999000, nil).Once()

	sec, ok := pebbleSection(t, callDBHealth(t, dbHealthStoreWithKeyCount{mockKeyCounter: kc}))
	if !ok {
		t.Fatal("no pebble section for a store that reports key counts")
	}
	if got := sec["key_count"]; got != float64(4242) {
		t.Errorf("key_count = %v, want 4242", got)
	}
	if got := sec["size_bytes"]; got != float64(999000) {
		t.Errorf("size_bytes = %v, want 999000", got)
	}
}

// TestGetDBHealth_OmitsPebbleSectionWhenTheCapabilityIsAbsent pins the silent
// partial answer. This is the outcome a bare type assertion produced against a
// decorated store, and the reason resolveKeyCounter exists: the endpoint still
// returns 200 and still says the database is healthy, it just stops mentioning
// Pebble. Nothing is logged and no error is surfaced.
func TestGetDBHealth_OmitsPebbleSectionWhenTheCapabilityIsAbsent(t *testing.T) {
	if _, ok := pebbleSection(t, callDBHealth(t, dbHealthStoreStub{})); ok {
		t.Fatal("a store with no KeyCount method produced a pebble section")
	}
}

// TestGetDBHealth_ReportsZerosRatherThanOmittingWhenKeyCountErrors pins the
// asymmetry, which is the part most likely to be "tidied" into a bug: a FAILING
// KeyCount is not treated like a missing one. The error is logged and the
// section is still emitted, carrying zeros.
//
// So `pebble: {key_count: 0}` means "asked and it failed", while no pebble key
// at all means "this backend does not keep the statistic". A reader of the
// payload can only tell those apart because the shapes differ, and anyone
// moving the assignment inside the `if err != nil` guard would collapse them.
func TestGetDBHealth_ReportsZerosRatherThanOmittingWhenKeyCountErrors(t *testing.T) {
	kc := newMockKeyCounter(t)
	kc.EXPECT().KeyCount().Return(0, 0, errors.New("pebble metrics unavailable")).Once()

	sec, ok := pebbleSection(t, callDBHealth(t, dbHealthStoreWithKeyCount{mockKeyCounter: kc}))
	if !ok {
		t.Fatal("a KeyCount error omitted the pebble section entirely — an error " +
			"and an unsupported backend must not look identical in the payload")
	}
	if got := sec["key_count"]; got != float64(0) {
		t.Errorf("key_count = %v, want 0", got)
	}
}
