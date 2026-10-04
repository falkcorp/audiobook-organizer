// file: internal/server/handlers/db_health_pebble_test.go
// version: 2.0.0
// guid: 8d3e51b7-0c94-4a26-b8f1-53a70e6d29cc
// last-edited: 2026-10-04

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The Pebble section of /db-health is gated on a CAPABILITY, not on an error
// path, and that makes its absence invisible: a store that cannot report a
// census produces a 200 with a healthy-looking payload that is silently missing
// a section. These tests pin which outcome each input produces.

// dbHealthStoreStub supplies only what GetDBHealth actually calls on the store.
// The embedded diagnosticsStore is deliberately nil: if the handler ever starts
// calling something else, this panics loudly instead of quietly returning a zero
// value and letting the assertions below pass for the wrong reason.
type dbHealthStoreStub struct {
	diagnosticsStore
}

func (dbHealthStoreStub) CountPrefix(string) (int64, error)            { return 0, nil }
func (dbHealthStoreStub) ScanPrefix(string) ([]database.KVPair, error) { return nil, nil }

// dbHealthStoreWithCensus carries the capability; dbHealthStoreStub does not.
// The difference between the two types IS the test fixture.
type dbHealthStoreWithCensus struct {
	dbHealthStoreStub
	census *database.DBCensus
	err    error
}

func (s dbHealthStoreWithCensus) DBCensus(context.Context, database.CensusOptions) (*database.DBCensus, error) {
	return s.census, s.err
}

// pebbleSection returns the "pebble" object from the response, and whether it
// was present at all. Presence is the assertion, so it is reported separately
// rather than collapsed into a zero value.
func pebbleSection(t *testing.T, body []byte) (map[string]any, bool) {
	t.Helper()
	sec, ok := dbHealthPayload(t, body)["pebble"].(map[string]any)
	return sec, ok
}

func dbHealthPayload(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("response is not JSON: %v — body %s", err, body)
	}
	if data, ok := envelope["data"].(map[string]any); ok {
		return data
	}
	return envelope
}

func callDBHealth(t *testing.T, store diagnosticsStore) []byte {
	t.Helper()
	return callDBHealthQuery(t, store, "")
}

func callDBHealthQuery(t *testing.T, store diagnosticsStore, rawQuery string) []byte {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/db-health?"+rawQuery, nil)

	NewDiagnosticsHandler(store, nil, nil, nil, nil).GetDBHealth(c)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — body %s", w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// The numbers reaching the payload are the census's, marked estimated.
func TestGetDBHealth_ReportsPebbleSectionFromTheCensus(t *testing.T) {
	store := dbHealthStoreWithCensus{census: &database.DBCensus{TotalKeys: 4242, DiskSpaceUsage: 999000}}

	sec, ok := pebbleSection(t, callDBHealth(t, store))
	if !ok {
		t.Fatal("no pebble section for a store that reports a census")
	}
	if got := sec["key_count"]; got != float64(4242) {
		t.Errorf("key_count = %v, want 4242", got)
	}
	if got := sec["size_bytes"]; got != float64(999000) {
		t.Errorf("size_bytes = %v, want 999000", got)
	}
	if got := sec["estimated"]; got != true {
		t.Errorf("estimated = %v, want true", got)
	}
}

// A store with no census capability (a non-Pebble backend) produces no pebble
// section. This is the outcome a bare type assertion produced against a
// decorated store: still a 200, with the section silently missing.
func TestGetDBHealth_OmitsPebbleSectionWhenTheCapabilityIsAbsent(t *testing.T) {
	if _, ok := pebbleSection(t, callDBHealth(t, dbHealthStoreStub{})); ok {
		t.Fatal("a store with no census capability produced a pebble section")
	}
}

// A failing census is logged and the section is omitted, matching the
// non-Pebble branch; it must not report zeros as if they were counts.
func TestGetDBHealth_OmitsPebbleSectionWhenTheCensusErrors(t *testing.T) {
	store := dbHealthStoreWithCensus{err: errors.New("census unavailable")}
	if _, ok := pebbleSection(t, callDBHealth(t, store)); ok {
		t.Fatal("a census error produced a pebble section")
	}
}
