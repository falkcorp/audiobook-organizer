// file: internal/plugins/maintenance/report_path_test.go
// version: 1.3.1
// guid: b7314ebb-e6db-49d3-af82-37738df6214a
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/stretchr/testify/require"
)

// With no explicit reportPath the report must land under the library root's
// app-owned .reports directory -- never a path relative to the process working
// directory, which is the package source dir under `go test` and the secure
// state dir in production.
func TestResolveReportPath_DefaultsUnderRootReports(t *testing.T) {
	root := t.TempDir()
	p := &Plugin{deps: rootDirDeps{root: root}}

	got, err := p.resolveReportPath("", "some-op-x.tsv")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, ".reports", "some-op-x.tsv"), got)
	require.True(t, filepath.IsAbs(got), "the default report path must be absolute")

	explicit := filepath.Join(root, ".reports", "chosen.tsv")
	got, err = p.resolveReportPath(explicit, "some-op-x.tsv")
	require.NoError(t, err)
	require.Equal(t, explicit, got, "an explicit reportPath inside the report directory is used verbatim")

	got, err = p.resolveReportPath("sub/chosen.tsv", "some-op-x.tsv")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, ".reports", "sub", "chosen.tsv"), got, "a relative reportPath is relative to the report directory")
}

// outsideReportPaths builds the caller-supplied report paths a library root must
// refuse: each would, unchecked, make an op write somewhere other than
// {root}/.reports. outside is a directory elsewhere that must stay empty.
func outsideReportPaths(t *testing.T, root string) (cases map[string]string, outside string) {
	t.Helper()
	outside = t.TempDir()
	reports := filepath.Join(root, ".reports")
	require.NoError(t, os.MkdirAll(reports, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(reports, "linked")))
	return map[string]string{
		"dot-dot traversal":            filepath.Join("..", "..", "escaped.tsv"),
		"absolute path outside":        filepath.Join(outside, "escaped.tsv"),
		"absolute path in the root":    filepath.Join(root, "escaped.tsv"),
		"symlinked parent escaping":    filepath.Join(reports, "linked", "escaped.tsv"),
		"symlinked parent, relative":   filepath.Join("linked", "nested", "escaped.tsv"),
		"traversal past a nested dir":  filepath.Join(reports, "a", "..", "..", "escaped.tsv"),
		"report directory as the file": reports,
	}, outside
}

// An explicit report path is confined to {RootDir}/.reports: traversal, an
// absolute path outside it, and a symlinked parent that leads out of it are all
// refused, and nothing is created at the place they pointed.
func TestResolveReportPath_ExplicitPathCannotLeaveTheReportDir(t *testing.T) {
	root := t.TempDir()
	p := &Plugin{deps: rootDirDeps{root: root}}
	cases, outside := outsideReportPaths(t, root)
	for name, explicit := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := p.resolveReportPath(explicit, "some-op-x.tsv")
			require.Error(t, err)
			require.Empty(t, got)
		})
	}
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries, "a refused path must not create anything where it pointed")
	_, statErr := os.Stat(filepath.Join(root, "escaped.tsv"))
	require.True(t, os.IsNotExist(statErr))
}

// With no root_dir there is no report directory to keep an explicit path inside,
// so it is refused instead of trusted.
func TestResolveReportPath_ExplicitPathNeedsARoot(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "chosen.tsv")
	for _, root := range []string{"", "relative/library"} {
		p := &Plugin{deps: rootDirDeps{root: root}}
		got, err := p.resolveReportPath(explicit, "some-op-x.tsv")
		require.Error(t, err, "root %q", root)
		require.Empty(t, got, "root %q", root)
	}
}

// An unset or relative root_dir has no app-owned default; resolving must fail
// instead of falling back to a cwd-relative path.
func TestResolveReportPath_ErrorsWithoutAbsoluteRoot(t *testing.T) {
	for _, root := range []string{"", "   ", "relative/library"} {
		p := &Plugin{deps: rootDirDeps{root: root}}
		got, err := p.resolveReportPath("", "some-op-x.tsv")
		require.Error(t, err, "root %q", root)
		require.Empty(t, got, "root %q", root)
	}
}

// End to end through a real op: default params write the report under
// {RootDir}/.reports, and nothing is written to the package directory.
func TestMetadataCacheReap_DefaultReportLandsUnderRoot(t *testing.T) {
	var deleted []string
	store := reapMockStore([]string{"b1", "b2"}, map[string]bool{"b1": true}, &deleted)
	root := t.TempDir()
	p := New(rootDirDeps{store: store, root: root})

	require.NoError(t, p.runMetadataCacheReap(context.Background(), nil, &fakeReporter{}))

	raw, err := os.ReadFile(filepath.Join(root, ".reports", "metadata-cache-reap-unknown-op.tsv"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "orphaned\tb2")

	_, statErr := os.Stat("reports")
	require.True(t, os.IsNotExist(statErr), "the op must not create a cwd-relative reports/ directory")
}

// An apply with no reportPath and no root_dir must fail before doing any work:
// a destructive run whose only complete record has nowhere to go is refused,
// not performed.
func TestMetadataCacheReap_NoRootAndNoReportPathRefusesBeforeWork(t *testing.T) {
	var deleted []string
	store := reapMockStore([]string{"b1", "b2"}, map[string]bool{"b1": true}, &deleted)
	p := New(rootDirDeps{store: store, root: ""})

	err := p.runMetadataCacheReap(context.Background(), []byte(`{"apply":true}`), &fakeReporter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no report path")
	require.Empty(t, deleted, "nothing may be deleted when the report has nowhere to go")
}

// noTouchStore panics on ANY store call: its embedded interface is nil. An op
// that refuses up front never reaches the store, so a run against it proves
// "nothing written or deleted" by never reaching the store at all, which is
// stronger than counting mutations.
type noTouchStore struct{ database.Store }

type reportOpCase struct {
	name string
	run  func(*Plugin, context.Context, json.RawMessage, sdk.Reporter) error
	file string // expected default report name with a reporter that has no op ID
}

// reportOpCases lists every maintenance op that writes a per-row report with a
// default location. A new report-writing op belongs here.
func reportOpCases() []reportOpCase {
	return []reportOpCase{
		{"metadata-cache-reap", (*Plugin).runMetadataCacheReap, "metadata-cache-reap-unknown-op.tsv"},
		{"merge-same-path-dupes", (*Plugin).runMergeSamePathDupes, "merge-same-path-dupes-unknown-op.tsv"},
		{"missing-file-repoint", (*Plugin).runMissingFileRepoint, "missing-file-repoint-unknown-op.tsv"},
		{"mark-missing-files", (*Plugin).runMarkMissingFiles, "mark-missing-files-unknown-op.tsv"},
		{"recover-missing-files", (*Plugin).runRecoverMissingFiles, "recover-missing-files-unknown-op.tsv"},
		{"dedupe-book-file-rows", (*Plugin).runDedupeBookFileRows, "dedupe-book-file-rows-dryrun.tsv"},
		// book-shape-report has no apply mode at all, so the apply-refusal case
		// below is passing it a parameter it ignores -- what it proves for this op
		// is that an unusable report directory is refused BEFORE the store is read.
		{"book-shape-report", (*Plugin).runBookShapeReport, "book-shape-report-unknown-op.tsv"},
	}
}

// runApplyAgainstNoTouchStore runs c as an APPLY with root as the library root
// and fails the test if the op reaches the store.
func runApplyAgainstNoTouchStore(t *testing.T, c reportOpCase, root string) error {
	t.Helper()
	p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: noTouchStore{}}, root: root}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s reached the store before refusing, so an apply would have run: %v", c.name, r)
		}
	}()
	return c.run(p, context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{})
}

// Every report-writing op refuses an apply, before touching the store, when its
// report has nowhere usable to go: no root_dir, or a .reports that cannot be
// created or written (a FILE sits at that name, or the directory is read-only).
func TestReportOps_RefuseApplyWithoutAUsableReportDir(t *testing.T) {
	for _, c := range reportOpCases() {
		t.Run(c.name+"/no-root", func(t *testing.T) {
			err := runApplyAgainstNoTouchStore(t, c, "")
			require.Error(t, err)
			require.Contains(t, err.Error(), "report path")
		})
		t.Run(c.name+"/reports-is-a-file", func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, ".reports"), []byte("squatter"), 0o644))
			err := runApplyAgainstNoTouchStore(t, c, root)
			require.Error(t, err)
			require.Contains(t, err.Error(), "report path")
			require.Contains(t, err.Error(), "not writable")
		})
		t.Run(c.name+"/reports-read-only", func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root ignores directory permissions")
			}
			root := t.TempDir()
			dir := filepath.Join(root, ".reports")
			require.NoError(t, os.Mkdir(dir, 0o555))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			err := runApplyAgainstNoTouchStore(t, c, root)
			require.Error(t, err)
			require.Contains(t, err.Error(), "not writable")
		})
	}
}

// With an absolute root and default params, every report-writing op leaves its
// report at {root}/.reports/<name> and nothing in the package directory.
func TestReportOps_DefaultReportLandsUnderRoot(t *testing.T) {
	s, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	defer s.Close()
	s.WaitForWarmup()

	for _, c := range reportOpCases() {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: s}, root: root}}
			require.NoError(t, c.run(p, context.Background(), nil, &fakeReporter{}))
			_, statErr := os.Stat(filepath.Join(root, ".reports", c.file))
			require.NoError(t, statErr, "the report must be at {root}/.reports/%s", c.file)
		})
	}
	_, statErr := os.Stat("reports")
	require.True(t, os.IsNotExist(statErr), "no op may create a cwd-relative reports/ directory")
}

// Every report-writing op refuses a reportPath that would leave
// {root}/.reports, before touching the store and before writing anything: this
// is what stops a caller with the op's permission from pointing its report at an
// arbitrary file the server can write.
func TestReportOps_RefuseAReportPathOutsideTheReportDir(t *testing.T) {
	for _, c := range reportOpCases() {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			cases, outside := outsideReportPaths(t, root)
			for name, explicit := range cases {
				p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: noTouchStore{}}, root: root}}
				raw, err := json.Marshal(map[string]any{"apply": true, "reportPath": explicit})
				require.NoError(t, err)
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("%s reached the store with reportPath %s (%q): %v", c.name, name, explicit, r)
						}
					}()
					runErr := c.run(p, context.Background(), raw, &fakeReporter{})
					require.Error(t, runErr, "%s: reportPath %s (%q) must be refused", c.name, name, explicit)
				}()
			}
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			require.Empty(t, entries, "%s wrote outside the report directory", c.name)
		})
	}
}

// A valid explicit reportPath inside the report directory is used: the report is
// written exactly there.
func TestReportOps_ExplicitReportPathInsideTheReportDirIsUsed(t *testing.T) {
	s, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	defer s.Close()
	s.WaitForWarmup()

	for _, c := range reportOpCases() {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: s}, root: root}}
			raw, err := json.Marshal(map[string]any{"reportPath": filepath.Join("mine", "chosen.tsv")})
			require.NoError(t, err)
			require.NoError(t, c.run(p, context.Background(), raw, &fakeReporter{}))
			_, statErr := os.Stat(filepath.Join(root, ".reports", "mine", "chosen.tsv"))
			require.NoError(t, statErr, "%s must write its report at the path it was given", c.name)
		})
	}
}

// The two ops that write the caller's report path themselves, rather than through
// the shared resolver's callers, are confined the same way: series-phantom-repair
// refuses before reading the library, and nothing lands where the path pointed.
func TestSeriesPhantomRepair_RefusesAReportPathOutsideTheReportDir(t *testing.T) {
	root := t.TempDir()
	cases, outside := outsideReportPaths(t, root)
	for name, explicit := range cases {
		t.Run(name, func(t *testing.T) {
			p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: noTouchStore{}}, root: root}}
			res, err := p.seriesPhantomRepair(context.Background(), seriesPhantomRepairParams{ReportPath: explicit}, &fakeReporter{})
			require.Error(t, err)
			require.Nil(t, res)
		})
	}
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// series-denumber's apply (which requires a reportPath, and writes
// ReportPath + ".applied" beside it) refuses a path outside the report directory
// before it plans or writes anything, and the ".applied" sibling is held to the
// same rule.
func TestSeriesDenumber_RefusesAReportPathOutsideTheReportDir(t *testing.T) {
	l := newDenumberLib(t)
	oneOwlsBook(l)
	cases, outside := outsideReportPaths(t, l.root)
	for name, explicit := range cases {
		_, err := l.run(fmt.Sprintf(`{"apply": true, "reportPath": %q}`, explicit))
		require.Error(t, err, "%s (%q)", name, explicit)
		l.assertNoWrites()
	}
	require.Zero(t, l.bulkReads, "a refused report path must be refused before the library is read")

	// The report file is fine but its ".applied" sibling is a symlink out.
	reports := filepath.Join(l.root, ".reports")
	require.NoError(t, os.Symlink(filepath.Join(outside, "victim"), filepath.Join(reports, "r.tsv.applied")))
	_, err := l.run(fmt.Sprintf(`{"apply": true, "reportPath": %q}`, filepath.Join(reports, "r.tsv")))
	require.Error(t, err)
	l.assertNoWrites()

	entries, rerr := os.ReadDir(outside)
	require.NoError(t, rerr)
	require.Empty(t, entries)
}
