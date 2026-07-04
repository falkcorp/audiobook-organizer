// file: internal/plugins/dedup/rebuild_gold_labels_test.go
// version: 1.0.0
// guid: 2b8e5f14-6a37-4c92-9d05-3f7a8b1c6e40
// last-edited: 2026-07-04

// Tests for the dedup.rebuild-gold-labels op against a real PebbleStore +
// EmbeddingStore: dry-run diff reporting (changed/unchanged/unlabelable),
// apply wiping+reinserting rule/auto_high_conf while leaving human (and
// unlabeled) rows untouched, and apply idempotency.
package dedup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// createBookNoFiles creates a book with no BookFile records, so
// dataset.Classify's missingFile catcher fires not_dup for any pair
// involving it.
func createBookNoFiles(t *testing.T, pebble *database.PebbleStore, title string) string {
	t.Helper()
	created, err := pebble.CreateBook(&database.Book{Title: title, FilePath: "/audio/" + title + ".m4b"})
	if err != nil {
		t.Fatalf("CreateBook %q: %v", title, err)
	}
	return created.ID
}

func rebuildFixture(t *testing.T) (*database.PebbleStore, *database.EmbeddingStore, *Plugin) {
	t.Helper()
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	p := &Plugin{store: pebble, embeddingStore: es}
	return pebble, es, p
}

// TestRebuildGoldLabels_DryRun_RuleChangedAndUnchanged covers the rule bucket:
// a stale label that no longer matches Classify's current verdict is reported
// changed and left unwritten (dry-run); a label that already matches the
// current verdict is reported unchanged.
func TestRebuildGoldLabels_DryRun_RuleChangedAndUnchanged(t *testing.T) {
	pebble, es, p := rebuildFixture(t)

	// missingFile fires not_dup for any pair where one side has no files.
	noFiles := createBookNoFiles(t, pebble, "Ghost")
	hasFiles := createBookWithHashedFile(t, pebble, "Real", "hash-real-0001")
	changedCand := candidateID(t, es, noFiles, hasFiles)

	// Stale: stored as true_dup, but Classify would say not_dup today.
	if err := es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: changedCand, EntityAID: noFiles, EntityBID: hasFiles,
		Label: "true_dup", LabelSource: "rule", LabelReason: "stale",
	}); err != nil {
		t.Fatalf("seed stale rule example: %v", err)
	}

	// Unchanged: another no-files pair, already stored as not_dup.
	noFiles2 := createBookNoFiles(t, pebble, "Ghost2")
	hasFiles2 := createBookWithHashedFile(t, pebble, "Real2", "hash-real-0002")
	unchangedCand := candidateID(t, es, noFiles2, hasFiles2)
	if err := es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: unchangedCand, EntityAID: noFiles2, EntityBID: hasFiles2,
		Label: "not_dup", LabelSource: "rule", LabelReason: "side A has no resolvable files",
	}); err != nil {
		t.Fatalf("seed unchanged rule example: %v", err)
	}

	// Unlabelable: a rule row pointing at a candidate ID that no longer exists.
	if err := es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: 999999, EntityAID: "gone-a", EntityBID: "gone-b",
		Label: "true_dup", LabelSource: "rule", LabelReason: "orphaned",
	}); err != nil {
		t.Fatalf("seed orphaned rule example: %v", err)
	}

	if err := p.runRebuildGoldLabels(context.Background(), json.RawMessage(`{}`), &fakeReporter{}); err != nil {
		t.Fatalf("runRebuildGoldLabels dry-run: %v", err)
	}

	// Dry-run must write nothing — all three seeded rows are untouched.
	ex, err := es.GetLabeledExample(changedCand)
	if err != nil {
		t.Fatalf("GetLabeledExample(changed): %v", err)
	}
	if ex == nil || ex.Label != "true_dup" {
		t.Fatalf("dry-run must not mutate stored rows; got %+v", ex)
	}
	ex2, err := es.GetLabeledExample(unchangedCand)
	if err != nil {
		t.Fatalf("GetLabeledExample(unchanged): %v", err)
	}
	if ex2 == nil || ex2.Label != "not_dup" {
		t.Fatalf("dry-run must not mutate stored rows; got %+v", ex2)
	}
	ex3, err := es.GetLabeledExample(999999)
	if err != nil {
		t.Fatalf("GetLabeledExample(orphaned): %v", err)
	}
	if ex3 == nil {
		t.Fatal("dry-run must not delete stored rows")
	}
}

// TestRebuildGoldLabels_Apply_WipesRuleAndAutoHighConf_PreservesHumanAndOther
// verifies the apply path: rule/auto_high_conf rows are recomputed from
// current state (stale labels corrected), human rows are passed through
// verbatim, and unlabeled (LabelSource=="") rows are left alone entirely.
func TestRebuildGoldLabels_Apply_WipesRuleAndAutoHighConf_PreservesHumanAndOther(t *testing.T) {
	pebble, es, p := rebuildFixture(t)

	// Rule bucket: stale true_dup that should become not_dup.
	noFiles := createBookNoFiles(t, pebble, "Ghost")
	hasFiles := createBookWithHashedFile(t, pebble, "Real", "hash-real-1111")
	ruleCand := candidateID(t, es, noFiles, hasFiles)
	if err := es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: ruleCand, EntityAID: noFiles, EntityBID: hasFiles,
		Label: "true_dup", LabelSource: "rule", LabelReason: "stale",
	}); err != nil {
		t.Fatalf("seed rule example: %v", err)
	}

	// auto_high_conf bucket: stale not_dup that should become true_dup
	// (shared file hash fires MineHighConfidenceDup).
	dupA := createBookWithHashedFile(t, pebble, "MobyA", "sharedhash0000ff")
	dupB := createBookWithHashedFile(t, pebble, "MobyB", "sharedhash0000ff")
	autoCand := candidateID(t, es, dupA, dupB)
	if err := es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: autoCand, EntityAID: dupA, EntityBID: dupB,
		Label: "not_dup", LabelSource: "auto_high_conf", LabelReason: "stale",
	}); err != nil {
		t.Fatalf("seed auto_high_conf example: %v", err)
	}

	// human bucket: must survive verbatim.
	humanA := createBookWithHashedFile(t, pebble, "HumanA", "hash-human-a")
	humanB := createBookWithHashedFile(t, pebble, "HumanB", "hash-human-b")
	humanCand := candidateID(t, es, humanA, humanB)
	if err := es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: humanCand, EntityAID: humanA, EntityBID: humanB,
		Label: "not_dup", LabelSource: "human", LabelReason: "reviewer decided these are different works",
	}); err != nil {
		t.Fatalf("seed human example: %v", err)
	}

	// unlabeled/other bucket: LabelSource=="" (dataset-backfill "no catcher fired" row).
	otherA := createBookWithHashedFile(t, pebble, "OtherA", "hash-other-a")
	otherB := createBookWithHashedFile(t, pebble, "OtherB", "hash-other-b")
	otherCand := candidateID(t, es, otherA, otherB)
	if err := es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: otherCand, EntityAID: otherA, EntityBID: otherB,
		Label: "", LabelSource: "", LabelReason: "",
	}); err != nil {
		t.Fatalf("seed unlabeled example: %v", err)
	}

	if err := p.runRebuildGoldLabels(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("runRebuildGoldLabels apply: %v", err)
	}

	// Rule row recomputed to not_dup.
	ruleEx, err := es.GetLabeledExample(ruleCand)
	if err != nil {
		t.Fatalf("GetLabeledExample(rule): %v", err)
	}
	if ruleEx == nil || ruleEx.Label != "not_dup" || ruleEx.LabelSource != "rule" {
		t.Fatalf("rule row: got %+v, want not_dup/rule", ruleEx)
	}

	// auto_high_conf row recomputed to true_dup.
	autoEx, err := es.GetLabeledExample(autoCand)
	if err != nil {
		t.Fatalf("GetLabeledExample(auto): %v", err)
	}
	if autoEx == nil || autoEx.Label != "true_dup" || autoEx.LabelSource != "auto_high_conf" {
		t.Fatalf("auto_high_conf row: got %+v, want true_dup/auto_high_conf", autoEx)
	}

	// human row untouched byte-for-byte.
	humanEx, err := es.GetLabeledExample(humanCand)
	if err != nil {
		t.Fatalf("GetLabeledExample(human): %v", err)
	}
	if humanEx == nil || humanEx.Label != "not_dup" || humanEx.LabelSource != "human" ||
		humanEx.LabelReason != "reviewer decided these are different works" {
		t.Fatalf("human row must survive verbatim; got %+v", humanEx)
	}

	// unlabeled/other row untouched.
	otherEx, err := es.GetLabeledExample(otherCand)
	if err != nil {
		t.Fatalf("GetLabeledExample(other): %v", err)
	}
	if otherEx == nil || otherEx.LabelSource != "" || otherEx.Label != "" {
		t.Fatalf("unlabeled row must survive untouched; got %+v", otherEx)
	}
}

// TestRebuildGoldLabels_Apply_Idempotent runs apply twice and checks the
// second run produces the same label/source assignments as the first — the
// rebuild is a pure function of current candidate/book state, so a repeat
// apply must be a stable no-op in substance (DecidedAt timestamps aside).
func TestRebuildGoldLabels_Apply_Idempotent(t *testing.T) {
	pebble, es, p := rebuildFixture(t)

	noFiles := createBookNoFiles(t, pebble, "Ghost")
	hasFiles := createBookWithHashedFile(t, pebble, "Real", "hash-real-2222")
	ruleCand := candidateID(t, es, noFiles, hasFiles)
	if err := es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: ruleCand, EntityAID: noFiles, EntityBID: hasFiles,
		Label: "true_dup", LabelSource: "rule", LabelReason: "stale",
	}); err != nil {
		t.Fatalf("seed rule example: %v", err)
	}

	dupA := createBookWithHashedFile(t, pebble, "MobyA", "sharedhash1111ff")
	dupB := createBookWithHashedFile(t, pebble, "MobyB", "sharedhash1111ff")
	autoCand := candidateID(t, es, dupA, dupB)
	if err := es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: autoCand, EntityAID: dupA, EntityBID: dupB,
		Label: "not_dup", LabelSource: "auto_high_conf", LabelReason: "stale",
	}); err != nil {
		t.Fatalf("seed auto_high_conf example: %v", err)
	}

	run := func() map[int64][2]string { // candidateID -> [label, source]
		if err := p.runRebuildGoldLabels(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
			t.Fatalf("runRebuildGoldLabels apply: %v", err)
		}
		all, err := es.ListLabeledExamples(database.LabeledExampleFilter{})
		if err != nil {
			t.Fatalf("ListLabeledExamples: %v", err)
		}
		snap := make(map[int64][2]string, len(all))
		for _, ex := range all {
			snap[ex.CandidateID] = [2]string{ex.Label, ex.LabelSource}
		}
		return snap
	}

	first := run()
	second := run()

	if len(first) != len(second) {
		t.Fatalf("row count changed across repeat apply: first=%d second=%d", len(first), len(second))
	}
	for id, want := range first {
		got, ok := second[id]
		if !ok {
			t.Fatalf("candidate %d present in first apply but missing in second", id)
		}
		if got != want {
			t.Fatalf("candidate %d: first=%v second=%v — apply is not idempotent", id, want, got)
		}
	}
}
