// file: internal/plugins/dedup/kinds_split_test.go
// version: 1.0.0
// guid: 5d2c8e41-7b09-4f36-a8d5-3e91c0b7f264
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// The fixture is Batman comic storylines: three one-chapter books of one work.
const splitBulkParams = `{"items":[` +
	`{"candidate_id":"c-year-one","book_ids":["yo-1","yo-2","yo-3"],"keep_id":"yo-1","suggested_title":"Batman: Year One"},` +
	`{"candidate_id":"c-hush","book_ids":["hu-1","hu-2"],"keep_id":"hu-1","suggested_title":"Hush"}]}`

func splitScanBuild(p *Plugin, d ScoringKindDeps) tasks.Definition {
	return p.splitBookScanNative(d, nil)
}
func splitMergeBuild(p *Plugin, d ScoringKindDeps) tasks.Definition {
	return p.splitBookBulkMergeNative(d, nil)
}

// --- split-book-scan ---

// A Live scan detects, replaces the saved candidates with the detected list and
// returns the count; the writer is the run's own.
func TestDedupSplit_ScanLiveSavesWhatTheDetectorFound(t *testing.T) {
	spy, d := newScoringSpy()
	spy.splitCands = []dedupengine.SplitBookCandidate{{BookIDs: []string{"yo-1", "yo-2"}}, {BookIDs: []string{"hu-1", "hu-2"}}}
	w := &sentinelWriter{}
	h, out, err := runKindResult(t, splitScanBuild(&Plugin{}, d), tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"DetectSplitBookCandidates", "SaveAll n=2"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Split-book scan complete — 2 candidate cluster(s) saved")
	var res splitBookScanResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, 2, res.Candidates)
}

// HEAD has no preview for the scan; a native Preview must not replace the saved
// rows, so it is refused before anything is built or read.
func TestDedupSplit_ScanPreviewIsRefused(t *testing.T) {
	spy, d := newScoringSpy()
	_, _, err := runKindResult(t, splitScanBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "preview not supported; start with mode live")
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
}

func TestDedupSplit_ScanRefusesModeKeysInParams(t *testing.T) {
	requireScoringModeKeysRefused(t, splitScanBuild)
}

// A detector error fails the run with HEAD's wrapping and saves nothing; a save
// error is HEAD's persist error.
func TestDedupSplit_ScanErrorPaths(t *testing.T) {
	spy, d := newScoringSpy()
	spy.detectErr = errors.New("pebble closed")
	_, _, err := runKindResult(t, splitScanBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "split-book scan: pebble closed")
	require.Empty(t, spy.written(), "a failed detection must not replace the saved candidates")

	spy, d = newScoringSpy()
	spy.saveErr = errors.New("disk full")
	_, _, err = runKindResult(t, splitScanBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "split-book scan persist: disk full")
}

func TestDedupSplit_ScanRefusesUnwiredDeps(t *testing.T) {
	_, d := newScoringSpy()
	d.SplitReads = nil
	_, _, err := runKindResult(t, splitScanBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.SplitReads is not set")

	_, d = newScoringSpy()
	d.NewSplitWriter = nil
	_, _, err = runKindResult(t, splitScanBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewSplitWriter is not set")
}

// The HEAD shell keeps its two preconditions, in order.
func TestDedupSplit_ScanHeadShellPreconditions(t *testing.T) {
	err := (&Plugin{}).runSplitBookScan(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "split-book scan: store not available")
}

// --- split-book-bulk-merge ---

// A Live run merges each cluster under the run's id, deletes a merged candidate's
// row, and keeps one that was refused (recheck, iTunes) or came back incomplete.
func TestDedupSplit_BulkMergeLiveOutcomes(t *testing.T) {
	spy, d := newScoringSpy()
	spy.mergeErr["yo-1"] = merge.ErrITunesProtected
	spy.mergeErr["hu-1"] = dedupengine.ErrSplitBookRefused
	items := `{"items":[` +
		`{"candidate_id":"c-ok","book_ids":["ok-1","ok-2"],"keep_id":"ok-1"},` +
		`{"candidate_id":"c-yo","book_ids":["yo-1","yo-2"],"keep_id":"yo-1"},` +
		`{"candidate_id":"c-hu","book_ids":["hu-1","hu-2"],"keep_id":"hu-1"},` +
		`{"candidate_id":"c-half","book_ids":["hf-1","hf-2","hf-3"],"keep_id":"hf-1"}]}`
	spy.mergeRes["hf-1"] = &dedupengine.SplitBookMergeResult{KeepID: "hf-1", MergedSrcCount: 1}
	w := &sentinelWriter{}
	h, out, err := runKindResult(t, splitMergeBuild(&Plugin{}, d), tasks.ModeLive, w, items)
	require.NoError(t, err)

	runID := string(h.RunID())
	require.NotEmpty(t, runID)
	require.Equal(t, []string{"ok-1<-" + runID, "yo-1<-" + runID, "hu-1<-" + runID, "hf-1<-" + runID}, spy.merged,
		"every merge journals under this run, so reverting the run undoes the batch")
	require.Equal(t, []string{"c-ok"}, spy.deleted, "only a complete merge deletes its candidate row")
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	var res splitBookBulkMergeResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, splitBookBulkMergeResult{Merged: 1, RefusedITunes: 1, RefusedRecheck: 1, Retained: 1}, res)
	require.Contains(t, h.lastNote(), "Split-book bulk merge finished; incomplete candidates remain reviewable (1 refused: iTunes library, 1 refused: not shown to be chapters)")
}

// A failed candidate delete after a merge is logged, not fatal: the merge stands.
func TestDedupSplit_BulkMergeDeleteFailureDoesNotFailTheRun(t *testing.T) {
	spy, d := newScoringSpy()
	spy.deleteErr = errors.New("pebble closed")
	_, out, err := runKindResult(t, splitMergeBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, splitBulkParams)
	require.NoError(t, err)
	var res splitBookBulkMergeResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, 2, res.Merged)
}

// Preview is HEAD's dry run: it rechecks every member and reports what the real
// run would do, with no merge, no delete and no writer.
func TestDedupSplit_BulkMergePreviewRechecksAndWritesNothing(t *testing.T) {
	spy, d := newScoringSpy()
	spy.recheck["hu-1"] = []string{"a member is 90 minutes long"}
	h, out, err := runKindResult(t, splitMergeBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, splitBulkParams)
	require.NoError(t, err)
	require.Equal(t, []string{"RecheckSplitBookMembers yo-1", "RecheckSplitBookMembers hu-1"}, spy.recorded())
	requirePreviewWritesNothing(t, spy)
	require.Empty(t, spy.merged)
	var res splitBookBulkMergeResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, splitBookBulkMergeResult{Ready: 1, RefusedRecheck: 1}, res)
	require.Contains(t, h.lastNote(), "1 refused: not shown to be chapters")

	spy, d = newScoringSpy()
	spy.recheckErr["yo-1"] = errors.New("book gone")
	_, out, err = runKindResult(t, splitMergeBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, splitBulkParams)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, splitBookBulkMergeResult{Ready: 1}, res, "an unreadable member is retained, counted neither ready nor refused")
}

func TestDedupSplit_BulkMergeRefusesBadStarts(t *testing.T) {
	spy, d := newScoringSpy()
	for _, params := range []string{"", `{"items":[]}`} {
		_, _, err := runKindResult(t, splitMergeBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, params)
		require.ErrorContains(t, err, "split-book bulk merge: no candidate snapshots", params)
	}
	for _, params := range []string{`{"dry_run":true,"items":[]}`, `{"dryRun":false}`, `{"Apply":true}`} {
		_, _, err := runKindResult(t, splitMergeBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, params)
		require.ErrorContains(t, err, "the run's mode comes from the start request", params)
	}
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())

	_, d = newScoringSpy()
	d.NewSplitWriter = nil
	_, _, err := runKindResult(t, splitMergeBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, splitBulkParams)
	require.ErrorContains(t, err, "Scoring.NewSplitWriter is not set")
	_, d = newScoringSpy()
	d.SplitReads = nil
	_, _, err = runKindResult(t, splitMergeBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, splitBulkParams)
	require.ErrorContains(t, err, "Scoring.SplitReads is not set")
}

// The HEAD shell resolves the dry-run key itself and keeps its refusals.
func TestDedupSplit_BulkMergeHeadShellRefusals(t *testing.T) {
	p := &Plugin{}
	err := p.runSplitBookBulkMerge(context.Background(), json.RawMessage(`{"items":[]}`), &mockReporter{})
	require.ErrorContains(t, err, "no candidate snapshots")
	err = p.runSplitBookBulkMerge(context.Background(), json.RawMessage(splitBulkParams), &mockReporter{})
	require.ErrorContains(t, err, "split-book bulk merge: store unavailable")
}
