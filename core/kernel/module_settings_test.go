// file: core/kernel/module_settings_test.go
// version: 1.0.0
// guid: b3f81c6e-5d27-4a90-8e14-7c09d2a65f31
// last-edited: 2026-11-18

package kernel

import (
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

type utilityBelt struct {
	Grapple int    `json:"grapple"`
	Cord    string `json:"cord"`
}

var (
	beltSection  = config.Define("modules.batcave.belt", utilityBelt{Grapple: 3, Cord: "kevlar"})
	otherSection = config.Define("modules.arkham.cells", utilityBelt{})
)

func beltModule(register func(r *module.Registrar) error, sections ...module.SettingsSection) testModule {
	return mod("batcave", register).with(func(m *module.Manifest) { m.Settings = sections })
}

func TestCatalog_SettingsOutsideTheModulesNamespace(t *testing.T) {
	// A disabled module's manifest stays known, so its declaration is checked too.
	for _, enabled := range []bool{true, false} {
		m := beltModule(nil, module.Settings(otherSection)).with(func(m *module.Manifest) { m.DefaultEnabled = enabled })
		k, err := New(t.Context(), testConfig(t, m))
		if k != nil || !hasKind(err, module.ProblemSettingsNamespace) {
			t.Fatalf("enabled=%v: New = %v, %v; want a ProblemSettingsNamespace", enabled, k, err)
		}
	}
}

func TestRegister_SettingOfADeclaredSectionHoldsDefaults(t *testing.T) {
	var got *config.Handle[utilityBelt]
	m := beltModule(func(r *module.Registrar) error {
		got = module.Setting(r, beltSection)
		return r.Err()
	}, module.Settings(beltSection))
	mustNew(t, testConfig(t, m))
	if got == nil || *got.Get() != (utilityBelt{Grapple: 3, Cord: "kevlar"}) {
		t.Fatalf("Setting handle = %v, want the declared defaults", got)
	}
}

func TestRegister_SettingOfAnUndeclaredSectionIsAProblem(t *testing.T) {
	m := beltModule(func(r *module.Registrar) error {
		_ = module.Setting(r, beltSection)
		return r.Err()
	})
	k, err := New(t.Context(), testConfig(t, m))
	if k != nil || !hasKind(err, module.ProblemSettingsUndeclared) {
		t.Fatalf("New = %v, %v; want a ProblemSettingsUndeclared", k, err)
	}
}

func TestCatalog_AtMostOneRewrite(t *testing.T) {
	rw := func(id string) *storage.RewriteStep {
		s := storage.NewRewriteStep(storage.Owner(id), id+".rewrite")
		return &s
	}
	one := mod("batcave", nil).with(func(m *module.Manifest) { m.Rewrite = rw("batcave") })
	mustNew(t, testConfig(t, one))

	// A disabled module's declaration counts: M8 is the App's, not the module's.
	two := []module.Module{
		mod("batcave", nil).with(func(m *module.Manifest) { m.Rewrite = rw("batcave"); m.DefaultEnabled = false }),
		mod("arkham", nil).with(func(m *module.Manifest) { m.Rewrite = rw("arkham") }),
	}
	k, err := New(t.Context(), testConfig(t, two...))
	if k != nil || !hasKind(err, module.ProblemMigrationOrder) {
		t.Fatalf("New = %v, %v; want a ProblemMigrationOrder for the second Rewrite", k, err)
	}
}
