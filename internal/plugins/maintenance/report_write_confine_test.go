// file: internal/plugins/maintenance/report_write_confine_test.go
// version: 1.0.1
// guid: 593d32e1-8cd4-406f-8acf-a3afb9fa890b
// last-edited: 2026-11-18

package maintenance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// reportWriters is every function that writes a maintenance report file, by the
// path an op resolved earlier: the twelve writers and the merge preflight. A new
// report writer belongs here, and must go through library.WriteReportFile (or
// OpenReportFile / PreflightReportFile), never os.WriteFile or os.Create, which
// follow a symlink planted at the name after the path was approved.
func reportWriters() []struct {
	name  string
	write func(path string) error
} {
	return []struct {
		name  string
		write func(path string) error
	}{
		{"book-shape-report", func(p string) error { return writeBookShapeReport(p, nil) }},
		{"dedupe-book-file-rows", func(p string) error { return writeDupeRowsReport(p, nil) }},
		{"dedupe-book-file-rows decisions", func(p string) error { return writeRowDecisionsReport(p, nil) }},
		{"mark-missing-files", func(p string) error { return writeMarkMissingReport(p, nil) }},
		{"merge-same-path-dupes", func(p string) error { return writeMergeReport(p, nil) }},
		{"merge-same-path-dupes preflight", preflightReportPath},
		{"metadata-cache-reap", func(p string) error { return writeReapReport(p, nil) }},
		{"missing-file-repoint", func(p string) error { return writeRepointReport(p, nil) }},
		{"recover-missing-files", func(p string) error { return writeRecoverReport(p, nil) }},
		{"repoint-missing-to-folder-audio", func(p string) error { return writeRFReport(p, nil) }},
		{"rewrite-path-prefix", func(p string) error { return writeRewritePathPrefixReport(p, nil) }},
		{"series-denumber", func(p string) error { return writeSeriesDenumberReport(p, nil, nil) }},
		{"series-denumber outcome (.applied)", func(p string) error { return writeReportRows(p, nil) }},
		{"series-phantom-repair", func(p string) error { return writeSeriesPhantomReport(p, nil) }},
	}
}

// Every report writer refuses a symlink or a hard link planted at the report's
// name, writes nothing through it, and leaves the file it points at as it was;
// and writes a report to a plain name. The plant happens after the path was
// resolved, which is the window the resolver cannot close.
func TestReportWriters_RefuseALinkPlantedAtTheReportName(t *testing.T) {
	for _, w := range reportWriters() {
		t.Run(w.name, func(t *testing.T) {
			reports := filepath.Join(t.TempDir(), ".reports")
			require.NoError(t, os.Mkdir(reports, 0o775))
			victim := filepath.Join(t.TempDir(), "victim.tsv")
			require.NoError(t, os.WriteFile(victim, []byte("keep me"), 0o644))

			ok := filepath.Join(reports, "ok.tsv")
			require.NoError(t, w.write(ok), "a plain report name must be written")

			sym := filepath.Join(reports, "sym.tsv")
			require.NoError(t, os.Symlink(victim, sym))
			require.Error(t, w.write(sym), "a symlink at the report name must be refused")

			hard := filepath.Join(reports, "hard.tsv")
			if err := os.Link(victim, hard); err != nil {
				t.Skipf("hard links are not available here: %v", err)
			}
			require.Error(t, w.write(hard), "a hard link at the report name must be refused")

			b, err := os.ReadFile(victim)
			require.NoError(t, err)
			require.Equal(t, "keep me", string(b), "a writer wrote through a planted link")
		})
	}
}

// A writer does not create directories: the report directory is made and
// checked by the resolver, and a path in a directory that is not there is an
// error, not a MkdirAll.
func TestReportWriters_DoNotCreateDirectories(t *testing.T) {
	for _, w := range reportWriters() {
		t.Run(w.name, func(t *testing.T) {
			root := t.TempDir()
			require.Error(t, w.write(filepath.Join(root, "nested", "dirs", "r.tsv")))
			_, err := os.Stat(filepath.Join(root, "nested"))
			require.True(t, os.IsNotExist(err), "a writer created a directory")
		})
	}
}

// Every op refuses, before it touches the store, a report that already exists as
// a link the writer would refuse: the resolver vets the existing file, so the
// operator is told up front rather than after an apply.
func TestReportOps_RefuseAPlantedReportBeforeTouchingTheStore(t *testing.T) {
	for _, c := range reportOpCases() {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			reports := filepath.Join(root, ".reports")
			require.NoError(t, os.Mkdir(reports, 0o775))
			victim := filepath.Join(t.TempDir(), "victim.tsv")
			require.NoError(t, os.WriteFile(victim, []byte("keep me"), 0o644))
			require.NoError(t, os.Symlink(victim, filepath.Join(reports, "planted-sym.tsv")))
			names := []string{"planted-sym.tsv"}
			if err := os.Link(victim, filepath.Join(reports, "planted-hard.tsv")); err == nil {
				names = append(names, "planted-hard.tsv")
			}
			for _, name := range names {
				p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: noTouchStore{}}, root: root}}
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("%s reached the store with a planted report %s: %v", c.name, name, r)
						}
					}()
					err := c.run(p, t.Context(), c.raw(t, true, map[string]any{c.key(): name}), &fakeReporter{})
					require.Error(t, err, "%s: a planted report %s must be refused", c.name, name)
				}()
			}
			b, err := os.ReadFile(victim)
			require.NoError(t, err)
			require.Equal(t, "keep me", string(b))
		})
	}
}

// dedupe-book-file-rows writes a second file, "<report>-decisions.tsv", beside the
// report. A link planted at THAT name is refused before any work, like the report
// itself, not found after an apply.
func TestDedupeBookFileRows_RefusesAPlantedDecisionsFileBeforeTouchingTheStore(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			reports := filepath.Join(root, ".reports")
			require.NoError(t, os.Mkdir(reports, 0o775))
			victim := filepath.Join(t.TempDir(), "victim.tsv")
			require.NoError(t, os.WriteFile(victim, []byte("keep me"), 0o644))
			planted := filepath.Join(reports, "dedupe-book-file-rows-apply-decisions.tsv")
			if kind == "symlink" {
				require.NoError(t, os.Symlink(victim, planted))
			} else if err := os.Link(victim, planted); err != nil {
				t.Skipf("hard links are not available here: %v", err)
			}
			p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: noTouchStore{}}, root: root}}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("the op reached the store with a planted decisions file: %v", r)
					}
				}()
				err := p.runDedupeBookFileRows(t.Context(), []byte(`{"apply":true}`), &fakeReporter{})
				require.Error(t, err)
				require.Contains(t, err.Error(), "decisions")
			}()
			b, err := os.ReadFile(victim)
			require.NoError(t, err)
			require.Equal(t, "keep me", string(b))
		})
	}
}
