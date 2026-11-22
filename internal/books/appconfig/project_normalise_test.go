// file: internal/books/appconfig/project_normalise_test.go
// version: 1.0.0
// guid: 1f0b0bbc-843e-4e89-bb4f-e28a4a1c6086
// last-edited: 2026-11-21

package appconfig

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	logging "github.com/falkcorp/mediacore/log"
)

// TestEngine_ANormalisedDefaultSurvivesASave: HEAD's boot ran Validate on the
// live config, which replaces each zero that means "the default" (min book
// size, min rescan age, the vector backend, the chapter consolidation
// threshold, the log retention window) in place, and HEAD's PUT ran the
// same normalisation on its candidate, so the process config never showed
// those zeros again. Flipped, the boot's Validate lands on the projection and
// the next rebuild's Project replaced it with the raw rows: after any save the
// zeros came back. The projection a save publishes must be normalised too.
func TestEngine_ANormalisedDefaultSurvivesASave(t *testing.T) {
	prev := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	f := newFake()
	f.vals["core.log.level"] = json.RawMessage(`"warn"`)
	e := New(f, config.Project)
	if err := e.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// What the boot does after the Facade is installed (books' boot step
	// config.Validate, cmd/root.go): normalise and validate the live config.
	_ = config.NormalizeAndValidate()
	booted := config.Snapshot()
	if booted.MinBookSizeBytes != 5*1024*1024 || booted.MinRescanAgeHours != 144 || booted.Embedding.VectorBackend != "hnsw" ||
		booted.ChapterConsolidationThresholdMin != config.DefaultChapterConsolidationThresholdMin ||
		booted.LogRetentionDays != logging.DefaultLogRetentionDays {
		t.Fatalf("after the boot's Validate: min_book_size_bytes=%d min_rescan_age_hours=%d vector_backend=%q chapter_consolidation_threshold_min=%d log_retention_days=%d",
			booted.MinBookSizeBytes, booted.MinRescanAgeHours, booted.Embedding.VectorBackend,
			booted.ChapterConsolidationThresholdMin, booted.LogRetentionDays)
	}

	// A save of an unrelated key rebuilds and projects.
	if err := e.Mutate(func(c *config.Config) { c.LogLevel = "debug" }); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	saved := config.Snapshot()
	if saved.LogLevel != "debug" {
		t.Fatalf("the save did not project: log_level=%q", saved.LogLevel)
	}
	if saved.MinBookSizeBytes != booted.MinBookSizeBytes || saved.MinRescanAgeHours != booted.MinRescanAgeHours ||
		saved.Embedding.VectorBackend != booted.Embedding.VectorBackend {
		t.Fatalf("a save un-normalised the config: min_book_size_bytes %d -> %d, min_rescan_age_hours %d -> %d, vector_backend %q -> %q",
			booted.MinBookSizeBytes, saved.MinBookSizeBytes, booted.MinRescanAgeHours, saved.MinRescanAgeHours,
			booted.Embedding.VectorBackend, saved.Embedding.VectorBackend)
	}
	// Readers go through Resolve* for these two, but GET /config shows the
	// field, and a 0 there is the value the rule says never shows.
	if saved.ChapterConsolidationThresholdMin != booted.ChapterConsolidationThresholdMin || saved.LogRetentionDays != booted.LogRetentionDays {
		t.Fatalf("a save un-normalised the config: chapter_consolidation_threshold_min %d -> %d, log_retention_days %d -> %d",
			booted.ChapterConsolidationThresholdMin, saved.ChapterConsolidationThresholdMin, booted.LogRetentionDays, saved.LogRetentionDays)
	}
}
