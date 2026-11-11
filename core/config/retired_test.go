// file: core/config/retired_test.go
// version: 1.0.1
// guid: c9512284-cfd6-44bd-a433-5161ae2a9d20
// last-edited: 2026-11-11

package config_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
)

// lairFix is a module section with one admin key, registered after Boot as
// the kernel registers module sections.
type lairFix struct {
	Door  string `json:"door" protect:"admin"`
	Light string `json:"light"`
}

// retiredRig is realRig with Options.Retired set.
func retiredRig(t *testing.T, env []string, yml string, retired ...config.RetiredName) *rig {
	t.Helper()
	return realRig(t, env, yml, rigOpts{o: config.Options{
		Retired:     retired,
		Passthrough: func(p config.Path) bool { return strings.HasPrefix(string(p), "integrations.ai.") },
	}})
}

// TestRetiredName_ProtectedOrSecretRefusesBoot (05-config §1.3.6, SEC10-9):
// a retired name set for an admin, owner or operator key, a secret key, or a
// file key whose subtree holds one refuses boot with *RetiredNameError
// naming it with its canonical env name, before the boot record's directory
// is opened; it is listed in RetiredSet. A module key is judged at Load, and
// a passthrough path by the app's Refuse flag.
func TestRetiredName_ProtectedOrSecretRefusesBoot(t *testing.T) {
	for _, c := range []struct {
		name    string
		env     []string
		yml     string
		retired config.RetiredName
	}{
		{"admin", []string{"BATCAVE_PATH=/srv/cave"}, "", config.RetiredName{Name: "BATCAVE_PATH", Canonical: kPath}},
		{"owner", []string{"BATCAVE_OWNER=bruce@192.0.2.1"}, "", config.RetiredName{Name: "BATCAVE_OWNER", Canonical: kOwner}},
		{"operator", []string{"BATCAVE_VAULT=deep"}, "", config.RetiredName{Name: "BATCAVE_VAULT", Canonical: kVault}},
		{"secret", []string{"BATCAVE_KEY=alfred"}, "", config.RetiredName{Name: "BATCAVE_KEY", Canonical: kAPIKey}},
		{"file subtree", nil, "batcave:\n  level: debug\n", config.RetiredName{Name: "batcave", File: true, Canonical: "core.batcave"}},
		{"unregistered core key", []string{"BATCAVE_MOAT=wide"}, "", config.RetiredName{Name: "BATCAVE_MOAT", Canonical: "core.batcave.moat"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := retiredRig(t, c.env, c.yml, c.retired)
			_, err := r.st.Boot(context.Background())
			re, ok := errors.AsType[*config.RetiredNameError](err)
			if !ok || !errors.Is(err, config.ErrRetiredName) {
				t.Fatalf("Boot: err = %v, want *RetiredNameError", err)
			}
			want := config.RetiredUse{Name: c.retired.Name, File: c.retired.File, Canonical: c.retired.Canonical,
				CanonicalEnv: config.CanonicalEnvName("GOTHAM", c.retired.Canonical), Refused: true}
			if len(re.Uses) != 1 || re.Uses[0] != want {
				t.Errorf("Uses = %+v, want [%+v]", re.Uses, want)
			}
			if !strings.Contains(err.Error(), c.retired.Name) || !strings.Contains(err.Error(), want.CanonicalEnv) {
				t.Errorf("the error does not name the retired and the canonical name: %v", err)
			}
			if len(r.roots) != 0 {
				t.Errorf("the boot record's directory was opened: %v", r.roots)
			}
			if got := r.st.RetiredSet(); !slices.Equal(got, []config.RetiredUse{want}) {
				t.Errorf("RetiredSet = %+v", got)
			}
		})
	}

	t.Run("module key at Load", func(t *testing.T) {
		r := retiredRig(t, []string{"LAIR_DOOR=open"}, "",
			config.RetiredName{Name: "LAIR_DOOR", Canonical: "modules.joker.lair.door"})
		if _, err := r.st.Boot(context.Background()); err != nil {
			t.Fatalf("Boot judged a module key it cannot see yet: %v", err)
		}
		if got := r.st.RetiredSet(); len(got) != 0 {
			t.Errorf("RetiredSet after Boot = %+v, want the module key unjudged", got)
		}
		if _, err := config.Register(r.st.Registry(), config.Define("modules.joker.lair", lairFix{})); err != nil {
			t.Fatal(err)
		}
		if err := r.st.Load(context.Background()); !errors.Is(err, config.ErrRetiredName) {
			t.Errorf("Load: err = %v, want ErrRetiredName", err)
		}
	})

	t.Run("missing key of an enabled module", func(t *testing.T) {
		// The module registers sections, so a key the list names and no
		// section declares is a defect of the list, as for core.
		r := retiredRig(t, []string{"LAIR_WINDOW=open"}, "",
			config.RetiredName{Name: "LAIR_WINDOW", Canonical: "modules.joker.lair.window"})
		if _, err := config.Register(r.st.Registry(), config.Define("modules.joker.lair", lairFix{})); err != nil {
			t.Fatal(err)
		}
		if err := r.boots(); !errors.Is(err, config.ErrRetiredName) {
			t.Errorf("boot err = %v, want ErrRetiredName", err)
		}
	})

	t.Run("key of a module with no sections is unchecked", func(t *testing.T) {
		r := retiredRig(t, []string{"PENGUIN_SECRET=umbrella"}, "",
			config.RetiredName{Name: "PENGUIN_SECRET", Canonical: "modules.penguin.auth.secret"})
		if err := r.boots(); err != nil {
			t.Fatalf("boot err = %v: a disabled module's key refused boot", err)
		}
		want := []config.RetiredUse{{Name: "PENGUIN_SECRET", Canonical: "modules.penguin.auth.secret",
			CanonicalEnv: "GOTHAM_PENGUIN_AUTH_SECRET", Unchecked: true}}
		if got := r.st.RetiredSet(); !slices.Equal(got, want) {
			t.Errorf("RetiredSet = %+v, want %+v", got, want)
		}
		if r.logs.count("unchecked=") != 1 {
			t.Errorf("the warning does not say the use is unchecked:\n%s", r.logs.all())
		}
	})

	t.Run("passthrough by the Refuse flag", func(t *testing.T) {
		for _, refuse := range []bool{true, false} {
			r := retiredRig(t, []string{"WAYNE_KEY=lucius"}, "",
				config.RetiredName{Name: "WAYNE_KEY", Canonical: ptAPIKey, Refuse: refuse})
			if _, err := r.st.Boot(context.Background()); err != nil {
				t.Fatalf("Boot judged a passthrough path: %v", err)
			}
			err := r.st.Load(context.Background())
			if refuse != errors.Is(err, config.ErrRetiredName) {
				t.Errorf("Refuse=%v: Load err = %v", refuse, err)
			}
		}
	})
}

// TestRetiredName_OtherWarns (05-config §1.3.6): a retired name set for a
// ProtectNone key, a dropped key, or a file key whose subtree holds no
// protected key is never read and is ignored with one warning per boot,
// health config_retired_name_ignored and the gauge; it is listed in
// RetiredSet and does not warn as an unknown name.
func TestRetiredName_OtherWarns(t *testing.T) {
	r := retiredRig(t,
		[]string{"GOTHAM_LOG_LEVEL=debug", "GOTHAM_BAT_SIGNAL=on", "GOTHAM_CORE_BATCAVE_LEVEL=warn"},
		"watch:\n  max: 30\n",
		config.RetiredName{Name: "GOTHAM_LOG_LEVEL", Canonical: kLevel},
		config.RetiredName{Name: "GOTHAM_BAT_SIGNAL", Message: "the signal moved to the roof"},
		config.RetiredName{Name: "watch", File: true, Canonical: "modules.gotham.watch"},
		config.RetiredName{Name: "GOTHAM_UNSET"},
	)
	r.start()
	if got := r.cave.Get().Level; got != "warn" {
		t.Errorf("level = %s: a retired name was read", got)
	}
	if got := r.watch.Get().Max; got != 10 {
		t.Errorf("max = %d: a retired file key was read", got)
	}
	want := []config.RetiredUse{
		{Name: "GOTHAM_LOG_LEVEL", Canonical: kLevel, CanonicalEnv: "GOTHAM_CORE_BATCAVE_LEVEL"},
		{Name: "GOTHAM_BAT_SIGNAL"},
		{Name: "watch", File: true, Canonical: "modules.gotham.watch", CanonicalEnv: "GOTHAM_GOTHAM_WATCH"},
	}
	if got := r.st.RetiredSet(); !slices.Equal(got, want) {
		t.Errorf("RetiredSet = %+v\nwant %+v", got, want)
	}
	for _, name := range []string{"name=GOTHAM_LOG_LEVEL", "name=GOTHAM_BAT_SIGNAL", "name=watch"} {
		if n := r.logs.count(name); n != 1 {
			t.Errorf("%s warned %d times, want once", name, n)
		}
	}
	if r.logs.count("the signal moved to the roof") != 1 {
		t.Error("the dropped name's message was not logged")
	}
	if n := r.logs.count("no registered section owns"); n != 0 {
		t.Errorf("a retired name also warned as unknown:\n%s", r.logs.all())
	}
	if !slices.Contains(r.st.HealthTokens(), "config_retired_name_ignored") || config.RetiredGauge(r.st) != 3 {
		t.Errorf("health %v gauge %d, want config_retired_name_ignored and 3", r.st.HealthTokens(), config.RetiredGauge(r.st))
	}

	clean := retiredRig(t, []string{"GOTHAM_LOG_LEVEL="}, "", config.RetiredName{Name: "GOTHAM_LOG_LEVEL", Canonical: kLevel})
	clean.start()
	if len(clean.st.RetiredSet()) != 0 || config.RetiredGauge(clean.st) != 0 || slices.Contains(clean.st.HealthTokens(), "config_retired_name_ignored") {
		t.Error("an empty retired name counts as set")
	}
}

// TestNewStore_RefusesBadRetiredList (05-config §1.3.6): an entry with no
// name, a canonical top-level file key, or a name listed twice fails
// NewStore.
func TestNewStore_RefusesBadRetiredList(t *testing.T) {
	for name, rs := range map[string][]config.RetiredName{
		"no name":   {{Canonical: kLevel}},
		"canonical": {{Name: "core", File: true}},
		"twice":     {{Name: "BATCAVE_PATH"}, {Name: "BATCAVE_PATH"}},
	} {
		if _, err := config.NewStore(config.Options{AppID: "gotham", Retired: rs}); err == nil {
			t.Errorf("%s: NewStore succeeded", name)
		}
	}
	if st, err := config.NewStore(config.Options{AppID: "gotham", Retired: []config.RetiredName{
		{Name: "batcave"}, {Name: "batcave", File: true},
	}}); err != nil {
		t.Errorf("an env name and a file key that share a spelling: %v", err)
	} else {
		_ = st.Close(context.Background())
	}
}
