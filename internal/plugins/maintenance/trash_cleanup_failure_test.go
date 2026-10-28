// file: internal/plugins/maintenance/trash_cleanup_failure_test.go
// version: 1.4.2
// guid: 9e2c4a17-5b83-4d60-a1f9-0c7d3e8b5a42
// last-edited: 2026-10-28

package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
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

// failingSetResultReporter is a levelReporter whose result write fails.
type failingSetResultReporter struct {
	levelReporter
	err error
}

func (r *failingSetResultReporter) SetResult(any) error { return r.err }

// A night whose purges failed AND whose result could not be stored fails with
// both: dropping the second left the owner chasing the purge failure while the
// Activity page had no result to show for the run.
func TestRunTrashCleanup_FailedPurgesAndUnstoredResultReportBoth(t *testing.T) {
	cause := errors.New("book_file lookup incomplete")
	stored := errors.New("result store unavailable")
	p := &Plugin{deps: trashCleanupDeps{res: versions.CleanupResult{Purged: 1, Failed: 1}, err: cause}}
	rep := &failingSetResultReporter{err: stored}

	err := p.runTrashCleanup(context.Background(), nil, rep)
	if !errors.Is(err, cause) || !errors.Is(err, stored) {
		t.Fatalf("runTrashCleanup = %v, want an error wrapping both the cleanup's (%v) and the result write's (%v)", err, cause, stored)
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

// A version left in the trash because its book serves nothing that exists on disk
// is its own line in the result and the log, carrying the book's path (the thing
// to fix), at Warn, not a failure, and not the "cannot tell / drive down" wording.
func TestRunTrashCleanup_NothingServedIsItsOwnBucketWithTheBookPath(t *testing.T) {
	res := versions.CleanupResult{
		NothingServed: []versions.KeptVersion{{
			VersionID: "ver-hush", BookID: "book-hush", BookPath: "/lib/Gotham/Hush/hush.m4b",
			Files: []database.PurgeFileNote{{Path: "/lib/Gotham/Hush/hush.m4b", Reason: "no such file or directory"}},
		}},
	}
	p := &Plugin{deps: trashCleanupDeps{res: res}}
	rep := &levelReporter{}
	if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
		t.Fatalf("runTrashCleanup = %v, want nil: a book that serves nothing is not a failure of the night", err)
	}
	got := rep.result.(TrashCleanupResult)
	if got.NothingServed != 1 || got.CannotTell != 0 || got.Failed != 0 ||
		len(got.NothingServedVersionIDs) != 1 || got.NothingServedVersionIDs[0] != "ver-hush" ||
		len(got.NothingServedBookIDs) != 1 || got.NothingServedBookIDs[0] != "book-hush" ||
		len(got.NothingServedFilePaths) != 1 || got.NothingServedFilePaths[0] != "/lib/Gotham/Hush/hush.m4b" {
		t.Errorf("result = %#v, want the version, book and the book's path under nothing_served only", got)
	}
	joined := strings.Join(rep.logs, "\n")
	for _, want := range []string{"serves nothing that exists on disk", "/lib/Gotham/Hush/hush.m4b", "their book serves nothing that exists on disk (versions ver-hush)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs %q do not contain %q", joined, want)
		}
	}
	if strings.Contains(joined, "drive that is down") {
		t.Errorf("logs %q describe a nothing-served version as a down drive", joined)
	}
}

// Two trashed versions of one book are two entries, but the book's path is one
// thing to fix: it is named once, in the order first met, and the per-version
// ids stay one each.
func TestRunTrashCleanup_NothingServedNamesABookPathOnce(t *testing.T) {
	hush := "/lib/Gotham/Hush/hush.m4b"
	res := versions.CleanupResult{
		NothingServed: []versions.KeptVersion{
			{VersionID: "ver-hush-1", BookID: "book-hush", BookPath: hush},
			{VersionID: "ver-year-one", BookID: "book-year-one", BookPath: "/lib/Gotham/Year One/year-one.m4b"},
			{VersionID: "ver-hush-2", BookID: "book-hush", BookPath: hush},
		},
	}
	p := &Plugin{deps: trashCleanupDeps{res: res}}
	rep := &levelReporter{}
	if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
		t.Fatalf("runTrashCleanup = %v, want nil", err)
	}
	got := rep.result.(TrashCleanupResult)
	want := []string{hush, "/lib/Gotham/Year One/year-one.m4b"}
	if !reflect.DeepEqual(got.NothingServedFilePaths, want) {
		t.Errorf("nothing_served_file_paths = %v, want %v", got.NothingServedFilePaths, want)
	}
	if got.NothingServed != 3 || len(got.NothingServedVersionIDs) != 3 {
		t.Errorf("result = %#v, want all three versions still counted", got)
	}
}

// A book with two versions in one group is two versions but one book, and the
// files the two name are listed once: the page keys its book and path lists by
// value, so a repeat is a duplicate key. Version ids stay one per version.
func TestRunTrashCleanup_ABookWithTwoVersionsInOneSetIsNamedOnce(t *testing.T) {
	part1 := "/lib/Gotham/Year One/part1.m4b"
	part2 := "/lib/Gotham/Year One/part2.m4b"
	note := func(paths ...string) []database.PurgeFileNote {
		var out []database.PurgeFileNote
		for _, p := range paths {
			out = append(out, database.PurgeFileNote{Path: p, Reason: "another row names it"})
		}
		return out
	}
	twin := func(files []database.PurgeFileNote) []versions.KeptVersion {
		return []versions.KeptVersion{
			{VersionID: "ver-1", BookID: "book-year-one", Files: files},
			{VersionID: "ver-2", BookID: "book-year-one", Files: files},
			{VersionID: "ver-3", BookID: "book-zero-year", Files: note("/lib/Gotham/Zero Year/zero-year.m4b")},
		}
	}
	res := versions.CleanupResult{
		KeptFiles:      twin(note(part1, part2)),
		Kept:           twin(note(part1, part2)),
		NeedsAttention: twin(nil),
		CannotTell:     twin(nil),
		NothingServed:  twin(nil),
	}
	p := &Plugin{deps: trashCleanupDeps{res: res}}
	rep := &levelReporter{}
	if err := p.runTrashCleanup(context.Background(), nil, rep); err != nil {
		t.Fatalf("runTrashCleanup = %v, want nil", err)
	}
	got := rep.result.(TrashCleanupResult)
	books := []string{"book-year-one", "book-zero-year"}
	paths := []string{part1, part2, "/lib/Gotham/Zero Year/zero-year.m4b"}
	versionIDs := []string{"ver-1", "ver-2", "ver-3"}
	for name, c := range map[string]struct{ got, want []string }{
		"kept books":             {got.KeptBookIDs, books},
		"kept paths":             {got.KeptFilePaths, paths},
		"served_kept books":      {got.ServedKeptBookIDs, books},
		"served_kept paths":      {got.ServedKeptFilePaths, paths},
		"needs_attention books":  {got.NeedsAttentionBookIDs, books},
		"cannot_tell books":      {got.CannotTellBookIDs, books},
		"nothing_served books":   {got.NothingServedBookIDs, books},
		"kept versions":          {got.KeptVersionIDs, versionIDs},
		"served_kept versions":   {got.ServedKeptVersionIDs, versionIDs},
		"nothing_served version": {got.NothingServedVersionIDs, versionIDs},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", name, c.got, c.want)
		}
	}
	if got.KeptFiles != 3 || got.ServedKept != 3 || got.NothingServed != 3 {
		t.Errorf("counts = %d/%d/%d, want 3 versions each", got.KeptFiles, got.ServedKept, got.NothingServed)
	}
}
