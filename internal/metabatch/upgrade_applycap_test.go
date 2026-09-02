// file: internal/metabatch/upgrade_applycap_test.go
// version: 1.0.0
// guid: 2c9d5e71-8f4b-4a3e-b6d0-7e1c3a5f9d24
// last-edited: 2026-09-02

package metabatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// capStubStore is the smallest Store that lets RunUpgrade get past its cap
// gate and prove it did so: every tag lookup is counted and returns nothing,
// so the run finishes with zero books checked and zero applies.
type capStubStore struct {
	tagLookups int
}

func (s *capStubStore) GetRecentOperations(int) ([]database.Operation, error) { return nil, nil }
func (s *capStubStore) ListOperationsV2Since(time.Time, int) ([]database.OperationV2Row, error) {
	return nil, nil
}
func (s *capStubStore) GetOperationResults(string) ([]database.OperationResult, error) {
	return nil, nil
}
func (s *capStubStore) GetBookByID(string) (*database.Book, error) {
	return nil, errors.New("must not be reached")
}
func (s *capStubStore) GetBooksByTag(string) ([]string, error) {
	s.tagLookups++
	return nil, nil
}

func withUpgradeBulkApplyCap(t *testing.T, n int) {
	t.Helper()
	prev := config.AppConfig.BulkApplyMaxItems
	config.AppConfig.BulkApplyMaxItems = n
	t.Cleanup(func() { config.AppConfig.BulkApplyMaxItems = prev })
}

// The bulk apply cap (internal/applycap) on RunUpgrade: `limit` bounds how
// many books one run may re-fetch AND apply, so a limit above the cap is
// refused up front rather than applying the first cap-many.
func TestRunUpgrade_RefusesLimitOverTheBulkApplyCap(t *testing.T) {
	withUpgradeBulkApplyCap(t, 100)
	store := &capStubStore{}
	svc := NewMetadataUpgradeService(store, &metafetch.Service{})

	res, err := svc.RunUpgrade(context.Background(), 101, nil)
	if err == nil {
		t.Fatal("want refusal, got nil error")
	}
	var ex *applycap.ExceededError
	if !errors.As(err, &ex) {
		t.Fatalf("want *applycap.ExceededError, got %T: %v", err, err)
	}
	if ex.Requested != 101 || ex.Cap != 100 {
		t.Fatalf("want requested=101 cap=100, got %+v", ex)
	}
	if res != nil {
		t.Fatalf("a refused run must return no result, got %+v", res)
	}
	if store.tagLookups != 0 {
		t.Fatalf("refusal must happen before any store read, got %d lookups", store.tagLookups)
	}
}

func TestRunUpgrade_LimitExactlyTheCapRuns(t *testing.T) {
	withUpgradeBulkApplyCap(t, 100)
	store := &capStubStore{}
	svc := NewMetadataUpgradeService(store, &metafetch.Service{})

	res, err := svc.RunUpgrade(context.Background(), 100, nil)
	if err != nil {
		t.Fatalf("limit == cap must run, got %v", err)
	}
	if res == nil || res.Checked != 0 {
		t.Fatalf("want an empty result from an empty library, got %+v", res)
	}
	if store.tagLookups != len(LowQualitySources) {
		t.Fatalf("want one tag lookup per low-quality source (%d), got %d", len(LowQualitySources), store.tagLookups)
	}
}

// The default limit (200, applied when the caller passes 0) must itself fit
// under the default cap — otherwise every scheduled upgrade would be refused.
func TestRunUpgrade_DefaultLimitFitsTheDefaultCap(t *testing.T) {
	withUpgradeBulkApplyCap(t, 0)
	store := &capStubStore{}
	svc := NewMetadataUpgradeService(store, &metafetch.Service{})
	if _, err := svc.RunUpgrade(context.Background(), 0, nil); err != nil {
		t.Fatalf("default limit under default cap must run, got %v", err)
	}
}
