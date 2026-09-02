// file: internal/config/update_service_dedup_ladder_test.go
// version: 2.0.0
// guid: 7d1c3a9e-5b2f-4e8a-9c6d-0f1e2a3b4c5d
// last-edited: 2026-09-02

package config

import (
	"context"
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
	svc.SetDedupScoreConfigSink(func(context.Context, unified.ScoreConfig) (string, error) { sinkCalls++; return "op-1", nil })

	// The exact UI failure mode: a 0–1 spinner step persisted band_certain_min=1.
	status, resp := svc.UpdateConfig(context.Background(), map[string]any{
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

	status, resp := svc.UpdateConfig(context.Background(), map[string]any{
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
	svc.SetDedupScoreConfigSink(func(_ context.Context, cfg unified.ScoreConfig) (string, error) {
		got = append(got, cfg)
		return "op-1", nil
	})

	status, resp := svc.UpdateConfig(context.Background(), map[string]any{
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
	svc.SetDedupScoreConfigSink(func(context.Context, unified.ScoreConfig) (string, error) { sinkCalls++; return "op-1", nil })

	if status, resp := svc.UpdateConfig(context.Background(), map[string]any{"root_dir": "/lib"}); status != http.StatusOK {
		t.Fatalf("status = %d; resp = %v", status, resp)
	}
	if sinkCalls != 0 {
		t.Errorf("sink fired %d time(s) for a PUT that did not touch dedup.signals", sinkCalls)
	}
}

// TestUpdateConfig_SinkErrorKeepsSavedLadderAndNamesTheRemedy (PR #3052
// follow-up, D2) replaces the old "…RollsBackMemoryAndBlob" test, which
// asserted the OPPOSITE rule.
//
// The old rule: a sink error rolled memory AND the blob back to the previous
// ladder. It was wrong in two ways. The message said "dedup engine rejected
// the new score ladder", which cannot happen — the config layer runs the same
// unified.ScoreConfig.Validate the engine runs, so a ladder that reaches the
// sink always passes it; the only thing that can fail is the hand-off (engine
// missing, re-band could not be queued). And the rollback created a
// three-way split: the sink had already swapped the ladder into the live
// engine before the failing step, so memory and the blob went back to the old
// ladder while the engine kept scoring on the new one.
//
// The rule now: the saved ladder stays saved and live, the response is a 500
// that says so, and it names the one action that finishes the job.
//
// Mutation check: restore the `Mutate(func(c *Config) { *c = prior })` +
// re-save in the sink-error branch of UpdateConfig and this test fails on
// band_certain_min = 97 and on the second blob write.
func TestUpdateConfig_SinkErrorKeepsSavedLadderAndNamesTheRemedy(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.Dedup.Signals = DedupSignalConfig{BandCertainMin: 97, BandHighMin: 90, BandMediumMin: 75, BandReviewMin: 60}
	})
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)
	svc.SetDedupScoreConfigSink(func(context.Context, unified.ScoreConfig) (string, error) {
		return "", errors.New("engine hand-off failed")
	})

	status, resp := svc.UpdateConfig(context.Background(), map[string]any{
		"dedup": map[string]any{"signals": map[string]any{"band_certain_min": 98}},
	})
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; resp = %v", status, resp)
	}
	if got := Snapshot().Dedup.Signals.BandCertainMin; got != 98 {
		t.Errorf("in-memory band_certain_min = %v after a hand-off failure, want the SAVED 98 kept (rolling back would disagree with the blob and the engine)", got)
	}
	if len(*blobs) != 1 {
		t.Fatalf("expected exactly 1 blob write (the save; no rollback re-save), got %d", len(*blobs))
	}
	if !strings.Contains((*blobs)[0], `"band_certain_min":98`) {
		t.Errorf("persisted blob should hold the new ladder: %s", (*blobs)[0])
	}
	msg, _ := resp["error"].(string)
	if !strings.Contains(msg, "saved") {
		t.Errorf("500 body must say the configuration WAS saved, got %q", msg)
	}
	if !strings.Contains(msg, "POST /api/v1/dedup/rescore") {
		t.Errorf("500 body must name the remedy endpoint, got %q", msg)
	}
	if strings.Contains(msg, "rejected") || strings.Contains(msg, "rolled back") {
		t.Errorf("500 body must not claim the engine rejected the ladder or that anything was rolled back, got %q", msg)
	}
	if saved, _ := resp["saved"].(bool); !saved {
		t.Errorf(`response must carry saved=true so a caller can tell "nothing happened" from "saved but not re-banded"; resp = %v`, resp)
	}
}

// TestUpdateConfig_SuccessReportsRescoreOpID: on the happy path the sink
// returns the id of the queued dedup.rescore operation, and UpdateConfig hands
// it back so the HTTP caller can follow the re-band it just triggered.
func TestUpdateConfig_SuccessReportsRescoreOpID(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { c.Dedup.Signals = DedupSignalConfig{} })
	ms, _ := ladderTestStore(t)
	svc := NewUpdateService(ms)
	svc.SetDedupScoreConfigSink(func(ctx context.Context, _ unified.ScoreConfig) (string, error) {
		if ctx == nil {
			t.Error("sink must receive the caller's context, not nil")
		}
		return "01JRESCORE", nil
	})

	status, resp := svc.UpdateConfig(context.Background(), map[string]any{
		"dedup": map[string]any{"signals": map[string]any{
			"band_certain_min": 98.5, "band_high_min": 91, "band_medium_min": 76, "band_review_min": 61,
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; resp = %v", status, resp)
	}
	if got, _ := resp["dedup_rescore_op_id"].(string); got != "01JRESCORE" {
		t.Fatalf("dedup_rescore_op_id = %q, want the queued op id 01JRESCORE", got)
	}
}

// TestUpdateConfig_RejectedPutLeavesNoStrayConfidenceKey is D1: the shallow-copy
// bug, reproduced end to end.
//
// The setup is what production looks like AFTER one confidence override has
// ever been saved: a NON-NIL Dedup.Signals.Confidence map in the live config.
// UpdateConfig used to json.Unmarshal the payload straight into that live
// struct — and json.Unmarshal MERGES into an existing map — so a PUT carrying
// a typo'd kind wrote the bad key into the live map before validation could
// reject it. The 400 rolled back with `*c = prior`, a struct assignment that
// shares the very same map, so the bad key survived. From then on EVERY config
// PUT failed validation on a key the operator never saved, and any unguarded
// SaveConfigToDatabase caller (scheduler_admin.go, system/handler.go) could
// persist the poisoned map into the blob, where it also blocks startup.
//
// Mutation check: change `candidate := c.Clone()` back to unmarshalling into
// `c` (or `prior = c.Clone()` back to `prior = *c`) and the follow-up PUT here
// returns 400.
func TestUpdateConfig_RejectedPutLeavesNoStrayConfidenceKey(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.Dedup.Signals = DedupSignalConfig{
			BandCertainMin: 97, BandHighMin: 90, BandMediumMin: 75, BandReviewMin: 60,
			Confidence: map[string]DedupKindConfidence{
				string(unified.SigEmbedMedium): {MinConfidence: 0.7, MaxConfidence: 0.8},
			},
		}
	})
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)

	status, _ := svc.UpdateConfig(context.Background(), map[string]any{
		"dedup": map[string]any{"signals": map[string]any{
			"confidence": map[string]any{"embeding_medium": map[string]any{"min_confidence": 0.7}},
		}},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("unknown-kind PUT status = %d, want 400", status)
	}

	// (1) The live map must not carry the rejected key.
	live := Snapshot().Dedup.Signals.Confidence
	if _, bad := live["embeding_medium"]; bad {
		t.Errorf("rejected PUT left the typo'd kind in the LIVE confidence map: %+v", live)
	}
	if len(live) != 1 {
		t.Errorf("live confidence map should still hold exactly the one real override, got %+v", live)
	}
	// (2) The live config must still be valid — i.e. a later, unrelated PUT works.
	if status, resp := svc.UpdateConfig(context.Background(), map[string]any{"log_level": "debug"}); status != http.StatusOK {
		t.Fatalf("a later unrelated PUT returned %d (%v) — the rejected PUT poisoned the live config", status, resp)
	}
	if got := Snapshot().LogLevel; got != "debug" {
		t.Errorf("log_level = %q after the follow-up PUT, want debug", got)
	}
	// (3) Only the follow-up PUT may have persisted anything.
	if len(*blobs) != 1 {
		t.Fatalf("expected exactly 1 blob write (the successful follow-up), got %d", len(*blobs))
	}
	if strings.Contains((*blobs)[0], "embeding_medium") {
		t.Errorf("the persisted blob carries the rejected kind: %s", (*blobs)[0])
	}
}

// TestUpdateConfig_SaveFailureRestoresMapContents is the rollback half of D1:
// when SaveConfigToDatabase fails the service restores the PREVIOUS config,
// and that restore has to include map CONTENTS. With `prior = *c` the rollback
// restored the same map header the update had already mutated in place, so a
// PUT that added a confidence override left it in memory even though the
// response said the save failed and memory was rolled back.
//
// Mutation check: `prior = c.Clone()` → `prior = *c` and this fails on the
// leftover key.
func TestUpdateConfig_SaveFailureRestoresMapContents(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.Dedup.Signals = DedupSignalConfig{
			BandCertainMin: 97, BandHighMin: 90, BandMediumMin: 75, BandReviewMin: 60,
			Confidence: map[string]DedupKindConfidence{
				string(unified.SigEmbedMedium): {MinConfidence: 0.7, MaxConfidence: 0.8},
			},
		}
	})
	ms := mocks.NewMockStore(t)
	ms.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(errors.New("disk on fire")).Maybe()
	ms.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	svc := NewUpdateService(ms)

	status, _ := svc.UpdateConfig(context.Background(), map[string]any{
		"dedup": map[string]any{"signals": map[string]any{
			"confidence": map[string]any{
				string(unified.SigEmbedHigh): map[string]any{"min_confidence": 0.9, "max_confidence": 0.95},
			},
		}},
	})
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on a save failure", status)
	}
	live := Snapshot().Dedup.Signals.Confidence
	if _, leaked := live[string(unified.SigEmbedHigh)]; leaked {
		t.Errorf("save failed and memory was reported rolled back, but the new confidence entry is still live: %+v", live)
	}
	if len(live) != 1 {
		t.Errorf("rollback should restore exactly the prior map contents, got %+v", live)
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

// TestUpdateConfig_RejectedPutDoesNotRotateSecrets is the secret half of the
// same rule as TestUpdateConfig_RejectedPutLeavesNoStrayConfidenceKey: a
// rejected PUT must change NOTHING.
//
// The five secret fields used to be written straight into the live AppConfig by
// their own Mutate calls, before the payload was validated. A PUT that rotated
// a key AND carried an invalid dedup ladder was rejected with a 400 while the
// new key stayed live in memory and never reached the blob — so the process
// authenticated with the new key until the next restart silently reverted it.
// The handler-level "roll back on any >=400" that used to hide this was removed
// with the deep-copy rework, which is what turned a latent ordering problem
// into a live one.
//
// Mutation check: move applySecretUpdates back out of the Mutate closure so it
// runs against the live config, and this fails — the key rotates on a 400.
func TestUpdateConfig_RejectedPutDoesNotRotateSecrets(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.OpenAIAPIKey = "old-openai-key"
		c.BasicAuthPassword = "old-password"
		c.Dedup.Signals = DedupSignalConfig{
			BandCertainMin: 97, BandHighMin: 90, BandMediumMin: 75, BandReviewMin: 60,
		}
	})
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)

	// One PUT that rotates two secrets AND carries an invalid ladder.
	status, _ := svc.UpdateConfig(context.Background(), map[string]any{
		"openai_api_key":      "rotated-openai-key",
		"basic_auth_password": "rotated-password",
		"dedup": map[string]any{"signals": map[string]any{
			"confidence": map[string]any{"embeding_medium": map[string]any{"min_confidence": 0.7}},
		}},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (the ladder is invalid)", status)
	}

	snap := Snapshot()
	if snap.OpenAIAPIKey != "old-openai-key" {
		t.Errorf("openai_api_key = %q after a REJECTED PUT, want the old key kept: the process would authenticate with a credential that was never persisted, and revert at the next restart", snap.OpenAIAPIKey)
	}
	if snap.BasicAuthPassword != "old-password" {
		t.Errorf("basic_auth_password = %q after a REJECTED PUT, want the old password kept", snap.BasicAuthPassword)
	}
	if len(*blobs) != 0 {
		t.Fatalf("a rejected PUT persisted %d blob(s); it must write nothing", len(*blobs))
	}
}

// TestUpdateConfig_InvalidConfigIsRejectedBeforeAnythingIsWritten covers the
// whole-config Validate moving from the HTTP handler (where it ran AFTER the
// save and rolled back memory only) into UpdateConfig, before the swap.
//
// Mutation check: drop the candidate.Validate() call and this fails — the
// invalid value is accepted with a 200 and persisted.
func TestUpdateConfig_InvalidConfigIsRejectedBeforeAnythingIsWritten(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { c.DatabaseType = "pebble" })
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)

	status, resp := svc.UpdateConfig(context.Background(), map[string]any{"database_type": "mysql"})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d (%v), want 400 for an invalid database_type", status, resp)
	}
	if got := Snapshot().DatabaseType; got != "pebble" {
		t.Errorf("live database_type = %q after a rejected PUT, want the original pebble", got)
	}
	if len(*blobs) != 0 {
		t.Fatalf("a config that fails Validate persisted %d blob(s); it must write nothing", len(*blobs))
	}
}
