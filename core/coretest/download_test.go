// file: core/coretest/download_test.go
// version: 1.0.0
// guid: 510d40a7-2102-4358-8418-180ed09a4f33
// last-edited: 2026-11-11

package coretest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
)

// fakeDownloadFixtures is the fake's own state table.
func fakeDownloadFixtures() DownloadFixtures {
	return DownloadFixtures{
		ReadyState:   "downloading",
		SeedingState: "seeding",
		States: []DownloadStateCase{
			{Native: "queued", Progress: 0, Want: download.StateQueued},
			{Native: "downloading", Progress: 0.5, Want: download.StateDownloading},
			{Native: "paused", Progress: 0.5, Want: download.StatePaused},
			{Native: "completed", Progress: 1, Want: download.StateCompleted},
			{Native: "seeding", Progress: 1, Want: download.StateCompleted, Seeding: true},
			{Native: "failed", Progress: 0.2, Want: download.StateFailed},
			{Native: "warning", Progress: 0.2, Want: download.StateWarning},
			{Native: "levitating", Progress: 1, Want: download.StateWarning, Unknown: true},
		},
	}
}

// The suite passes against the fake, which is the proof the suite itself is
// satisfiable; the Deluge adapter runs the same suite in its own package.
func TestFakeDownloadClient_Conformance(t *testing.T) {
	DownloadConformance(t, func(t testing.TB, s *DownloadScenario) download.Client {
		return NewFakeDownloadClient("fake", FakeDownloadScenario(s))
	}, fakeDownloadFixtures())
}

func TestFakeDownloadClient_AdvanceAndTick(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	f := NewFakeDownloadClient("fake", FakeDownloadNow(func() time.Time { return now }))
	id := download.ItemID("aa")
	f.Add(ScenarioItem{ID: id, Name: "Batman: Year One", TotalSize: 100})
	ctx := context.Background()
	state := func() download.Item {
		t.Helper()
		items, err := f.Items(ctx, download.ItemFilter{})
		if err != nil || len(items) != 1 {
			t.Fatalf("items = %v, %v", items, err)
		}
		return items[0]
	}
	if it := state(); it.State != download.StateQueued || !it.AddedAt.Equal(now) {
		t.Fatalf("a new item = %+v, want queued and added now", it)
	}
	f.Advance(id, download.StateDownloading)
	f.SetRate(0.25)
	f.Tick(2 * time.Second)
	if it := state(); it.State != download.StateDownloading || it.Progress != 0.5 || it.RemainingSize != 50 {
		t.Errorf("after 2s at 0.25/s = %+v", it)
	}
	f.Tick(2 * time.Second)
	if it := state(); it.State != download.StateCompleted || it.Progress != 1 || !it.CanBeRemoved || !it.CanMoveFiles {
		t.Errorf("after 4s = %+v, want completed and free to move", it)
	}
	f.Seed(id, true)
	if it := state(); !it.Seeding || it.CanBeRemoved || it.CanMoveFiles {
		t.Errorf("seeding = %+v", it)
	}
	f.Seed(id, false)
	if it := state(); it.Seeding || it.State != download.StateCompleted {
		t.Errorf("stopped seeding = %+v", it)
	}
	f.Advance(id, download.StateFailed)
	if it := state(); it.State != download.StateFailed {
		t.Errorf("advance to failed = %+v", it)
	}
	if f.Advance("missing", download.StateCompleted) || f.Seed("missing", true) {
		t.Error("an unknown item cannot be advanced")
	}
}

func TestFakeDownloadClient_CapsFaultsAndCounters(t *testing.T) {
	f := NewFakeDownloadClient("fake")
	f.Add(ScenarioItem{ID: "aa", SavePath: "/remote"})
	ctx := context.Background()

	f.SetCaps(download.Caps{Protocol: download.Torrent})
	if _, err := f.Files(ctx, "aa"); integrations.ClassOf(ctx, err) != integrations.ClassUnsupported {
		t.Errorf("Files with the cap off: %v", err)
	}
	if err := f.SetLocation(ctx, []download.ItemID{"aa"}, "/x"); integrations.ClassOf(ctx, err) != integrations.ClassUnsupported {
		t.Errorf("SetLocation with the cap off: %v", err)
	}
	if got := len(f.Scenario().Moves()); got != 0 {
		t.Errorf("a refused move was applied: %d", got)
	}

	boom := &integrations.Error{Class: integrations.ClassTransient, Err: errors.New("boom")}
	f.Fail("items", boom)
	if _, err := f.Items(ctx, download.ItemFilter{}); !errors.Is(err, boom) {
		t.Errorf("injected fault = %v", err)
	}
	f.Fail("items", nil)
	if _, err := f.Items(ctx, download.ItemFilter{}); err != nil {
		t.Errorf("after healing: %v", err)
	}
	if f.Calls("items") != 2 || f.Calls("files") != 1 || f.Calls("status") != 0 {
		t.Errorf("calls: items=%d files=%d status=%d", f.Calls("items"), f.Calls("files"), f.Calls("status"))
	}

	only := f.ClientOnly()
	if _, ok := only.(download.FileLister); ok {
		t.Error("ClientOnly implements FileLister")
	}
	if _, ok := only.(download.Relocator); ok {
		t.Error("ClientOnly implements Relocator")
	}
	if items, err := only.Items(ctx, download.ItemFilter{}); err != nil || len(items) != 1 {
		t.Errorf("ClientOnly items = %v, %v", items, err)
	}
}

func TestDownloadScenario_MoveIsAtomic(t *testing.T) {
	s := NewDownloadScenario()
	s.Put(ScenarioItem{ID: "aa", SavePath: "/old"})
	err := s.Move([]download.ItemID{"aa", "zz"}, "/new")
	if !errors.Is(err, ErrUnknownDownloadItem) {
		t.Fatalf("err = %v", err)
	}
	if it, _ := s.Item("aa"); it.SavePath != "/old" || len(s.Moves()) != 0 {
		t.Errorf("a failed move changed state: %+v moves=%v", it, s.Moves())
	}
}
