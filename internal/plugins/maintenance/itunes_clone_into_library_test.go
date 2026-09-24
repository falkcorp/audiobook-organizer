// file: internal/plugins/maintenance/itunes_clone_into_library_test.go
// version: 1.0.0
// guid: 2e8b5d10-7c4a-4f93-8a61-d9f3b7c2e045
// last-edited: 2026-09-24

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// icTestDeps drives the op with a cloner that COPIES (tests run where
// reflink may not exist) but otherwise goes through the real organize
// service on the real store, so the PID transfer, the source demotion and
// the primary hand-off are the production code.
type icTestDeps struct {
	fakeDeps
	s    *database.PebbleStore
	root string
}

func (d icTestDeps) plan(book *database.Book, files []database.BookFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = filepath.Join(d.root, "Author", book.Title, filepath.Base(f.FilePath))
	}
	return out
}

func (d icTestDeps) PlanLibraryClone(book *database.Book, files []database.BookFile) ([]string, error) {
	return d.plan(book, files), nil
}

func (d icTestDeps) CloneBookIntoLibrary(book *database.Book, files []database.BookFile, opID string) (string, error) {
	dests := d.plan(book, files)
	landing := &organizer.Landing{Files: map[string]string{}}
	for i, f := range files {
		b, err := os.ReadFile(f.FilePath)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dests[i]), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dests[i], b, 0o644); err != nil {
			return "", err
		}
		landing.Files[f.FilePath] = dests[i]
		landing.Created = append(landing.Created, dests[i])
	}
	src := *book
	if len(files) == 1 {
		src.FilePath = files[0].FilePath
		landing.Path, landing.Files = dests[0], nil
	} else {
		landing.Path = filepath.Dir(dests[0])
	}
	created, err := organizer.NewService(d.s).CreateOrganizedVersion(&src, landing, opID, logger.New("test"))
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func (d icTestDeps) LibraryITunesPath(p string) string { return "W:" + p }

type icFixture struct {
	s          *database.PebbleStore
	base, root string
	deps       icTestDeps
}

func newICFixture(t *testing.T) *icFixture {
	base := t.TempDir()
	root := filepath.Join(base, "books", "audiobook-organizer")
	require.NoError(t, os.MkdirAll(root, 0o755))
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	s := newSeriesPhantomStore(t)
	return &icFixture{s: s, base: base, root: root, deps: icTestDeps{fakeDeps: fakeDeps{store: s}, s: s, root: root}}
}

// book creates a live organized primary in gid whose files are at paths
// (created on disk), each with PID "PID-<i>-<id>" when pids is true.
func (f *icFixture) book(t *testing.T, id, gid, title string, paths []string) {
	t.Helper()
	st, g, tru := "organized", gid, true
	c := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	_, err := f.s.CreateBook(&database.Book{ID: id, Title: title, VersionGroupID: &g, LibraryState: &st,
		IsPrimaryVersion: &tru, CreatedAt: &c, FilePath: paths[0]})
	require.NoError(t, err)
	for i, p := range paths {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("audio-"+filepath.Base(p)), 0o644))
		require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: id + "-f" + string(rune('0'+i)), BookID: id,
			FilePath: p, FileSize: 10, ITunesPersistentID: "PID-" + id + string(rune('0'+i))}))
	}
}

func (f *icFixture) itunes(name string) string {
	return filepath.Join(f.base, "books", "itunes", "iTunes Media", "Audiobooks", name)
}

func (f *icFixture) run(t *testing.T, params icParams) *icReport {
	t.Helper()
	p := &Plugin{deps: f.deps}
	rep, err := p.itunesCloneIntoLibrary(context.Background(), params, f.root, &opIDReporter{id: "op-ic-test"})
	require.NoError(t, err)
	return rep
}

func icGroup(t *testing.T, rep *icReport, gid string) icGroupReport {
	t.Helper()
	for _, g := range rep.Groups {
		if g.GroupID == gid {
			return g
		}
	}
	t.Fatalf("group %s not in report", gid)
	return icGroupReport{}
}

func TestITunesClone_VersionApplyAndRollback(t *testing.T) {
	f := newICFixture(t)
	src := f.itunes("Book T.m4b")
	f.book(t, "T", "vg-t", "Book T", []string{src})
	before, err := os.ReadFile(src)
	require.NoError(t, err)

	// Dry run: planned, nothing written.
	rep := f.run(t, icParams{})
	g := icGroup(t, rep, "vg-t")
	require.Equal(t, icDecisionClone, g.Decision, g.Reason+g.Error)
	require.Equal(t, icKindVersion, g.Kind)
	dest := filepath.Join(f.root, "Author", "Book T", "Book T.m4b")
	require.Equal(t, []string{dest}, g.Destinations)
	_, err = os.Stat(dest)
	require.True(t, os.IsNotExist(err), "dry run wrote a file")

	// Apply.
	rep = f.run(t, icParams{Apply: true, GroupIDs: []string{"vg-t"}})
	g = icGroup(t, rep, "vg-t")
	require.Equal(t, icOutcomeApplied, g.Outcome, g.Error)
	cloneID := g.CloneBookID
	require.NotEmpty(t, cloneID)

	owner, err := f.s.GetBookFileByPID("PID-T0")
	require.NoError(t, err)
	require.Equal(t, cloneID, owner.BookID, "the library copy owns the PID")
	require.Equal(t, dest, owner.FilePath)
	clone, err := f.s.GetBookByID(cloneID)
	require.NoError(t, err)
	require.Equal(t, "organized", *clone.LibraryState)
	require.True(t, *clone.IsPrimaryVersion, "the library copy is crowned")
	tb, err := f.s.GetBookByID("T")
	require.NoError(t, err)
	require.Equal(t, "organized_source", *tb.LibraryState)
	require.False(t, *tb.IsPrimaryVersion)
	after, err := os.ReadFile(src)
	require.NoError(t, err)
	require.Equal(t, before, after, "the iTunes file is untouched")

	// Rollback from the record.
	rep = f.run(t, icParams{Rollback: true, GroupIDs: []string{"vg-t"}})
	g = icGroup(t, rep, "vg-t")
	require.Equal(t, icOutcomeRolled, g.Outcome, g.Error)
	owner, err = f.s.GetBookFileByPID("PID-T0")
	require.NoError(t, err)
	require.Equal(t, "T", owner.BookID, "the PID is back on the source row")
	gone, err := f.s.GetBookByID(cloneID)
	require.NoError(t, err)
	require.True(t, gone == nil || gone.IsSoftDeleted(), "clone book removed")
	_, err = os.Stat(dest)
	require.True(t, os.IsNotExist(err), "clone file removed")
	tb, err = f.s.GetBookByID("T")
	require.NoError(t, err)
	require.Equal(t, "organized", *tb.LibraryState)
	require.True(t, *tb.IsPrimaryVersion)
	after, err = os.ReadFile(src)
	require.NoError(t, err)
	require.Equal(t, before, after)

	// The record is cleared: a second rollback has nothing to undo.
	rep = f.run(t, icParams{Rollback: true, GroupIDs: []string{"vg-t"}})
	require.Equal(t, "no_clone_record", icGroup(t, rep, "vg-t").Reason)
}

func TestITunesClone_MixedCompletesInPlaceAndRollsBack(t *testing.T) {
	f := newICFixture(t)
	lib := filepath.Join(f.root, "Author", "Book M", "01.mp3")
	it := f.itunes("02.mp3")
	f.book(t, "M", "vg-m", "Book M", []string{lib, it})

	rep := f.run(t, icParams{Apply: true, GroupIDs: []string{"vg-m"}})
	g := icGroup(t, rep, "vg-m")
	require.Equal(t, icKindMixed, g.Kind)
	require.Equal(t, icOutcomeApplied, g.Outcome, g.Reason+g.Error)
	dest := filepath.Join(f.root, "Author", "Book M", "02.mp3")
	rows, err := f.s.GetBookFiles("M")
	require.NoError(t, err)
	paths := map[string]string{}
	for _, r := range rows {
		paths[r.ID] = r.FilePath
	}
	require.Equal(t, dest, paths["M-f1"])
	require.Equal(t, lib, paths["M-f0"])
	_, err = os.Stat(it)
	require.NoError(t, err, "the iTunes file is still there")

	rep = f.run(t, icParams{Rollback: true, GroupIDs: []string{"vg-m"}})
	require.Equal(t, icOutcomeRolled, icGroup(t, rep, "vg-m").Outcome)
	rows, err = f.s.GetBookFiles("M")
	require.NoError(t, err)
	for _, r := range rows {
		if r.ID == "M-f1" {
			require.Equal(t, it, r.FilePath)
		}
	}
	_, err = os.Stat(dest)
	require.True(t, os.IsNotExist(err))
}

// A mixed book whose library file is not where the planner would put it is
// a conflict: reported, never written.
func TestITunesClone_MixedConflictIsReportOnly(t *testing.T) {
	f := newICFixture(t)
	lib := filepath.Join(f.root, "Elsewhere", "01.mp3")
	f.book(t, "M", "vg-m", "Book M", []string{lib, f.itunes("02.mp3")})
	rep := f.run(t, icParams{Apply: true, GroupIDs: []string{"vg-m"}})
	g := icGroup(t, rep, "vg-m")
	require.Equal(t, icDecisionSkip, g.Decision)
	require.Equal(t, "mixed_library_files_not_at_planned_path", g.Reason)
	require.Empty(t, g.Outcome)
}

func TestITunesClone_Refusals(t *testing.T) {
	f := newICFixture(t)
	// Destination already exists.
	f.book(t, "E", "vg-e", "Book E", []string{f.itunes("Book E.m4b")})
	occupied := filepath.Join(f.root, "Author", "Book E", "Book E.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(occupied), 0o755))
	require.NoError(t, os.WriteFile(occupied, []byte("someone else"), 0o644))
	// Owner-curated.
	f.book(t, "D", "vg-d", "Doctor Who - The Daleks", []string{f.itunes("Doctor Who - The Daleks.m4b")})

	rep := f.run(t, icParams{Apply: true, GroupIDs: []string{"vg-e", "vg-d"}})
	require.Equal(t, "destination_exists", icGroup(t, rep, "vg-e").Reason)
	require.Equal(t, "owner_manual_only", icGroup(t, rep, "vg-d").Reason)
	b, err := os.ReadFile(occupied)
	require.NoError(t, err)
	require.Equal(t, "someone else", string(b))

	// Apply without ids refuses.
	p := &Plugin{deps: f.deps}
	_, err = p.itunesCloneIntoLibrary(context.Background(), icParams{Apply: true}, f.root, &opIDReporter{id: "x"})
	require.ErrorContains(t, err, "group_ids")
}
