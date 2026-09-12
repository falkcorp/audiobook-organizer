// file: internal/plugins/maintenance/report_path_test.go
// version: 1.1.0
// guid: b7314ebb-e6db-49d3-af82-37738df6214a
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

	explicit := filepath.Join(t.TempDir(), "chosen.tsv")
	got, err = p.resolveReportPath(explicit, "some-op-x.tsv")
	require.NoError(t, err)
	require.Equal(t, explicit, got, "an explicit reportPath is used verbatim")
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
	p := New(rootDirDeps{fakeDeps: fakeDeps{store: store}, root: root})

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
	p := New(rootDirDeps{fakeDeps: fakeDeps{store: store}, root: ""})

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
	s, err := database.NewPebbleStore(t.TempDir())
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
