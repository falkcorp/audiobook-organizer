// file: core/kernel/updater_wire_test.go
// version: 0.1.0
// guid: 5b0e7d21-9c4a-4f63-8a17-d2e6c1f94b08
// last-edited: 2026-11-22

package kernel

import (
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/updater"
)

// updaterFlipHost is the identity-boot host with an updater shipped
// (Config.Updater). The source is a fake that never answers: boot builds the
// updater and reads nothing from it.
func updaterFlipHost(t *testing.T) (*swapHost, Config) {
	t.Helper()
	h, c := identityHost(t)
	in := upInputs()
	o := updaterOptions(in)
	c.Updater = &o
	c.Version = "v1.2.3" // updater.New refuses a build with no running version
	return h, c
}

// updaterWired is what a boot holds of the updater: the four things the flip
// adds, each read from the booted kernel and nothing else.
type updaterWired struct {
	topic, owner, keyspace, health bool
	routes                         []string
}

func updaterWiredIn(k *Kernel) updaterWired {
	var w updaterWired
	if k.events != nil {
		_, w.topic = k.events.bus.Catalog().Lookup(updater.FoundTopicName)
	}
	w.owner = slices.Contains(ownerIDs(k), updaterOwnerID)
	_, w.keyspace = k.store.db.Keyspace(updaterOwnerID)
	_, w.health = states(k.healthReg)[componentUpdater]
	for key := range updaterRoutes(k.router) {
		w.routes = append(w.routes, key)
	}
	slices.Sort(w.routes)
	return w
}

var updaterRouteKeys = []string{
	"GET /api/core/v1/updater",
	"POST /api/core/v1/updater/apply",
	"POST /api/core/v1/updater/check",
	"POST /api/core/v1/updater/rollback",
}

// TestUpdaterWire_FlipOn: an app that declared the identity flip and ships an
// updater gets the topic, the c/updater/ owner (claimed in the store), the
// core.updater component and the four routes. Each facet is its own subtest so
// that dropping one wiring line fails exactly one named case.
func TestUpdaterWire_FlipOn(t *testing.T) {
	h, c := updaterFlipHost(t)
	k := h.mustBoot(c)
	w := updaterWiredIn(k)
	t.Run("topic", func(t *testing.T) {
		if !w.topic {
			t.Errorf("the bus catalog lacks %s", updater.FoundTopicName)
		}
	})
	t.Run("owner", func(t *testing.T) {
		if !w.owner {
			t.Errorf("storageOwners lacks %s: %v", updaterOwnerID, ownerIDs(k))
		}
		if !w.keyspace {
			t.Errorf("the booted store has no %s keyspace", updaterOwnerID)
		}
	})
	t.Run("health", func(t *testing.T) {
		c, ok := states(k.healthReg)[componentUpdater]
		if !ok {
			t.Fatal("the health registry lacks core.updater")
		}
		if c.Critical {
			t.Error("core.updater is registered critical; registerCoreComponents registers it non-critical")
		}
	})
	t.Run("routes", func(t *testing.T) {
		if !slices.Equal(w.routes, updaterRouteKeys) {
			t.Errorf("updater routes = %v, want %v", w.routes, updaterRouteKeys)
		}
	})
}

// TestUpdaterWire_NoNilHTTPAssumption (I10, F7-09 not merged): the boot wires
// the updater with Deps.HTTP and Deps.Notify nil and still boots, so the
// release source's own https-only client and the no-notification fallback are
// what a flip boot runs on. A wiring that required either would fail here.
func TestUpdaterWire_NoNilHTTPAssumption(t *testing.T) {
	h, c := updaterFlipHost(t)
	k, err := h.boot(c)
	if err != nil {
		t.Fatalf("a flip boot with no providerhttp client and no notify service: %v", err)
	}
	u, err := k.buildUpdater()
	if err != nil {
		t.Fatalf("buildUpdater: %v", err)
	}
	if u == nil {
		t.Fatal("buildUpdater returned no updater")
	}
}

// TestUpdaterWire_FlipOff: with the identity flip off (today's ABO) an
// Updater in the Config wires none of it: the owner list is what it is with no
// Updater at all, there is no c/updater/ keyspace, no component, no route,
// and no topic (the bus is not built). This is the "prod unchanged" proof.
func TestUpdaterWire_FlipOff(t *testing.T) {
	hOff, cOff := updaterFlipHost(t)
	cOff.IdentityBoot = false
	kOff := hOff.mustBoot(cOff)

	hBase, cBase := updaterFlipHost(t)
	cBase.IdentityBoot = false
	cBase.Updater = nil
	kBase := hBase.mustBoot(cBase)

	if got, want := ownerIDs(kOff), ownerIDs(kBase); !slices.Equal(got, want) {
		t.Errorf("owners with an Updater on a flip-off boot = %v, want the no-Updater list %v", got, want)
	}
	if w := updaterWiredIn(kOff); w.topic || w.owner || w.keyspace || w.health || len(w.routes) != 0 {
		t.Errorf("a flip-off boot wired the updater: %+v", w)
	}
}

// TestUpdaterWire_FlipOnWithoutUpdater: an app that flipped but ships no
// updater (Config.Updater nil) gets no owner, component or route. The topic is
// a catalog entry every identity boot declares (coreTopics), with no publisher.
func TestUpdaterWire_FlipOnWithoutUpdater(t *testing.T) {
	h, c := identityHost(t)
	k := h.mustBoot(c)
	w := updaterWiredIn(k)
	if w.owner || w.keyspace || w.health || len(w.routes) != 0 {
		t.Errorf("an identity boot with no Updater wired the updater: %+v", w)
	}
	if !w.topic {
		t.Errorf("the catalog lacks %s on an identity boot", updater.FoundTopicName)
	}
}

// TestUpdaterWire_OwnerIsLastCoreOwner: the claim goes after the identity
// owners and before the modules', in storageOwners' order.
func TestUpdaterWire_OwnerIsLastCoreOwner(t *testing.T) {
	h, c := updaterFlipHost(t)
	k := h.mustBoot(c)
	ids := ownerIDs(k)
	i := slices.Index(ids, updaterOwnerID)
	if i < 1 || ids[i+1] != storage.Owner("alfred") {
		t.Fatalf("owners = %v, want %s directly before alfred", ids, updaterOwnerID)
	}
}

// updaterDeclared is the one predicate.
func TestUpdaterDeclared(t *testing.T) {
	o := updaterOptions(upInputs())
	for _, tc := range []struct {
		name string
		cfg  Config
		want bool
	}{
		{"flip and updater", Config{IdentityBoot: true, Updater: &o}, true},
		{"updater alone (offline)", Config{Updater: &o}, false},
		{"flip alone", Config{IdentityBoot: true}, false},
		{"neither", Config{}, false},
	} {
		if got := (&Kernel{cfg: tc.cfg}).updaterDeclared(); got != tc.want {
			t.Errorf("%s: updaterDeclared = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The starter finds the executor when the request arrives; at boot, with the
// tasks start rule not met (no core kind is registered at this base), it is
// not there and the start fails closed.
func TestLateExecutor_NoExecutorFailsClosed(t *testing.T) {
	_, err := newUpdaterStarter(lateExecutor{k: &Kernel{}}).StartCheck(t.Context(), "alfred")
	if err == nil {
		t.Fatal("a start with no executor succeeded")
	}
	// The principal check runs first, so the refusal here is the actor's; ask
	// the late executor directly for its own.
	if _, err := (lateExecutor{k: &Kernel{}}).Start(t.Context(), updater.KindCheck, tasks.StartRequest{}); err != errUpdaterNoExecutor {
		t.Errorf("Start with no executor = %v, want errUpdaterNoExecutor", err)
	}
}

func TestUpdaterSettingsOver(t *testing.T) {
	get := func(s *updaterSettings) func() *updaterSettings { return func() *updaterSettings { return s } }
	if got := updaterSettingsOver(get(nil), updater.ModeCheck)(); got != updater.DefaultSettings(updater.ModeCheck) {
		t.Errorf("nil section = %+v, want the defaults", got)
	}
	got := updaterSettingsOver(get(&updaterSettings{Mode: "check", Channel: "prerelease", MaxDownload: 1 << 20}), updater.ModeOff)()
	if got.Mode != updater.ModeCheck || got.Channel != updater.ChannelPrerelease || got.MaxDownload != 1<<20 {
		t.Errorf("settings = %+v", got)
	}
	got = updaterSettingsOver(get(&updaterSettings{Mode: "auto", Channel: "nightly"}), updater.ModeCheck)()
	if got.Mode != updater.ModeOff || got.Channel != updater.ChannelStable {
		t.Errorf("unknown values = %+v, want off and stable", got)
	}
}

func TestUpdaterRestarter_RefusesBeforeReady(t *testing.T) {
	l := newLifecycle()
	r := updaterRestarter{l: l}
	if err := r.Restart(t.Context(), "swap"); err != errUpdaterNotReady {
		t.Errorf("Restart before ready = %v, want errUpdaterNotReady", err)
	}
	l.ready.Store(true)
	if err := r.Restart(t.Context(), "swap"); err != nil {
		t.Errorf("Restart when ready = %v", err)
	}
	if err := (updaterRestarter{}).Restart(t.Context(), "swap"); err != errUpdaterNotReady {
		t.Errorf("a nil lifecycle = %v, want errUpdaterNotReady", err)
	}
}
