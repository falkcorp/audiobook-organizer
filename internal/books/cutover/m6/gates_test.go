// file: internal/books/cutover/m6/gates_test.go
// version: 1.0.2
// guid: d8a24c71-5e30-4b96-a1f7-0c93e6b2d845
// last-edited: 2026-11-19

package m6_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/mediacore/storage"
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

// reportLines is a report as sorted "kind|id|detail" lines.
func reportLines(rep *m6.Report) []string {
	var out []string
	for _, e := range rep.Entries() {
		out = append(out, e.Kind+"|"+e.ID+"|"+e.Detail)
	}
	slices.Sort(out)
	return out
}

// TestPlan_ChangesNothing is 05-metadata M1.10's dry run: Plan reports what the
// step would do over a store and writes nothing anywhere: no native row, no
// quarantine record, no legacy row removed, and nothing in core's own
// bookkeeping (a plan, a level stamp, a cursor or a state record is what the
// runner would leave behind, and what made a runner-driven "dry run" unsafe).
func TestPlan_ChangesNothing(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	fsys := r.seed(t)
	var rep *m6.Report
	err := r.withStore(t, fsys, func(ctx context.Context, db *storage.DB) error {
		before := r.dump(ctx, t, db)
		lr, _ := db.Legacy(cutover.BooksOwner)
		var err error
		if rep, err = m6.Plan(ctx, lr, r.config()); err != nil {
			return err
		}
		after := r.dump(ctx, t, db)
		for ks, rows := range before {
			if d := diff(sortedKeys(after[ks]), sortedKeys(rows)); d != "" {
				t.Errorf("Plan changed the keys of %s:\n%s", ks, d)
			}
			for k, v := range rows {
				if string(after[ks][k]) != string(v) {
					t.Errorf("Plan changed %s %q", ks, k)
				}
			}
		}
		if len(before["legacy"]) == 0 {
			t.Error("the legacy range is empty: the test proves nothing")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := reportLines(rep); len(got) == 0 {
		t.Error("Plan reported nothing")
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestPlan_IsTheRealRunsReport: on a store whose native side is empty, which is
// every store M6 runs against at cutover, Plan's report is the real run's, entry
// for entry, so the operator who reads the preview reads what will happen.
func TestPlan_IsTheRealRunsReport(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	fsys := r.seed(t)
	live := newRig(t, withRows(headRows(t)))
	if _, err := live.runOn(t, crashClone(fsys)); err != nil {
		t.Fatalf("the real run: %v", err)
	}

	var rep *m6.Report
	if err := r.withStore(t, fsys, func(ctx context.Context, db *storage.DB) error {
		lr, _ := db.Legacy(cutover.BooksOwner)
		var err error
		rep, err = m6.Plan(ctx, lr, r.config())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want, got := reportLines(live.report), reportLines(rep)
	if len(got) == 0 {
		t.Fatal("Plan reported nothing")
	}
	if d := diff(got, want); d != "" {
		t.Errorf("Plan's report differs from the real run's:\n%s", d)
	}
}

// TestPlan_ReportsAQuarantinedPulledForwardRowOnce: the config group quarantines
// every torn import_path row when it reads the whole group, and the walk then
// reaches the next such key. The runner skips it (it is in the shard's written
// set); Plan must too, or the group runs again over rows that are still there and
// the plan reports each quarantine twice, unlike the real run.
func TestPlan_ReportsAQuarantinedPulledForwardRowOnce(t *testing.T) {
	rows := map[string][]byte{"import_path:7": []byte(`{"id":7,"path":`), "import_path:8": []byte(`{"id":8,"path":`)}
	r := newRig(t, withRows(rows))
	fsys := r.seed(t)
	live := newRig(t, withRows(rows))
	if _, err := live.runOn(t, crashClone(fsys)); err != nil {
		t.Fatalf("the real run: %v", err)
	}
	var rep *m6.Report
	if err := r.withStore(t, fsys, func(ctx context.Context, db *storage.DB) error {
		lr, _ := db.Legacy(cutover.BooksOwner)
		var err error
		rep, err = m6.Plan(ctx, lr, r.config())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for key := range rows {
		count := func(rp *m6.Report) (n int) {
			for _, e := range rp.Entries() {
				if e.Kind == "quarantine" && e.ID == key {
					n++
				}
			}
			return n
		}
		if got, want := count(rep), count(live.report); got != 1 || want != 1 {
			t.Errorf("quarantine entries for %s: plan %d, real run %d, want 1 each", key, got, want)
		}
	}
}

// TestPlan_ThenARealRunCompletes is the regression the runner-driven dry run
// failed: a boot that plans and a boot that runs, on the same store. The first
// leaves the store as it found it, so the second steps every row and records the
// step done, and a Plan over the finished store finds nothing to do.
func TestPlan_ThenARealRunCompletes(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	fsys := r.seed(t)
	plan := func() []string {
		var rep *m6.Report
		if err := r.withStore(t, fsys, func(ctx context.Context, db *storage.DB) error {
			lr, _ := db.Legacy(cutover.BooksOwner)
			var err error
			rep, err = m6.Plan(ctx, lr, r.config())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return reportLines(rep)
	}
	if len(plan()) == 0 {
		t.Fatal("Plan reported nothing")
	}
	s, err := r.runOn(t, fsys)
	if err != nil {
		t.Fatalf("the real run after a plan: %v", err)
	}
	if s.State != "done" {
		t.Errorf("state %q after the real run", s.State)
	}
	if len(s.Verify.Failures) != 0 {
		t.Errorf("verify failed after the real run: %v", s.Verify.Failures)
	}
	if len(s.Rows[string(ownerInteg)]) == 0 {
		t.Error("the real run after a plan imported no instance")
	}
	for k := range s.Legacy {
		for _, rg := range m6.SourceRanges() {
			if k >= string(rg.Start) && k < string(rg.End) {
				t.Errorf("a source row %q is still present after the real run", k)
			}
		}
	}
	if got := plan(); len(got) != 0 {
		t.Errorf("Plan over a finished store reported %d entries: %v", len(got), got)
	}
}

// TestPlan_BootsTheSameStoreTwice: opening a store to plan does not stop the
// next open from planning it again: Plan is repeatable and each answer is the
// same.
func TestPlan_BootsTheSameStoreTwice(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	fsys := r.seed(t)
	var first, second []string
	for _, out := range []*[]string{&first, &second} {
		if err := r.withStore(t, fsys, func(ctx context.Context, db *storage.DB) error {
			lr, _ := db.Legacy(cutover.BooksOwner)
			rep, err := m6.Plan(ctx, lr, r.config())
			*out = reportLines(rep)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if d := diff(second, first); d != "" {
		t.Errorf("a second Plan over the same store differs:\n%s", d)
	}
}

// TestPlan_RefusesAnIncompleteConfig: Plan reads through the same Config the step
// does and refuses what it cannot read, before it walks anything.
func TestPlan_RefusesAnIncompleteConfig(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	ctx := context.Background()
	if _, err := m6.Plan(ctx, nil, r.config()); err == nil {
		t.Error("Plan accepted no legacy range")
	}
	err := r.withStore(t, r.seed(t), func(ctx context.Context, db *storage.DB) error {
		lr, _ := db.Legacy(cutover.BooksOwner)
		for name, mut := range map[string]func(c *m6.Config){
			"secret names": func(c *m6.Config) { c.SecretNames = nil },
			"secret value": func(c *m6.Config) { c.SecretValue = nil },
			"effective":    func(c *m6.Config) { c.Effective = nil },
		} {
			c := r.config()
			mut(&c)
			if _, err := m6.Plan(ctx, lr, c); err == nil {
				t.Errorf("Plan accepted a config without %s", name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
