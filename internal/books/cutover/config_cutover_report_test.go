// file: internal/books/cutover/config_cutover_report_test.go
// version: 0.1.0
// guid: 3b7e1c94-5a20-4d68-9f13-c6a8e2b40d57
// last-edited: 2026-11-21

package cutover

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// reportCounts is the part of a VerifyReport the count rule reads.
type reportCounts struct{ Read, Written, Held, Quarantined int64 }

func countsOf(r datamigrate.VerifyReport) reportCounts {
	return reportCounts{Read: r.Read, Written: r.Written, Held: r.Held, Quarantined: r.Quarantined}
}

// TestConfigCutover_VerifyReportCountsTheRows (05-storage 5S.3.13 "read =
// imported + quarantined + deleted-by-rule"; 05-config 1.12.2 "M2's Source is
// the four encrypted rows"): the VerifyReport M2 stores counts its Source rows
// outside the Residual, the four encrypted rows, each in exactly one bucket.
// Read is the rows present; Written the rows whose own value was imported;
// Quarantined the rows quarantined (a row that does not decrypt, a value whose
// name already holds content). What is left was deleted by rule (an empty
// row). The settings blob is the Residual: rewritten in place, it is no row of
// the rule, and a quarantine record of one of its members
// (setting:config_blob#<member>) is not counted.
func TestConfigCutover_VerifyReportCountsTheRows(t *testing.T) {
	garbage := func(name string) func(*rig) {
		return func(r *rig) {
			prev := r.mutate
			r.mutate = func(t testing.TB, lr storage.LegacyRange) {
				prev(t, lr)
				if err := lr.Set(t.Context(), settingKey(name), []byte("not a settings row")); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	empty := func(name string) func(*rig) {
		return func(r *rig) {
			prev := r.mutate
			r.mutate = func(t testing.TB, lr storage.LegacyRange) {
				prev(t, lr)
				if err := lr.Set(t.Context(), settingKey(name), settingRow(name, "", "string", true)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	absent := func(name string) func(*rig) {
		return func(r *rig) {
			prev := r.mutate
			r.mutate = func(t testing.TB, lr storage.LegacyRange) {
				prev(t, lr)
				if err := lr.Delete(t.Context(), settingKey(name)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	fileHolds := func(yaml string) func(*rig) {
		return func(r *rig) {
			r.confFile = filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(r.confFile, []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name string
		opts []func(*rig)
		pre  func(ctx context.Context, e *bootEnv) error
		want reportCounts
		// quarantined lists the Source rows the store must hold a record of.
		quarantined []string
	}{
		{name: "every row imported", opts: []func(*rig){seedKnown},
			want: reportCounts{Read: 4, Written: 4}},
		{name: "an empty row is deleted by rule", opts: []func(*rig){seedKnown, empty("basic_auth_password")},
			want: reportCounts{Read: 4, Written: 3}},
		{name: "an absent row is not read", opts: []func(*rig){seedKnown, absent("google_books_api_key")},
			want: reportCounts{Read: 3, Written: 3}},
		{name: "a row that does not decrypt is quarantined", opts: []func(*rig){seedKnown, garbage("hardcover_api_token")},
			want: reportCounts{Read: 4, Written: 3, Quarantined: 1}, quarantined: []string{"setting:hardcover_api_token"}},
		{name: "a row recovered from config.yaml counts once, as quarantined",
			opts: []func(*rig){seedKnown, garbage("openai_api_key"), fileHolds("openai_api_key: Oracle-file-token-0013\n")},
			want: reportCounts{Read: 4, Written: 3, Quarantined: 1}, quarantined: []string{"setting:openai_api_key"}},
		{name: "a value whose name holds content is quarantined; a blob member's record is not a row",
			opts: []func(*rig){seedKnown},
			pre: func(ctx context.Context, e *bootEnv) error {
				for _, name := range []secrets.Name{"integrations.ai.openai.api_key", "modules.books.acoustid.api_key"} {
					if _, err := e.sec.Put(ctx, name, secrets.NewValue("existing-owner-value"), "owner"); err != nil {
						return err
					}
				}
				return nil
			},
			want: reportCounts{Read: 4, Written: 3, Quarantined: 1}, quarantined: []string{"setting:openai_api_key", "setting:config_blob#acoustid_api_key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, "stage1", tc.opts...)
			r.preMigrate = tc.pre
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			if !snap.Reported {
				t.Fatal("M2 stored no VerifyReport")
			}
			if got := countsOf(snap.Report); got != tc.want {
				t.Errorf("stored report counts = %+v, want %+v", got, tc.want)
			}
			if len(snap.Report.Failures) != 0 || snap.State != datamigrate.StateDone {
				t.Errorf("state %s, failures %v", snap.State, snap.Report.Failures)
			}
			for _, key := range tc.quarantined {
				if _, ok := quarantineFor(snap, key); !ok {
					t.Errorf("no quarantine record of %s: %+v", key, snap.Quarantine)
				}
			}
		})
	}

	t.Run("a second boot keeps the first boot's report", func(t *testing.T) {
		r := newRig(t, "stage1", seedKnown, empty("basic_auth_password"))
		mem := r.newMem(t)
		if _, err := r.runOn(t, mem); err != nil {
			t.Fatalf("first boot: %v", err)
		}
		snap, err := r.runOn(t, mem)
		if err != nil {
			t.Fatalf("second boot: %v", err)
		}
		if got, want := countsOf(snap.Report), (reportCounts{Read: 4, Written: 3}); got != want || len(snap.Report.Failures) != 0 {
			t.Errorf("after a second boot the report is %+v (failures %v), want the first boot's %+v", got, snap.Report.Failures, want)
		}
	})
}

// TestConfigStep_VerifyBalancesItsCounts: Verify fails when this boot's
// accounting does not balance (a row counted in no bucket, or in two), which
// the store alone cannot show once the rows are gone; a balanced count over
// the drained store passes.
func TestConfigStep_VerifyBalancesItsCounts(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		read, written, quar, delete int64
		fail                        bool
	}{
		{name: "balanced", read: 4, written: 2, quar: 1, delete: 1},
		{name: "a row in no bucket", read: 4, written: 3, fail: true},
		{name: "a row in two buckets", read: 4, written: 3, quar: 1, delete: 1, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &configStep{}
			s.read.Store(tc.read)
			s.written.Store(tc.written)
			s.quarantined.Store(tc.quar)
			s.deleted.Store(tc.delete)
			rep, err := s.verify(t.Context(), emptyReader{})
			if err != nil {
				t.Fatal(err)
			}
			if failed := len(rep.Failures) > 0; failed != tc.fail {
				t.Errorf("failures %v, want failing %v", rep.Failures, tc.fail)
			}
			if got, want := countsOf(rep), (reportCounts{Read: tc.read, Written: tc.written, Quarantined: tc.quar}); got != want {
				t.Errorf("report counts = %+v, want %+v", got, want)
			}
		})
	}
}
