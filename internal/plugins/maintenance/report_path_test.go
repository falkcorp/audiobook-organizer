// file: internal/plugins/maintenance/report_path_test.go
// version: 1.0.0
// guid: b7314ebb-e6db-49d3-af82-37738df6214a
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
