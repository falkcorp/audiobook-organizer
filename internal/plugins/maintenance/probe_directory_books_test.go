// file: internal/plugins/maintenance/probe_directory_books_test.go
// version: 1.1.0
// guid: 4a71c9e6-2b58-4d03-8f19-7c6e0b2a5d84
// last-edited: 2026-08-06

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
)

// ── test doubles ─────────────────────────────────────────────────────────────
//
// The prober is injected rather than shelling out to a real ffprobe: these tests
// must assert what the op does with probe FAILURES, and a real binary cannot be
// made to fail on demand. Swapping the package vars is process-global, so no
// test here calls t.Parallel and every swap is restored via t.Cleanup.

// fakeProber returns durations by file basename. A name absent from the map
// fails, which is how "could not probe" is expressed.
type fakeProber struct {
	mu    sync.Mutex
	bySec map[string]float64
	calls int
}

func (f *fakeProber) probe(_ context.Context, _, filePath string) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if secs, ok := f.bySec[filepath.Base(filePath)]; ok {
		return secs, nil
	}
	return 0, fmt.Errorf("ffprobe %s: exit status 1", filePath)
}

// installProber points the op at fp and restores the real prober afterwards.
func installProber(t *testing.T, fp *fakeProber) {
	t.Helper()
	prev := probeDurationSecondsFn
	probeDurationSecondsFn = fp.probe
	t.Cleanup(func() { probeDurationSecondsFn = prev })
}

// installFFprobeLookup overrides binary resolution for the duration of a test.
func installFFprobeLookup(t *testing.T, path string, err error) {
	t.Helper()
	prev := lookupFFprobeFn
	lookupFFprobeFn = func() (string, error) { return path, err }
	t.Cleanup(func() { lookupFFprobeFn = prev })
}

// makeFolder writes zero-byte files with the given names into a new temp dir.
// Contents are irrelevant — the prober is faked; only the directory listing and
// the names matter.
func makeFolder(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	return dir
}

// classifyFolder runs the op's real probe+classify pipeline over one directory.
func classifyFolder(t *testing.T, dir string) linkintegrity.DirVerdict {
	t.Helper()
	names, subdirs := readDirNames(dir)
	audio := folderAudioNames(names)
	probes := probeFolderDurations(context.Background(), "ffprobe", dir, audio, nil)
	return linkintegrity.ClassifyDirProbed(names, subdirs, probes)
}

// ── the four required cases ──────────────────────────────────────────────────

// Chapter-length files sharing one stem: the case nil durations were blocking.
func TestProbeDirectoryBooks_ChapterFolderBecomesOneBook(t *testing.T) {
	files := []string{
		"The Given Sacrifice_01_.mp3", "The Given Sacrifice_02_.mp3",
		"The Given Sacrifice_03_.mp3",
	}
	dir := makeFolder(t, files...)
	installProber(t, &fakeProber{bySec: map[string]float64{
		files[0]: 1500.4, files[1]: 1480.6, files[2]: 1520.0,
	}})

	got := classifyFolder(t, dir)
	if !got.OneBook {
		t.Fatalf("OneBook = false, want true once durations are measured; reason=%q", got.Reason)
	}
	if got.ProbesOK != 3 || got.ProbesFailed != 0 {
		t.Errorf("ProbesOK/Failed = %d/%d, want 3/0", got.ProbesOK, got.ProbesFailed)
	}
}

// Book-length files sharing one stem: the "Super Sales on Super Heroes 1..5"
// shape. Measurement must CONFIRM the review hold, not dissolve it.
func TestProbeDirectoryBooks_BookLengthFolderStaysInReview(t *testing.T) {
	files := []string{
		"Super Sales on Super Heroes 1.m4b", "Super Sales on Super Heroes 2.m4b",
		"Super Sales on Super Heroes 3.m4b", "Super Sales on Super Heroes 4.m4b",
		"Super Sales on Super Heroes 5.m4b",
	}
	dir := makeFolder(t, files...)
	secs := map[string]float64{}
	for _, f := range files {
		secs[f] = 9 * 3600
	}
	installProber(t, &fakeProber{bySec: secs})

	got := classifyFolder(t, dir)
	if got.OneBook {
		t.Fatalf("OneBook = true — this would merge 5 distinct novels; reason=%q", got.Reason)
	}
	if !strings.Contains(got.Reason, "whole books") {
		t.Errorf("reason must name the series-guard evidence, got %q", got.Reason)
	}
}

// 🔴 Partial probe failure. Numbers chosen so EXCLUSION alone decides: one
// book-length file measured, three unprobeable. Counting the failures as zero
// would yield 1-of-4 long → OneBook (wrong); excluding them yields 1-of-1 long →
// series guard fires.
func TestProbeDirectoryBooks_FailedProbesExcludedNotZeroed(t *testing.T) {
	files := []string{
		"Wandering Inn 1.mp3", "Wandering Inn 2.mp3",
		"Wandering Inn 3.mp3", "Wandering Inn 4.mp3",
	}
	dir := makeFolder(t, files...)
	// Only the first file probes; the other three are absent from the map and
	// therefore fail.
	installProber(t, &fakeProber{bySec: map[string]float64{files[0]: 6000}})

	names, subdirs := readDirNames(dir)
	audio := folderAudioNames(names)
	probes := probeFolderDurations(context.Background(), "ffprobe", dir, audio, nil)

	// Assert the exclusion directly: a failed probe must carry OK=false, and the
	// slice must still hold one entry per file so a truncated run is visible.
	if len(probes) != 4 {
		t.Fatalf("probes = %d, want one entry per audio file", len(probes))
	}
	okCount := 0
	for _, p := range probes {
		if p.OK {
			okCount++
			continue
		}
		if p.Sec != 0 {
			t.Errorf("failed probe for %q carries Sec=%d — a failure must not carry a duration", p.Name, p.Sec)
		}
	}
	if okCount != 1 {
		t.Fatalf("okCount = %d, want exactly 1 measured file", okCount)
	}

	got := linkintegrity.ClassifyDirProbed(names, subdirs, probes)
	if got.OneBook {
		t.Fatalf("OneBook = true — the three failed probes were counted as zero-length chapters; reason=%q", got.Reason)
	}
	if got.ProbesOK != 1 || got.ProbesFailed != 3 {
		t.Errorf("ProbesOK/Failed = %d/%d, want 1/3", got.ProbesOK, got.ProbesFailed)
	}
}

// The reverse partial: measured subset looks chapter-length, but most of the
// folder is unmeasured. Must not confirm on partial evidence.
func TestProbeDirectoryBooks_PartialEvidenceCannotConfirmOneBook(t *testing.T) {
	files := []string{"Skyward 1.mp3", "Skyward 2.mp3", "Skyward 3.mp3", "Skyward 4.mp3"}
	dir := makeFolder(t, files...)
	installProber(t, &fakeProber{bySec: map[string]float64{files[0]: 1200}})

	got := classifyFolder(t, dir)
	if got.OneBook {
		t.Fatalf("OneBook = true on 1 of 4 files measured; reason=%q", got.Reason)
	}
	if !strings.Contains(got.Reason, "could not be probed") {
		t.Errorf("reason must name the unmeasured files, got %q", got.Reason)
	}
}

// ffprobe entirely unavailable: the op must REFUSE, not report a clean run.
//
// The check runs before the store is touched, so a nil-deps Plugin is enough —
// which is itself the assertion that nothing else happened first.
func TestProbeDirectoryBooks_RefusesWhenFFprobeMissing(t *testing.T) {
	installFFprobeLookup(t, "", audioutil.ErrFFprobeNotAvailable)

	rep := &mockReporter{}
	p := New(nil)
	err := p.runProbeDirectoryBooks(context.Background(), nil, rep)

	if err == nil {
		t.Fatal("op returned nil — a missing ffprobe must fail the run, not report that nothing was found")
	}
	if !errors.Is(err, audioutil.ErrFFprobeNotAvailable) {
		t.Errorf("error must wrap ErrFFprobeNotAvailable so callers can distinguish " +
			"'tool missing' from 'files unreadable'")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ffprobe") {
		t.Errorf("error must name ffprobe, got %q", err)
	}
	// A cannot-run must not look like a successful empty run.
	for _, l := range rep.logs {
		if strings.Contains(l, "RECONCILE") || strings.Contains(l, "First Aid") {
			t.Errorf("op emitted a run summary %q despite being unable to measure anything", l)
		}
	}
}

// ── probe-result semantics ───────────────────────────────────────────────────

// A successful ffprobe exit reporting zero or negative seconds is not a
// measurement. ProbeDurationSeconds explicitly does not validate this, so the op
// must.
func TestProbeOne_ZeroOrNegativeIsNotMeasured(t *testing.T) {
	for _, secs := range []float64{0, -1} {
		prev := probeDurationSecondsFn
		probeDurationSecondsFn = func(context.Context, string, string) (float64, error) { return secs, nil }
		got := probeOne(context.Background(), "ffprobe", "/x/a.mp3", "a.mp3")
		probeDurationSecondsFn = prev

		if got.OK {
			t.Errorf("probeOne(secs=%v).OK = true — a zero/negative duration is an absence of evidence", secs)
		}
		if got.Sec != 0 {
			t.Errorf("probeOne(secs=%v).Sec = %d, want 0", secs, got.Sec)
		}
	}
}

// Rounding, not truncation — matching duration_reextract.go's handling of the
// same float→int conversion.
func TestProbeOne_RoundsToNearestSecond(t *testing.T) {
	prev := probeDurationSecondsFn
	probeDurationSecondsFn = func(context.Context, string, string) (float64, error) { return 1499.6, nil }
	got := probeOne(context.Background(), "ffprobe", "/x/a.mp3", "a.mp3")
	probeDurationSecondsFn = prev

	if !got.OK || got.Sec != 1500 {
		t.Errorf("probeOne = {Sec:%d OK:%v}, want {Sec:1500 OK:true}", got.Sec, got.OK)
	}
}

// Non-audio entries must never be probed: they are not evidence about the book
// and every needless probe costs a subprocess.
func TestFolderAudioNames_FiltersAndSorts(t *testing.T) {
	names := []string{"cover.jpg", "b.mp3", "notes.txt", "a.M4B", "Disc 1"}
	got := folderAudioNames(names)
	want := []string{"a.M4B", "b.mp3"}
	if len(got) != len(want) {
		t.Fatalf("folderAudioNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("folderAudioNames = %v, want %v (deterministic order)", got, want)
		}
	}
}

// ── concurrency ──────────────────────────────────────────────────────────────

// probeAll fans folders across a worker pool and writes each verdict back
// in place. Each worker owns one index, so the writes need no lock — this test
// exists to prove that under -race with enough folders to force real overlap.
func TestProbeAll_ParallelWritesAreIndexPartitioned(t *testing.T) {
	const folders = 40
	secs := map[string]float64{}
	cands := make([]probeCandidate, 0, folders)
	for i := 0; i < folders; i++ {
		// Alternate the two shapes so the verdicts differ per index and a
		// cross-worker write would show up as a wrong verdict, not just a race.
		var files []string
		dur := 1200.0
		if i%2 == 1 {
			dur = 9 * 3600
		}
		for j := 1; j <= 3; j++ {
			n := fmt.Sprintf("Book %02d Part %d.mp3", i, j)
			files = append(files, n)
			secs[n] = dur
		}
		dir := makeFolder(t, files...)
		cands = append(cands, probeCandidate{
			finding: linkintegrity.Finding{BookID: fmt.Sprintf("b%02d", i), FilePath: dir},
		})
	}
	installProber(t, &fakeProber{bySec: secs})

	var done atomic.Int64
	rep := &concurrentReporter{}
	if err := probeAll(context.Background(), rep, "ffprobe", cands, &done, len(cands)); err != nil {
		t.Fatalf("probeAll: %v", err)
	}

	if int(done.Load()) != folders {
		t.Errorf("completed folders = %d, want %d", done.Load(), folders)
	}
	for i, c := range cands {
		wantOne := i%2 == 0 // chapter-length folders resolve, book-length do not
		if c.verdict.OneBook != wantOne {
			t.Errorf("folder %d: OneBook = %v, want %v — a verdict landed on the wrong index; reason=%q",
				i, c.verdict.OneBook, wantOne, c.verdict.Reason)
		}
		if len(c.probes) != 3 {
			t.Errorf("folder %d: probes = %d, want 3", i, len(c.probes))
		}
	}
}

// ── apply path ───────────────────────────────────────────────────────────────

// The apply path writes MEASURED durations onto the new book_file rows. Seeding
// zero — which tier 1 must do, having never probed — would leave the regroup
// series guard just as inert as having no rows at all, defeating the point of
// probing.
func TestLinkProbedFolder_WritesMeasuredDurations(t *testing.T) {
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	files := []string{"Chapter 01.mp3", "Chapter 02.mp3", "Chapter 03.mp3"}
	dir := makeFolder(t, files...)
	bk, err := s.CreateBook(&database.Book{Title: "Probed Book", FilePath: dir})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	c := probeCandidate{
		finding: linkintegrity.Finding{BookID: bk.ID, FilePath: dir, Shape: linkintegrity.ShapeDirectory},
		verdict: linkintegrity.DirVerdict{OneBook: true, AudioCount: 3, ProbesOK: 3},
		probes: []linkintegrity.ProbedDuration{
			{Name: files[0], Sec: 1500, OK: true},
			{Name: files[1], Sec: 1480, OK: true},
			{Name: files[2], Sec: 1520, OK: true},
		},
	}

	n, err := linkProbedFolder(s, c)
	if err != nil {
		t.Fatalf("linkProbedFolder: %v", err)
	}
	if n != 3 {
		t.Fatalf("created = %d, want 3", n)
	}

	rows, err := s.GetBookFiles(bk.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("book_file rows = %d, want 3", len(rows))
	}
	byName := map[string]int{}
	for _, r := range rows {
		byName[filepath.Base(r.FilePath)] = r.Duration
	}
	for name, want := range map[string]int{files[0]: 1500, files[1]: 1480, files[2]: 1520} {
		if got := byName[name]; got != want {
			t.Errorf("%s duration = %d, want the MEASURED %d — a zero here leaves the series guard inert",
				name, got, want)
		}
	}
}

// Idempotency: a book that gained rows between probe and apply must not get a
// second set. Re-running the op is expected operationally, and duplicate rows
// are what maintenance.dedupe-book-file-rows exists to clean up.
func TestLinkProbedFolder_SkipsAlreadyLinkedBook(t *testing.T) {
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	files := []string{"Chapter 01.mp3", "Chapter 02.mp3"}
	dir := makeFolder(t, files...)
	bk, err := s.CreateBook(&database.Book{Title: "Already Linked", FilePath: dir})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	c := probeCandidate{
		finding: linkintegrity.Finding{BookID: bk.ID, FilePath: dir, Shape: linkintegrity.ShapeDirectory},
		verdict: linkintegrity.DirVerdict{OneBook: true, AudioCount: 2, ProbesOK: 2},
		probes: []linkintegrity.ProbedDuration{
			{Name: files[0], Sec: 1500, OK: true},
			{Name: files[1], Sec: 1480, OK: true},
		},
	}

	if _, err := linkProbedFolder(s, c); err != nil {
		t.Fatalf("first linkProbedFolder: %v", err)
	}
	n, err := linkProbedFolder(s, c)
	if err != nil {
		t.Fatalf("second linkProbedFolder: %v", err)
	}
	if n != 0 {
		t.Errorf("second run created %d rows, want 0 — the op must be idempotent", n)
	}
	rows, _ := s.GetBookFiles(bk.ID)
	if len(rows) != 2 {
		t.Errorf("book_file rows = %d, want 2 after two runs", len(rows))
	}
}

// ── reconcile arithmetic ─────────────────────────────────────────────────────

// The op fails itself when a phase does not reconcile, so the bucket arithmetic
// has to hold in both the dry-run and applied shapes. Every examined folder must
// land in exactly one of actioned/skipped/errors.
func TestProbeDirectoryBooks_PhaseReconciles(t *testing.T) {
	cases := []struct{ examined, linked, errs int }{
		{examined: 1019, linked: 0, errs: 0},   // dry run
		{examined: 1019, linked: 812, errs: 3}, // applied
		{examined: 0, linked: 0, errs: 0},      // nothing flagged
	}
	for _, tc := range cases {
		phase := linkintegrity.PhaseResult{
			Name:     "probe-directory-books",
			Examined: tc.examined,
			Actioned: tc.linked,
			Errors:   tc.errs,
			Skipped:  tc.examined - tc.linked - tc.errs,
		}
		if !phase.Reconciles() {
			t.Errorf("phase %+v does not reconcile — the op would fail itself at the finish line", phase)
		}
	}
}

// 🔴 THE CENTRAL SAFETY PROPERTY: a folder measurement CONFIRMED to be a series
// must never have its files linked into one book. Phase 3's disposition check is
// the live gate, but the consequence of getting it wrong is the incident this
// whole op exists to prevent — five distinct novels merged into one row, whose
// later merge hard-deletes the absorbed rows — so the write path refuses on its
// own rather than trusting its caller.
func TestLinkProbedFolder_RefusesSeriesVerdict(t *testing.T) {
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	files := []string{
		"Super Sales on Super Heroes 1.m4b", "Super Sales on Super Heroes 2.m4b",
		"Super Sales on Super Heroes 3.m4b",
	}
	dir := makeFolder(t, files...)
	bk, err := s.CreateBook(&database.Book{Title: "Super Sales on Super Heroes", FilePath: dir})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	probes := make([]linkintegrity.ProbedDuration, 0, len(files))
	for _, f := range files {
		probes = append(probes, linkintegrity.ProbedDuration{Name: f, Sec: 9 * 3600, OK: true})
	}
	names, subdirs := readDirNames(dir)
	verdict := linkintegrity.ClassifyDirProbed(names, subdirs, probes)
	if verdict.OneBook {
		t.Fatal("precondition failed: book-length members must classify as a series")
	}

	c := probeCandidate{
		finding: linkintegrity.Finding{BookID: bk.ID, FilePath: dir, Shape: linkintegrity.ShapeDirectory},
		verdict: verdict,
		probes:  probes,
	}
	n, err := linkProbedFolder(s, c)
	if err == nil {
		t.Error("linkProbedFolder returned nil error for a series verdict — it must refuse loudly, " +
			"not silently do nothing, so a caller bug stays visible")
	}
	if n != 0 {
		t.Errorf("created = %d rows for a CONFIRMED SERIES — this is the 5-novels-into-1 merge", n)
	}
	rows, _ := s.GetBookFiles(bk.ID)
	if len(rows) != 0 {
		t.Fatalf("book_file rows = %d, want 0 — no file of a confirmed series may be linked", len(rows))
	}
}

// The verdict describes the folder AS MEASURED. A file that appeared between
// probe and apply was never measured and could be another work entirely, so the
// apply must refuse rather than link it with a duration of zero.
func TestLinkProbedFolder_RefusesWhenFolderChangedSinceProbing(t *testing.T) {
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	files := []string{"Chapter 01.mp3", "Chapter 02.mp3"}
	dir := makeFolder(t, files...)
	bk, err := s.CreateBook(&database.Book{Title: "Drifting Book", FilePath: dir})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	c := probeCandidate{
		finding: linkintegrity.Finding{BookID: bk.ID, FilePath: dir, Shape: linkintegrity.ShapeDirectory},
		verdict: linkintegrity.DirVerdict{OneBook: true, AudioCount: 2, ProbesOK: 2},
		probes: []linkintegrity.ProbedDuration{
			{Name: files[0], Sec: 1500, OK: true},
			{Name: files[1], Sec: 1480, OK: true},
		},
	}

	// A third file lands after the probe pass — never measured, not covered by
	// the verdict.
	if err := os.WriteFile(filepath.Join(dir, "Chapter 03.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	n, err := linkProbedFolder(s, c)
	if err == nil {
		t.Error("linkProbedFolder accepted a folder that changed since probing")
	}
	if n != 0 {
		t.Errorf("created = %d rows, want 0 — the verdict describes different contents", n)
	}
	rows, _ := s.GetBookFiles(bk.ID)
	if len(rows) != 0 {
		t.Errorf("book_file rows = %d, want 0", len(rows))
	}
}

// reviewCategory buckets by the verdict's structured fields, not its prose, so a
// reworded Reason cannot silently miscategorise the tally an operator reads.
func TestReviewCategory_BucketsByEvidence(t *testing.T) {
	cases := []struct {
		name string
		v    linkintegrity.DirVerdict
		want string
	}{
		{"measured series", linkintegrity.DirVerdict{AudioCount: 5, DistinctStems: 1, ProbesOK: 5}, "confirmed-series"},
		{"unmeasurable files", linkintegrity.DirVerdict{AudioCount: 4, DistinctStems: 1, ProbesOK: 1, ProbesFailed: 3}, "unprobeable-files"},
		{"many works", linkintegrity.DirVerdict{AudioCount: 3, DistinctStems: 3, ProbesOK: 3}, "distinct-titles"},
		{"empty folder", linkintegrity.DirVerdict{AudioCount: 0}, "no-audio"},
	}
	for _, tc := range cases {
		if got := reviewCategory(tc.v); got != tc.want {
			t.Errorf("%s: reviewCategory = %q, want %q", tc.name, got, tc.want)
		}
	}
}
