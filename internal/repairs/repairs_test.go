// file: internal/repairs/repairs_test.go
// version: 1.14.0
// guid: e4b7c2a9-1d63-4f58-9a0e-8c3f6d2b7a41
// last-edited: 2026-10-04

package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// ---- fakes ----

type memStore struct {
	mu      sync.Mutex
	books   map[string]*database.Book
	files   map[string][]database.BookFile
	history []database.MetadataChangeRecord
	ops     map[string]*database.OperationV2Row
	// authors and bookAuthors back the credits guard; authorErr fails its
	// author read.
	authors     map[int]*database.Author
	bookAuthors map[string][]database.BookAuthor
	authorErr   error
	// narrators and bookNarrators back its narrator read.
	narrators     map[int]*database.Narrator
	bookNarrators map[string][]database.BookNarrator
	// failHistory makes RecordMetadataChange fail for this field.
	failHistory string
}

func newMemStore() *memStore {
	return &memStore{books: map[string]*database.Book{}, files: map[string][]database.BookFile{},
		ops: map[string]*database.OperationV2Row{}}
}

func (s *memStore) add(id, title, path string, seriesID *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.books[id] = &database.Book{ID: id, Title: title, FilePath: path, SeriesID: seriesID}
}

func (s *memStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.books[id]
	if !ok {
		return nil, nil
	}
	cp := *b
	return &cp, nil
}

func (s *memStore) GetBookAuthors(id string) ([]database.BookAuthor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.BookAuthor(nil), s.bookAuthors[id]...), nil
}

func (s *memStore) GetBookNarrators(id string) ([]database.BookNarrator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.BookNarrator(nil), s.bookNarrators[id]...), nil
}

func (s *memStore) GetNarratorByID(id int) (*database.Narrator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.narrators[id], nil
}

func (s *memStore) GetAuthorByID(id int) (*database.Author, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authorErr != nil {
		return nil, s.authorErr
	}
	return s.authors[id], nil
}

func (s *memStore) GetBookFiles(id string) ([]database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]database.BookFile(nil), s.files[id]...), nil
}

func (s *memStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.books[id]
	if !ok {
		return nil, nil
	}
	cp := *b
	if err := fn(&cp); err != nil {
		return nil, err
	}
	s.books[id] = &cp
	out := cp
	return &out, nil
}

func (s *memStore) RecordMetadataChange(r *database.MetadataChangeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failHistory != "" && r.Field == s.failHistory {
		return errors.New("history store down")
	}
	s.history = append(s.history, *r)
	return nil
}

func (s *memStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ops[id], nil
}

func (s *memStore) title(id string) string {
	b, _ := s.GetBookByID(id)
	return b.Title
}

type nopReporter struct{}

func (nopReporter) UpdateProgress(_, _ int, _ string) error          { return nil }
func (nopReporter) Log(_ slog.Level, _ string, _ ...slog.Attr) error { return nil }
func (nopReporter) Logger() *slog.Logger                             { return slog.Default() }
func (nopReporter) Checkpoint(_ any) error                           { return nil }
func (nopReporter) IsCanceled() bool                                 { return false }
func (r nopReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}
func (nopReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }
func (nopReporter) SetCurrentItem(_ string)                          {}

// trimFixer proposes trimming whitespace from every title that has any. Its
// fingerprint is the current title, so any title change moves it.
type trimFixer struct {
	s       *memStore
	applied sync.Map
}

func (f *trimFixer) ID() string          { return "trim-titles" }
func (f *trimFixer) Title() string       { return "Trim titles" }
func (f *trimFixer) Description() string { return "test fixer" }

func (f *trimFixer) rowFor(id string) Row {
	t := f.s.title(id)
	r := Row{RowID: id, BookIDs: []string{id}, Title: t, Current: map[string]string{"title": t},
		Proposed: map[string]string{"title": strings.TrimSpace(t)}, Reason: "untrimmed", Risk: RiskLow,
		Fingerprint: "fp:" + t}
	if strings.TrimSpace(t) == t {
		r.Skipped, r.SkipReason = "already_clean", "nothing to trim"
	}
	return r
}

func (f *trimFixer) Plan(_ context.Context, _ json.RawMessage, _ registry.Reporter) ([]Row, error) {
	f.s.mu.Lock()
	var ids []string
	for id := range f.s.books {
		ids = append(ids, id)
	}
	f.s.mu.Unlock()
	var rows []Row
	for _, id := range ids {
		rows = append(rows, f.rowFor(id))
	}
	return rows, nil
}

func (f *trimFixer) Replan(_ context.Context, _ json.RawMessage, planned Row, _ registry.Reporter) (Row, error) {
	return f.rowFor(planned.RowID), nil
}

func (f *trimFixer) Apply(_ context.Context, w *Writer, fresh Row) error {
	want := fresh.Current["title"]
	_, err := w.Modify(fresh.RowID, func(b *database.Book) error {
		if b.Title != want {
			return ErrChangedSincePlan
		}
		b.Title = strings.TrimSpace(b.Title)
		return nil
	})
	if err == nil {
		f.applied.Store(fresh.RowID, true)
	}
	return err
}

// fakeStandDown is a scan controller whose scan parks on acquire.
type fakeStandDown struct {
	mu            sync.Mutex
	failAcquires  int // this many acquires fail before one succeeds
	acquires      int
	released      int
	renewsLeft    int // -1 = unlimited
	scanRunning   bool
	scanWasPaused bool
}

func (s *fakeStandDown) AcquireScanStandDown(_ context.Context, holder, _ string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquires++
	if holder == "" {
		return nil, errors.New("empty holder")
	}
	if s.failAcquires > 0 {
		s.failAcquires--
		return nil, errors.New("scan stand-down: scan did not park within 60s")
	}
	if s.scanRunning {
		s.scanRunning, s.scanWasPaused = false, true
	}
	return func() {
		s.mu.Lock()
		s.released++
		s.scanRunning = s.scanWasPaused
		s.mu.Unlock()
	}, nil
}

func (s *fakeStandDown) RenewScanStandDown(string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.renewsLeft < 0 {
		return true
	}
	if s.renewsLeft == 0 {
		return false
	}
	s.renewsLeft--
	return true
}

func (s *fakeStandDown) ScanStandDownValid(string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewsLeft != 0
}

var immediate = WaitOptions{Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}

func seed(s *memStore) {
	s.add("b1", " One ", "/lib/Author/One/one.m4b", nil)
	s.add("b2", " Two ", "/lib/Author/Two/two.m4b", nil)
	s.add("b3", "Three", "/lib/Author/Three/three.m4b", nil)
	s.add("b4", " iTunes Copy ", "/lib/Author/Four/four.m4b", nil)
	s.files["b4"] = []database.BookFile{{ID: "f4", BookID: "b4", FilePath: "/mnt/books/itunes/Music/four.m4b", Missing: true}}
	s.add("b5", " Big Finish Drama ", "/lib/Big Finish/Five/five.m4b", nil)
	dw := 7
	s.add("b6", " Series Guarded ", "/lib/Other/Six/six.m4b", &dw)
}

func seriesNamer(id int) string {
	if id == 7 {
		return "Doctor Who: The Eighth Doctor"
	}
	return ""
}

func planFor(t *testing.T, s *memStore, f Fixer) *PlanResult {
	t.Helper()
	res, err := RunPlan(context.Background(), f, nil, PlanDeps{Guard: s, Series: seriesNamer, Concurrency: 3}, nopReporter{})
	require.NoError(t, err)
	return res
}

func deps(s *memStore, sd StandDown) ApplyDeps {
	return ApplyDeps{Guard: s, Series: seriesNamer, StandDown: sd, OpID: "op-apply",
		Writer: NewWriter(s, s, "trim-titles", "bulk_update", "rp-"), Wait: immediate, Concurrency: 3}
}

// ---- plan + guards ----

func TestRunPlan_FrameworkGuardsSkipITunesAndOwnerManual(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	res := planFor(t, s, f)
	require.Equal(t, 6, res.Total)
	byID := map[string]Row{}
	for _, r := range res.Rows {
		byID[r.RowID] = r
	}
	require.True(t, byID["b1"].Applicable())
	require.True(t, byID["b2"].Applicable())
	require.Equal(t, "already_clean", byID["b3"].Skipped)
	require.Equal(t, SkipITunes, byID["b4"].Skipped, "a MISSING book_file row under books/itunes still guards")
	require.Equal(t, SkipOwnerManual, byID["b5"].Skipped, "Big Finish by path")
	require.Equal(t, SkipOwnerManual, byID["b6"].Skipped, "Doctor Who by series name")
	require.Equal(t, 2, res.Applicable)
	require.Equal(t, map[string]int{"already_clean": 1, SkipITunes: 1, SkipOwnerManual: 2}, res.SkippedByKind)
	// Sorted by row id.
	for i := 1; i < len(res.Rows); i++ {
		require.Less(t, res.Rows[i-1].RowID, res.Rows[i].RowID)
	}
}

func TestGuardBookPaths_TorchwoodAndDoctorWhoSeparators(t *testing.T) {
	for _, p := range []string{"/lib/Torchwood/x.m4b", "/lib/Doctor_Who/x.m4b", "/lib/DoctorWho - Y/x.m4b"} {
		k, _ := GuardBookPaths("b", []string{p}, "")
		require.Equal(t, SkipOwnerManual, k, p)
	}
	k, _ := GuardBookPaths("b", nil, "Torchwood")
	require.Equal(t, SkipOwnerManual, k, "series alone, no paths")
	k, _ = GuardBookPaths("b", []string{"/lib/Author/Title/t.m4b"}, "Discworld")
	require.Empty(t, k)
}

// TestPathResolver_SymlinksIntoITunes (N6): a folder link, a file link and a
// missing file under a linked folder all resolve into books/itunes/**, and
// the folder is resolved once for the whole run.
func TestPathResolver_SymlinksIntoITunes(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	itunes := filepath.Join(root, "books", "itunes", "Real")
	require.NoError(t, os.MkdirAll(itunes, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(itunes, "a.m4b"), []byte("x"), 0o644))
	lib := filepath.Join(root, "lib")
	require.NoError(t, os.MkdirAll(filepath.Join(lib, "Plain"), 0o755))
	require.NoError(t, os.Symlink(itunes, filepath.Join(lib, "Link")))
	require.NoError(t, os.Symlink(filepath.Join(itunes, "a.m4b"), filepath.Join(lib, "Plain", "file-link.m4b")))
	require.NoError(t, os.Symlink(filepath.Join(root, "nowhere.m4b"), filepath.Join(itunes, "dead.m4b")))
	// Dangling links in a plain folder whose link text names the iTunes tree
	// (L-c), absolute and relative.
	require.NoError(t, os.Symlink(filepath.Join(itunes, "gone.m4b"), filepath.Join(lib, "Plain", "dead-abs.m4b")))
	require.NoError(t, os.Symlink(filepath.Join("..", "..", "books", "itunes", "Real", "gone.m4b"), filepath.Join(lib, "Plain", "dead-rel.m4b")))
	// Its text names a path through a folder link into the iTunes tree.
	require.NoError(t, os.Symlink(filepath.Join(lib, "Link", "gone2.m4b"), filepath.Join(lib, "Plain", "dead-via-link.m4b")))

	res := NewPathResolver()
	cached := func() int {
		res.mu.Lock()
		defer res.mu.Unlock()
		return len(res.dirs)
	}
	var first int
	for pass := 0; pass < 2; pass++ {
		for _, p := range []string{
			filepath.Join(lib, "Link", "a.m4b"),
			filepath.Join(lib, "Link", "gone.m4b"), // missing, folder is a link
			filepath.Join(lib, "Link", "dead.m4b"), // dangling link, folder is a link
			filepath.Join(lib, "Plain", "file-link.m4b"),
			filepath.Join(lib, "Plain", "dead-abs.m4b"),
			filepath.Join(lib, "Plain", "dead-rel.m4b"),
			filepath.Join(lib, "Plain", "dead-via-link.m4b"),
		} {
			k, why := GuardBookPathsWith(res, "b", []string{p}, "")
			require.Equal(t, SkipITunes, k, "%s: %s", p, why)
		}
		k, _ := GuardBookPathsWith(res, "b", []string{filepath.Join(lib, "Plain", "own.m4b")}, "")
		require.Empty(t, k, "a plain folder is not iTunes")
		if pass == 0 {
			first = cached()
			// Link, Plain and the iTunes folder the dead links name.
			require.LessOrEqual(t, first, 4)
		}
	}
	require.Equal(t, first, cached(), "a second pass resolves no folder again")
	var k string
	// Unresolvable: checked lexically only.
	k, _ = GuardBookPathsWith(res, "b", []string{"/nonexistent-192.0.2.1/books/itunes/x.m4b"}, "")
	require.Equal(t, SkipITunes, k)
}

// A Doctor Who book on a neutral path with no series row is still guarded:
// its title names it.
func TestGuardBooks_OwnerManualByTitle(t *testing.T) {
	s := newMemStore()
	s.add("dw", "Doctor Who: Placebo Effect", "/lib/bbc/pe", nil)
	s.add("ok", "Placebo Effect", "/lib/bbc/pe2", nil)
	k, why, err := GuardBooks(s, nil, nil, NewPathResolver(), []string{"dw"})
	require.NoError(t, err)
	require.Equal(t, SkipOwnerManual, k, why)
	k, _, err = GuardBooks(s, nil, nil, NewPathResolver(), []string{"ok"})
	require.NoError(t, err)
	require.Empty(t, k)
}

// The title check names the franchise, not the words: "The Doctor Who Fooled
// the World" is not a Doctor Who book; "Frontios - Doctor Who" is.
func TestGuardBookTitle_BothDirections(t *testing.T) {
	for title, want := range map[string]bool{
		"Doctor Who: Placebo Effect":         true,
		"Doctor Who: Apollo 23":              true,
		"Doctor Who - The Daleks":            true,
		"Doctor Who":                         true,
		"Frontios - Doctor Who":              true,
		"Frontios (Doctor Who)":              true,
		"Big Finish Productions Presents: X": true,
		"Big Finish Ident":                   true,
		"Torchwood: Aliens Among Us":         true,
		"Torchwood":                          true,
		"The Doctor Who Fooled the World":    false,
		"A Doctor Who Cared":                 false,
		"The Big Finish":                     false,
		"Big Finish to the Season":           false,
		"Secrets of the Torchwood Estate":    false,
		"Placebo Effect":                     false,
		// Prod titles the leading/trailing rule missed (follow-up L4).
		"Nelvana Doctor Who":                     true,
		"The Language of Doctor Who":             true,
		"Another Pirate's History of Doctor Who": true,
		"Doctor.Who - Shada":                     true,
		// "_" is a regexp word character; the organizer writes ":" as "_ "
		// (#3616 review F1).
		"Doctor Who_ Mindwarp":            true,
		"Doctor_Who_Mindwarp":             true,
		"Torchwood_ Border Princes":       true,
		"Big_Finish_Productions":          true,
		"Doctor Whoopsie":                 false,
		"The_Doctor_Who_Fooled_the_World": false,
	} {
		k, why := GuardBookTitle("b", title)
		require.Equal(t, want, k == SkipOwnerManual, "%q: %s", title, why)
	}
}

// #3616 review F1: a prod book escaped every guard -- "_" is a regexp word
// character, so \b never fired in "Doctor Who_ Mindwarp".
func TestGuardBookPaths_UnderscoreColon(t *testing.T) {
	k, why := GuardBookPaths("b", []string{"/x/Unknown Author/Doctor Who_ Mindwarp/01.mp3"}, "")
	require.Equal(t, SkipOwnerManual, k, why)
	for _, series := range []string{"Doctor Who_ Mindwarp", "Doctor_Who_Mindwarp", "Torchwood_ Border Princes", "Big_Finish_Productions"} {
		k, why = GuardBookPaths("b", []string{"/x/neutral/01.mp3"}, series)
		require.Equal(t, SkipOwnerManual, k, "%q: %s", series, why)
	}
	for _, p := range []string{"/x/Doctor_Who_Mindwarp/01.mp3", "/x/Torchwood_ Border Princes/01.mp3", "/x/Big_Finish_Productions/01.mp3"} {
		k, why = GuardBookPaths("b", []string{p}, "")
		require.Equal(t, SkipOwnerManual, k, "%q: %s", p, why)
	}
	k, _ = GuardBookPaths("b", []string{"/x/Doctor Whoopsie/01.mp3"}, "")
	require.Empty(t, k)
}

// itunesLinkRoot builds <root>/books/itunes/Real and <root>/lib, with
// <root>/lib/Link a folder link into the iTunes folder.
func itunesLinkRoot(t *testing.T) (root, itunes, lib string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	itunes = filepath.Join(root, "books", "itunes", "Real")
	require.NoError(t, os.MkdirAll(itunes, 0o755))
	lib = filepath.Join(root, "lib")
	require.NoError(t, os.MkdirAll(filepath.Join(lib, "Plain"), 0o755))
	require.NoError(t, os.Symlink(itunes, filepath.Join(lib, "Link")))
	return root, itunes, lib
}

// TestGuard_DeadLinkIntoMissingSubfolderOfAnITunesLink (F2, review LC1): a
// dead link whose text names a path two folders below a folder link into
// books/itunes/** (the middle folder is gone) resolves through the longest
// existing ancestor and is skipped.
func TestGuard_DeadLinkIntoMissingSubfolderOfAnITunesLink(t *testing.T) {
	_, _, lib := itunesLinkRoot(t)
	require.NoError(t, os.Symlink(filepath.Join(lib, "Link", "GoneDisc", "gone.m4b"), filepath.Join(lib, "Plain", "dead-deep.m4b")))
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{filepath.Join(lib, "Plain", "dead-deep.m4b")}, "")
	require.Equal(t, SkipITunes, k, why)
}

// TestGuard_MissingRowTwoBelowAnITunesFolderLink (F2, review LC2): a missing
// row two folders below a folder link into iTunes, no link text involved.
func TestGuard_MissingRowTwoBelowAnITunesFolderLink(t *testing.T) {
	_, _, lib := itunesLinkRoot(t)
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{filepath.Join(lib, "Link", "GoneDisc", "gone.m4b")}, "")
	require.Equal(t, SkipITunes, k, why)
}

// TestGuard_MissingRowBelowADanglingFolderLink (F2): the missing row's folder
// is itself a dangling link whose text names the iTunes tree.
func TestGuard_MissingRowBelowADanglingFolderLink(t *testing.T) {
	_, itunes, lib := itunesLinkRoot(t)
	require.NoError(t, os.Symlink(filepath.Join(itunes, "GoneFolder"), filepath.Join(lib, "DeadDir")))
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{filepath.Join(lib, "DeadDir", "Disc 1", "x.m4b")}, "")
	require.Equal(t, SkipITunes, k, why)
}

// TestGuard_RelativeEscapeAndLoop (review LC3, control): a relative ".."
// hop into a dead iTunes path is skipped; a link loop points nowhere and is
// cleared, not doubted.
func TestGuard_RelativeEscapeAndLoop(t *testing.T) {
	root, itunes, _ := itunesLinkRoot(t)
	lib := filepath.Join(root, "lib", "A", "B")
	require.NoError(t, os.MkdirAll(lib, 0o755))
	require.NoError(t, os.Symlink(filepath.Join(itunes, "gone.m4b"), filepath.Join(root, "lib", "hop2.m4b")))
	require.NoError(t, os.Symlink(filepath.Join("..", "..", "hop2.m4b"), filepath.Join(lib, "hop1.m4b")))
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{filepath.Join(lib, "hop1.m4b")}, "")
	require.Equal(t, SkipITunes, k, why)

	require.NoError(t, os.Symlink(filepath.Join(lib, "y.m4b"), filepath.Join(lib, "x.m4b")))
	require.NoError(t, os.Symlink(filepath.Join(lib, "x.m4b"), filepath.Join(lib, "y.m4b")))
	k, why = GuardBookPathsWith(NewPathResolver(), "b", []string{filepath.Join(lib, "x.m4b")}, "")
	require.Empty(t, k, why)
}

// linkChain makes n links in dir, each naming the next, the last naming end;
// it returns the first.
func linkChain(t *testing.T, dir string, n int, end string) string {
	t.Helper()
	name := func(i int) string { return filepath.Join(dir, fmt.Sprintf("l%04d.m4b", i)) }
	require.NoError(t, os.Symlink(end, name(n-1)))
	for i := n - 2; i >= 0; i-- {
		require.NoError(t, os.Symlink(name(i+1), name(i)))
	}
	return name(0)
}

// TestGuard_LongChainIntoITunes (F2, review LC4): a 41-link chain (past the
// kernel's 40, so EvalSymlinks fails) whose last link names a dead iTunes
// path is walked by hand and skipped.
func TestGuard_LongChainIntoITunes(t *testing.T) {
	_, itunes, lib := itunesLinkRoot(t)
	first := linkChain(t, filepath.Join(lib, "Plain"), 41, filepath.Join(itunes, "gone.m4b"))
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{first}, "")
	require.Equal(t, SkipITunes, k, why)
}

// TestGuard_HopExhaustionIsDoubt (F2): a chain longer than maxLinkHops is not
// settled, so the row is skipped as unreadable, never cleared.
func TestGuard_HopExhaustionIsDoubt(t *testing.T) {
	_, _, lib := itunesLinkRoot(t)
	first := linkChain(t, filepath.Join(lib, "Plain"), maxLinkHops+2, filepath.Join(lib, "Plain", "nowhere.m4b"))
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{first}, "")
	require.Equal(t, SkipGuardUnreadable, k, why)
}

// TestGuard_UnreadableFolderIsDoubt (F2): a row in a folder the guard cannot
// read could be anywhere; it is skipped as unreadable.
func TestGuard_UnreadableFolderIsDoubt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	_, _, lib := itunesLinkRoot(t)
	locked := filepath.Join(lib, "Locked")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{filepath.Join(locked, "x.m4b")}, "")
	require.Equal(t, SkipGuardUnreadable, k, why)
	// A plain missing row is an answer, not doubt.
	k, why = GuardBookPathsWith(NewPathResolver(), "b", []string{filepath.Join(lib, "Plain", "gone", "x.m4b")}, "")
	require.Empty(t, k, why)
}

// TestGuard_OverlongComponentIsNotDoubt (review LC6): a stored path with a
// component longer than the system allows cannot exist on disk. Lstat answers
// ENAMETOOLONG, which is "missing", not doubt, so the row is cleared.
func TestGuard_OverlongComponentIsNotDoubt(t *testing.T) {
	_, _, lib := itunesLinkRoot(t)
	p := filepath.Join(lib, "Plain", strings.Repeat("a", 300)+".m4b")
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{p}, "")
	require.Empty(t, k, why)
	p = filepath.Join(lib, strings.Repeat("a", 300), "x.m4b")
	k, why = GuardBookPathsWith(NewPathResolver(), "b", []string{p}, "")
	require.Empty(t, k, why)
}

// TestGuard_LoopThroughITunesIsCaught (control): a link loop with one member
// inside books/itunes/** is caught on the way round.
func TestGuard_LoopThroughITunesIsCaught(t *testing.T) {
	_, itunes, lib := itunesLinkRoot(t)
	require.NoError(t, os.Symlink(filepath.Join(itunes, "y.m4b"), filepath.Join(lib, "Plain", "x.m4b")))
	require.NoError(t, os.Symlink(filepath.Join(lib, "Plain", "x.m4b"), filepath.Join(itunes, "y.m4b")))
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{filepath.Join(lib, "Plain", "x.m4b")}, "")
	require.Equal(t, SkipITunes, k, why)
}

// TestGuard_LongChainIsWalkedOnce: a dangling chain is resolved with
// EvalSymlinks once, on the path the walk starts from, and then by hand.
// Asking EvalSymlinks again at every hop re-walks the rest of the chain each
// time: quadratic, 537 ms for 250 links on APFS.
func TestGuard_LongChainIsWalkedOnce(t *testing.T) {
	_, _, lib := itunesLinkRoot(t)
	first := linkChain(t, filepath.Join(lib, "Plain"), 250, filepath.Join(lib, "gone.m4b"))
	var calls int
	orig := evalSymlinks
	evalSymlinks = func(p string) (string, error) {
		calls++
		return orig(p)
	}
	t.Cleanup(func() { evalSymlinks = orig })
	k, why := GuardBookPathsWith(NewPathResolver(), "b", []string{first}, "")
	require.Empty(t, k, why)
	// The start, and the one folder every link is in (resolved once, then
	// cached); the chain's end sits in lib.
	require.LessOrEqual(t, calls, 3, "EvalSymlinks per hop")
}

// ---- paging ----

func TestPlanResult_Page(t *testing.T) {
	s := newMemStore()
	seed(s)
	res := planFor(t, s, &trimFixer{s: s})

	p, err := res.Page("op-plan", FilterAll, "", 0, 4)
	require.NoError(t, err)
	require.Equal(t, 6, p.Total)
	require.Len(t, p.Rows, 4)
	require.Equal(t, "b1", p.Rows[0].RowID)

	p, err = res.Page("op-plan", FilterAll, "", 4, 4)
	require.NoError(t, err)
	require.Len(t, p.Rows, 2)
	require.Equal(t, "b5", p.Rows[0].RowID)

	p, err = res.Page("op-plan", FilterAll, "", 10, 4)
	require.NoError(t, err)
	require.NotNil(t, p.Rows)
	require.Empty(t, p.Rows)

	p, err = res.Page("op-plan", FilterApplicable, "", 0, 50)
	require.NoError(t, err)
	require.Equal(t, 2, p.Total)
	p, err = res.Page("op-plan", FilterSkipped, "", 1, 50)
	require.NoError(t, err)
	require.Equal(t, 4, p.Total)
	require.Len(t, p.Rows, 3)

	// A class filter narrows within the filter; the in-filter tally counts
	// exactly the rows each class chip lists.
	res.Rows[0].Class, res.Rows[1].Class = "moved", "moved"
	p, err = res.Page("op-plan", FilterAll, "moved", 0, 50)
	require.NoError(t, err)
	require.Equal(t, 2, p.Total)
	require.Equal(t, 2, p.ByClassInFilter["moved"])
	for _, r := range p.Rows {
		require.Equal(t, "moved", r.Class)
	}

	// Every per-kind skip count pages exactly the rows it counts.
	sum := 0
	for kind, n := range res.SkippedByKind {
		p, err = res.Page("op-plan", FilterSkippedKindPrefix+kind, "", 0, 50)
		require.NoError(t, err)
		require.Equal(t, n, p.Total, kind)
		for _, r := range p.Rows {
			require.Equal(t, kind, r.Skipped)
		}
		sum += p.Total
	}
	require.Equal(t, 4, sum)

	_, err = res.Page("op-plan", "bogus", "", 0, 1)
	require.Error(t, err)
	_, err = res.Page("op-plan", FilterSkippedKindPrefix, "", 0, 1)
	require.Error(t, err, "an empty kind is not a filter")
}

// TestPlanResult_PageSkipKindsInClass: under a selected class the skip-kind
// chips count only that class's skipped rows (skipped_by_kind_in_class), so
// each chip opens exactly the rows it counts.
func TestPlanResult_PageSkipKindsInClass(t *testing.T) {
	res := &PlanResult{FixerID: "x", Rows: []Row{
		{RowID: "a", Skipped: SkipITunes, Class: "moved"},
		{RowID: "b", Skipped: SkipITunes, Class: "copy"},
		{RowID: "c", Class: "moved"},
	}, SkippedByKind: map[string]int{SkipITunes: 2}, Applicable: 1}

	p, err := res.Page("op", FilterSkipped, "moved", 0, 50)
	require.NoError(t, err)
	require.Equal(t, map[string]int{SkipITunes: 1}, p.SkippedByKindInClass)
	require.Equal(t, 2, p.SkippedByKind[SkipITunes], "the whole-plan tally is unchanged")
	require.Equal(t, 1, p.Total)

	// It counts the class's skipped rows whatever the filter.
	p, err = res.Page("op", FilterApplicable, "moved", 0, 50)
	require.NoError(t, err)
	require.Equal(t, map[string]int{SkipITunes: 1}, p.SkippedByKindInClass)

	// Returned only when a class is set.
	p, err = res.Page("op", FilterSkipped, "", 0, 50)
	require.NoError(t, err)
	require.Nil(t, p.SkippedByKindInClass)

	// A class with no skipped rows reports an empty tally, not nil.
	res.Rows = append(res.Rows, Row{RowID: "d", Class: "no-parent"})
	p, err = res.Page("op", FilterSkipped, "no-parent", 0, 50)
	require.NoError(t, err)
	require.NotNil(t, p.SkippedByKindInClass)
	require.Empty(t, p.SkippedByKindInClass)
}

// TestPlanResult_PageSkipKindWithClass: "skipped:<kind>" and a class narrow
// together, and every in-class kind count pages exactly its rows.
func TestPlanResult_PageSkipKindWithClass(t *testing.T) {
	res := &PlanResult{FixerID: "x", Rows: []Row{
		{RowID: "a", Skipped: SkipITunes, Class: "moved"},
		{RowID: "b", Skipped: SkipITunes, Class: "copy"},
		{RowID: "c", Skipped: SkipOwnerManual, Class: "moved"},
		{RowID: "d", Skipped: SkipOwnerManual, Class: "moved"},
		{RowID: "e", Class: "moved"},
	}, SkippedByKind: map[string]int{SkipITunes: 2, SkipOwnerManual: 2}, Applicable: 1}

	p, err := res.Page("op", FilterSkippedKindPrefix+SkipITunes, "moved", 0, 50)
	require.NoError(t, err)
	require.Equal(t, 1, p.Total)
	require.Len(t, p.Rows, 1)
	require.Equal(t, "a", p.Rows[0].RowID)
	require.Equal(t, map[string]int{"moved": 1, "copy": 1}, p.ByClassInFilter)

	for _, class := range []string{"moved", "copy"} {
		p, err = res.Page("op", FilterSkipped, class, 0, 50)
		require.NoError(t, err)
		for kind, n := range p.SkippedByKindInClass {
			kp, err := res.Page("op", FilterSkippedKindPrefix+kind, class, 0, 50)
			require.NoError(t, err)
			require.Equal(t, n, kp.Total, "%s/%s", class, kind)
			for _, r := range kp.Rows {
				require.Equal(t, kind, r.Skipped)
				require.Equal(t, class, r.Class)
			}
		}
	}
	p, err = res.Page("op", FilterSkippedKindPrefix+SkipOwnerManual, "copy", 0, 50)
	require.NoError(t, err)
	require.Zero(t, p.Total)
	require.NotNil(t, p.Rows)
}

// TestPlanResult_PageInClassTalliesAreInTheJSON (F4, review MED1): under a
// class, an empty in-class skip tally and a zero in-class applicable count
// are in the JSON (the panel reads a missing key as "no class counts" and
// falls back to the whole plan's); without a class, applicable_in_class is
// absent.
func TestPlanResult_PageInClassTalliesAreInTheJSON(t *testing.T) {
	res := &PlanResult{FixerID: "x", Rows: []Row{
		{RowID: "r1", Class: "moved"},
		{RowID: "r2", Skipped: SkipITunes, Class: "copy"},
	}, SkippedByKind: map[string]int{SkipITunes: 1}, Applicable: 1}
	keys := func(p *RowsPage) map[string]json.RawMessage {
		raw, err := json.Marshal(p)
		require.NoError(t, err)
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &m))
		return m
	}

	p, err := res.Page("op", FilterAll, "moved", 0, 10)
	require.NoError(t, err)
	m := keys(p)
	require.JSONEq(t, `{}`, string(m["skipped_by_kind_in_class"]))
	require.JSONEq(t, `1`, string(m["applicable_in_class"]))

	p, err = res.Page("op", FilterSkipped, "copy", 0, 10)
	require.NoError(t, err)
	m = keys(p)
	require.JSONEq(t, `{"skipped_itunes":1}`, string(m["skipped_by_kind_in_class"]))
	require.JSONEq(t, `0`, string(m["applicable_in_class"]), "a zero count is present, not absent")

	p, err = res.Page("op", FilterAll, "", 0, 10)
	require.NoError(t, err)
	m = keys(p)
	_, ok := m["applicable_in_class"]
	require.False(t, ok, "no class: no in-class applicable count")
}

// TestPlanResult_PageApplicableInClass (F3): the in-class applicable count
// is the class's applicable rows whatever the filter, and equals the Total
// the "applicable" filter pages for that class.
func TestPlanResult_PageApplicableInClass(t *testing.T) {
	res := &PlanResult{FixerID: "x", Rows: []Row{
		{RowID: "a", Class: "moved"},
		{RowID: "b", Class: "moved"},
		{RowID: "c", Class: "copy"},
		{RowID: "d", Skipped: SkipITunes, Class: "moved"},
	}, SkippedByKind: map[string]int{SkipITunes: 1}, Applicable: 3}
	for _, filter := range []string{FilterAll, FilterApplicable, FilterSkipped, FilterSkippedKindPrefix + SkipITunes} {
		for class, want := range map[string]int{"moved": 2, "copy": 1, "none": 0} {
			p, err := res.Page("op", filter, class, 0, 50)
			require.NoError(t, err)
			require.NotNil(t, p.ApplicableInClass, "%s/%s", filter, class)
			require.Equal(t, want, *p.ApplicableInClass, "%s/%s", filter, class)
			require.Equal(t, 3, p.Applicable, "the whole-plan count is unchanged")
			ap, err := res.Page("op", FilterApplicable, class, 0, 50)
			require.NoError(t, err)
			require.Equal(t, ap.Total, *p.ApplicableInClass, "%s/%s", filter, class)
		}
	}
	p, err := res.Page("op", FilterAll, "", 0, 50)
	require.NoError(t, err)
	require.Nil(t, p.ApplicableInClass)
}

func TestLoadPlan_ChecksDefStatusAndFixer(t *testing.T) {
	s := newMemStore()
	seed(s)
	res := planFor(t, s, &trimFixer{s: s})
	data, err := json.Marshal(res)
	require.NoError(t, err)
	str := string(data)
	s.ops["op-plan"] = &database.OperationV2Row{ID: "op-plan", DefID: PlanOpID, Status: "completed", ResultData: &str}
	s.ops["op-running"] = &database.OperationV2Row{ID: "op-running", DefID: PlanOpID, Status: "running"}
	s.ops["op-other"] = &database.OperationV2Row{ID: "op-other", DefID: "library.scan", Status: "completed", ResultData: &str}

	plan, err := LoadPlan(s, "op-plan", "trim-titles")
	require.NoError(t, err)
	require.Len(t, plan.Rows, 6)
	require.Equal(t, res.Rows[0].Fingerprint, plan.Rows[0].Fingerprint)

	_, err = LoadPlan(s, "op-plan", "other-fixer")
	require.ErrorIs(t, err, ErrPlanOtherFixer)
	_, err = LoadPlan(s, "op-running", "trim-titles")
	require.ErrorIs(t, err, ErrPlanNotComplete)
	_, err = LoadPlan(s, "op-other", "trim-titles")
	require.ErrorIs(t, err, ErrNotAPlan)
	_, err = LoadPlan(s, "op-missing", "trim-titles")
	require.ErrorIs(t, err, ErrPlanNotFound)
}

// ---- apply ----

func TestRunApply_AppliesSelectedRowsAndWritesHistory(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b3", "b4", "nope"}, false, deps(s, sd), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied)
	require.Equal(t, []string{"nope"}, res.NotInPlan)
	require.Equal(t, map[string]int{OutcomeApplied: 1, OutcomeNotApplicable: 2}, res.ByOutcome)
	require.Equal(t, "One", s.title("b1"))
	require.Equal(t, " Two ", s.title("b2"), "a row not selected is untouched")
	require.Equal(t, " iTunes Copy ", s.title("b4"), "a guarded row is never written")

	// Every applied change has a history row, after the write.
	require.Len(t, s.history, 1)
	h := s.history[0]
	require.Equal(t, "b1", h.BookID)
	require.Equal(t, "title", h.Field)
	require.Equal(t, `" One "`, *h.PreviousValue)
	require.Equal(t, `"One"`, *h.NewValue)
	require.Equal(t, "trim-titles", h.Source)
	require.True(t, strings.HasPrefix(h.BatchID, "rp-"))
	require.Equal(t, 1, res.HistoryRows)
	require.Equal(t, 1, sd.released, "the stand-down is released")
}

// TestRunApply_CheckpointsAndResumes: every settled row is checkpointed, and
// a run resumed from that checkpoint reports those rows as they were without
// applying them again, and applies only the rest.
func TestRunApply_CheckpointsAndResumes(t *testing.T) {
	old := checkpointEvery
	checkpointEvery = 1
	t.Cleanup(func() { checkpointEvery = old })

	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	var mu sync.Mutex
	var last ApplyCheckpoint
	d := deps(s, &fakeStandDown{renewsLeft: -1})
	d.Checkpoint = func(cp ApplyCheckpoint) error {
		mu.Lock()
		defer mu.Unlock()
		last = cp
		return nil
	}
	_, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Len(t, last.Settled, 1)
	require.Equal(t, RowResult{RowID: "b1", Outcome: OutcomeApplied}, last.Settled[0])

	// Undo b1 by hand: a resumed run must NOT re-apply it.
	_, err = s.ModifyBook("b1", func(b *database.Book) error { b.Title = " One "; return nil })
	require.NoError(t, err)
	d2 := deps(s, &fakeStandDown{renewsLeft: -1})
	cp := last
	cp.Settled = append(cp.Settled, RowResult{RowID: "b2", Outcome: OutcomeAborted})
	d2.Resumed = &cp
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b2"}, false, d2, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 2, res.Applied, "b1 from the checkpoint, b2 (aborted, never written) applied now")
	require.Equal(t, " One ", s.title("b1"), "a checkpointed row is not applied again")
	require.Equal(t, "Two", s.title("b2"))
}

func TestRunApply_RefusesRowWhoseFingerprintChanged(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	// Someone edits b2 after the plan.
	_, err := s.ModifyBook("b2", func(b *database.Book) error { b.Title = " Two (edited) "; return nil })
	require.NoError(t, err)
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b2"}, false, deps(s, &fakeStandDown{renewsLeft: -1}), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Applied)
	require.Equal(t, 1, res.ChangedSincePlan)
	require.Equal(t, " Two (edited) ", s.title("b2"), "the changed row is left exactly as the other writer left it")
	for _, h := range s.history {
		require.NotEqual(t, "b2", h.BookID)
	}
}

func TestRunApply_ReGuardsOnFreshReads(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	// After the plan, b1 gains a file under books/itunes.
	s.files["b1"] = []database.BookFile{{ID: "f1", BookID: "b1", FilePath: "/Volumes/books/iTunes/x/one.m4b"}}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, deps(s, &fakeStandDown{renewsLeft: -1}), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, OutcomeGuarded, res.Rows[0].Outcome)
	require.Equal(t, SkipITunes, res.Rows[0].Skipped)
	require.Equal(t, " One ", s.title("b1"))
	require.Empty(t, s.history)
}

func TestRunApply_DryRunWritesNothingAndTakesNoStandDown(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, scanRunning: true}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b2"}, true, deps(s, sd), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, map[string]int{OutcomeWouldApply: 2}, res.ByOutcome)
	require.Equal(t, " One ", s.title("b1"))
	require.Zero(t, sd.acquires, "a preview never pauses the scan")
	require.Empty(t, s.history)
}

func TestRunApply_PausesARunningScanAndProceeds(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, scanRunning: true}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1", "b2"}, false, deps(s, sd), nopReporter{})
	require.NoError(t, err, "a running scan is paused, never a refusal")
	require.Equal(t, 2, res.Applied)
	require.True(t, res.StandDownHeld)
	require.True(t, sd.scanWasPaused)
	require.True(t, sd.scanRunning, "the scan resumes on release")
}

func TestRunApply_AcquireFailureWaitsAndRetries(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, failAcquires: 2, scanRunning: true}
	var slept int
	d := deps(s, sd)
	d.Wait = WaitOptions{Sleep: func(context.Context, time.Duration) error { slept++; return nil }}
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, d, nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 3, sd.acquires)
	require.Equal(t, 2, slept)
	require.Equal(t, 1, res.Applied)
}

func TestRunApply_AcquireWaitEndsOnlyWithTheContext(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	sd := &fakeStandDown{renewsLeft: -1, failAcquires: 1 << 30}
	ctx, cancel := context.WithCancel(context.Background())
	d := deps(s, sd)
	tries := 0
	d.Wait = WaitOptions{Sleep: func(c context.Context, _ time.Duration) error {
		tries++
		if tries == 5 {
			cancel()
		}
		return c.Err()
	}}
	_, err := RunApply(ctx, f, plan, "op-plan", []string{"b1"}, false, d, nopReporter{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 5, sd.acquires)
	require.Equal(t, " One ", s.title("b1"))
}

// touchReporter counts liveness stamps.
type touchReporter struct {
	nopReporter
	mu      sync.Mutex
	touches int
}

func (r *touchReporter) TouchLiveness() {
	r.mu.Lock()
	r.touches++
	r.mu.Unlock()
}

func (r *touchReporter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.touches
}

// blockingStandDown's acquire blocks until the test lets the scan park.
type blockingStandDown struct {
	fakeStandDown
	park chan struct{}
}

func (s *blockingStandDown) AcquireScanStandDown(ctx context.Context, holder, reason string) (func(), error) {
	select {
	case <-s.park:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.fakeStandDown.AcquireScanStandDown(ctx, holder, reason)
}

// A scan that takes long to park is waited out in ONE attempt (no per-attempt
// timeout re-queueing it), with liveness stamped while the attempt blocks.
func TestAcquireStandDownWaiting_OneLongAttemptKeepsTheOpAlive(t *testing.T) {
	sd := &blockingStandDown{fakeStandDown: fakeStandDown{renewsLeft: -1, scanRunning: true}, park: make(chan struct{})}
	rep := &touchReporter{}
	done := make(chan error, 1)
	go func() {
		rel, held, err := AcquireStandDownWaiting(context.Background(), sd, "op-1", "test", rep,
			WaitOptions{TouchEvery: time.Millisecond, Sleep: immediate.Sleep})
		if err == nil && held {
			rel()
		}
		done <- err
	}()
	require.Eventually(t, func() bool { return rep.count() >= 3 }, 5*time.Second, time.Millisecond,
		"liveness is stamped while the acquire blocks")
	close(sd.park)
	require.NoError(t, <-done)
	sd.mu.Lock()
	defer sd.mu.Unlock()
	require.Equal(t, 1, sd.acquires, "exactly one attempt")
	require.True(t, sd.scanWasPaused)
}

// partialFixer writes, then reports the row only partly applied.
type partialFixer struct{ trimFixer }

func (f *partialFixer) Apply(ctx context.Context, w *Writer, fresh Row) error {
	if err := f.trimFixer.Apply(ctx, w, fresh); err != nil {
		return err
	}
	return fmt.Errorf("%w: second half hit a change", ErrPartiallyApplied)
}

func TestRunApply_PartialIsNotReportedAsUnchanged(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &partialFixer{trimFixer{s: s}}
	plan := planFor(t, s, f)
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, deps(s, &fakeStandDown{renewsLeft: -1}), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Partial)
	require.Zero(t, res.ChangedSincePlan)
	require.Equal(t, OutcomePartial, res.Rows[0].Outcome)
	require.Contains(t, res.Rows[0].Error, "second half")
}

func TestRunApply_LeaseLapseAbortsRemainingRows(t *testing.T) {
	s := newMemStore()
	for i := 0; i < 5; i++ {
		s.add(fmt.Sprintf("r%d", i), fmt.Sprintf(" T%d ", i), fmt.Sprintf("/lib/A/T%d/t.m4b", i), nil)
	}
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	// Three renewals succeed (row r0's start, its pre-write check and its one
	// write), then the lease is gone.
	sd := &fakeStandDown{renewsLeft: 3}
	d := deps(s, sd)
	d.Concurrency = 1
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"r0", "r1", "r2", "r3", "r4"}, false, d, nopReporter{})
	require.ErrorIs(t, err, ErrStandDownLost)
	require.NotNil(t, res, "the per-row report comes back with the abort")
	require.Equal(t, ErrStandDownLost.Error(), res.Aborted)
	require.Len(t, res.Rows, 5)
	require.Equal(t, 1, res.ByOutcome[OutcomeApplied], "one row renewed, passed the validity check and wrote")
	require.Equal(t, 4, res.ByOutcome[OutcomeAborted])
	written := 0
	for i := 0; i < 5; i++ {
		if s.title(fmt.Sprintf("r%d", i)) == fmt.Sprintf("T%d", i) {
			written++
		}
	}
	require.Equal(t, 1, written)
}

func TestRunApply_NoOpIDRefusesToWrite(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	plan := planFor(t, s, f)
	d := deps(s, &fakeStandDown{renewsLeft: -1})
	d.OpID = ""
	_, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, d, nopReporter{})
	require.ErrorIs(t, err, ErrNoHolderID)
	require.Equal(t, " One ", s.title("b1"))
}

func TestRunApply_NeedsRowIDs(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	_, err := RunApply(context.Background(), f, planFor(t, s, f), "op-plan", nil, false, deps(s, nil), nopReporter{})
	require.ErrorContains(t, err, "row_ids")
}

// ---- partitioning ----

func TestPartitionRows_SharedBooksNeverSplit(t *testing.T) {
	rows := []Row{
		{RowID: "a", BookIDs: []string{"1", "2"}},
		{RowID: "b", BookIDs: []string{"3"}},
		{RowID: "c", BookIDs: []string{"2", "4"}},
		{RowID: "d", BookIDs: []string{"4", "5"}},
		{RowID: "e", BookIDs: []string{"6"}},
	}
	parts := partitionRows(rows)
	owner := map[string]int{}
	for pi, part := range parts {
		for _, r := range part {
			for _, b := range r.BookIDs {
				if prev, ok := owner[b]; ok {
					require.Equal(t, prev, pi, "book %s in two partitions", b)
				}
				owner[b] = pi
			}
		}
	}
	require.Len(t, parts, 3)
	require.Equal(t, []string{"a", "c", "d"}, []string{parts[0][0].RowID, parts[0][1].RowID, parts[0][2].RowID})
}

// ---- writer ----

// TestWriter_HasNoDeletePrimitive pins the write surface a fixer gets: the
// framework must never offer a way to delete a book or a book_file row.
func TestWriter_HasNoDeletePrimitive(t *testing.T) {
	typ := reflect.TypeOf(&Writer{})
	var names []string
	for i := 0; i < typ.NumMethod(); i++ {
		n := typ.Method(i).Name
		names = append(names, n)
		lower := strings.ToLower(n)
		require.False(t, strings.Contains(lower, "delete") || strings.Contains(lower, "remove") ||
			strings.Contains(lower, "purge"), "Writer exposes %s", n)
	}
	require.ElementsMatch(t, []string{"Modify", "Writes", "HistoryRows", "HistoryFailed",
		"WithJournal", "WithLiveness", "Touch", "Journal", "Journaled", "JournaledValue", "Step",
		"RepointBookFile", "MoveBookFiles", "SetTrackNumber", "Recompute",
		"WithCredits", "ModifyCredits", "SetPrimaryAuthor", "RecordChange", "Beat", "LockWaiting",
		"WithFieldStates", "LockFields", "JournalStep", "WithTags", "AddBookTag"}, names)
}

func TestWriter_HistoryFailureWritesIncompleteMarker(t *testing.T) {
	s := newMemStore()
	s.add("b1", "Old", "/lib/a.m4b", nil)
	s.failHistory = "title"
	w := NewWriter(s, s, "src", "bulk_update", "rp-")
	changed, err := w.Modify("b1", func(b *database.Book) error {
		b.Title = "New"
		n := "Narr"
		b.Narrator = &n
		return nil
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"title", "narrator"}, changed)
	require.Equal(t, 1, w.HistoryFailed())
	var marker bool
	for _, h := range s.history {
		if h.ChangeType == ChangeTypeApplyIncomplete {
			marker = true
		}
	}
	require.True(t, marker, "undo must see the batch is incomplete")
}

// TestWriter_SeriesIDHistoryCarriesRefs: undo-last-apply restores series_id
// from the history row's refs and fails the field without them.
func TestWriter_SeriesIDHistoryCarriesRefs(t *testing.T) {
	s := newMemStore()
	s.add("b1", "Book", "/lib/a.m4b", nil)
	w := NewWriter(s, s, "src", "bulk_update", "rp-")
	changed, err := w.Modify("b1", func(b *database.Book) error {
		id := 42
		b.SeriesID = &id
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"series_id"}, changed)
	require.Len(t, s.history, 1)
	h := s.history[0]
	require.NotNil(t, h.PreviousRef)
	require.NotNil(t, h.NewRef)
	require.Nil(t, h.PreviousRef.SeriesID)
	require.NotNil(t, h.NewRef.SeriesID)
	require.Equal(t, 42, *h.NewRef.SeriesID)
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	s := newMemStore()
	require.NoError(t, r.Register(&trimFixer{s: s}))
	require.Error(t, r.Register(&trimFixer{s: s}))
	f, ok := r.Get("trim-titles")
	require.True(t, ok)
	require.Equal(t, "trim-titles", f.ID())
	require.Len(t, r.List(), 1)
	_, ok = r.Get("nope")
	require.False(t, ok)
}

// TestGuardBookPaths_ITunesClearedStillFailsClosedOnDoubt: a fixer cleared
// for iTunes database rows still skips a row whose folder could not be
// resolved (a permission error): behind it may be a Doctor Who / Big Finish
// / Torchwood folder.
func TestGuardBookPaths_ITunesClearedStillFailsClosedOnDoubt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Author", "Book", "01.mp3")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := evalSymlinks
	t.Cleanup(func() { evalSymlinks = orig })
	evalSymlinks = func(string) (string, error) { return "", os.ErrPermission }
	for _, allow := range []bool{false, true} {
		kind, reason := guardBookPaths(NewPathResolver(), "b1", []string{p}, "", allow)
		if kind != SkipGuardUnreadable {
			t.Fatalf("allowITunes=%t: kind %q (%s), want %q", allow, kind, reason, SkipGuardUnreadable)
		}
	}
}

// TestGuardBooks_Credits: a Big Finish release on a neutral path with a
// neutral title is manual-only by its publisher, an author (primary or a
// book_authors row) or its narrator; an author read failure is an error.
func TestGuardBooks_Credits(t *testing.T) {
	str := func(s string) *string { return &s }
	seedOne := func(mut func(s *memStore, b *database.Book)) *memStore {
		s := newMemStore()
		b := &database.Book{ID: "b", Title: "Michael Fenton Stevens/The Ultimate Foe", FilePath: "/lib/neutral/b.m4b"}
		s.authors = map[int]*database.Author{1: {ID: 1, Name: "Michael Fenton Stevens"}, 2: {ID: 2, Name: "Big Finish Productions"}}
		s.bookAuthors = map[string][]database.BookAuthor{}
		mut(s, b)
		s.books["b"] = b
		return s
	}
	for name, mut := range map[string]func(s *memStore, b *database.Book){
		"publisher":      func(_ *memStore, b *database.Book) { b.Publisher = str("Big Finish Productions") },
		"narrator":       func(_ *memStore, b *database.Book) { b.Narrator = str("Big Finish Audio Cast") },
		"primary author": func(_ *memStore, b *database.Book) { two := 2; b.AuthorID = &two },
		"book_authors row": func(s *memStore, _ *database.Book) {
			s.bookAuthors["b"] = []database.BookAuthor{{BookID: "b", AuthorID: 2}}
		},
		"book_narrators row": func(s *memStore, _ *database.Book) {
			s.narrators = map[int]*database.Narrator{7: {ID: 7, Name: "Big Finish Audio Cast"}}
			s.bookNarrators = map[string][]database.BookNarrator{"b": {{BookID: "b", NarratorID: 7}}}
		},
	} {
		k, why, err := GuardBooks(seedOne(mut), nil, nil, NewPathResolver(), []string{"b"})
		require.NoError(t, err, name)
		require.Equal(t, SkipOwnerManual, k, "%s: %s", name, why)
	}
	clean := seedOne(func(_ *memStore, b *database.Book) {
		one := 1
		b.AuthorID, b.Publisher, b.Narrator = &one, str("Audible Studios"), str("Michael Fenton Stevens")
	})
	k, why, err := GuardBooks(clean, nil, nil, NewPathResolver(), []string{"b"})
	require.NoError(t, err)
	require.Empty(t, k, why)
	broken := seedOne(func(s *memStore, b *database.Book) { one := 1; b.AuthorID = &one; s.authorErr = errors.New("boom") })
	_, _, err = GuardBooks(broken, nil, nil, NewPathResolver(), []string{"b"})
	require.Error(t, err, "an unreadable author never clears the book")
}
