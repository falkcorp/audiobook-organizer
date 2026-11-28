// file: core/updater/tasks_test.go
// version: 1.1.1
// guid: 0d5e8a31-7c94-4b26-a1f3-e82b6c09d174
// last-edited: 2026-11-18

//go:build unix

package updater

// The task definitions (5U.4), the once-per-version announcement (5U.4 and
// 5U.8), the metrics (5U.9) and the policy that a nil updater registers
// nothing.

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

const updaterOwner storage.Owner = "core/updater"

// openKeyspace is the updater's c/updater/ keyspace over an in-memory store.
func openKeyspace(t *testing.T) storage.Keyspace {
	t.Helper()
	o, err := storage.Open(t.Context(), storage.OpenOptions{
		Path:   "/batcave/main.pebble",
		Owners: []storage.OwnerSpec{{Owner: updaterOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true}},
		Build:  "updater+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(t.Context())
	if err == nil {
		err = db.Migrate(t.Context())
	}
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace(updaterOwner)
	if !ok {
		t.Fatal("no keyspace for the updater owner")
	}
	return ks
}

// fakeWriter is a tasks.Writer that performs every write and records it.
type fakeWriter struct {
	mu      sync.Mutex
	writes  []tasks.Write
	preview bool
}

func (w *fakeWriter) Do(ctx context.Context, wr tasks.Write, fn func(context.Context) ([]tasks.Change, error)) error {
	w.mu.Lock()
	w.writes = append(w.writes, wr)
	w.mu.Unlock()
	if w.preview {
		return tasks.ErrPreview
	}
	_, err := fn(ctx)
	return err
}
func (*fakeWriter) MarkRowApplied(context.Context, string)           {}
func (*fakeWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

func (w *fakeWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

// storeRig is a rig whose updater has the keyspace.
func storeRig(t *testing.T, ks storage.Keyspace, latest string) *rig {
	t.Helper()
	src := newFakeSource()
	src.add(latest, fakeBinary(testApp, latest), true)
	r := newRig(t, "v0.223.0", src)
	r.u.d.Store = ks
	return r
}

func TestCheck_NotifiesOncePerVersionAcrossRestart(t *testing.T) {
	ks := openKeyspace(t)
	w := &fakeWriter{}

	first := storeRig(t, ks, "v0.224.0")
	for range 3 {
		if _, err := first.u.runCheck(t.Context(), w); err != nil {
			t.Fatal(err)
		}
	}
	if got := w.count(); got != 1 {
		t.Fatalf("three checks of one version announced %d times, want 1", got)
	}

	// A restart: a new Updater, the same store.
	second := storeRig(t, ks, "v0.224.0")
	if _, err := second.u.runCheck(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if got := w.count(); got != 1 {
		t.Fatalf("after a restart the same version announced again (%d writes)", got)
	}

	// A newer version announces once more.
	third := storeRig(t, ks, "v0.225.0")
	if _, err := third.u.runCheck(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if got := w.count(); got != 2 {
		t.Fatalf("a newer version made %d writes in all, want 2", got)
	}
	rec, err := third.u.readSeen(t.Context())
	if err != nil || rec.Version != "0.225.0" {
		t.Fatalf("seen = %+v, %v; want 0.225.0", rec, err)
	}
	if w.writes[0].Name != "core.updater.seen" {
		t.Errorf("write name = %q, want core.updater.seen", w.writes[0].Name)
	}
}

func TestCheck_PreviewAnnouncesNothing(t *testing.T) {
	ks := openKeyspace(t)
	w := &fakeWriter{preview: true}
	r := storeRig(t, ks, "v0.224.0")
	if _, err := r.u.runCheck(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if rec, _ := r.u.readSeen(t.Context()); rec.Version != "" {
		t.Errorf("a preview recorded %q", rec.Version)
	}
}

func TestCheck_NoStoreAnnouncesNothing(t *testing.T) {
	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	r := newRig(t, "v0.223.0", src)
	w := &fakeWriter{}
	st, err := r.u.runCheck(t.Context(), w)
	if err != nil || !st.Available {
		t.Fatalf("status %+v, err %v", st, err)
	}
	if w.count() != 0 {
		t.Errorf("an updater with no store wrote %d times", w.count())
	}
}

func TestCheck_UnreadableSeenIsAnnouncedAgain(t *testing.T) {
	ks := openKeyspace(t)
	if err := ks.Set(t.Context(), []byte(seenKey), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	w := &fakeWriter{}
	r := storeRig(t, ks, "v0.224.0")
	if _, err := r.u.runCheck(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if w.count() != 1 {
		t.Fatalf("writes = %d, want the version announced and the record rewritten", w.count())
	}
}

func TestFoundNotification_LinkIsHTTPOnly(t *testing.T) {
	r := storeRig(t, openKeyspace(t), "v0.224.0")
	for raw, want := range map[string]string{
		"https://example.invalid/r/1": "https://example.invalid/r/1",
		"javascript:alert(1)":         "",
		"file:///etc/passwd":          "",
		"":                            "",
		"/relative":                   "",
	} {
		n := r.u.foundNotification(Status{Current: "v1.0.0", Latest: "1.0.1"}, Release{URL: raw})
		var got string
		if len(n.Links) > 0 {
			got = n.Links[0].URL
		}
		if got != want {
			t.Errorf("link for %q = %q, want %q", raw, got, want)
		}
	}
}

func TestTasks_Conformance(t *testing.T) {
	r := newRig(t, "v0.223.0", newFakeSource())
	taskstest.Conformance(t, CheckTask(r.u), ApplyTask(r.u), RollbackTask(r.u))
}

func TestTasks_ApplyHasNoScheduleAndOneConcurrencyKey(t *testing.T) {
	// The kinds, the permission and the keys are the spec's: a check is a
	// manage-scope scheduled task, apply and rollback are admin-only and
	// serialise on one key. Checked through the declared surface, not the
	// run bodies.
	r := newRig(t, "v0.223.0", newFakeSource())
	for _, d := range []tasks.Definition{CheckTask(r.u), ApplyTask(r.u), RollbackTask(r.u)} {
		if p := tasks.Problems(d); len(p) != 0 {
			t.Errorf("%s: %v", d.Kind(), p)
		}
	}
	if CheckTask(r.u).Kind() != KindCheck || ApplyTask(r.u).Kind() != KindApply || RollbackTask(r.u).Kind() != KindRollback {
		t.Error("a constructor returned the wrong kind")
	}
}

func TestPolicy_NilOptionsNoRoutesNoTasks(t *testing.T) {
	// A nil updater yields definitions that fail registration closed.
	for _, d := range []tasks.Definition{CheckTask(nil), ApplyTask(nil), RollbackTask(nil)} {
		p := tasks.Problems(d)
		if len(p) == 0 {
			t.Errorf("%s with a nil updater is registrable", d.Kind())
			continue
		}
		if !strings.Contains(errors.Join(p...).Error(), "no Run") {
			t.Errorf("%s: problems = %v, want the missing Run reported", d.Kind(), p)
		}
	}
	if _, err := New(Options{AppID: testApp, BinaryName: testBinary}, Deps{}); err == nil {
		t.Error("New without a Source succeeded")
	}
}

func TestNoGlobals_Updater(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	archtest.NoInit(t, root, "./updater/...")

	// Two Updaters share nothing: a status one holds is not the other's.
	a := newRig(t, "v0.223.0", newFakeSource())
	b := newRig(t, "v0.223.0", newFakeSource())
	a.src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	if st, err := a.u.Check(t.Context()); err != nil || !st.Available {
		t.Fatalf("first updater: %+v, %v", st, err)
	}
	if b.u.Status().Available || b.u.Status().Latest != "" {
		t.Error("an Updater's status shows through to another instance")
	}
}

func TestUpdaterSeries_Golden(t *testing.T) {
	ctx := t.Context()
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("updater", ScopeAttributes()...)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMetrics(sc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMetrics(nil); err == nil {
		t.Fatal("NewMetrics without a scope succeeded")
	}

	src := newFakeSource()
	src.add("v0.224.0", fakeBinary(testApp, "v0.224.0"), true)
	r := newRig(t, "v0.223.0", src, withMetrics(m),
		withReader(func(context.Context, string, string) error { return nil }))
	if _, err := r.u.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.u.Apply(r.rc(KindApply), ""); err != nil {
		t.Fatal(err)
	}
	if err := r.u.Rollback(r.rc(KindRollback)); err != nil {
		t.Fatal(err)
	}
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")

	for family, want := range map[string]map[string]string{
		"mediacore_updater_checks_total":    {"outcome": "ok"},
		"mediacore_updater_apply_total":     {"outcome": "applied"},
		"mediacore_updater_rollbacks_total": {"outcome": "done"},
	} {
		if v, ok := seriesValue(t, p, family, want); !ok || v != 1 {
			t.Errorf("%s%v = %v, %v; want 1", family, want, v, ok)
		}
	}
	if v, ok := seriesValue(t, p, "mediacore_updater_download_bytes_total", nil); !ok || v != float64(len(fakeBinary(testApp, "v0.224.0"))) {
		t.Errorf("download bytes = %v, %v; want the release's size", v, ok)
	}
}

// seriesValue reads one sample of family whose labels include want.
func seriesValue(t *testing.T, p *metrics.Provider, family string, want map[string]string) (float64, bool) {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			for k, v := range want {
				if labels[k] != v {
					continue next
				}
			}
			if c := m.GetCounter(); c != nil {
				return c.GetValue(), true
			}
			return m.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

// TestTasks_ApplyAndRollbackAreOwnerRouteOnly: the apply and rollback routes
// require an interactive session on top of core:admin (HEAD's credential
// guard), so neither kind may be startable through the generic task API,
// which checks only Perms.Start. The check kind has no such gate.
func TestTasks_ApplyAndRollbackAreOwnerRouteOnly(t *testing.T) {
	for name, d := range map[string]tasks.Definition{"apply": ApplyTask(nil), "rollback": RollbackTask(nil)} {
		if tasks.CommonOf(d).Perms.OwnerRouteOnly == "" {
			t.Errorf("%s is startable through the generic task API, which skips the route's interactive-session gate", name)
		}
	}
	if got := tasks.CommonOf(CheckTask(nil)).Perms.OwnerRouteOnly; got != "" {
		t.Errorf("check is owner-route-only (%q); its route has no gate beyond its permission", got)
	}
}

// TestScheduledKindsCarrySystemStart: the schedule and the SystemStart flag agree
// on every updater definition. The scheduler starts core.updater.check as core's
// system actor, which cannot hold core:system.manage (AuthRequired), so it
// carries the reviewed reason; apply and rollback have no Schedule and carry
// none. Start is untouched: a person's or an API key's start of check still needs
// core:system.manage.
func TestScheduledKindsCarrySystemStart(t *testing.T) {
	cases := []struct {
		def       tasks.Definition
		scheduled bool
	}{
		{CheckTask(nil), true},
		{ApplyTask(nil), false},
		{RollbackTask(nil), false},
	}
	for _, tc := range cases {
		c := tasks.CommonOf(tc.def)
		if got := c.Schedule != nil; got != tc.scheduled {
			t.Errorf("%s: Schedule declared = %v, want %v", tc.def.Kind(), got, tc.scheduled)
		}
		if got := c.Perms.SystemStart != ""; got != tc.scheduled {
			t.Errorf("%s: SystemStart set = %v, want %v (the flag and the schedule agree)", tc.def.Kind(), got, tc.scheduled)
		}
	}
	if got := tasks.CommonOf(CheckTask(nil)).Perms.Start.Permissions(); len(got) != 1 || got[0] != "core:system.manage" {
		t.Errorf("check Start = %v, want core:system.manage unchanged", got)
	}
}
