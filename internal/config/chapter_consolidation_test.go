// file: internal/config/chapter_consolidation_test.go
// version: 1.0.0
// guid: a48766bd-8d5c-496e-b7f9-1cddea31517e
// last-edited: 2026-10-03

// A 0 in chapter_consolidation_threshold_min used to switch chapter
// consolidation off, and 0 is also the zero value every partially-populated
// Config carries -- so production ran eleven days with consolidation silently
// disabled (12,525 books imported without book_file rows). These tests pin the
// replacement rule: 0 or less means the default, on every path a value can
// arrive by.

package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveChapterConsolidationThresholdMin(t *testing.T) {
	for _, tc := range []struct {
		stored, want int
	}{
		{0, DefaultChapterConsolidationThresholdMin},
		{-3, DefaultChapterConsolidationThresholdMin},
		{1, 1},
		{25, 25},
	} {
		c := Config{ChapterConsolidationThresholdMin: tc.stored}
		require.Equal(t, tc.want, c.ResolveChapterConsolidationThresholdMin(), "stored %d", tc.stored)
	}
}

// TestValidateNormalizesZeroChapterThreshold: Validate runs after InitConfig,
// after the database overlay and on every PUT candidate. It must rewrite the
// zero (so GET /config never shows a 0 that behaves like 10) and must not
// reject it (refusing to start over a recovered setting would be an outage).
func TestValidateNormalizesZeroChapterThreshold(t *testing.T) {
	for _, stored := range []int{0, -1} {
		c := Config{DatabaseType: "pebble", ChapterConsolidationThresholdMin: stored}
		require.NoError(t, c.Validate())
		require.Equal(t, DefaultChapterConsolidationThresholdMin, c.ChapterConsolidationThresholdMin)
	}
	c := Config{DatabaseType: "pebble", ChapterConsolidationThresholdMin: 7}
	require.NoError(t, c.Validate())
	require.Equal(t, 7, c.ChapterConsolidationThresholdMin, "a real value is left alone")
}

// TestLoadedBlobWithZeroChapterThresholdUsesDefault is the incident shape: a
// blob that carries an explicit 0. It used to load as "disabled" and win over
// viper's default on every boot.
func TestLoadedBlobWithZeroChapterThresholdUsesDefault(t *testing.T) {
	restoreAppConfig(t)
	ResetToDefaults()

	require.NoError(t, LoadConfigFromDatabase(storeWithBlob(t, `{"chapter_consolidation_threshold_min":0}`)))

	require.Equal(t, DefaultChapterConsolidationThresholdMin, Snapshot().ChapterConsolidationThresholdMin)
}

// TestLoadedBlobKeepsARealChapterThreshold guards the other direction: the
// normalization must not clobber an operator's real value.
func TestLoadedBlobKeepsARealChapterThreshold(t *testing.T) {
	restoreAppConfig(t)
	ResetToDefaults()

	require.NoError(t, LoadConfigFromDatabase(storeWithBlob(t, `{"chapter_consolidation_threshold_min":4}`)))

	require.Equal(t, 4, Snapshot().ChapterConsolidationThresholdMin)
}

// TestSaveNeverPersistsZeroChapterThreshold: the blob is what the next boot
// reads, so a zero that reached the live config (a test, a restored backup, a
// struct literal) must be written as the default, not reloaded forever.
func TestSaveNeverPersistsZeroChapterThreshold(t *testing.T) {
	restoreAppConfig(t)
	ResetToDefaults()
	Mutate(func(c *Config) { c.ChapterConsolidationThresholdMin = 0 })

	store := newMockSettingsStore()
	require.NoError(t, SaveConfigToDatabase(store))

	blob, ok := store.settings["config_blob"]
	require.True(t, ok, "config_blob was not written")
	var stored map[string]any
	require.NoError(t, json.Unmarshal([]byte(blob.Value), &stored))
	require.EqualValues(t, DefaultChapterConsolidationThresholdMin, stored["chapter_consolidation_threshold_min"])
}
