// file: core/tasks/taskstest/parity/metrics.go
// version: 1.0.0
// guid: 0a179a5a-9678-465f-9077-03cb4bef241e
// last-edited: 2026-11-10

package parity

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// ProbeKind is the kind of the instrument probe's runs. Every task-run series
// the probe records carries it (attribute def_id), so a registry the probe
// was not given must have no series with that value.
const ProbeKind tasks.Kind = "gotham.parity-probe"

// probeYield is the probe's kind that yields once (the yields counter).
const probeYield tasks.Kind = "gotham.parity-yield"

// ExerciseInstruments is the executor cell of 05-metrics §12 (F2-03): it
// boots a standalone executor over a fresh in-memory store with its
// instruments on, in p's core ops scope (Provider.Core("ops",
// executor.Attributes()...), the scope the kernel builds at F5), and drives
// it until each of the 18 instruments has a sample: a run in flight that
// reports progress, takes a ref lock and runs items; a second run of its
// concurrency key, queued and deferred; a run that yields once; a held run
// of a disabled module; an undecodable stored record; a declared lane; and
// the three schedule instruments through executor.Schedules. It fails t
// unless p's gather returns no error and has a family for every instrument,
// and returns the names of the ops families p gathered.
func ExerciseInstruments(t *testing.T, p *metrics.Provider) []string {
	t.Helper()
	sc, err := p.Core("ops", executor.Attributes()...)
	if err != nil {
		t.Fatalf("core ops scope: %v", err)
	}
	k := NewKit(t)
	k.Plant("joker.heist", tasks.StateQueued) // held: module joker is disabled
	bad := k.Plant(ProbeKind, tasks.StateQueued)
	if err := k.write(func(b *storage.Batch) error {
		key := taskstore.RunKey(string(bad))
		if _, err := b.GetForUpdate(context.Background(), k.ks, key); err != nil {
			return err
		}
		return b.Set(key, []byte("the Riddler's cipher"))
	}); err != nil {
		t.Fatalf("plant an undecodable record: %v", err)
	}

	main := k.Probe(string(ProbeKind)).Body(func(p *Probe, rc *tasks.RC, _ Params) (string, error) {
		rc.Progress().Set(1, tasks.Known(3))
		release, err := rc.LockRefs(entity.Ref{Module: "gotham", Kind: "parity-probe", ID: "ace"})
		if err != nil {
			return "", err
		}
		release()
		if _, err := tasks.RunItems(rc, tasks.Sequential("one file at a time"), []string{"file-01", "file-02"},
			func(*tasks.ItemRC, string) error { return nil }); err != nil {
			return "", err
		}
		send(p.Started, rc.RunID())
		select {
		case <-p.Release:
			return "case closed", nil
		case <-rc.Context().Done():
			return "", context.Cause(rc.Context())
		}
	})
	yield := k.Probe(string(probeYield)).Body(func(_ *Probe, rc *tasks.RC, _ Params) (string, error) {
		if rc.Resumed() {
			return "case closed", nil
		}
		rc.Progress().Set(1, tasks.Known(2))
		if err := rc.Checkpoint(1); err != nil {
			return "", err
		}
		return "", tasks.ErrContinueInPlace
	})
	x := k.Boot(Defs(main.Def(Key("gotham.parity-probe"), ProgressTimeout(time.Hour)), yield.Def()), func(o *executor.Options) {
		o.Metrics, o.InstrumentsOff = sc, false
		o.Manifests = []executor.Manifest{{Module: "joker", Enabled: false}}
		o.Lanes = []tasks.Lane{{ID: "gotham.batmobile", Title: "Batmobile", Capacity: func() int { return 1 }}}
	})
	first := k.Start(ProbeKind, nil)
	k.WaitStarted(main)
	second := k.Start(ProbeKind, Params{Idx: 2}) // waits behind first's concurrency key
	k.WaitState(k.Start(probeYield, nil), tasks.StateSucceeded)
	sm := executor.Schedules(x)
	sm.ScheduleLag(ProbeKind, 2*time.Second)
	sm.ScheduleMissed(ProbeKind, 1)
	sm.ScheduleError(ProbeKind)
	k.Quiet(500 * time.Millisecond) // dispatch passes see second deferred

	names := gatherOps(t, p, sc.Prefix())
	base := strings.ReplaceAll(sc.Prefix(), ".", "_") + "_"
	for _, n := range executor.InstrumentNames() {
		fam := base + strings.ReplaceAll(n, ".", "_")
		found := false
		for _, g := range names {
			if g == fam || strings.HasPrefix(g, fam+"_") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("instrument %s has no gathered family %s*; gathered:\n%s", n, fam, strings.Join(names, "\n"))
		}
	}
	main.Free()
	k.WaitState(first, tasks.StateSucceeded)
	k.WaitState(second, tasks.StateSucceeded)
	return gatherOps(t, p, sc.Prefix())
}

// gatherOps gathers p and returns the names of the families under prefix
// (dots as underscores), failing t on a gather error.
func gatherOps(t *testing.T, p *metrics.Provider, prefix string) []string {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatalf("the executor's registry gather: %v", err)
	}
	want := strings.ReplaceAll(prefix, ".", "_") + "_"
	var out []string
	for _, mf := range mfs {
		if strings.HasPrefix(mf.GetName(), want) {
			out = append(out, mf.GetName())
		}
	}
	return out
}
