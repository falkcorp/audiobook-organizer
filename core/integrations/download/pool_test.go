// file: core/integrations/download/pool_test.go
// version: 1.0.0
// guid: 24159203-d959-4a43-af07-d438c1d504a1
// last-edited: 2026-11-11

package download_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
)

func classOf(err error) integrations.Class { return integrations.ClassOf(context.Background(), err) }

// standardMappings maps seedbox's /remote/books to the library's incoming
// directory and rpi-box's /mnt/rpi to another directory.
func standardMappings() download.PathMapper {
	return mapper(map[integrations.InstanceID][]download.Mapping{
		"seedbox": {{Remote: "/remote/books", Local: "/srv/library/incoming"}},
		"rpi-box": {{Remote: "/mnt/rpi/dl", Local: "/srv/library/rpi"}},
	})
}

func TestPool_ItemsFillsLocalPath(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	f.Add(item("aa", "Year One", "/remote/books", "seeding", 1))
	f.Add(item("bb", "Hush", "/remote/books/sub", "completed", 1))
	f.Add(item("cc", "Owls", "/elsewhere/books", "completed", 1))
	f.Add(item("dd", "Dark Knight", "/remote/books-old", "completed", 1)) // shares a prefix, not a directory
	p := h.newPool(download.PoolOptions{Paths: standardMappings()})

	items, err := p.Items(context.Background(), seedbox, download.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[download.ItemID]download.LocalPath{}
	for _, it := range items {
		got[it.Ref.ID] = it.LocalPath
		if it.Ref.Instance != seedbox {
			t.Errorf("%s: instance = %v", it.Ref.ID, it.Ref.Instance)
		}
	}
	want := map[download.ItemID]download.LocalPath{
		"aa": "/srv/library/incoming",
		"bb": "/srv/library/incoming/sub",
		"cc": "",
		"dd": "",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: LocalPath = %q, want %q", id, got[id], w)
		}
	}
	// The client's own answer is left alone.
	if items[0].RemotePath == "" {
		t.Error("RemotePath was lost")
	}
}

func TestPool_ItemsWithoutMapperMapsNothing(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	f.Add(item("aa", "Year One", "/remote/books", "completed", 1))
	items, err := h.newPool(download.PoolOptions{}).Items(context.Background(), seedbox, download.ItemFilter{})
	if err != nil || len(items) != 1 || items[0].LocalPath != "" {
		t.Fatalf("items = %+v, %v", items, err)
	}
}

func TestPool_ItemsErrors(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	p := h.newPool(download.PoolOptions{})
	ctx := context.Background()

	f.Fail("items", errors.New("the seedbox is on fire"))
	_, err := p.Items(ctx, seedbox, download.ItemFilter{})
	e, ok := errors.AsType[*integrations.Error](err)
	if !ok || e.Ref != seedbox || e.Op != "items" {
		t.Errorf("a plain client error is wrapped as the instance's: %#v", err)
	}
	f.Fail("items", &integrations.Error{Ref: seedbox, Op: "items", Class: integrations.ClassTransient, Status: 503})
	if _, err := p.Items(ctx, seedbox, download.ItemFilter{}); classOf(err) != integrations.ClassTransient {
		t.Errorf("an adapter's own *Error passes through: %v", err)
	}
	f.Fail("items", nil)

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Items(cctx, seedbox, download.ItemFilter{}); err == nil {
		t.Error("a cancelled ctx must fail")
	}
	if _, err := p.Items(ctx, integrations.Ref{Kind: integrations.KindDownload, Instance: "nowhere"}, download.ItemFilter{}); !errors.Is(err, integrations.ErrNotConfigured) {
		t.Errorf("unknown instance: %v", err)
	}
	if _, err := p.Items(ctx, integrations.Ref{Kind: integrations.KindNotify, Instance: "seedbox"}, download.ItemFilter{}); classOf(err) != integrations.ClassInvalid {
		t.Errorf("another kind's ref: %v", err)
	}
}

func TestPool_RelocateGroupsByInstance(t *testing.T) {
	h := newHarness(t)
	a := h.addFake(seedbox, crateAdapter, 0)
	b := h.addFake(rpiBox, crateAdapter, 1)
	a.Add(item("aa", "Year One", "/remote/books", "completed", 1))
	a.Add(item("a2", "Hush", "/remote/books", "completed", 1))
	b.Add(item("bb", "Owls", "/mnt/rpi/dl", "completed", 1))
	p := h.newPool(download.PoolOptions{Paths: mapper(map[integrations.InstanceID][]download.Mapping{
		"seedbox": {{Remote: "/remote/library", Local: "/srv/library"}},
		"rpi-box": {{Remote: "/mnt/rpi/library", Local: "/srv/library"}},
	})})

	refs := []download.ItemRef{{Instance: seedbox, ID: "aa"}, {Instance: rpiBox, ID: "bb"}, {Instance: seedbox, ID: "a2"}, {Instance: seedbox, ID: "aa"}}
	if err := p.Relocate(context.Background(), refs, "/srv/library/Batman"); err != nil {
		t.Fatal(err)
	}
	if a.Calls("relocate") != 1 || b.Calls("relocate") != 1 {
		t.Errorf("relocate calls: seedbox=%d rpi-box=%d, want one each", a.Calls("relocate"), b.Calls("relocate"))
	}
	am, bm := a.Scenario().Moves(), b.Scenario().Moves()
	if len(am) != 1 || am[0].Dir != "/remote/library/Batman" || !slices.Equal(am[0].IDs, []download.ItemID{"aa", "a2"}) {
		t.Errorf("seedbox saw %+v", am)
	}
	if len(bm) != 1 || bm[0].Dir != "/mnt/rpi/library/Batman" || !slices.Equal(bm[0].IDs, []download.ItemID{"bb"}) {
		t.Errorf("rpi-box saw %+v", bm)
	}
	if err := p.Relocate(context.Background(), nil, "/srv/library"); err != nil {
		t.Errorf("nothing to move: %v", err)
	}
}

// Nothing is sent until every instance accepted the call.
func TestPool_RelocateValidatesBeforeSending(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]struct {
		prepare func(h *harness, b *coretest.FakeDownloadClient)
		class   integrations.Class
		mapper  download.PathMapper
	}{
		"a second instance with no mapping": {mapper: mapper(map[integrations.InstanceID][]download.Mapping{
			"seedbox": {{Remote: "/remote/library", Local: "/srv/library"}},
		}), class: integrations.ClassInvalid},
		"a second instance without the capability": {
			mapper: mapper(map[integrations.InstanceID][]download.Mapping{
				"seedbox": {{Remote: "/remote/library", Local: "/srv/library"}},
				"rpi-box": {{Remote: "/mnt/rpi/library", Local: "/srv/library"}},
			}),
			prepare: func(_ *harness, b *coretest.FakeDownloadClient) { b.SetCaps(download.Caps{Protocol: download.Torrent}) },
			class:   integrations.ClassUnsupported,
		},
		"a second instance that is not a Relocator": {
			mapper: mapper(map[integrations.InstanceID][]download.Mapping{
				"seedbox": {{Remote: "/remote/library", Local: "/srv/library"}},
				"rpi-box": {{Remote: "/mnt/rpi/library", Local: "/srv/library"}},
			}),
			prepare: func(h *harness, b *coretest.FakeDownloadClient) {
				h.serve(rpiBox, crateAdapter, b.ClientOnly())
			},
			class: integrations.ClassUnsupported,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			a := h.addFake(seedbox, crateAdapter, 0)
			b := h.addFake(rpiBox, crateAdapter, 1)
			a.Add(item("aa", "Year One", "/remote/books", "completed", 1))
			b.Add(item("bb", "Owls", "/mnt/rpi/dl", "completed", 1))
			if setup.prepare != nil {
				setup.prepare(h, b)
			}
			p := h.newPool(download.PoolOptions{Paths: setup.mapper})
			err := p.Relocate(ctx, []download.ItemRef{{Instance: seedbox, ID: "aa"}, {Instance: rpiBox, ID: "bb"}}, "/srv/library/Batman")
			if classOf(err) != setup.class {
				t.Fatalf("class = %v (%v), want %v", classOf(err), err, setup.class)
			}
			if a.Calls("relocate") != 0 || len(a.Scenario().Moves()) != 0 || len(b.Scenario().Moves()) != 0 {
				t.Errorf("a refused Relocate still sent something: seedbox calls=%d", a.Calls("relocate"))
			}
		})
	}
}

func TestPool_RelocateJoinsInstanceErrors(t *testing.T) {
	h := newHarness(t)
	a := h.addFake(seedbox, crateAdapter, 0)
	b := h.addFake(rpiBox, crateAdapter, 1)
	a.Add(item("aa", "Year One", "/remote/books", "completed", 1))
	b.Add(item("bb", "Owls", "/mnt/rpi/dl", "completed", 1))
	p := h.newPool(download.PoolOptions{Paths: mapper(map[integrations.InstanceID][]download.Mapping{
		"seedbox": {{Remote: "/remote/library", Local: "/srv/library"}},
		"rpi-box": {{Remote: "/mnt/rpi/library", Local: "/srv/library"}},
	})})
	a.Fail("relocate", &integrations.Error{Ref: seedbox, Op: "relocate", Class: integrations.ClassTransient})
	err := p.Relocate(context.Background(), []download.ItemRef{{Instance: seedbox, ID: "aa"}, {Instance: rpiBox, ID: "bb"}}, "/srv/library/Batman")
	if err == nil || !strings.Contains(err.Error(), "seedbox") || strings.Contains(err.Error(), "rpi-box") {
		t.Fatalf("err = %v, want only seedbox's failure", err)
	}
	if len(b.Scenario().Moves()) != 1 {
		t.Error("the healthy instance was not moved: one failure must not cancel the others")
	}
}

// The fan-out over instances is bounded by PoolOptions.Parallelism.
func TestPool_RelocateFanOutIsBounded(t *testing.T) {
	const instances, limit = 8, 3
	h := newHarness(t)
	var running, peak atomic.Int64
	rows := map[integrations.InstanceID][]download.Mapping{}
	var refs []download.ItemRef
	for i := range instances {
		ref := integrations.Ref{Kind: integrations.KindDownload, Instance: integrations.InstanceID("box-" + string(rune('a'+i)))}
		h.serve(ref, crateAdapter, &slowRelocator{FakeDownloadClient: coretest.NewFakeDownloadClient(ref.Instance), running: &running, peak: &peak})
		rows[ref.Instance] = []download.Mapping{{Remote: "/remote/library", Local: "/srv/library"}}
		refs = append(refs, download.ItemRef{Instance: ref, ID: "aa"})
	}
	p := h.newPool(download.PoolOptions{Paths: mapper(rows), Parallelism: limit})
	if err := p.Relocate(context.Background(), refs, "/srv/library/Batman"); err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got > limit || got < 2 {
		t.Errorf("peak concurrency = %d, want between 2 and %d", got, limit)
	}
}

// slowRelocator records how many SetLocation calls overlap.
type slowRelocator struct {
	*coretest.FakeDownloadClient
	running, peak *atomic.Int64
}

func (s *slowRelocator) SetLocation(context.Context, []download.ItemID, string) error {
	n := s.running.Add(1)
	defer s.running.Add(-1)
	for {
		old := s.peak.Load()
		if n <= old || s.peak.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	return nil
}

func TestPaths_UnmappedRelocateRefused(t *testing.T) {
	for name, paths := range map[string]download.PathMapper{
		"no mapper":              nil,
		"no mapping for the dir": mapper(map[integrations.InstanceID][]download.Mapping{"seedbox": {{Remote: "/remote/library", Local: "/srv/other"}}}),
		"a prefix is not a dir":  mapper(map[integrations.InstanceID][]download.Mapping{"seedbox": {{Remote: "/remote/library", Local: "/srv/lib"}}}),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			f := h.addFake(seedbox, crateAdapter, 0)
			f.Add(item("aa", "Year One", "/remote/books", "completed", 1))
			p := h.newPool(download.PoolOptions{Paths: paths})
			err := p.Relocate(context.Background(), []download.ItemRef{{Instance: seedbox, ID: "aa"}}, "/srv/library/Batman")
			e, ok := errors.AsType[*integrations.Error](err)
			if !ok || e.Class != integrations.ClassInvalid || !strings.Contains(err.Error(), "no mapping covers /srv/library/Batman") {
				t.Fatalf("err = %v", err)
			}
			if len(f.Scenario().Moves()) != 0 || f.Calls("relocate") != 0 {
				t.Errorf("a host path the client cannot see was sent: %+v", f.Scenario().Moves())
			}
		})
	}
}

func TestPool_RelocateEmptyDirIsRefused(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	p := h.newPool(download.PoolOptions{Paths: standardMappings()})
	err := p.Relocate(context.Background(), []download.ItemRef{{Instance: seedbox, ID: "aa"}}, "")
	if classOf(err) != integrations.ClassInvalid || f.Calls("relocate") != 0 {
		t.Errorf("err = %v, calls = %d", err, f.Calls("relocate"))
	}
}

func TestPool_IntegrationsDisabledNoClientCall(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	f.Add(item("aa", "Year One", "/remote/books", "completed", 1))
	p := h.newPool(download.PoolOptions{Paths: standardMappings()})
	ctx := context.Background()
	ref := download.ItemRef{Instance: seedbox, ID: "aa"}
	before := h.buildCount(seedbox)

	check := func(label string) {
		t.Helper()
		_, e1 := p.Items(ctx, seedbox, download.ItemFilter{})
		_, e2 := p.Files(ctx, ref)
		e3 := p.Relocate(ctx, []download.ItemRef{ref}, "/srv/library/incoming/Batman")
		_, _, e4 := p.Lease(ctx, seedbox)
		for i, err := range []error{e1, e2, e3, e4} {
			if !errors.Is(err, integrations.ErrDisabled) || classOf(err) != integrations.ClassRefusedLocal {
				t.Errorf("%s: call %d: %v, want a local refusal (ErrDisabled)", label, i, err)
			}
		}
		for _, op := range []string{"items", "files", "relocate", "caps", "status"} {
			if f.Calls(op) != 0 {
				t.Errorf("%s: the client received %s (%d calls)", label, op, f.Calls(op))
			}
		}
		if h.buildCount(seedbox) != before {
			t.Errorf("%s: a refused lease built the instance", label)
		}
	}

	h.disabled.Store(true) // core.integrations.disabled
	check("integrations disabled")
	h.disabled.Store(false)
	h.put(seedbox, crateAdapter, 0, false) // the instance itself disabled
	check("instance disabled")
	if got := p.Instances(0); len(got) != 0 {
		t.Errorf("Instances lists a disabled instance: %v", got)
	}

	h.put(seedbox, crateAdapter, 0, true)
	if _, err := p.Items(ctx, seedbox, download.ItemFilter{}); err != nil {
		t.Errorf("re-enabled: %v", err)
	}
}

func TestPool_Instances(t *testing.T) {
	h := newHarness(t)
	h.addFake(seedbox, crateAdapter, 5)
	h.addFake(rpiBox, crateAdapter, 1)
	h.addFake(wayne, nzbAdapter, 1)
	off := integrations.Ref{Kind: integrations.KindDownload, Instance: "off"}
	h.addFake(off, crateAdapter, 0)
	h.put(off, crateAdapter, 0, false)
	p := h.newPool(download.PoolOptions{Paths: standardMappings()})

	// Priority, then ID; the disabled instance is absent.
	want := []integrations.Ref{rpiBox, wayne, seedbox}
	if got := p.Instances(0); !slices.Equal(got, want) {
		t.Errorf("Instances(0) = %v, want %v", got, want)
	}
	// No protocol is known before a Caps answered: every instance is listed.
	if got := p.Instances(download.Usenet); !slices.Equal(got, want) {
		t.Errorf("Instances(Usenet) before any Caps = %v", got)
	}
	// Learn the protocols: the nzb adapter's client says usenet.
	h.fakes["wayne-nzb"].SetCaps(download.Caps{Protocol: download.Usenet})
	for _, ref := range []integrations.Ref{wayne, rpiBox} {
		_ = p.Relocate(context.Background(), []download.ItemRef{{Instance: ref, ID: "x"}}, "/srv/library/x")
	}
	if got := p.Instances(download.Usenet); !slices.Equal(got, []integrations.Ref{wayne}) {
		t.Errorf("Instances(Usenet) = %v, want only the nzb instance", got)
	}
	if got := p.Instances(download.Torrent); !slices.Equal(got, []integrations.Ref{rpiBox, seedbox}) {
		t.Errorf("Instances(Torrent) = %v, want the torrent instances (seedbox is unlearned but shares its adapter)", got)
	}
}

func TestPool_Files(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	f.Add(coretest.ScenarioItem{ID: "aa", Name: "Year One", SavePath: "/remote/books/", Native: "completed", Progress: 1, Files: []download.File{
		{Path: "Year One/part1.m4b", Size: 800, Progress: 1, Priority: 1},
		{Path: "/Year One/cover.jpg", Size: 200, Progress: 1, Priority: 1},
	}})
	f.Add(item("bb", "Hush", "/elsewhere", "completed", 1))
	p := h.newPool(download.PoolOptions{Paths: standardMappings()})
	ctx := context.Background()

	files, err := p.Files(ctx, download.ItemRef{Instance: seedbox, ID: "aa"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].LocalPath != "/srv/library/incoming/Year One/part1.m4b" || files[1].LocalPath != "/srv/library/incoming/Year One/cover.jpg" {
		t.Errorf("files = %+v", files)
	}
	if files, err = p.Files(ctx, download.ItemRef{Instance: seedbox, ID: "bb"}); err != nil || len(files) != 1 || files[0].LocalPath != "" {
		t.Errorf("files of an unmapped item = %+v, %v", files, err)
	}
	if _, err := p.Files(ctx, download.ItemRef{Instance: seedbox, ID: "zz"}); classOf(err) != integrations.ClassNotFound {
		t.Errorf("an item the client does not hold: %v", err)
	}
	f.SetCaps(download.Caps{Protocol: download.Torrent})
	h.clock.advance(2 * time.Hour) // past the caps TTL
	if _, err := p.Files(ctx, download.ItemRef{Instance: seedbox, ID: "aa"}); classOf(err) != integrations.ClassUnsupported {
		t.Errorf("Files with Caps.Files off: %v", err)
	}
}

func TestPool_FilesWithoutFileLister(t *testing.T) {
	h := newHarness(t)
	f := coretest.NewFakeDownloadClient("seedbox")
	f.Add(item("aa", "Year One", "/remote/books", "completed", 1))
	h.serve(seedbox, crateAdapter, f.ClientOnly())
	p := h.newPool(download.PoolOptions{Paths: standardMappings()})
	if _, err := p.Files(context.Background(), download.ItemRef{Instance: seedbox, ID: "aa"}); classOf(err) != integrations.ClassUnsupported {
		t.Errorf("err = %v, want unsupported", err)
	}
	if f.Calls("files") != 0 {
		t.Error("Files was called on a client that has no FileLister")
	}
}

func TestPool_CapsAreCachedForTheTTL(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	f.Add(item("aa", "Year One", "/remote/books", "completed", 1))
	p := h.newPool(download.PoolOptions{Paths: standardMappings(), CapsTTL: time.Hour})
	ctx := context.Background()
	ref := download.ItemRef{Instance: seedbox, ID: "aa"}
	files := func() {
		t.Helper()
		if _, err := p.Files(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	files()
	files()
	h.clock.advance(59 * time.Minute)
	files()
	if f.Calls("caps") != 1 {
		t.Errorf("caps calls = %d within the TTL, want 1", f.Calls("caps"))
	}
	h.clock.advance(2 * time.Minute)
	files()
	if f.Calls("caps") != 2 {
		t.Errorf("caps calls = %d after the TTL, want 2", f.Calls("caps"))
	}
	// A new row version (a settings change) drops the cache even inside the TTL.
	h.put(seedbox, crateAdapter, 7, true)
	files()
	if f.Calls("caps") != 3 {
		t.Errorf("caps calls = %d after the instance changed, want 3", f.Calls("caps"))
	}
	// A failing Caps is an error, not a cached "no".
	f.Fail("caps", &integrations.Error{Class: integrations.ClassTransient})
	h.clock.advance(2 * time.Hour)
	if _, err := p.Files(ctx, ref); classOf(err) != integrations.ClassTransient {
		t.Errorf("err = %v", err)
	}
}

func TestPool_ConcurrentUseIsSafe(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	for i := range 20 {
		f.Add(item(download.ItemID("a"+string(rune('a'+i))), "Book", "/remote/books", "completed", 1))
	}
	p := h.newPool(download.PoolOptions{Paths: standardMappings()})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			ctx := context.Background()
			if _, err := p.Items(ctx, seedbox, download.ItemFilter{}); err != nil {
				t.Error(err)
			}
			if _, err := p.Files(ctx, download.ItemRef{Instance: seedbox, ID: "aa"}); err != nil {
				t.Error(err)
			}
			_ = p.Instances(0)
		})
	}
	wg.Wait()
}

func TestNewPool_NeedsARegistry(t *testing.T) {
	if _, err := download.NewPool(nil, download.PoolOptions{}); err == nil {
		t.Error("NewPool(nil) succeeded")
	}
}

func TestAdapter_ForcesTheDownloadKind(t *testing.T) {
	d := download.Adapter(integrations.Meta{ID: "hello.crate", Kind: integrations.KindAI}, nil)
	if d.Meta().Kind != integrations.KindDownload {
		t.Errorf("Kind = %q, want download whatever Meta said", d.Meta().Kind)
	}
}

func TestItemFilter_Match(t *testing.T) {
	it := download.Item{Ref: download.ItemRef{ID: "aa"}, Category: "Audiobooks", Tags: []string{"batman", "dc"}, State: download.StateCompleted}
	for name, tc := range map[string]struct {
		f    download.ItemFilter
		want bool
	}{
		"zero":                     {download.ItemFilter{}, true},
		"id":                       {download.ItemFilter{IDs: []download.ItemID{"bb", "aa"}}, true},
		"other id":                 {download.ItemFilter{IDs: []download.ItemID{"bb"}}, false},
		"category any case":        {download.ItemFilter{Categories: []string{"x", " AUDIOBOOKS "}}, true},
		"other category":           {download.ItemFilter{Categories: []string{"comics"}}, false},
		"all tags":                 {download.ItemFilter{Tags: []string{"batman", "dc"}}, true},
		"one tag missing":          {download.ItemFilter{Tags: []string{"batman", "marvel"}}, false},
		"state":                    {download.ItemFilter{States: []download.State{download.StatePaused, download.StateCompleted}}, true},
		"other state":              {download.ItemFilter{States: []download.State{download.StatePaused}}, false},
		"every field must narrow":  {download.ItemFilter{IDs: []download.ItemID{"aa"}, Categories: []string{"comics"}}, false},
		"empty category is a wild": {download.ItemFilter{Categories: []string{}}, true},
	} {
		if got := tc.f.Match(it); got != tc.want {
			t.Errorf("%s: Match = %v, want %v", name, got, tc.want)
		}
	}
}

func TestStringers(t *testing.T) {
	for want, got := range map[string]string{
		"torrent": download.Torrent.String(), "usenet": download.Usenet.String(), "unknown": download.Protocol(0).String(),
		"queued": download.StateQueued.String(), "paused": download.StatePaused.String(), "downloading": download.StateDownloading.String(),
		"completed": download.StateCompleted.String(), "failed": download.StateFailed.String(), "warning": download.StateWarning.String(),
		"state(42)":           download.State(42).String(),
		"download/seedbox#aa": download.ItemRef{Instance: seedbox, ID: "aa"}.String(),
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if download.StateUnknown.String() != "unknown" {
		t.Errorf("StateUnknown = %q", download.StateUnknown.String())
	}
}
