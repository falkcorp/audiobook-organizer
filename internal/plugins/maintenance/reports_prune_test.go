// file: internal/plugins/maintenance/reports_prune_test.go
// version: 1.0.0
// guid: 0d7a6a53-9b1e-4c2f-8f35-71c2e0b94a18
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type reportsPruneQueue struct {
	ids []string
	err error
}

func (q reportsPruneQueue) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	var rows []database.OperationV2Row
	for _, id := range q.ids {
		rows = append(rows, database.OperationV2Row{ID: id, Status: "running"})
	}
	return rows, q.err
}

type reportsPruneDeps struct {
	fakeDeps
	root string
	q    OpQueueReader
}

func (d reportsPruneDeps) RootDir() string                    { return d.root }
func (d reportsPruneDeps) OperationQueueStore() OpQueueReader { return d.q }

// seedReport writes name under dir with the given age and returns its path.
func seedReport(t *testing.T, dir, name string, age time.Duration, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

const day = 24 * time.Hour

func TestReportsPrune_Retention(t *testing.T) {
	type file struct {
		name string
		age  time.Duration
	}
	cases := []struct {
		name        string
		files       []file
		live        []string
		dryRun      bool
		wantGone    []string
		wantKept    []string
		wantRemoved int
		wantBytes   int64
	}{
		{
			name: "keeps newest 3 per kind and removes older old ones",
			files: []file{
				{"backfill-file-hashes-A1.tsv", 20 * day}, {"backfill-file-hashes-A2.tsv", 30 * day},
				{"backfill-file-hashes-A3.tsv", 40 * day}, {"backfill-file-hashes-A4.tsv", 50 * day},
				{"backfill-file-hashes-A5.tsv", 60 * day},
				{"book-shape-report-B1.tsv", 90 * day},
			},
			wantGone:    []string{"backfill-file-hashes-A4.tsv", "backfill-file-hashes-A5.tsv"},
			wantKept:    []string{"backfill-file-hashes-A1.tsv", "backfill-file-hashes-A2.tsv", "backfill-file-hashes-A3.tsv", "book-shape-report-B1.tsv"},
			wantRemoved: 2, wantBytes: 10,
		},
		{
			name: "keeps young files beyond the newest 3",
			files: []file{
				{"mark-missing-files-M1.tsv", 1 * day}, {"mark-missing-files-M2.tsv", 2 * day},
				{"mark-missing-files-M3.tsv", 3 * day}, {"mark-missing-files-M4.tsv", 4 * day},
				{"mark-missing-files-M5.tsv", 30 * day},
			},
			wantGone:    []string{"mark-missing-files-M5.tsv"},
			wantKept:    []string{"mark-missing-files-M1.tsv", "mark-missing-files-M4.tsv"},
			wantRemoved: 1, wantBytes: 5,
		},
		{
			name: "skips .pending files",
			files: []file{
				{"recover-missing-files-R1.tsv", 1 * day}, {"recover-missing-files-R2.tsv", 2 * day},
				{"recover-missing-files-R3.tsv", 3 * day},
				{"recover-missing-files-R4.tsv.pending", 90 * day},
				{"recover-missing-files-R5.pending", 90 * day},
			},
			wantKept: []string{"recover-missing-files-R4.tsv.pending", "recover-missing-files-R5.pending"},
		},
		{
			name: "skips a live run's file",
			files: []file{
				{"rewrite-path-prefix-N1.tsv", 1 * day}, {"rewrite-path-prefix-N2.tsv", 2 * day},
				{"rewrite-path-prefix-N3.tsv", 3 * day}, {"rewrite-path-prefix-LIVE.tsv", 99 * day},
				{"rewrite-path-prefix-N5.tsv", 98 * day},
			},
			live:        []string{"LIVE"},
			wantGone:    []string{"rewrite-path-prefix-N5.tsv"},
			wantKept:    []string{"rewrite-path-prefix-LIVE.tsv"},
			wantRemoved: 1, wantBytes: 5,
		},
		{
			name: "ignores names outside the op family",
			files: []file{
				{"notes.tsv", 99 * day}, {"backfill-file-hashes-.tsv", 99 * day},
				{"dedupe-book-file-rows-apply.tsv", 99 * day}, {"mystery-op-X.tsv", 99 * day},
				{"backfill-file-hashes-Z.txt", 99 * day},
			},
			wantKept: []string{"notes.tsv", "backfill-file-hashes-.tsv", "dedupe-book-file-rows-apply.tsv", "mystery-op-X.tsv", "backfill-file-hashes-Z.txt"},
		},
		{
			name: "dry run removes nothing but counts",
			files: []file{
				{"missing-file-repoint-P1.tsv", 20 * day}, {"missing-file-repoint-P2.tsv", 30 * day},
				{"missing-file-repoint-P3.tsv", 40 * day}, {"missing-file-repoint-P4.tsv", 50 * day},
			},
			dryRun:      true,
			wantKept:    []string{"missing-file-repoint-P4.tsv"},
			wantRemoved: 1, wantBytes: 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".reports")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range tc.files {
				seedReport(t, dir, f.name, f.age, 5)
			}
			live := map[string]struct{}{}
			for _, id := range tc.live {
				live[id] = struct{}{}
			}
			res, err := pruneReportsDir(context.Background(), root, reportsPruneOpts{
				Keep: 3, MaxAge: 14 * day, Live: live, Now: time.Now(), DryRun: tc.dryRun,
				Logf: func(string, ...any) {},
			})
			if err != nil {
				t.Fatalf("prune: %v", err)
			}
			for _, n := range tc.wantGone {
				if exists(filepath.Join(dir, n)) {
					t.Errorf("%s should have been removed", n)
				}
			}
			for _, n := range tc.wantKept {
				if !exists(filepath.Join(dir, n)) {
					t.Errorf("%s should have been kept", n)
				}
			}
			if res.Removed != tc.wantRemoved || res.BytesFreed != tc.wantBytes {
				t.Errorf("removed=%d bytes=%d, want %d / %d", res.Removed, res.BytesFreed, tc.wantRemoved, tc.wantBytes)
			}
			if res.DryRun != tc.dryRun {
				t.Errorf("DryRun = %v", res.DryRun)
			}
		})
	}
}

func TestReportsPrune_SkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".reports")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b", "c"} {
		seedReport(t, dir, "book-shape-report-"+n+".tsv", 1*day, 1)
	}
	// An outside file that a followed symlink would delete.
	outside := seedReport(t, t.TempDir(), "victim.tsv", 99*day, 7)
	link := filepath.Join(dir, "book-shape-report-link.tsv")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	old := time.Now().Add(-99 * day)
	_ = os.Chtimes(link, old, old) // follows; harmless for the assertion
	res, err := pruneReportsDir(context.Background(), root, reportsPruneOpts{
		Keep: 3, MaxAge: 14 * day, Now: time.Now(), Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(outside) || !exists(link) {
		t.Fatal("symlink or its target was removed")
	}
	if res.SkippedSymlink != 1 || res.Removed != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestReportsPrune_RefusesBadRootAndSymlinkedDir(t *testing.T) {
	o := reportsPruneOpts{Keep: 3, MaxAge: 14 * day, Now: time.Now(), Logf: func(string, ...any) {}}
	for _, root := range []string{"", "relative/dir"} {
		if _, err := pruneReportsDir(context.Background(), root, o); err == nil {
			t.Errorf("root %q: want refusal", root)
		}
	}
	root := t.TempDir()
	target := t.TempDir()
	victim := seedReport(t, target, "book-shape-report-x.tsv", 99*day, 1)
	if err := os.Symlink(target, filepath.Join(root, ".reports")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := pruneReportsDir(context.Background(), root, o); err == nil {
		t.Error("symlinked .reports: want refusal")
	}
	if !exists(victim) {
		t.Error("file behind a symlinked .reports was removed")
	}
	// A missing .reports is not an error.
	if res, err := pruneReportsDir(context.Background(), t.TempDir(), o); err != nil || res.Removed != 0 {
		t.Errorf("missing dir: %+v, %v", res, err)
	}
}

func seedOpFixture(t *testing.T) (root, doomed string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, ".reports")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, n := range []string{"1", "2", "3"} {
		seedReport(t, dir, "merge-same-path-dupes-"+n+".tsv", time.Duration(i+20)*day, 1)
	}
	doomed = seedReport(t, dir, "merge-same-path-dupes-4.tsv", 60*day, 4)
	return root, doomed
}

func TestReportsPrune_OpModes(t *testing.T) {
	cases := []struct {
		name     string
		params   string
		wantGone bool
		wantErr  bool
	}{
		{"omitted is a preview", ``, false, false},
		{"empty object is a preview", `{}`, false, false},
		{"dry_run true", `{"dry_run":true}`, false, false},
		{"dryRun true", `{"dryRun":true}`, false, false},
		{"dry_run false is live", `{"dry_run":false}`, true, false},
		{"dryRun false is live", `{"dryRun":false}`, true, false},
		{"disagreeing spellings refuse and preview", `{"dry_run":false,"dryRun":true}`, false, true},
		{"keep_per_kind below 1 refused", `{"dry_run":false,"keep_per_kind":0}`, false, true},
		{"max_age_days below 1 refused", `{"dry_run":false,"max_age_days":0}`, false, true},
		{"keep_per_kind 5 retains it", `{"dry_run":false,"keep_per_kind":5}`, false, false},
		{"max_age_days 90 retains it", `{"dryRun":false,"max_age_days":90}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, doomed := seedOpFixture(t)
			p := New(reportsPruneDeps{root: root, q: reportsPruneQueue{}})
			rep := &resultReporter{}
			err := p.runReportsPrune(context.Background(), json.RawMessage(tc.params), rep)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if gone := !exists(doomed); gone != tc.wantGone {
				t.Errorf("doomed removed = %v, want %v", gone, tc.wantGone)
			}
			if tc.wantGone {
				res, ok := rep.result.(ReportsPruneResult)
				if !ok || res.Removed != 1 || res.BytesFreed != 4 || res.DryRun {
					t.Errorf("result = %#v", rep.result)
				}
			}
		})
	}
}

func TestReportsPrune_LiveRunAndQueueFailures(t *testing.T) {
	root, doomed := seedOpFixture(t)
	live := New(reportsPruneDeps{root: root, q: reportsPruneQueue{ids: []string{"4"}}})
	if err := live.runReportsPrune(context.Background(), json.RawMessage(`{"dry_run":false}`), &resultReporter{}); err != nil {
		t.Fatal(err)
	}
	if !exists(doomed) {
		t.Error("a live run's report was removed")
	}
	for name, d := range map[string]reportsPruneDeps{
		"nil queue":   {root: root},
		"queue error": {root: root, q: reportsPruneQueue{err: os.ErrInvalid}},
	} {
		if err := New(d).runReportsPrune(context.Background(), json.RawMessage(`{"dry_run":false}`), &resultReporter{}); err == nil {
			t.Errorf("%s: want refusal", name)
		}
		if !exists(doomed) {
			t.Errorf("%s: file removed despite refusal", name)
		}
	}
}

func TestReportsPrune_RegisteredWithPreview(t *testing.T) {
	def := New(reportsPruneDeps{}).reportsPruneDef()
	if def.ID != "maintenance.reports-prune" || def.Preview.Params == nil {
		t.Fatalf("def = %+v", def)
	}
}
