// file: internal/books/cutover/m6/gates_test.go
// version: 1.0.0
// guid: d8a24c71-5e30-4b96-a1f7-0c93e6b2d845
// last-edited: 2026-11-19

package m6_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
)

// treeHash is a digest of every file under dir (names and bytes), the way a
// search index on disk is compared before and after a boot.
func treeHash(t testing.TB, fsys vfs.FS, dir string) string {
	t.Helper()
	h := sha256.New()
	var walk func(p string)
	walk = func(p string) {
		names, err := fsys.List(p)
		if err != nil {
			t.Fatalf("list %s: %v", p, err)
		}
		slices.Sort(names)
		for _, n := range names {
			full := fsys.PathJoin(p, n)
			fi, err := fsys.Stat(full)
			if err != nil {
				t.Fatalf("stat %s: %v", full, err)
			}
			h.Write([]byte(full + "\x00"))
			if fi.IsDir() {
				walk(full)
				continue
			}
			f, err := fsys.Open(full)
			if err != nil {
				t.Fatalf("open %s: %v", full, err)
			}
			b, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				t.Fatalf("read %s: %v", full, err)
			}
			h.Write(b)
		}
	}
	walk(dir)
	return hex.EncodeToString(h.Sum(nil))
}

// TestBooksIndexRebuiltAtCutover is M6's half of the spec test (05-search): the
// dark half. The native dirty set replaces books' idx:sidx:dirty: rows, so M6
// drops them, and it leaves the search index on disk (library.bleve) as it was:
// the rebuild is the index module's work at its own cutover, never M6's. The
// corpus's index is the one R0 wrote.
func TestBooksIndexRebuiltAtCutover(t *testing.T) {
	r := newRig(t, fromCorpus("stage1"), afterM2, withRows(map[string][]byte{
		"idx:sidx:dirty:01JYEARONE": []byte("1"),
		"idx:sidx:dirty:01JHUSH":    []byte("1"),
		"idx:sidx:book:01JHUSH":     []byte("kept"), // the index's other rows are not M6's
	}))
	fsys := r.seed(t)
	indexBefore := treeHash(t, fsys, rigDir+"/library.bleve")
	before := r.before(t, fsys)
	if _, ok := before["idx:sidx:dirty:01JHUSH"]; !ok {
		t.Fatal("the dirty rows were not seeded")
	}
	s, err := r.runOn(t, fsys)
	if err != nil {
		t.Fatalf("M6: %v\nlogs:\n%s", err, r.logs.String())
	}
	for k := range s.Legacy {
		if strings.HasPrefix(k, m6.PrefixDirty) {
			t.Errorf("the dirty row %q survived M6", k)
		}
	}
	if string(s.Legacy["idx:sidx:book:01JHUSH"]) != "kept" {
		t.Error("a non-dirty index row was touched")
	}
	if got := treeHash(t, fsys, rigDir+"/library.bleve"); got != indexBefore {
		t.Error("M6 changed library.bleve on disk")
	}
}

// TestConfigCutover_RowsGone is 05-config 1.12.2's after-M6 clause (F3-06 owns
// the part that runs after M2): no setting:config_blob remains, and every other
// range of books' store is byte-identical to what the build before left. The
// bystanders here are the corpus's own rows, not a hand-made few.
func TestConfigCutover_RowsGone(t *testing.T) {
	for _, name := range []string{"stage1", "overlay"} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, fromCorpus(name), afterM2)
			fsys := r.seed(t)
			before := r.before(t, fsys)
			if _, ok := before[m6.KeyBlob]; !ok {
				t.Fatal("the corpus has no settings blob")
			}
			s, err := r.runOn(t, fsys)
			if err != nil {
				t.Fatalf("M6: %v\nlogs:\n%s", err, r.logs.String())
			}
			if _, ok := s.Legacy[m6.KeyBlob]; ok {
				t.Error("setting:config_blob remains after M6")
			}
			identicalOutsideM6(t, before, s.Legacy)
			// Everything outside the drained families is still there.
			for k := range before {
				if _, ok := s.Legacy[k]; !ok && !isDrained(k) {
					t.Errorf("row %q was removed and is in no drained family", k)
				}
			}
		})
	}
}

// TestStep_DryRunWritesNothing is 05-metadata M1.10's dry run: the plan is
// reported per kind, nothing is written (no native row, no quarantine record, no
// legacy row removed), and the step is not recorded done, so a real boot runs it.
func TestStep_DryRunWritesNothing(t *testing.T) {
	live := newRig(t, withRows(headRows(t)))
	if _, err := live.run(t); err != nil {
		t.Fatalf("the real run: %v", err)
	}

	dry := newRig(t, withRows(headRows(t)), func(r *rig) { r.dryRun = true })
	fsys := dry.seed(t)
	before := dry.before(t, fsys)
	s, err := dry.runOn(t, fsys)
	if err == nil {
		t.Fatal("a dry run was recorded as a finished step")
	}
	if s == nil {
		t.Fatalf("no snapshot after the dry run: %v", err)
	}
	if s.State == "done" {
		t.Errorf("state %q after a dry run", s.State)
	}
	for owner, rows := range s.Rows {
		if len(rows) != 0 {
			t.Errorf("a dry run wrote %d rows into %s", len(rows), owner)
		}
	}
	if len(s.Quarantine) != 0 {
		t.Errorf("a dry run quarantined %d rows", len(s.Quarantine))
	}
	for k, v := range before {
		if got, ok := s.Legacy[k]; !ok && !strings.HasPrefix(k, "preference:") {
			t.Errorf("a dry run removed %q", k)
		} else if ok && string(got) != string(v) && !strings.HasPrefix(k, "preference:") && k != "counter:preference" {
			t.Errorf("a dry run changed %q", k)
		}
	}
	// The plan is the real run's plan, per kind and by ID.
	line := func(e m6.Entry) string { return e.Kind + "|" + e.ID + "|" + e.Detail }
	var want, got []string
	for _, e := range live.report.Entries() {
		want = append(want, line(e))
	}
	for _, e := range dry.report.Entries() {
		got = append(got, line(e))
	}
	slices.Sort(want)
	slices.Sort(got)
	if d := diff(got, want); d != "" {
		t.Errorf("the dry run's report differs from the real run's:\n%s", d)
	}
	if len(got) == 0 {
		t.Error("the dry run reported nothing")
	}
}

// TestStep_SecondBootIsANoOp is I5: once recorded done the step does not run
// again, and a boot over the finished store changes no row.
func TestStep_SecondBootIsANoOp(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	fsys := r.seed(t)
	first, err := r.runOn(t, fsys)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	r.report = &m6.Report{}
	second, err := r.runOn(t, fsys)
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}
	if d := diff(flatten(second), flatten(first)); d != "" {
		t.Errorf("the second boot changed the store:\n%s", d)
	}
	if n := len(r.report.Entries()); n != 0 {
		t.Errorf("the second boot ran the step again (%d report entries)", n)
	}
}
