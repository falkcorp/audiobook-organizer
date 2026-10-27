// file: internal/plugins/maintenance/trash_cleanup_failure_test.go
// version: 1.2.0
// guid: 9e2c4a17-5b83-4d60-a1f9-0c7d3e8b5a42
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// trashCleanupDeps answers CleanupTrashedVersions with a fixed outcome.
type trashCleanupDeps struct {
	fakeDeps
	res versions.CleanupResult
	err error
}

func (d trashCleanupDeps) CleanupTrashedVersions() (versions.CleanupResult, error) {
	return d.res, d.err
}

// levelReporter records each log line with its level and keeps the result the
// op persisted (the trash cleanup always persists one).
type levelReporter struct {
	fakeReporter
	levels []slog.Level
	result any
}

func (r *levelReporter) Log(l slog.Level, msg string, attrs ...slog.Attr) error {
	r.levels = append(r.levels, l)
	return r.fakeReporter.Log(l, msg, attrs...)
}

func (r *levelReporter) SetResult(v any) error { r.result = v; return nil }

// A night on which due purges failed must not end "completed": the op returns
// the error, and the log names the purged and failed counts at Warn.
func TestRunTrashCleanup_FailedPurgesFailTheOp(t *testing.T) {
	cause := errors.New("book_file lookup incomplete")
	p := &Plugin{deps: trashCleanupDeps{res: versions.CleanupResult{Purged: 2, Failed: 3}, err: cause}}
	rep := &levelReporter{}

	err := p.runTrashCleanup(context.Background(), nil, rep)
	if !errors.Is(err, cause) {
		t.Fatalf("runTrashCleanup = %v, want the cleanup's error", err)
	}
	if len(rep.logs) != 1 || rep.levels[0] != slog.LevelWarn {
		t.Fatalf("logs=%v levels=%v, want one Warn line", rep.logs, rep.levels)
	}
	for _, want := range []string{"purged 2", "3 failed", "book_file lookup incomplete"} {
		if !strings.Contains(rep.logs[0], want) {
			t.Errorf("log %q does not contain %q", rep.logs[0], want)
		}
	}
}

// A clean run is a success and logs the count at Info (the control: the op does
// succeed when nothing failed, including when nothing was due).
func TestRunTrashCleanup_CleanRunSucceeds(t *testing.T) {
	for _, purged := range []int{0, 4} {
		p := &Plugin{deps: trashCleanupDeps{res: versions.CleanupResult{Purged: purged}}}
		rep := &levelReporter{}
		if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
			t.Fatalf("purged=%d: runTrashCleanup = %v, want nil", purged, err)
		}
		if len(rep.logs) != 1 || rep.levels[0] != slog.LevelInfo {
			t.Fatalf("purged=%d: logs=%v levels=%v, want one Info line", purged, rep.logs, rep.levels)
		}
		got, ok := rep.result.(TrashCleanupResult)
		if !ok || got.Purged != purged || got.KeptFiles != 0 || got.NeedsAttention != 0 {
			t.Errorf("purged=%d: result = %#v", purged, rep.result)
		}
	}
}

// A version purged with files kept is not "purged N" and nothing else: the
// summary line says how many kept files and which versions, and the result
// payload carries the count and the version and book ids the page links from.
func TestRunTrashCleanup_KeptFilesAreReportedNotPresentedAsPurged(t *testing.T) {
	res := versions.CleanupResult{
		Purged: 3,
		KeptFiles: []versions.KeptVersion{{
			VersionID: "ver-hush", BookID: "book-hush",
			Files: []database.PurgeFileNote{{Path: "/lib/Gotham/Hush/hush.m4b", Reason: "it is the file path of book(s) [book-hush-2] (live or in the trash)"}},
		}},
	}
	p := &Plugin{deps: trashCleanupDeps{res: res}}
	rep := &levelReporter{}
	if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
		t.Fatalf("runTrashCleanup = %v, want nil (a kept file is not a failure)", err)
	}
	joined := strings.Join(rep.logs, "\n")
	for _, want := range []string{"purged 3 versions, 1 of them with files kept", "ver-hush"} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs %q do not contain %q", joined, want)
		}
	}
	got := rep.result.(TrashCleanupResult)
	if got.KeptFiles != 1 || len(got.KeptVersionIDs) != 1 || got.KeptVersionIDs[0] != "ver-hush" ||
		len(got.KeptBookIDs) != 1 || got.KeptBookIDs[0] != "book-hush" ||
		len(got.KeptFilePaths) != 1 || got.KeptFilePaths[0] != "/lib/Gotham/Hush/hush.m4b" {
		t.Errorf("result = %#v, want the kept version, book and file ids", got)
	}
	if rep.levels[0] != slog.LevelWarn {
		t.Errorf("the kept-files line is at level %v, want Warn", rep.levels[0])
	}
}

// A purge that gave up (purge_failed) is listed for the owner and does not fail
// the op: the night is not red for ever for a file that cannot be removed.
func TestRunTrashCleanup_PurgeFailedVersionsNeedAttentionAndDoNotFailTheOp(t *testing.T) {
	res := versions.CleanupResult{
		NeedsAttention: []versions.KeptVersion{{VersionID: "ver-asylum", BookID: "book-asylum"}},
	}
	p := &Plugin{deps: trashCleanupDeps{res: res}}
	rep := &levelReporter{}
	if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
		t.Fatalf("runTrashCleanup = %v, want nil: a purge_failed version is not a failure of the night", err)
	}
	got := rep.result.(TrashCleanupResult)
	if got.NeedsAttention != 1 || got.NeedsAttentionVersionIDs[0] != "ver-asylum" || got.NeedsAttentionBookIDs[0] != "book-asylum" || got.Failed != 0 {
		t.Errorf("result = %#v", got)
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "need attention (versions ver-asylum)") {
		t.Errorf("logs %v do not name the version that needs attention", rep.logs)
	}
}

// A night that stopped at a busy merge lock is not a failed night: the op
// completes, the result says it stopped, and the log tells the owner the rest
// runs next night.
func TestRunTrashCleanup_StoppedForMergeIsReportedAndDoesNotFailTheOp(t *testing.T) {
	p := &Plugin{deps: trashCleanupDeps{res: versions.CleanupResult{Purged: 1, StoppedForMerge: true}}}
	rep := &levelReporter{}
	if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
		t.Fatalf("runTrashCleanup = %v, want nil: a busy merge lock is not a failure of the night", err)
	}
	got := rep.result.(TrashCleanupResult)
	if !got.StoppedForMerge || got.Purged != 1 || got.Failed != 0 {
		t.Errorf("result = %#v, want purged 1, failed 0, stopped_for_merge", got)
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "stopped early: a merge or combine was running, the rest runs next night") {
		t.Errorf("logs %v do not say the run stopped early", rep.logs)
	}
}

// The night's summary is logged whether or not it failed: a run that failed one
// purge has still purged others, kept files, and stopped for the merge lock, and
// the log of the failing night is the one that gets read.
func TestRunTrashCleanup_FailedRunStillLogsTheWholeSummary(t *testing.T) {
	cause := errors.New("book_file lookup incomplete")
	res := versions.CleanupResult{
		Purged: 2, Failed: 1, StoppedForMerge: true,
		KeptFiles:      []versions.KeptVersion{{VersionID: "ver-hush", BookID: "book-hush", Files: []database.PurgeFileNote{{Path: "/lib/Gotham/Hush/hush.m4b", Reason: "kept"}}}},
		NeedsAttention: []versions.KeptVersion{{VersionID: "ver-asylum", BookID: "book-asylum"}},
	}
	p := &Plugin{deps: trashCleanupDeps{res: res, err: cause}}
	rep := &levelReporter{}

	if err := p.runTrashCleanup(context.Background(), nil, rep); !errors.Is(err, cause) {
		t.Fatalf("runTrashCleanup = %v, want the cleanup's error", err)
	}
	joined := strings.Join(rep.logs, "\n")
	for _, want := range []string{"purged 2", "1 of them with files kept", "ver-hush", "need attention (versions ver-asylum)", "stopped early: a merge or combine was running", "1 failed", "book_file lookup incomplete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs %q do not contain %q", joined, want)
		}
	}
}

// One or two stopped nights are reported as they always were. The streak is on
// the result, but nothing is escalated.
func TestRunTrashCleanup_ShortMergeStopStreakIsNotEscalated(t *testing.T) {
	p := &Plugin{deps: trashCleanupDeps{res: versions.CleanupResult{StoppedForMerge: true, MergeStopStreak: versions.MergeStopEscalateAfter - 1}}}
	rep := &levelReporter{}
	if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
		t.Fatalf("runTrashCleanup = %v", err)
	}
	got := rep.result.(TrashCleanupResult)
	if got.StoppedForMergeRepeatedly || got.StoppedForMergeNights != versions.MergeStopEscalateAfter-1 {
		t.Errorf("result = %#v, want the streak recorded and not flagged as repeated", got)
	}
	for i, l := range rep.levels {
		if l >= slog.LevelWarn {
			t.Errorf("log %q is at %v, want Info", rep.logs[i], l)
		}
	}
}

// Several stopped nights in a row are a warning in the log and a flag on the
// result, and still not a failure: the op completes.
func TestRunTrashCleanup_RepeatedMergeStopsWarnWithoutFailingTheOp(t *testing.T) {
	p := &Plugin{deps: trashCleanupDeps{res: versions.CleanupResult{StoppedForMerge: true, MergeStopStreak: versions.MergeStopEscalateAfter}}}
	rep := &levelReporter{}
	if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
		t.Fatalf("runTrashCleanup = %v, want nil: the op stays green", err)
	}
	got := rep.result.(TrashCleanupResult)
	if !got.StoppedForMergeRepeatedly || got.StoppedForMergeNights != versions.MergeStopEscalateAfter {
		t.Errorf("result = %#v, want the streak flagged as repeated", got)
	}
	warned := false
	for i, l := range rep.levels {
		if l >= slog.LevelWarn && strings.Contains(rep.logs[i], "nights in a row") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning about the repeated stops in %v (levels %v)", rep.logs, rep.levels)
	}
}
