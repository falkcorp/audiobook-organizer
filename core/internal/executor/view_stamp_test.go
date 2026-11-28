// file: core/internal/executor/view_stamp_test.go
// version: 1.0.0
// guid: 4c7e2b95-1d08-4a63-b5f9-0e3a8d6c2f71
// last-edited: 2026-11-28

package executor

import (
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// stampView records v for kind through the native importer, as an app's
// cutover does, and commits it.
func stampView(t testing.TB, h *harness, kind tasks.Kind, v tasks.KindView) error {
	t.Helper()
	st, ok := tasks.NativeImporter(h.ks).(tasks.ViewStamper)
	if !ok {
		t.Fatal("the native importer cannot record a kind's View rule")
	}
	b := h.ks.NewIndexedBatch()
	if err := st.StampView(t.Context(), b, kind, v); err != nil {
		_ = b.Close()
		return err
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatalf("commit stamp: %v", err)
	}
	return nil
}

// TestStampView_ImportedKindIsReadUnderTheStampedRule: a kind the build has no
// definition for, whose runs an import carried over, is read under exactly the
// rule the import stamped, and under none (closed) when nothing was stamped.
func TestStampView_ImportedKindIsReadUnderTheStampedRule(t *testing.T) {
	cells := []struct {
		name   string
		stamp  *tasks.KindView // nil: nothing stamped
		viewer bool
		admin  bool
	}{
		{"unrestricted", &tasks.KindView{}, true, true},
		{"requires a scope the viewer lacks", &tasks.KindView{Requires: rbac.Need(scopeSettings)}, false, true},
		{"any of two scopes, the admin holds one", &tasks.KindView{Requires: rbac.AnyOf(scopeSettings, "gotham:other")}, false, true},
		{"nothing stamped", nil, false, false},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			h := reachHarness(t, nil)
			const kind = tasks.Kind("books.retired.batcave")
			if c.stamp != nil {
				if err := stampView(t, h, kind, *c.stamp); err != nil {
					t.Fatal(err)
				}
			}
			importRuns(t, h, []tasks.RunRecord{doneRun("stamped-1", kind, tasks.StateSucceeded)})
			for _, who := range []struct {
				name string
				want bool
			}{{"viewer", c.viewer}, {"admin", c.admin}} {
				ctx := viewerCtx()
				if who.name == "admin" {
					ctx = adminCtx()
				}
				_, err := h.x.Get(ctx, "stamped-1")
				if (err == nil) != who.want {
					t.Errorf("%s Get = %v, want visible=%v", who.name, err, who.want)
				}
			}
		})
	}
}

// TestStampView_RefusesARequirementTheSnapshotCannotCarry: a requirement the
// snapshot would record as "nobody" is an error, not a silent close.
func TestStampView_RefusesARequirementTheSnapshotCannotCarry(t *testing.T) {
	h := reachHarness(t, nil)
	err := stampView(t, h, "books.retired.batcave", tasks.KindView{Requires: rbac.LegacyAtom("library.view")})
	if !errors.Is(err, tasks.ErrViewNotRecordable) {
		t.Fatalf("StampView of a legacy atom = %v; want ErrViewNotRecordable", err)
	}
}
