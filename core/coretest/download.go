// file: core/coretest/download.go
// version: 1.0.0
// guid: 9ad40ee5-d0fe-456f-8998-36f34a9ac675
// last-edited: 2026-11-11

package coretest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
)

// The download adapter test kit (05-download §9): DownloadConformance, the one
// suite every download adapter passes, and FakeDownloadClient, the in-memory
// client modules test against. Both stand on DownloadScenario, a neutral
// description of a remote download client's world: an adapter's test turns the
// scenario into its own wire (a Deluge Web UI, a qBittorrent API) and the suite
// drives the adapter against it, so one suite covers adapters whose protocols
// share nothing.

// ErrUnknownDownloadItem is what DownloadScenario.Move reports for an item the
// scenario does not hold; a wire translator answers it the way its client
// answers an unknown id.
var ErrUnknownDownloadItem = errors.New("coretest: unknown download item")

// ScenarioItem is one download of a DownloadScenario, in the client's own
// terms: Native is the state string the client reports ("Seeding", "Paused",
// "Hibernating"), which an adapter's table maps to a download.State.
type ScenarioItem struct {
	ID        download.ItemID
	Name      string
	Category  string
	SavePath  string  // remote
	Native    string  // the client's own state name
	Progress  float64 // 0..1
	TotalSize int64
	Ratio     float64
	Added     time.Time
	Files     []download.File
}

// ScenarioMove is one accepted relocation.
type ScenarioMove struct {
	IDs []download.ItemID
	Dir string
}

// DownloadScenario is the remote client's world: its items, its identity and
// its session. All methods are safe for concurrent use, since a wire
// translator serves requests from several goroutines.
type DownloadScenario struct {
	mu           sync.Mutex
	items        map[download.ItemID]ScenarioItem
	version      string
	defaultSave  string
	freeSpace    int64
	categories   []string
	logins       int
	sessionValid bool
	rejectLogins bool
	moves        []ScenarioMove
}

// NewDownloadScenario is an empty scenario: no items, version "1.0.0", free
// space unknown, no session yet.
func NewDownloadScenario() *DownloadScenario {
	return &DownloadScenario{items: map[download.ItemID]ScenarioItem{}, version: "1.0.0", freeSpace: -1}
}

// Put adds or replaces an item.
func (s *DownloadScenario) Put(it ScenarioItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[it.ID] = it
}

// Item reads one item.
func (s *DownloadScenario) Item(id download.ItemID) (ScenarioItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	return it, ok
}

// Items lists every item, by ID.
func (s *DownloadScenario) Items() []ScenarioItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.SortedFunc(maps.Values(s.items), func(a, b ScenarioItem) int { return strings.Compare(string(a.ID), string(b.ID)) })
}

// Update changes one item under the lock; false when it is unknown.
func (s *DownloadScenario) Update(id download.ItemID, f func(*ScenarioItem)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if ok {
		f(&it)
		s.items[id] = it
	}
	return ok
}

// SetIdentity sets what Status reports: the client version, its default save
// path (remote), its free space in bytes (-1 unknown) and its categories.
func (s *DownloadScenario) SetIdentity(version, defaultSave string, freeSpace int64, categories ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version, s.defaultSave, s.freeSpace, s.categories = version, defaultSave, freeSpace, slices.Clone(categories)
}

// Identity reads what SetIdentity set.
func (s *DownloadScenario) Identity() (version, defaultSave string, freeSpace int64, categories []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version, s.defaultSave, s.freeSpace, slices.Clone(s.categories)
}

// Login is the client's login: it counts the attempt and opens the session
// unless the scenario rejects credentials. It returns whether the login worked.
func (s *DownloadScenario) Login() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logins++
	if s.rejectLogins {
		return false
	}
	s.sessionValid = true
	return true
}

// Authorized reports whether a request may run: a session is open.
func (s *DownloadScenario) Authorized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionValid
}

// ExpireSession closes the session server-side: the next request needs a login.
func (s *DownloadScenario) ExpireSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionValid = false
}

// RejectCredentials makes every login fail (or work again).
func (s *DownloadScenario) RejectCredentials(reject bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectLogins = reject
}

// Logins is the number of login attempts so far.
func (s *DownloadScenario) Logins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins
}

// Move relocates items' data: every item's SavePath becomes dir, atomically; an
// unknown id fails the whole move with ErrUnknownDownloadItem and changes nothing.
func (s *DownloadScenario) Move(ids []download.ItemID, dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if _, ok := s.items[id]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownDownloadItem, id)
		}
	}
	for _, id := range ids {
		it := s.items[id]
		it.SavePath = dir
		s.items[id] = it
	}
	s.moves = append(s.moves, ScenarioMove{IDs: slices.Clone(ids), Dir: dir})
	return nil
}

// Moves lists the accepted relocations, oldest first.
func (s *DownloadScenario) Moves() []ScenarioMove {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.moves)
}

// ---- the conformance suite ------------------------------------------------

// DownloadFactory builds the adapter under test over a wire that serves s: the
// adapter's test starts its fake remote (reading and writing s through the
// DownloadScenario methods) and returns the adapter's client for it. The suite
// closes the client.
type DownloadFactory func(t testing.TB, s *DownloadScenario) download.Client

// DownloadStateCase is one row of an adapter's state golden table: the client's
// native state at a progress (0..1) and what the Item must say.
type DownloadStateCase struct {
	Native   string
	Progress float64
	Want     download.State
	Seeding  bool
	// Unknown marks a native state the adapter's table does not know: the Item
	// must be StateWarning and carry download.RawUnknownState.
	Unknown bool
}

// DownloadFixtures is what the suite cannot know about an adapter.
type DownloadFixtures struct {
	// States is the adapter's golden table of native states; the suite checks
	// every row. It should include a native state the adapter does not know.
	States []DownloadStateCase
	// ReadyState is a native state that is neither seeding nor complete, for
	// the suite's plain items (typically the client's "Downloading").
	ReadyState string
	// SeedingState is the native state of a finished, still-seeding torrent.
	SeedingState string
	// Live, when set, builds a client for a real instance (a container the
	// operator started). It runs only when MEDIACORE_LIVE_DOWNLOAD=1, and only
	// the read-only cases: nothing is added, moved or changed.
	Live func(t testing.TB) download.Client
}

// The suite's ids: valid 40-hex torrent ids of Batman-themed fixtures.
const (
	conformanceYearOne  download.ItemID = "00000000000000000000000000000000ba7a0001"
	conformanceHush     download.ItemID = "00000000000000000000000000000000ba7a0002"
	conformanceOwls     download.ItemID = "00000000000000000000000000000000ba7a0003"
	conformanceNotThere download.ItemID = "00000000000000000000000000000000ba7a00ff"
)

// DownloadConformance is the suite every download adapter passes (05-download
// §9, phase-1 cases): the items state golden table, item filters, status,
// files, relocate, auth failure and re-login, unknown state, a cancelled ctx.
// Optional capabilities are exercised when the client implements them and
// claims them in Caps; a client that implements neither has those cases
// skipped, so the suite also runs against a minimal adapter.
func DownloadConformance(t *testing.T, factory DownloadFactory, fx DownloadFixtures) {
	t.Helper()
	if len(fx.States) == 0 || fx.ReadyState == "" || fx.SeedingState == "" {
		t.Fatal("coretest.DownloadConformance: DownloadFixtures needs States, ReadyState and SeedingState")
	}
	ctx := context.Background()

	newClient := func(t *testing.T) (download.Client, *DownloadScenario) {
		t.Helper()
		s := NewDownloadScenario()
		s.SetIdentity("2.1.1", "/remote/incoming", 4096, "audiobooks", "comics")
		s.Put(ScenarioItem{ID: conformanceYearOne, Name: "Batman: Year One", Category: "Audiobooks", SavePath: "/remote/books", Native: fx.ReadyState, Progress: 0.4, TotalSize: 1000,
			Files: []download.File{{Path: "Batman: Year One/part1.m4b", Size: 800, Progress: 0.5, Priority: 1}, {Path: "Batman: Year One/cover.jpg", Size: 200, Progress: 0.1, Priority: 1}}})
		s.Put(ScenarioItem{ID: conformanceHush, Name: "Batman: Hush", Category: "comics", SavePath: "/remote/books", Native: fx.SeedingState, Progress: 1, TotalSize: 500,
			Files: []download.File{{Path: "Batman: Hush/hush.m4b", Size: 500, Progress: 1, Priority: 1}}})
		s.Put(ScenarioItem{ID: conformanceOwls, Name: "Batman: Court of Owls", Category: "audiobooks", SavePath: "/remote/owls", Native: fx.SeedingState, Progress: 1, TotalSize: 300,
			Files: []download.File{{Path: "Batman: Court of Owls/owls.m4b", Size: 300, Progress: 1, Priority: 1}}})
		c := factory(t, s)
		t.Cleanup(func() {
			if err := c.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
		return c, s
	}

	t.Run("Caps", func(t *testing.T) {
		c, _ := newClient(t)
		caps, err := c.Caps(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if caps.Protocol == 0 {
			t.Error("Caps.Protocol is unset")
		}
		_, isFiles := c.(download.FileLister)
		_, isReloc := c.(download.Relocator)
		if caps.Files != isFiles || caps.Relocate != isReloc {
			t.Errorf("Caps claims files=%v relocate=%v but the client implements files=%v relocate=%v", caps.Files, caps.Relocate, isFiles, isReloc)
		}
	})

	t.Run("Items/StateTable", func(t *testing.T) {
		for _, tc := range fx.States {
			t.Run(fmt.Sprintf("%s@%g", tc.Native, tc.Progress), func(t *testing.T) {
				c, s := newClient(t)
				s.Update(conformanceYearOne, func(it *ScenarioItem) { it.Native, it.Progress = tc.Native, tc.Progress })
				items, err := c.Items(ctx, download.ItemFilter{IDs: []download.ItemID{conformanceYearOne}})
				if err != nil {
					t.Fatal(err)
				}
				if len(items) != 1 {
					t.Fatalf("got %d items, want 1", len(items))
				}
				it := items[0]
				assertDownloadItem(t, it)
				_, flagged := it.Raw[download.RawUnknownState]
				if it.State != tc.Want || it.Seeding != tc.Seeding || flagged != tc.Unknown {
					t.Errorf("state=%v seeding=%v unknown=%v; want %v %v %v", it.State, it.Seeding, flagged, tc.Want, tc.Seeding, tc.Unknown)
				}
				if tc.Unknown && it.State == download.StateCompleted {
					t.Error("an unknown client state was reported as Completed: a wrong Completed starts an import")
				}
			})
		}
	})

	t.Run("Items/Fields", func(t *testing.T) {
		c, s := newClient(t)
		items, err := c.Items(ctx, download.ItemFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 3 {
			t.Fatalf("got %d items, want 3", len(items))
		}
		for _, it := range items {
			assertDownloadItem(t, it)
			want, _ := s.Item(it.Ref.ID)
			if it.Name != want.Name || it.Category != want.Category || it.RemotePath != want.SavePath || it.TotalSize != want.TotalSize {
				t.Errorf("%s: name=%q category=%q remote=%q size=%d; want %+v", it.Ref.ID, it.Name, it.Category, it.RemotePath, it.TotalSize, want)
			}
			if it.LocalPath != "" {
				t.Errorf("%s: an adapter must leave LocalPath to the Pool, got %q", it.Ref.ID, it.LocalPath)
			}
		}
	})

	t.Run("Items/Filters", func(t *testing.T) {
		c, _ := newClient(t)
		ids := func(f download.ItemFilter) string {
			t.Helper()
			items, err := c.Items(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(items))
			for _, it := range items {
				got = append(got, string(it.Ref.ID))
			}
			slices.Sort(got)
			return strings.Join(got, ",")
		}
		join := func(v ...download.ItemID) string {
			s := make([]string, len(v))
			for i, id := range v {
				s[i] = string(id)
			}
			slices.Sort(s)
			return strings.Join(s, ",")
		}
		for name, tc := range map[string]struct {
			f    download.ItemFilter
			want string
		}{
			"zero filter selects all":  {download.ItemFilter{}, join(conformanceYearOne, conformanceHush, conformanceOwls)},
			"by id":                    {download.ItemFilter{IDs: []download.ItemID{conformanceHush}}, join(conformanceHush)},
			"by id, upper case":        {download.ItemFilter{IDs: []download.ItemID{download.ItemID(strings.ToUpper(string(conformanceHush)))}}, join(conformanceHush)},
			"unknown id is empty":      {download.ItemFilter{IDs: []download.ItemID{conformanceNotThere}}, ""},
			"category, any case":       {download.ItemFilter{Categories: []string{"AUDIOBOOKS"}}, join(conformanceYearOne, conformanceOwls)},
			"category and id":          {download.ItemFilter{Categories: []string{"audiobooks"}, IDs: []download.ItemID{conformanceOwls}}, join(conformanceOwls)},
			"category matches nothing": {download.ItemFilter{Categories: []string{"podcasts"}}, ""},
			"by state":                 {download.ItemFilter{States: []download.State{download.StateCompleted}}, join(conformanceHush, conformanceOwls)},
		} {
			if got := ids(tc.f); got != tc.want {
				t.Errorf("%s: got %q, want %q", name, got, tc.want)
			}
		}
	})

	t.Run("Status", func(t *testing.T) {
		c, s := newClient(t)
		st, err := c.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		version, save, free, cats := s.Identity()
		if st.Version != version || st.DefaultSavePath != save || st.FreeSpace != free {
			t.Errorf("status = %+v, want version %q save %q free %d", st, version, save, free)
		}
		if st.FreeSpace < -1 {
			t.Errorf("FreeSpace %d: unknown is -1", st.FreeSpace)
		}
		for _, name := range cats {
			if st.Categories != nil {
				if _, ok := st.Categories[name]; !ok {
					t.Errorf("category %q is missing from %v", name, st.Categories)
				}
			}
		}
	})

	t.Run("Files", func(t *testing.T) {
		c, s := newClient(t)
		fl, ok := c.(download.FileLister)
		if !ok {
			t.Skip("the adapter has no FileLister")
		}
		got, err := fl.Files(ctx, conformanceYearOne)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := s.Item(conformanceYearOne)
		if len(got) != len(want.Files) {
			t.Fatalf("got %d files, want %d", len(got), len(want.Files))
		}
		for i, f := range got {
			w := want.Files[i]
			if f.Path != w.Path || f.Size != w.Size || f.Priority != w.Priority || f.Progress < 0 || f.Progress > 1 || f.LocalPath != "" {
				t.Errorf("file %d = %+v, want %+v", i, f, w)
			}
			if d := f.Progress - w.Progress; d > 1e-6 || d < -1e-6 {
				t.Errorf("file %d progress = %v, want %v", i, f.Progress, w.Progress)
			}
		}
		if _, err := fl.Files(ctx, conformanceNotThere); integrations.ClassOf(ctx, err) != integrations.ClassNotFound {
			t.Errorf("Files of an unknown item: %v, want class not_found", err)
		}
	})

	t.Run("Relocate", func(t *testing.T) {
		c, s := newClient(t)
		r, ok := c.(download.Relocator)
		if !ok {
			t.Skip("the adapter has no Relocator")
		}
		if err := r.SetLocation(ctx, []download.ItemID{conformanceHush, conformanceOwls}, "/remote/library"); err != nil {
			t.Fatal(err)
		}
		moves := s.Moves()
		if len(moves) != 1 || moves[0].Dir != "/remote/library" || len(moves[0].IDs) != 2 {
			t.Fatalf("the remote saw %+v", moves)
		}
		items, err := c.Items(ctx, download.ItemFilter{IDs: []download.ItemID{conformanceHush, conformanceOwls}})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.RemotePath != "/remote/library" {
				t.Errorf("%s: RemotePath = %q after the move", it.Ref.ID, it.RemotePath)
			}
		}
		err = r.SetLocation(ctx, []download.ItemID{conformanceNotThere}, "/remote/library")
		if integrations.ClassOf(ctx, err) != integrations.ClassNotFound {
			t.Errorf("SetLocation of an unknown item: %v, want class not_found", err)
		}
		if len(s.Moves()) != 1 {
			t.Error("a failed move was recorded as accepted")
		}
	})

	t.Run("Auth/Rejected", func(t *testing.T) {
		c, s := newClient(t)
		s.RejectCredentials(true)
		if _, err := c.Items(ctx, download.ItemFilter{}); integrations.ClassOf(ctx, err) != integrations.ClassAuth {
			t.Errorf("Items with rejected credentials: %v, want class auth", err)
		}
		if _, err := c.Status(ctx); integrations.ClassOf(ctx, err) != integrations.ClassAuth {
			t.Errorf("Status with rejected credentials: %v, want class auth", err)
		}
		if h := c.Health(ctx); h.Status != integrations.HealthAuthFailed {
			t.Errorf("Health = %v, want auth_failed", h.Status)
		}
		s.RejectCredentials(false) // the operator fixed the password
		if _, err := c.Items(ctx, download.ItemFilter{}); err != nil {
			t.Errorf("Items after the credentials work again: %v", err)
		}
	})

	t.Run("Auth/ReloginOnExpiredSession", func(t *testing.T) {
		c, s := newClient(t)
		if _, err := c.Items(ctx, download.ItemFilter{}); err != nil {
			t.Fatal(err)
		}
		before := s.Logins()
		s.ExpireSession()
		if _, err := c.Items(ctx, download.ItemFilter{}); err != nil {
			t.Fatalf("an expired session must be renewed silently: %v", err)
		}
		if got := s.Logins() - before; got != 1 {
			t.Errorf("logins after expiry = %d, want exactly 1", got)
		}
	})

	t.Run("Auth/ReloginRejectedDoesNotLoop", func(t *testing.T) {
		c, s := newClient(t)
		if _, err := c.Items(ctx, download.ItemFilter{}); err != nil {
			t.Fatal(err)
		}
		before := s.Logins()
		s.ExpireSession()
		s.RejectCredentials(true)
		if _, err := c.Items(ctx, download.ItemFilter{}); integrations.ClassOf(ctx, err) != integrations.ClassAuth {
			t.Errorf("err = %v, want class auth", err)
		}
		if got := s.Logins() - before; got != 1 {
			t.Errorf("login attempts = %d, want 1: one re-login, no loop", got)
		}
	})

	t.Run("Context/Canceled", func(t *testing.T) {
		c, _ := newClient(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := c.Items(cctx, download.ItemFilter{}); integrations.ClassOf(cctx, err) != integrations.ClassCanceled {
			t.Errorf("Items on a cancelled ctx: %v, want class canceled", err)
		}
		if _, err := c.Status(cctx); integrations.ClassOf(cctx, err) != integrations.ClassCanceled {
			t.Errorf("Status on a cancelled ctx: %v, want class canceled", err)
		}
	})

	t.Run("Health", func(t *testing.T) {
		c, _ := newClient(t)
		if h := c.Health(ctx); h.Status != integrations.HealthOK {
			t.Errorf("Health = %+v, want ok", h)
		}
	})

	t.Run("Live", func(t *testing.T) {
		if os.Getenv("MEDIACORE_LIVE_DOWNLOAD") != "1" || fx.Live == nil {
			t.Skip("live download conformance needs MEDIACORE_LIVE_DOWNLOAD=1 and DownloadFixtures.Live")
		}
		c := fx.Live(t)
		t.Cleanup(func() {
			if err := c.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
		if _, err := c.Caps(ctx); err != nil {
			t.Fatalf("Caps: %v", err)
		}
		if _, err := c.Status(ctx); err != nil {
			t.Fatalf("Status: %v", err)
		}
		items, err := c.Items(ctx, download.ItemFilter{})
		if err != nil {
			t.Fatalf("Items: %v", err)
		}
		for _, it := range items {
			assertDownloadItem(t, it)
		}
	})
}

// assertDownloadItem checks what every Item must satisfy whatever the client.
func assertDownloadItem(t *testing.T, it download.Item) {
	t.Helper()
	switch {
	case it.Ref.ID == "":
		t.Errorf("an item has no id: %+v", it)
	case it.Protocol == 0:
		t.Errorf("%s: Protocol unset", it.Ref.ID)
	case it.Progress < 0 || it.Progress > 1:
		t.Errorf("%s: Progress %v is outside 0..1", it.Ref.ID, it.Progress)
	case it.RemainingSize < 0 || it.TotalSize < 0 || it.Ratio < 0 || it.ETA < 0:
		t.Errorf("%s: negative number in %+v", it.Ref.ID, it)
	case it.State == download.StateUnknown:
		t.Errorf("%s: StateUnknown: an adapter reports an unmapped state as warning", it.Ref.ID)
	case it.State == download.StateCompleted && it.Progress < 1:
		t.Errorf("%s: Completed at progress %v", it.Ref.ID, it.Progress)
	case it.Seeding && it.State != download.StateCompleted:
		t.Errorf("%s: Seeding in state %v", it.Ref.ID, it.State)
	case it.CanBeRemoved && (it.State != download.StateCompleted || it.Seeding):
		t.Errorf("%s: CanBeRemoved while state=%v seeding=%v", it.Ref.ID, it.State, it.Seeding)
	case len(it.Raw) > 16:
		t.Errorf("%s: Raw holds %d entries, it is bounded", it.Ref.ID, len(it.Raw))
	}
}

// ---- FakeDownloadClient -----------------------------------------------------

// FakeDownloadClient is an in-memory download.Client (05-download §9): a state
// machine a test advances by hand (Advance) or by the clock (Tick), with
// capability toggles (SetCaps) and fault injection (Fail). It implements
// Client, FileLister and Relocator; ClientOnly hides the optional two, for the
// Pool's missing-capability path. It reports through a DownloadScenario, so the
// same scenario can feed a wire translator instead.
//
// Native states of the fake: "queued", "paused", "downloading", "completed",
// "seeding" (completed, still seeding), "failed", "warning". Any other native
// state maps to warning and is flagged download.RawUnknownState.
type FakeDownloadClient struct {
	ref integrations.Ref
	sc  *DownloadScenario
	now func() time.Time

	mu     sync.Mutex
	caps   download.Caps
	faults map[string]error
	calls  map[string]int
	rate   float64 // fraction of an item per second a Tick downloads
}

// FakeDownloadOption configures NewFakeDownloadClient.
type FakeDownloadOption func(*FakeDownloadClient)

// FakeDownloadNow sets the fake's clock (default time.Now).
func FakeDownloadNow(now func() time.Time) FakeDownloadOption {
	return func(f *FakeDownloadClient) { f.now = now }
}

// FakeDownloadScenario makes the fake serve sc (default: a fresh scenario).
func FakeDownloadScenario(sc *DownloadScenario) FakeDownloadOption {
	return func(f *FakeDownloadClient) { f.sc = sc }
}

// NewFakeDownloadClient is a fake of instance download/<instance>, torrent
// protocol, with every phase-1 capability on.
func NewFakeDownloadClient(instance integrations.InstanceID, opts ...FakeDownloadOption) *FakeDownloadClient {
	f := &FakeDownloadClient{
		ref:    integrations.Ref{Kind: integrations.KindDownload, Instance: instance},
		now:    time.Now,
		caps:   download.Caps{Protocol: download.Torrent, Files: true, Relocate: true, Categories: true},
		faults: map[string]error{},
		calls:  map[string]int{},
		rate:   0.1,
	}
	for _, o := range opts {
		o(f)
	}
	if f.sc == nil {
		f.sc = NewDownloadScenario()
	}
	return f
}

var (
	_ download.Client     = (*FakeDownloadClient)(nil)
	_ download.FileLister = (*FakeDownloadClient)(nil)
	_ download.Relocator  = (*FakeDownloadClient)(nil)
)

// Scenario is the world the fake serves.
func (f *FakeDownloadClient) Scenario() *DownloadScenario { return f.sc }

// Ref is the fake's instance.
func (f *FakeDownloadClient) Ref() integrations.Ref { return f.ref }

// Add puts a torrent into the fake, in state "queued" at progress 0 unless it
// says otherwise.
func (f *FakeDownloadClient) Add(it ScenarioItem) {
	if it.Native == "" {
		it.Native = "queued"
	}
	if it.Added.IsZero() {
		it.Added = f.now()
	}
	f.sc.Put(it)
}

// Advance moves an item to state st the way a client would: Completed is
// progress 1, Queued and Paused and Failed keep their progress, Downloading
// leaves it below 1. It reports false for an unknown item.
func (f *FakeDownloadClient) Advance(id download.ItemID, st download.State) bool {
	return f.sc.Update(id, func(it *ScenarioItem) {
		switch st {
		case download.StateCompleted:
			it.Native, it.Progress = "completed", 1
		case download.StateDownloading:
			it.Native = "downloading"
			if it.Progress >= 1 {
				it.Progress = 0.5
			}
		case download.StateQueued:
			it.Native = "queued"
		case download.StatePaused:
			it.Native = "paused"
		case download.StateFailed:
			it.Native = "failed"
		default:
			it.Native = "warning"
		}
	})
}

// Seed makes a completed item a seeding one (or stops its seeding).
func (f *FakeDownloadClient) Seed(id download.ItemID, seeding bool) bool {
	return f.sc.Update(id, func(it *ScenarioItem) {
		if seeding {
			it.Native, it.Progress = "seeding", 1
		} else if it.Native == "seeding" {
			it.Native = "completed"
		}
	})
}

// SetRate sets how much of an item a second of Tick downloads (default 0.1).
func (f *FakeDownloadClient) SetRate(perSecond float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rate = perSecond
}

// Tick lets d of the fake's time pass: every downloading item gains d times
// the rate, and one that reaches 1 completes.
func (f *FakeDownloadClient) Tick(d time.Duration) {
	f.mu.Lock()
	rate := f.rate
	f.mu.Unlock()
	for _, it := range f.sc.Items() {
		if it.Native != "downloading" {
			continue
		}
		f.sc.Update(it.ID, func(it *ScenarioItem) {
			it.Progress += rate * d.Seconds()
			if it.Progress >= 1 {
				it.Native, it.Progress = "completed", 1
			}
		})
	}
}

// SetCaps replaces what Caps reports. Files false makes Files fail with
// ClassUnsupported, Relocate false does the same for SetLocation: the fake
// still implements the interfaces, as an adapter whose caps say no does.
func (f *FakeDownloadClient) SetCaps(c download.Caps) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caps = c
}

// Fail makes every call of op return err until Fail(op, nil). op is one of
// "caps", "status", "items", "files", "relocate", "health".
func (f *FakeDownloadClient) Fail(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.faults, op)
		return
	}
	f.faults[op] = err
}

// Calls is how many times op was called.
func (f *FakeDownloadClient) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

// ClientOnly is the fake seen as a bare download.Client: it implements neither
// FileLister nor Relocator.
func (f *FakeDownloadClient) ClientOnly() download.Client { return clientOnly{f} }

type clientOnly struct{ f *FakeDownloadClient }

func (c clientOnly) Health(ctx context.Context) integrations.Health  { return c.f.Health(ctx) }
func (c clientOnly) Close() error                                    { return c.f.Close() }
func (c clientOnly) Caps(ctx context.Context) (download.Caps, error) { return c.f.Caps(ctx) }
func (c clientOnly) Status(ctx context.Context) (download.ClientStatus, error) {
	return c.f.Status(ctx)
}
func (c clientOnly) Items(ctx context.Context, fl download.ItemFilter) ([]download.Item, error) {
	return c.f.Items(ctx, fl)
}

// enter is the start of every call: count it, honour ctx, apply a fault, then
// the scenario's session (a login when none is open).
func (f *FakeDownloadClient) enter(ctx context.Context, op string) error {
	f.mu.Lock()
	f.calls[op]++
	fault := f.faults[op]
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return &integrations.Error{Ref: f.ref, Op: op, Class: integrations.ClassCanceled, Err: err}
	}
	if fault != nil {
		return fault
	}
	if !f.sc.Authorized() && !f.sc.Login() {
		return &integrations.Error{Ref: f.ref, Op: op, Class: integrations.ClassAuth, Err: errors.New("login rejected")}
	}
	return nil
}

// Health implements integrations.Instance.
func (f *FakeDownloadClient) Health(ctx context.Context) integrations.Health {
	if err := f.enter(ctx, "health"); err != nil {
		if integrations.ClassOf(ctx, err) == integrations.ClassAuth {
			return integrations.Health{Status: integrations.HealthAuthFailed}
		}
		return integrations.Health{Status: integrations.HealthDown}
	}
	v, _, _, _ := f.sc.Identity()
	return integrations.Health{Status: integrations.HealthOK, RemoteVersion: v}
}

// Close implements integrations.Instance.
func (f *FakeDownloadClient) Close() error { return nil }

// Caps implements download.Client.
func (f *FakeDownloadClient) Caps(ctx context.Context) (download.Caps, error) {
	if err := f.enter(ctx, "caps"); err != nil {
		return download.Caps{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caps, nil
}

// Status implements download.Client.
func (f *FakeDownloadClient) Status(ctx context.Context) (download.ClientStatus, error) {
	if err := f.enter(ctx, "status"); err != nil {
		return download.ClientStatus{}, err
	}
	v, save, free, cats := f.sc.Identity()
	st := download.ClientStatus{Version: v, DefaultSavePath: save, FreeSpace: free, Categories: map[string]string{}}
	for _, c := range cats {
		st.Categories[c] = ""
	}
	return st, nil
}

// Items implements download.Client.
func (f *FakeDownloadClient) Items(ctx context.Context, fl download.ItemFilter) ([]download.Item, error) {
	if err := f.enter(ctx, "items"); err != nil {
		return nil, err
	}
	match := fl
	match.IDs = make([]download.ItemID, len(fl.IDs))
	for i, id := range fl.IDs {
		match.IDs[i] = download.ItemID(strings.ToLower(string(id)))
	}
	var out []download.Item
	for _, si := range f.sc.Items() {
		it := f.item(si)
		if match.Match(it) {
			out = append(out, it)
		}
	}
	return out, nil
}

// item is the Item of one scenario row.
func (f *FakeDownloadClient) item(si ScenarioItem) download.Item {
	st, seeding, unknown := fakeState(si.Native)
	it := download.Item{
		Ref:           download.ItemRef{Instance: f.ref, ID: si.ID},
		Protocol:      f.protocol(),
		Name:          si.Name,
		Category:      si.Category,
		State:         st,
		Seeding:       seeding,
		Progress:      min(max(si.Progress, 0), 1),
		TotalSize:     si.TotalSize,
		RemainingSize: max(si.TotalSize-int64(float64(si.TotalSize)*si.Progress), 0),
		Ratio:         max(si.Ratio, 0),
		AddedAt:       si.Added,
		RemotePath:    si.SavePath,
		CanMoveFiles:  st == download.StateCompleted && !seeding,
		CanBeRemoved:  st == download.StateCompleted && !seeding,
	}
	if st == download.StateCompleted {
		it.Progress = 1
		it.RemainingSize = 0
	}
	if unknown {
		it.Raw = map[string]string{download.RawUnknownState: si.Native}
	}
	return it
}

func (f *FakeDownloadClient) protocol() download.Protocol {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caps.Protocol
}

// fakeState maps the fake's native vocabulary.
func fakeState(native string) (st download.State, seeding, unknown bool) {
	switch native {
	case "queued":
		return download.StateQueued, false, false
	case "paused":
		return download.StatePaused, false, false
	case "downloading":
		return download.StateDownloading, false, false
	case "completed":
		return download.StateCompleted, false, false
	case "seeding":
		return download.StateCompleted, true, false
	case "failed":
		return download.StateFailed, false, false
	case "warning":
		return download.StateWarning, false, false
	}
	return download.StateWarning, false, true
}

// Files implements download.FileLister.
func (f *FakeDownloadClient) Files(ctx context.Context, id download.ItemID) ([]download.File, error) {
	if err := f.enter(ctx, "files"); err != nil {
		return nil, err
	}
	if err := f.unsupported("files", func(c download.Caps) bool { return c.Files }); err != nil {
		return nil, err
	}
	si, ok := f.sc.Item(download.ItemID(strings.ToLower(string(id))))
	if !ok {
		return nil, &integrations.Error{Ref: f.ref, Op: "files", Class: integrations.ClassNotFound, Err: fmt.Errorf("%w: %s", ErrUnknownDownloadItem, id)}
	}
	return slices.Clone(si.Files), nil
}

// SetLocation implements download.Relocator.
func (f *FakeDownloadClient) SetLocation(ctx context.Context, ids []download.ItemID, remoteDir string) error {
	if err := f.enter(ctx, "relocate"); err != nil {
		return err
	}
	if err := f.unsupported("relocate", func(c download.Caps) bool { return c.Relocate }); err != nil {
		return err
	}
	norm := make([]download.ItemID, len(ids))
	for i, id := range ids {
		norm[i] = download.ItemID(strings.ToLower(string(id)))
	}
	if err := f.sc.Move(norm, remoteDir); err != nil {
		return &integrations.Error{Ref: f.ref, Op: "relocate", Class: integrations.ClassNotFound, Err: err}
	}
	return nil
}

func (f *FakeDownloadClient) unsupported(op string, has func(download.Caps) bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if has(f.caps) {
		return nil
	}
	return &integrations.Error{Ref: f.ref, Op: op, Class: integrations.ClassUnsupported, Err: errors.New("the fake's caps turn this off")}
}
