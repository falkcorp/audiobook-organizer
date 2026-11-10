// file: core/config/configtest/fixtures.go
// version: 1.0.0
// guid: 3747199d-9cae-4c7f-a7a3-623dbc1385fe
// last-edited: 2026-11-10

// Package configtest is the conformance kit of core config (05-config
// §1.11): OverrideStoreSuite, the contract every config.OverrideStore meets;
// fixture sections with one key of each kind; and in-memory doubles of the
// override store and the committer for tests that drive a config.Store
// without storage.
package configtest

import (
	"errors"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// Fixture paths.
const (
	BatcavePath config.Path = "core.batcave"
	WatchPath   config.Path = "modules.gotham.watch"
	ModulesPath config.Path = "core.modules"
)

// Batcave is a fixture core section with one key of each kind: live,
// restart and boot keys, every protection class, a nested struct, a list and
// a secret.
type Batcave struct {
	Level    string          `json:"level" schema:"enum=debug|info|warn"`
	Interval config.Duration `json:"interval"`
	Workers  int             `json:"workers" reload:"restart" schema:"minimum=1,maximum=64"`
	Mode     string          `json:"mode" reload:"boot" schema:"enum=night|day"`
	Path     string          `json:"path" protect:"admin" schema:"format=path"`
	Signal   string          `json:"signal" protect:"interactive"`
	Owner    string          `json:"owner_email" protect:"owner" sensitive:"true"`
	Vault    string          `json:"vault" protect:"operator"`
	Gadget   Gadget          `json:"gadget"`
	Allies   []string        `json:"allies"`
	APIKey   secrets.Ref     `json:"api_key"`
}

// Gadget is Batcave's nested struct: each field is a key of its own.
type Gadget struct {
	Name  string `json:"name"`
	Count int    `json:"count" schema:"minimum=0"`
}

// BatcaveDefaults is Batcave's declared default.
func BatcaveDefaults() Batcave {
	return Batcave{
		Level: "info", Interval: config.Duration(90 * time.Second), Workers: 4, Mode: "night",
		Path: "/batcave", Signal: "bat-signal", Owner: "", Vault: "wayne-manor",
		Gadget: Gadget{Name: "batarang", Count: 3}, Allies: []string{"robin"},
	}
}

// BatcaveSection declares Batcave at core.batcave.
func BatcaveSection(opts ...config.SectionOption) config.Section[Batcave] {
	return config.Define(BatcavePath, BatcaveDefaults(), opts...)
}

// Watch is a fixture module section with a cross-field Validator.
type Watch struct {
	Min      int               `json:"min"`
	Max      int               `json:"max"`
	Villains []string          `json:"villains"`
	Rogues   map[string]string `json:"rogues"`
}

// Validate refuses Min above Max, attributed to the min key.
func (w *Watch) Validate() error {
	if w.Min > w.Max {
		return &config.FieldError{Path: WatchPath + ".min", Err: errors.New("min is above max")}
	}
	return nil
}

// WatchSection declares Watch at modules.gotham.watch.
func WatchSection(opts ...config.SectionOption) config.Section[Watch] {
	return config.Define(WatchPath, Watch{Min: 1, Max: 10, Villains: []string{"joker"}, Rogues: map[string]string{"arkham": "scarecrow"}}, opts...)
}

// Module is one entry of the fixture core.modules map section.
type Module struct {
	Enabled bool `json:"enabled" reload:"boot"`
}

// ModulesSection declares the map section core.modules with the given
// declared entries (module ID is data, 05-config §1.3.6).
func ModulesSection(defaults map[string]Module) config.Section[map[string]Module] {
	return config.Define(ModulesPath, defaults)
}
