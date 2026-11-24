// file: core/internal/executor/standdown_renew_test.go
// version: 0.1.0
// guid: 4d1f7a52-9b3e-4c06-8a2d-e5b07c91f3a8
// last-edited: 2026-11-22

package executor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// holdProbe is a run that takes a stand-down hold on gotham.scan and hands the
// hold and its run context to the test, then waits until the test lets it go.
type holdProbe struct {
	holds chan tasks.Hold
	rcs   chan *tasks.RC
	free  chan struct{}
}

func newHoldProbe() *holdProbe {
	return &holdProbe{holds: make(chan tasks.Hold, 1), rcs: make(chan *tasks.RC, 1), free: make(chan struct{})}
}

func (p *holdProbe) def(kind string) tasks.Definition {
	return taskDef(kind, func(rc *tasks.RC, _ noParams) (string, error) {
		h, err := rc.StandDown("gotham.scan")
		if err != nil {
			return "", err
		}
		p.holds <- h
		p.rcs <- rc
		<-p.free
		return "case closed", nil
	})
}

// write is one gated write through the run's writer; called reports whether fn
// ran.
func write(rc *tasks.RC, kind string) (called bool, err error) {
	err = rc.Writer().Do(context.Background(), tasks.Write{Name: "gotham.note", Resource: casesRef(kind),
		Subject: tasks.ResID("gotham", "cases", "riddler")}, func(context.Context) ([]tasks.Change, error) {
		called = true
		return nil, nil
	})
	return called, err
}

// A run that keeps calling Renew keeps its hold for far longer than one lease
// (the default lease is 5 minutes; this run lives 9). Without Renew the reaper
// would have dropped the hold at the fifth minute and the Renew after it fails.
func TestHoldRenew_KeepsTheHoldForTheRunsWholeLife(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newHoldProbe()
		h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, synctestConfig)
		id := h.start("gotham.compact", tasks.StartRequest{})
		hold := <-p.holds
		rc := <-p.rcs
		defer close(p.free)

		for i := range 6 {
			time.Sleep(90 * time.Second)
			if err := hold.Renew(); err != nil {
				t.Fatalf("renew %d at %d s: %v; a run that renews inside its lease keeps the hold", i, (i+1)*90, err)
			}
			if !h.e.keyHeld("gotham.scan") {
				t.Fatalf("after renew %d the key is no longer held", i)
			}
		}
		if called, err := write(rc, "gotham.compact"); err != nil || !called {
			t.Fatalf("a write by a run that renewed: err %v, called %v; want it to run", err, called)
		}
		hold.Release()
		if h.e.keyHeld("gotham.scan") {
			t.Fatal("the key is still held after Release")
		}
		err := hold.Renew()
		if !errors.Is(err, tasks.ErrHoldLost) {
			t.Fatalf("Renew after Release: %v, want tasks.ErrHoldLost", err)
		}
		if r := h.get(id); r.State != tasks.StateRunning {
			t.Fatalf("run %s; the hold's end must not end the run", r.State)
		}
	})
}

// A lease that lapses is gone for good, and the run's writer stops with it: a
// late Renew reports the loss (whether the reaper got there first or the Renew
// found the lapse itself), no later Renew succeeds, and the writer refuses with
// ErrFenced before it calls fn.
func TestHoldRenew_ALapsedLeaseFailsClosed(t *testing.T) {
	for _, c := range []struct {
		name string
		tick time.Duration // dispatch tick: past the sleep, the reaper never runs; short, it reaps
	}{
		{name: "the reaper dropped the hold first", tick: time.Second},
		{name: "Renew finds the lapse itself", tick: time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newHoldProbe()
				h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, func(o *Options) {
					o.Config = Config{DispatchTick: c.tick, WatchdogInterval: time.Hour}
				})
				h.start("gotham.compact", tasks.StartRequest{})
				hold := <-p.holds
				rc := <-p.rcs
				defer close(p.free)

				time.Sleep(6 * time.Minute) // past the 5 minute lease with no renewal
				for i := range 2 {
					if err := hold.Renew(); !errors.Is(err, tasks.ErrHoldLost) {
						t.Fatalf("Renew %d after the lease lapsed: %v, want tasks.ErrHoldLost", i, err)
					}
				}
				called, err := write(rc, "gotham.compact")
				if !errors.Is(err, tasks.ErrFenced) || called {
					t.Fatalf("write after the lapse: err %v, called %v; want ErrFenced and fn never called", err, called)
				}
				if h.e.keyHeld("gotham.scan") {
					t.Fatal("a lapsed hold still holds its key")
				}
			})
		})
	}
}

// A run that lost one hold lost its writer, so no other hold of that run reports
// itself healthy: Renew on the surviving hold fails too, instead of telling the
// shell all is well while every write is refused. The reaper is kept out of this
// (a 1 h tick) so the loss is found by Renew alone.
func TestHoldRenew_ALostHoldFailsEveryHoldOfTheRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		holds := make(chan [2]tasks.Hold, 1)
		rcs := make(chan *tasks.RC, 1)
		free := make(chan struct{})
		def := taskDef("gotham.compact", func(rc *tasks.RC, _ noParams) (string, error) {
			a, err := rc.StandDown("gotham.scan")
			if err != nil {
				return "", err
			}
			b, err := rc.StandDown("arkham.scan")
			if err != nil {
				return "", err
			}
			holds <- [2]tasks.Hold{a, b}
			rcs <- rc
			<-free
			return "case closed", nil
		})
		h := newHarness(t, []tasks.Definition{def}, func(o *Options) {
			o.Config = Config{DispatchTick: time.Hour, WatchdogInterval: time.Hour}
		})
		h.start("gotham.compact", tasks.StartRequest{})
		pair := <-holds
		rc := <-rcs
		defer close(free)

		time.Sleep(4 * time.Minute)
		if err := pair[1].Renew(); err != nil {
			t.Fatalf("renew of the second hold inside its lease: %v", err)
		}
		time.Sleep(2 * time.Minute) // the first hold is 6 min old (lease 5), the second 2 min
		if err := pair[0].Renew(); !errors.Is(err, tasks.ErrHoldLost) {
			t.Fatalf("Renew of the lapsed first hold: %v, want tasks.ErrHoldLost", err)
		}
		if err := pair[1].Renew(); !errors.Is(err, tasks.ErrHoldLost) {
			t.Fatalf("Renew of the second hold after the run lost one: %v, want tasks.ErrHoldLost (the run is fenced)", err)
		}
		if called, err := write(rc, "gotham.compact"); !errors.Is(err, tasks.ErrFenced) || called {
			t.Fatalf("write: err %v, called %v; want ErrFenced", err, called)
		}
	})
}

// Renew is for the hold it is called on. After Release it never extends the
// lease another holder of the same key still relies on, and it is safe to call
// from the run's pool workers while another worker releases (go test -race).
func TestHoldRenew_AfterReleaseDoesNotTouchAnotherHoldersLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := newHoldProbe(), newHoldProbe()
		h := newHarness(t, []tasks.Definition{first.def("gotham.compact"), second.def("arkham.compact")}, synctestConfig)
		h.start("gotham.compact", tasks.StartRequest{})
		h.start("arkham.compact", tasks.StartRequest{})
		hold1, hold2 := <-first.holds, <-second.holds
		defer close(first.free)
		defer close(second.free)

		expires := func() time.Time {
			h.e.mu.Lock()
			defer h.e.mu.Unlock()
			sh := h.e.holds["gotham.scan"]
			if sh == nil {
				t.Fatal("no live hold on the key")
			}
			return sh.expires
		}

		time.Sleep(30 * time.Second)
		if err := hold2.Renew(); err != nil {
			t.Fatalf("second holder's Renew: %v", err)
		}
		hold1.Release()
		want := expires()

		time.Sleep(30 * time.Second)
		if err := hold1.Renew(); !errors.Is(err, tasks.ErrHoldLost) {
			t.Fatalf("released holder's Renew: %v, want tasks.ErrHoldLost", err)
		}
		if got := expires(); !got.Equal(want) {
			t.Fatalf("a released holder's Renew moved the shared lease from %v to %v", want, got)
		}
		if !h.e.keyHeld("gotham.scan") {
			t.Fatal("the second holder's hold ended with the first's release")
		}

		// Pool workers renew while one of them releases.
		var wg sync.WaitGroup
		var okBeforeRelease, lost atomic.Int64
		for range 8 {
			wg.Go(func() {
				for range 50 {
					switch err := hold2.Renew(); {
					case err == nil:
						okBeforeRelease.Add(1)
					case errors.Is(err, tasks.ErrHoldLost):
						lost.Add(1)
					default:
						t.Errorf("Renew: %v", err)
					}
				}
			})
		}
		wg.Go(func() {
			time.Sleep(time.Millisecond)
			hold2.Release()
			hold2.Release() // idempotent
		})
		wg.Wait()
		if err := hold2.Renew(); !errors.Is(err, tasks.ErrHoldLost) {
			t.Fatalf("Renew after the last Release: %v, want tasks.ErrHoldLost", err)
		}
		if h.e.keyHeld("gotham.scan") {
			t.Fatal("the key is still held after both holders released")
		}
	})
}
