// file: internal/plugins/acoustid/window_backfill_test.go
// version: 1.2.0
// guid: 28adbfaa-a61f-4e34-ae2b-516e70cf775f
// last-edited: 2026-09-19

package acoustid

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/tools"
)

// ---- fixtures ----

// wbReporter records checkpoints of the window op.
type wbReporter struct {
	mu          sync.Mutex
	checkpoints []WindowBackfillParams
}

func (r *wbReporter) UpdateProgress(int, int, string) error      { return nil }
func (r *wbReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *wbReporter) Logger() *slog.Logger                       { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func (r *wbReporter) IsCanceled() bool                           { return false }
func (r *wbReporter) Trigger(context.Context, string, any) error { return nil }
func (r *wbReporter) SetCurrentItem(string)                      {}
func (r *wbReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}
func (r *wbReporter) Checkpoint(state any) error {
	cp, ok := state.(WindowBackfillParams)
	if !ok {
		return fmt.Errorf("checkpoint state is %T", state)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoints = append(r.checkpoints, cp)
	return nil
}
func (r *wbReporter) last() *WindowBackfillParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.checkpoints) == 0 {
		return nil
	}
	cp := r.checkpoints[len(r.checkpoints)-1]
	return &cp
}

// wbEnv is a real PebbleStore, a library dir, and fake ffmpeg/fpcalc scripts
// that log every file they are asked to cut.
type wbEnv struct {
	t      *testing.T
	store  *database.PebbleStore
	lib    string
	log    string
	plugin *Plugin
	tools  fingerprint.WindowTools
}

func skipNonUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binaries are /bin/sh scripts")
	}
}

func newWBEnv(t *testing.T) *wbEnv {
	t.Helper()
	skipNonUnix(t)
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	bin := t.TempDir()
	e := &wbEnv{t: t, store: store, lib: t.TempDir(), log: filepath.Join(bin, "ffmpeg.log")}

	// ffmpeg: log the input path, then behave by path substring:
	//   corrupt  exit 1 with a deterministic decoder message, every time
	//   flaky    exit 1 on the first call for that path only
	//   eio      exit 1 with "Input/output error" (an NFS/ZFS read failure)
	//   sigkill  die of SIGKILL (the OOM killer) with the ctx still live
	//   flip     exit 1 on the first call, a 10 s short cut after that
	//   slow     sleep first, so a test can cancel mid-run
	// A file whose first line is "dur=N" is N seconds long: a cut past N
	// emits only what is left, like a real seek near EOF. Otherwise it is
	// endless and every cut is full length.
	ffmpeg := filepath.Join(bin, "ffmpeg")
	require.NoError(t, os.WriteFile(ffmpeg, []byte(`#!/bin/sh
in=""; len=0; ss=0; prev=""
for a in "$@"; do
  [ "$prev" = "-i" ] && in="${a#file:}"
  [ "$prev" = "-t" ] && len="${a%%.*}"
  [ "$prev" = "-ss" ] && ss="${a%%.*}"
  prev="$a"
done
echo "$in" >> "`+e.log+`"
case "$in" in *corrupt*) echo "Invalid data found when processing input" >&2; exit 1;; esac
case "$in" in *eio*) echo "Input/output error" >&2; exit 1;; esac
case "$in" in *sigkill*) kill -9 $$;; esac
case "$in" in *flaky*) if [ ! -e "$in.flaked" ]; then : > "$in.flaked"; echo "Invalid data found" >&2; exit 1; fi;; esac
case "$in" in *flip*) if [ ! -e "$in.flipped" ]; then : > "$in.flipped"; echo "Invalid data found" >&2; exit 1; fi; head -c 220500 /dev/zero; exit 0;; esac
case "$in" in *slow*) sleep 0.2;; esac
real=$(head -c 64 "$in" | sed -n 's/^dur=\([0-9]*\).*/\1/p' | head -1)
if [ -n "$real" ]; then
  left=$((real - ss)); [ $left -lt 0 ] && left=0
  [ $left -lt $len ] && len=$left
fi
[ "$len" -gt 0 ] && head -c $((len * 22050)) /dev/zero
exit 0
`), 0o755))
	prevProbe := windowProbeDuration
	t.Cleanup(func() { windowProbeDuration = prevProbe })
	windowProbeDuration = func(_ context.Context, path string) (float64, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		var d float64
		if _, err := fmt.Sscanf(string(b), "dur=%g", &d); err != nil {
			return 0, fmt.Errorf("fake ffprobe: no duration in %s", path)
		}
		return d, nil
	}
	vals := make([]string, 200)
	for i := range vals {
		vals[i] = fmt.Sprint(1000 + i)
	}
	fpcalc := filepath.Join(bin, "fpcalc")
	require.NoError(t, os.WriteFile(fpcalc, []byte(fmt.Sprintf(`#!/bin/sh
cat >/dev/null
printf '{"duration": 0.00, "fingerprint": [%s]}\n'
`, strings.Join(vals, ","))), 0o755))

	e.tools = fingerprint.WindowTools{FpcalcPath: fpcalc, FFmpegPath: ffmpeg,
		Versions: fingerprint.ToolVersionInfo{Fpcalc: "1.6.0-fake", FFmpeg: "8.0.1-fake"}}
	e.plugin = &Plugin{store: store}
	e.plugin.windowToolsFn = func(context.Context) (fingerprint.WindowTools, error) { return e.tools, nil }
	return e
}

// addFile creates a book (or reuses bookID) with one row at rel. present
// writes the file to disk; durSec is BookFile.Duration.
func (e *wbEnv) addFile(bookID, rel string, present bool, durSec int) (string, string) {
	e.t.Helper()
	if bookID == "" {
		b, err := e.store.CreateBook(&database.Book{Title: "Book " + rel, FilePath: filepath.Join(e.lib, filepath.Dir(rel))})
		require.NoError(e.t, err)
		bookID = b.ID
	}
	path := filepath.Join(e.lib, rel)
	if present {
		require.NoError(e.t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(e.t, os.WriteFile(path, []byte("audio:"+rel), 0o644))
	}
	f := &database.BookFile{BookID: bookID, FilePath: path, Format: "m4b", Duration: durSec}
	require.NoError(e.t, e.store.CreateBookFile(f))
	return bookID, f.ID
}

// addFileWith is addFile with explicit on-disk content and stored Duration.
func (e *wbEnv) addFileWith(bookID, rel, content string, durSec int) (string, string) {
	e.t.Helper()
	bookID, id := e.addFile(bookID, rel, false, durSec)
	path := filepath.Join(e.lib, rel)
	require.NoError(e.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(e.t, os.WriteFile(path, []byte(content), 0o644))
	return bookID, id
}

// fakeAudio is file content whose real length is durSec seconds (see the fake
// ffmpeg), padded to size bytes.
func fakeAudio(durSec, size int) string {
	head := fmt.Sprintf("dur=%d\n", durSec)
	return head + strings.Repeat("x", max(0, size-len(head)))
}

// invoked returns the input paths ffmpeg was called with, in order.
func (e *wbEnv) invoked() []string {
	e.t.Helper()
	fh, err := os.Open(e.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(e.t, err)
	defer fh.Close()
	var out []string
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func (e *wbEnv) run(ctx context.Context, rep *wbReporter, p WindowBackfillParams) (*windowRunResult, error) {
	if rep == nil {
		rep = &wbReporter{}
	}
	return e.plugin.windowBackfill(ctx, rep, p)
}

func (e *wbEnv) windows(fileID string) []database.FingerprintWindow {
	e.t.Helper()
	ws, err := e.store.GetFingerprintWindows(database.FileWindowRef(fileID))
	require.NoError(e.t, err)
	return ws
}

// ---- tests ----

func TestWindowWorkers_ExplicitAndClamped(t *testing.T) {
	prev := config.AppConfig.FPParallelWorkers
	t.Cleanup(func() { config.AppConfig.FPParallelWorkers = prev })

	config.AppConfig.FPParallelWorkers = 0
	require.Equal(t, max(1, min(runtime.NumCPU()/2, 16)), windowWorkers(0))
	require.Equal(t, 4, windowWorkers(4))
	require.Equal(t, windowBackfillMaxWorkers, windowWorkers(500))
	config.AppConfig.FPParallelWorkers = 12
	require.Equal(t, 12, windowWorkers(0))
	require.Equal(t, 3, windowWorkers(3), "an explicit param beats the config knob")

	// The options RunItems actually receives carry the pool size, a
	// checkpoint hook that works at Concurrency > 1, and a label.
	opts := windowRunOptions(6, 0, 10, 0, []windowItem{{FileID: "a"}}, WindowBackfillParams{Live: true}, &wbReporter{}, &windowTally{})
	require.Equal(t, 6, opts.Concurrency)
	require.NotNil(t, opts.CheckpointStateFn, "CheckpointFn is never called by runItemsPar; the watermark hook is required")
	require.Nil(t, opts.CheckpointFn)
	require.NotNil(t, opts.Label)
}

func TestWindowBackfill_DefUsesFingerprintKey(t *testing.T) {
	def := (&Plugin{}).windowBackfillDef()
	require.Equal(t, "acoustid.window-backfill", def.ID)
	require.Equal(t, "acoustid.fingerprint", def.ConcurrencyKey)
	require.NotEqual(t, "library.scan", def.ConcurrencyKey)
	require.NotNil(t, def.Run)
	require.NoError(t, registry.ValidateOpDef(def))

	// Register publishes it, and no two of the plugin's defs share an ID.
	seen := map[string]bool{}
	for _, d := range (&Plugin{}).opDefs() {
		require.NoError(t, registry.ValidateOpDef(d), d.ID)
		require.False(t, seen[d.ID], "duplicate def %s", d.ID)
		seen[d.ID] = true
	}
	require.True(t, seen[windowBackfillDefID], "Register does not publish %s", windowBackfillDefID)
	// Compile-time: the production store type satisfies the op's store needs.
	var _ pluginStore = (database.Store)(nil)
}

func TestWindowBackfill_TierOrderUnmatchedBooksFirst(t *testing.T) {
	e := newWBEnv(t)
	// IDs are time-ordered ULIDs and the T0 file is created after the T1
	// files, so file-ID order alone would run it last.
	_, _ = e.addFile("", "b-present/one.m4b", true, 300)
	_, _ = e.addFile("", "c-present/two.m4b", true, 300)
	ub, _ := e.addFile("", "a-unmatched/present.m4b", true, 300)
	_, _ = e.addFile(ub, "a-unmatched/missing.m4b", false, 300)

	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, Concurrency: 1})
	require.NoError(t, err)
	require.Equal(t, 1, res.plan.eligible[windowTierUnmatched])
	require.Equal(t, 2, res.plan.eligible[windowTierPresent])
	require.Equal(t, 1, res.plan.missingRows)
	got := e.invoked()
	require.Len(t, got, 3)
	require.Equal(t, filepath.Join(e.lib, "a-unmatched/present.m4b"), got[0], "T0 (a book with a missing file) must run first")
	require.EqualValues(t, 3, res.written)
}

func TestWindowBackfill_DryRunWritesNothing(t *testing.T) {
	e := newWBEnv(t)
	_, f1 := e.addFile("", "a/one.m4b", true, 3600)
	_, f2 := e.addFile("", "b/two.m4b", true, 100)
	rep := &wbReporter{}
	res, err := e.run(context.Background(), rep, WindowBackfillParams{})
	require.NoError(t, err)
	require.Equal(t, 2, len(res.plan.tiers[windowTierPresent]), "dry run reports the plan")
	require.Empty(t, e.invoked(), "dry run ran ffmpeg")
	require.Empty(t, e.windows(f1))
	require.Empty(t, e.windows(f2))
	require.Nil(t, rep.last(), "dry run must not checkpoint")
	require.Zero(t, res.written)
}

func TestWindowBackfill_IdempotentRerunDoesNoWork(t *testing.T) {
	e := newWBEnv(t)
	_, long := e.addFile("", "a/long.m4b", true, 3600)
	_, short := e.addFile("", "b/short.m4b", true, 100)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, Concurrency: 2})
	require.NoError(t, err)
	require.Len(t, e.windows(long), 3, "a 1 h file gets slots 1000/5000/9000")
	require.Len(t, e.windows(short), 1)
	require.True(t, e.windows(short)[0].CoversWhole)
	calls := len(e.invoked())
	require.Equal(t, 4, calls)

	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, Concurrency: 2})
	require.NoError(t, err)
	require.Len(t, e.invoked(), calls, "rerun ran ffmpeg again")
	require.Equal(t, 2, res.plan.current[windowTierPresent])
	require.Empty(t, res.plan.tiers[windowTierPresent])
	require.Zero(t, res.written)
}

func TestWindowBackfill_ToolVersionChangeReplans(t *testing.T) {
	e := newWBEnv(t)
	_, id := e.addFile("", "a/x.m4b", true, 300)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	e.tools.Versions.FFmpeg = "8.1.0-fake"
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, res.written)
	require.Equal(t, "8.1.0-fake", e.windows(id)[0].FFmpegVersion)
}

func TestWindowBackfill_StaleMtimeReplans(t *testing.T) {
	e := newWBEnv(t)
	_, changed := e.addFile("", "a/changed.m4b", true, 300)
	_, _ = e.addFile("", "b/same.m4b", true, 300)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	before := e.windows(changed)[0].SourceMtimeUnix

	later := time.Unix(before, 0).Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(e.lib, "a/changed.m4b"), later, later))
	n := len(e.invoked())
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(e.lib, "a/changed.m4b")}, e.invoked()[n:], "only the changed file is redone")
	require.EqualValues(t, 1, res.written)
	require.Equal(t, later.Unix(), e.windows(changed)[0].SourceMtimeUnix)
}

func TestWindowBackfill_TombstoneSkipsUntilTheFileChanges(t *testing.T) {
	e := newWBEnv(t)
	_, bad := e.addFile("", "a/corrupt.m4b", true, 300)
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, res.failed)
	fail, err := e.store.GetFingerprintWindowFailure(database.FileWindowRef(bad))
	require.NoError(t, err)
	require.NotNil(t, fail)
	require.Equal(t, "ffmpeg", fail.Reason)
	require.Contains(t, fail.Detail, "Invalid data found")

	n := len(e.invoked())
	res, err = e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Len(t, e.invoked(), n, "a current tombstone must not be retried")
	require.Equal(t, 1, res.plan.tombstoned[windowTierPresent])

	later := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(e.lib, "a/corrupt.m4b"), later, later))
	_, err = e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Greater(t, len(e.invoked()), n, "a changed file is retried despite its tombstone")
}

func TestWindowBackfill_ITunesTreeExcluded(t *testing.T) {
	e := newWBEnv(t)
	// A book with a present file and a missing file both under books/itunes/:
	// neither is read, and the missing one does not make the book unmatched.
	ib, itunesFile := e.addFile("", "books/itunes/iTunes Media/Audiobooks/a.m4b", true, 300)
	_, _ = e.addFile(ib, "books/itunes/iTunes Media/Audiobooks/gone.m4b", false, 300)
	_, _ = e.addFile("", "books/audiobook-organizer/b.m4b", true, 300)

	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Equal(t, 2, res.plan.excluded["itunes_tree"])
	require.Zero(t, res.plan.missingRows)
	for _, p := range e.invoked() {
		require.NotContains(t, p, "books/itunes/")
	}
	require.Empty(t, e.windows(itunesFile))
	require.EqualValues(t, 1, res.written)
}

func TestWindowBackfill_MissingFilesSkipped(t *testing.T) {
	e := newWBEnv(t)
	b, gone := e.addFile("", "a/gone.m4b", false, 300)
	_, _ = e.addFile(b, "a/here.m4b", true, 300)
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(e.lib, "a/here.m4b")}, e.invoked())
	require.Empty(t, e.windows(gone))
	require.Equal(t, 1, res.plan.eligible[windowTierUnmatched])
}

func TestWindowBackfill_UnknownDurationIsTransientNotTombstoned(t *testing.T) {
	e := newWBEnv(t)
	prev := windowProbeDuration
	t.Cleanup(func() { windowProbeDuration = prev })
	windowProbeDuration = func(context.Context, string) (float64, error) { return 0, errors.New("no ffprobe") }
	_, id := e.addFile("", "a/nodur.m4b", true, 0)
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, res.transient)
	fail, err := e.store.GetFingerprintWindowFailure(database.FileWindowRef(id))
	require.NoError(t, err)
	require.Nil(t, fail)
}

// Cancel mid-run at concurrency 4, then resume from the last checkpoint: every
// file ends with windows, no file finished in run 1 is cut again in run 2, and
// the checkpoint never names a file past one that is unfinished.
func TestWindowBackfill_CancelThenResume(t *testing.T) {
	e := newWBEnv(t)
	prevEvery := windowCheckpointEvery
	t.Cleanup(func() { windowCheckpointEvery = prevEvery })
	windowCheckpointEvery = 1
	const n = 40
	var ids []string
	for i := range n {
		_, id := e.addFile("", fmt.Sprintf("slow-%02d/f.m4b", i), true, 300)
		ids = append(ids, id)
	}

	ctx, cancel := context.WithCancel(context.Background())
	rep := &wbReporter{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for len(e.invoked()) < n/3 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	_, err := e.run(ctx, rep, WindowBackfillParams{Live: true, Concurrency: 4})
	<-done
	require.ErrorIs(t, err, context.Canceled)

	finished := map[string]bool{}
	for _, id := range ids {
		if len(e.windows(id)) > 0 {
			finished[id] = true
		}
	}
	require.NotEmpty(t, finished)
	require.Less(t, len(finished), n, "cancel came too late to test resume")

	cp := rep.last()
	require.NotNil(t, cp, "no checkpoint before the cancel")
	require.NotNil(t, cp.Resume)
	require.True(t, cp.Live, "the checkpoint must carry the caller's params")
	// Watermark soundness: every file at or below the cursor has windows.
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	for _, id := range sorted {
		if id > cp.Resume.AfterFileID {
			break
		}
		require.True(t, finished[id], "checkpoint %s passed unfinished file %s", cp.Resume.AfterFileID, id)
	}

	pathOf := map[string]string{}
	for i, id := range ids {
		pathOf[filepath.Join(e.lib, fmt.Sprintf("slow-%02d/f.m4b", i))] = id
	}
	before := len(e.invoked())
	res, err := e.run(context.Background(), &wbReporter{}, *cp)
	require.NoError(t, err)
	for _, p := range e.invoked()[before:] {
		require.False(t, finished[pathOf[p]], "resume redid finished file %s", p)
	}
	for _, id := range ids {
		require.NotEmpty(t, e.windows(id), "file %s has no windows after resume", id)
	}
	require.EqualValues(t, n-len(finished), res.written)
}

// Real ffmpeg + fpcalc through a ToolRegistry, as the server resolves them.
func TestWindowBackfill_Integration_RealBinaries(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "fpcalc"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	lib := t.TempDir()
	path := filepath.Join(lib, "tone.m4a")
	out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "aevalsrc='0.5*sin(2*PI*(400+300*sin(2*PI*t/7))*t)':s=22050:d=200",
		"-f", "lavfi", "-i", "anoisesrc=d=200:c=pink:r=22050:a=0.3:seed=3",
		"-filter_complex", "[0:a][1:a]amix=inputs=2:normalize=0", "-ac", "1", "-c:a", "aac", "-b:a", "64k", path).CombinedOutput()
	require.NoError(t, err, "%s", out)
	b, err := store.CreateBook(&database.Book{Title: "Tone", FilePath: lib})
	require.NoError(t, err)
	f := &database.BookFile{BookID: b.ID, FilePath: path, Format: "m4a", Duration: 200}
	require.NoError(t, store.CreateBookFile(f))

	reg := tools.NewToolRegistry(&tools.ToolsConfig{Fpcalc: tools.ToolConfig{Mode: tools.ToolModeSystem}})
	reg.Register(tools.ToolDef{Name: "fpcalc"})
	reg.Register(tools.ToolDef{Name: "ffmpeg"})
	p := &Plugin{store: store}
	p.SetToolRegistry(reg)

	res, err := p.windowBackfill(context.Background(), &wbReporter{}, WindowBackfillParams{Live: true, Concurrency: 2})
	require.NoError(t, err)
	require.EqualValues(t, 1, res.written, "failed=%d transient=%d", res.failed, res.transient)
	ws, err := store.GetFingerprintWindows(database.FileWindowRef(f.ID))
	require.NoError(t, err)
	require.Len(t, ws, 1, "a 200 s file gets slot 5000 only")
	require.Equal(t, 5000, ws[0].SlotBP)
	require.GreaterOrEqual(t, ws[0].Frames, fingerprint.MinUsefulFingerprintFrames)
	require.Equal(t, fingerprint.WindowPipelineID, ws[0].Pipeline)
	require.NotEmpty(t, ws[0].FpcalcVersion)
	require.NotEmpty(t, ws[0].FFmpegVersion)
}

// ---- review fixes (PR #3455) ----

func (e *wbEnv) tombstone(fileID string) *database.FingerprintWindowFailure {
	e.t.Helper()
	f, err := e.store.GetFingerprintWindowFailure(database.FileWindowRef(fileID))
	require.NoError(e.t, err)
	return f
}

// FIX 1: a pre-CONS-18 millisecond Duration planned windows at 30000 s+ in a
// 300 s file; every cut decoded nothing and the file was tombstoned forever.
func TestWindowBackfill_MillisecondDurationIsNormalized(t *testing.T) {
	e := newWBEnv(t)
	// 1 MB file, Duration 300000 "seconds" = 26 bps: milliseconds. The row's
	// FileSize is left 0 so the write chokepoint cannot repair it first.
	_, id := e.addFileWith("", "a/ms.m4b", fakeAudio(300, 1_000_000), 300_000)
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Nil(t, e.tombstone(id), "a unit error must not become a durable tombstone")
	ws := e.windows(id)
	require.Len(t, ws, 1, "300 s plans slot 5000 only")
	require.InDelta(t, 300, ws[0].DurationUsedSec, 0.001)
	require.EqualValues(t, 1, res.written)
}

// FIX 1: a size/bitrate estimate 2x too long seeks past EOF. The op must
// ffprobe the real duration and retry before tombstoning.
func TestWindowBackfill_WrongDurationReprobesBeforeTombstone(t *testing.T) {
	e := newWBEnv(t)
	_, id := e.addFileWith("", "a/est.m4b", fakeAudio(200, 4096), 400)
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Nil(t, e.tombstone(id))
	ws := e.windows(id)
	require.Len(t, ws, 1)
	require.Equal(t, string(fingerprint.DurationSourceFFprobe), ws[0].DurationSource)
	require.InDelta(t, 200, ws[0].DurationUsedSec, 0.001)
	require.EqualValues(t, 1, res.written)
}

// FIX 1: a tombstone records the duration it was planned from; correcting the
// row's duration later re-plans the file.
func TestWindowBackfill_TombstoneClearsWhenDurationIsCorrected(t *testing.T) {
	e := newWBEnv(t)
	b, id := e.addFile("", "a/corrupt.m4b", true, 300)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.NotNil(t, e.tombstone(id))
	n := len(e.invoked())
	_, err = e.store.ModifyBookFile(b, id, func(f *database.BookFile) error { f.Duration = 900; return nil })
	require.NoError(t, err)
	_, err = e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Greater(t, len(e.invoked()), n, "a corrected duration must re-plan a tombstoned file")
}

// FIX 2: an OOM kill, an I/O error and a one-off failure are not durable.
func TestWindowBackfill_TransientToolFailuresAreNotTombstoned(t *testing.T) {
	e := newWBEnv(t)
	_, killed := e.addFile("", "a/sigkill.m4b", true, 300)
	_, eio := e.addFile("", "b/eio.m4b", true, 300)
	_, flaky := e.addFile("", "c/flaky.m4b", true, 300)
	_, bad := e.addFile("", "d/corrupt.m4b", true, 300)
	_, flip := e.addFile("", "e/flip.m4b", true, 300)
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, Concurrency: 1})
	require.NoError(t, err)
	require.Nil(t, e.tombstone(killed), "signal death tombstoned")
	require.Nil(t, e.tombstone(eio), "I/O error tombstoned")
	require.Nil(t, e.tombstone(flaky), "a failure that did not repeat was tombstoned")
	require.Len(t, e.windows(flaky), 1, "the inline retry should have succeeded")
	require.Nil(t, e.tombstone(flip), "a retry that failed differently is not a repeat")
	require.NotNil(t, e.tombstone(bad), "a deterministic repeated failure must still be tombstoned")
	require.EqualValues(t, 1, res.failed)
	require.EqualValues(t, 1, res.written)
}

// FIX 3: a write failure for a row that moved to another book is a real
// write error, not "row gone".
func TestWindowBackfill_MovedRowWriteFailureIsAWriteError(t *testing.T) {
	e := newWBEnv(t)
	a, id := e.addFile("", "a/moved.m4b", true, 300)
	bBook, err := e.store.CreateBook(&database.Book{Title: "Other", FilePath: e.lib})
	require.NoError(t, err)
	require.NoError(t, e.store.MoveBookFilesToBook([]string{id}, a, bBook.ID))
	var tally windowTally
	e.plugin.countWriteErr(windowItem{FileID: id, BookID: a}, errors.New("pebble: disk full"), &tally)
	require.EqualValues(t, 1, tally.writeErrors.Load())
	require.Zero(t, tally.rowGone.Load())
}

func TestWindowBackfill_ITunesExclusionIgnoresCaseAndFollowsSymlinks(t *testing.T) {
	e := newWBEnv(t)
	_, upper := e.addFile("", "Books/iTunes/iTunes Media/a.m4b", true, 300)
	// A symlinked directory elsewhere in the library that points into the tree.
	_, _ = e.addFile("", "books/itunes/iTunes Media/real/b.m4b", true, 300)
	require.NoError(t, os.Symlink(filepath.Join(e.lib, "books/itunes/iTunes Media/real"), filepath.Join(e.lib, "via-link")))
	b, err := e.store.CreateBook(&database.Book{Title: "Link", FilePath: filepath.Join(e.lib, "via-link")})
	require.NoError(t, err)
	link := &database.BookFile{BookID: b.ID, FilePath: filepath.Join(e.lib, "via-link", "b.m4b"), Format: "m4b", Duration: 300}
	require.NoError(t, e.store.CreateBookFile(link))
	_, _ = e.addFile("", "books/audiobook-organizer/c.m4b", true, 300)

	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Equal(t, 3, res.plan.excluded["itunes_tree"])
	require.Empty(t, e.windows(upper))
	require.Empty(t, e.windows(link.ID))
	require.Equal(t, []string{filepath.Join(e.lib, "books/audiobook-organizer/c.m4b")}, e.invoked())
}

// MEDIUM: a dead mount makes every file an I/O failure. The run must stop
// with a clear error instead of walking the library and finishing green.
func TestWindowBackfill_BreakerStopsOnConsecutiveIOFailures(t *testing.T) {
	e := newWBEnv(t)
	prev := windowBreakerThreshold
	t.Cleanup(func() { windowBreakerThreshold = prev })
	windowBreakerThreshold = 10
	for i := range 30 {
		e.addFile("", fmt.Sprintf("dead-%02d/sigkill.m4b", i), true, 300)
	}
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, Concurrency: 1})
	require.ErrorIs(t, err, errWindowBreaker)
	require.Less(t, len(e.invoked()), 30, "the breaker should stop the walk early")
	require.Zero(t, res.written)
}

func TestWindowBackfill_BreakerResetsOnAGoodFile(t *testing.T) {
	e := newWBEnv(t)
	prev := windowBreakerThreshold
	t.Cleanup(func() { windowBreakerThreshold = prev })
	windowBreakerThreshold = 10
	for i := range 8 {
		e.addFile("", fmt.Sprintf("a-%02d/sigkill.m4b", i), true, 300)
	}
	e.addFile("", "b/good.m4b", true, 300)
	for i := range 8 {
		e.addFile("", fmt.Sprintf("c-%02d/sigkill.m4b", i), true, 300)
	}
	res, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true, Concurrency: 1})
	require.NoError(t, err)
	require.EqualValues(t, 1, res.written)
	require.EqualValues(t, 16, res.transient)
}

type fakeScanProbe struct{ busy atomic.Int64 }

func (f *fakeScanProbe) LibraryScanRunning() bool { return f.busy.Add(-1) >= 0 }

// The op yields to a running library.scan (pauses, never takes the stand-down).
func TestWindowBackfill_PausesWhileLibraryScanRuns(t *testing.T) {
	e := newWBEnv(t)
	prev := windowScanPoll
	t.Cleanup(func() { windowScanPoll = prev })
	windowScanPoll = time.Millisecond
	_, id := e.addFile("", "a/x.m4b", true, 300)
	probe := &fakeScanProbe{}
	probe.busy.Store(3) // "running" for the first three checks
	e.plugin.setScanProbe(probe)
	_, err := e.run(context.Background(), nil, WindowBackfillParams{Live: true})
	require.NoError(t, err)
	require.Less(t, probe.busy.Load(), int64(0), "the probe was not polled until the scan ended")
	require.Len(t, e.windows(id), 1)

	// Cancelled while paused: returns the ctx error, cuts nothing.
	_, id2 := e.addFile("", "b/y.m4b", true, 300)
	probe.busy.Store(1 << 40)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = e.run(ctx, nil, WindowBackfillParams{Live: true})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, e.windows(id2))
}
