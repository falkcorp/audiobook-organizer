// file: internal/server/handlers/census_capability_test.go
// version: 1.0.0
// guid: 5045b811-3a0d-452b-8b80-2012988f7682
// last-edited: 2026-10-04

package handlers

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// censusCapableStore carries the capability; censusDecorator embeds the
// database.Store INTERFACE and unwraps, exactly as internal/server's
// indexedStore does in production.
type censusCapableStore struct {
	database.Store
}

func (censusCapableStore) DBCensus(context.Context, database.CensusOptions) (*database.DBCensus, error) {
	return &database.DBCensus{TotalKeys: 7}, nil
}

type censusDecorator struct {
	database.Store
	inner database.Store
}

func (d censusDecorator) Unwrap() database.Store { return d.inner }

// db-health and /cache/stats resolve the census through the decorator chain. A
// bare assertion returns nothing through the decorator and both silently fall
// back or drop their section.
func TestResolveCensusProviderThroughDecorator(t *testing.T) {
	wrapped := censusDecorator{inner: censusCapableStore{}}

	got := resolveHealthCensus(context.Background(), wrapped)
	if got == nil {
		t.Fatal("resolveHealthCensus returned nil through the decorator; db-health would drop resp.Pebble in production")
	}
	if got.TotalKeys != 7 {
		t.Fatalf("TotalKeys = %d, want 7", got.TotalKeys)
	}
}

func TestResolveCensusProviderOnUncapableBackend(t *testing.T) {
	type plain struct{ database.Store }
	if got := resolveHealthCensus(context.Background(), plain{}); got != nil {
		t.Fatalf("resolveHealthCensus = %v on a store without a census, want nil", got)
	}
}
