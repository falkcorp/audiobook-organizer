// file: internal/config/update_service_dedup_ladder_test.go
// version: 1.0.0
// guid: 7d1c3a9e-5b2f-4e8a-9c6d-0f1e2a3b4c5d
// last-edited: 2026-09-02

package config

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/stretchr/testify/mock"
)

// ladderTestStore returns a mock SettingsStore that records every config_blob
// write so a test can assert on WHAT was persisted (or that nothing was),
// rather than only on the HTTP status the service reports.
func ladderTestStore(t *testing.T) (*mocks.MockStore, *[]string) {
	t.Helper()
	var blobs []string
	ms := mocks.NewMockStore(t)
	ms.On("SetSetting", "config_blob", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { blobs = append(blobs, args.String(1)) }).
		Return(nil).Maybe()
	ms.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	ms.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	return ms, &blobs
}

// TestUpdateConfig_RejectsInvalidDedupLadderBeforePersisting is H1(a) from
// the #3052 review: an out-of-order ladder must be refused with 400, the
// in-memory config must be exactly what it was, and NO blob may have been
// written — because registry_wire.go refuses to start on a bad ladder and the
// persisted blob overrides config.yaml, a persisted bad value is a crash loop
// that editing config.yaml cannot repair.
func TestUpdateConfig_RejectsInvalidDedupLadderBeforePersisting(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.Dedup.Signals = DedupSignalConfig{BandCertainMin: 97, BandHighMin: 90, BandMediumMin: 75, BandReviewMin: 60}
	})
	before := Snapshot()

	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)
	sinkCalls := 0
	svc.SetDedupScoreConfigSink(func(unified.ScoreConfig) error { sinkCalls++; return nil })

	// The exact UI failure mode: a 0–1 spinner step persisted band_certain_min=1.
	status, resp := svc.UpdateConfig(map[string]any{
		"dedup": map[string]any{"signals": map[string]any{"band_certain_min": 1}},
	})

	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; resp = %v", status, resp)
	}
	msg, _ := resp["error"].(string)
	if !strings.Contains(msg, "band_certain_min") || !strings.Contains(msg, "nothing was saved") {
		t.Errorf("error should name the field and say nothing was saved, got %q", msg)
	}
	if len(*blobs) != 0 {
		t.Fatalf("config_blob was written %d time(s) for a rejected update — the invalid ladder is now persisted", len(*blobs))
	}
	if after := Snapshot(); !reflect.DeepEqual(after.Dedup.Signals, before.Dedup.Signals) {
		t.Errorf("in-memory ladder changed by a rejected update: before %+v, after %+v", before.Dedup.Signals, after.Dedup.Signals)
	}
	if sinkCalls != 0 {
		t.Errorf("sink called %d time(s) for a rejected update", sinkCalls)
	}
}

// TestUpdateConfig_RejectsUnknownConfidenceKind: a typo'd confidence kind is
// rejected the same way (M4-silent), not silently dropped.
func TestUpdateConfig_RejectsUnknownConfidenceKind(t *testing.T) {
	restoreAppConfig(t)
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)

	status, resp := svc.UpdateConfig(map[string]any{
		"dedup": map[string]any{"signals": map[string]any{
			"confidence": map[string]any{"embeding_medium": map[string]any{"min_confidence": 0.7}},
		}},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; resp = %v", status, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "embeding_medium") {
		t.Errorf("error should name the unknown kind, got %q", msg)
	}
	if len(*blobs) != 0 {
		t.Fatalf("blob written %d time(s) for a rejected update", len(*blobs))
	}
}

// TestUpdateConfig_ValidDedupLadderReachesSink is H2: a PUT that changes the
// ladder must hand the NEW effective ScoreConfig to the sink after the blob is
// persisted, so the live engine does not keep scoring on the old ladder until
// the next restart.
func TestUpdateConfig_ValidDedupLadderReachesSink(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { c.Dedup.Signals = DedupSignalConfig{} })

	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)
	var got []unified.ScoreConfig
	svc.SetDedupScoreConfigSink(func(cfg unified.ScoreConfig) error { got = append(got, cfg); return nil })

	status, resp := svc.UpdateConfig(map[string]any{
		"dedup": map[string]any{"signals": map[string]any{
			"band_certain_min": 98.5, "band_high_min": 91, "band_medium_min": 76, "band_review_min": 61,
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; resp = %v", status, resp)
	}
	if len(*blobs) != 1 {
		t.Fatalf("expected exactly 1 blob write, got %d", len(*blobs))
	}
	if !strings.Contains((*blobs)[0], `"band_certain_min":98.5`) {
		t.Errorf("persisted blob does not carry the new ladder: %s", (*blobs)[0])
	}
	if len(got) != 1 {
		t.Fatalf("sink called %d time(s), want exactly 1", len(got))
	}
	if got[0].BandCertainMin != 98.5 || got[0].BandHighMin != 91 || got[0].BandMediumMin != 76 || got[0].BandReviewMin != 61 {
		t.Errorf("sink received %+v, want 98.5/91/76/61", got[0])
	}
}

// TestUpdateConfig_UnrelatedFieldDoesNotTriggerSink: the sink triggers a
// rescore of every pending candidate, so a PUT that only edits root_dir must
// not fire it.
func TestUpdateConfig_UnrelatedFieldDoesNotTriggerSink(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.Dedup.Signals = DedupSignalConfig{BandCertainMin: 97, BandHighMin: 90, BandMediumMin: 75, BandReviewMin: 60}
	})
	ms, _ := ladderTestStore(t)
	svc := NewUpdateService(ms)
	sinkCalls := 0
	svc.SetDedupScoreConfigSink(func(unified.ScoreConfig) error { sinkCalls++; return nil })

	if status, resp := svc.UpdateConfig(map[string]any{"root_dir": "/lib"}); status != http.StatusOK {
		t.Fatalf("status = %d; resp = %v", status, resp)
	}
	if sinkCalls != 0 {
		t.Errorf("sink fired %d time(s) for a PUT that did not touch dedup.signals", sinkCalls)
	}
}

// TestUpdateConfig_SinkErrorRollsBackMemoryAndBlob: if the engine refuses a
// ladder that passed Validate, the update must fail AND the prior ladder must
// be re-persisted — otherwise the blob holds a value the engine will refuse
// again at the next startup.
func TestUpdateConfig_SinkErrorRollsBackMemoryAndBlob(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.Dedup.Signals = DedupSignalConfig{BandCertainMin: 97, BandHighMin: 90, BandMediumMin: 75, BandReviewMin: 60}
	})
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)
	svc.SetDedupScoreConfigSink(func(unified.ScoreConfig) error { return errors.New("engine says no") })

	status, resp := svc.UpdateConfig(map[string]any{
		"dedup": map[string]any{"signals": map[string]any{"band_certain_min": 98}},
	})
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; resp = %v", status, resp)
	}
	if got := Snapshot().Dedup.Signals.BandCertainMin; got != 97 {
		t.Errorf("in-memory band_certain_min = %v after sink failure, want rolled back to 97", got)
	}
	// Two writes: the attempted new blob, then the rollback re-save of the prior.
	if len(*blobs) != 2 {
		t.Fatalf("expected 2 blob writes (attempt + rollback), got %d", len(*blobs))
	}
	if !strings.Contains((*blobs)[1], `"band_certain_min":97`) {
		t.Errorf("rollback blob does not restore the prior ladder: %s", (*blobs)[1])
	}
}

// TestConfigValidate_NamesPersistedSourceForBadLadder is H1(c): the startup
// error has to tell the operator where the bad value lives (the persisted
// settings blob), because "fix config.yaml" cannot repair a blob-sourced value.
func TestConfigValidate_NamesPersistedSourceForBadLadder(t *testing.T) {
	c := Config{DatabaseType: "pebble"}
	c.Dedup.Signals = DedupSignalConfig{BandCertainMin: 1}
	err := c.Validate()
	if err == nil {
		t.Fatal("Config.Validate accepted band_certain_min=1 below band_high_min")
	}
	for _, want := range []string{"band_certain_min", "PUT /api/v1/config", "persisted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate error should contain %q, got: %v", want, err)
		}
	}
}
