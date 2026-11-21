// file: core/kernel/module_settings.go
// version: 1.0.0
// guid: 4d7a92e1-0c85-4b3f-a6d9-e18f50b3c724
// last-edited: 2026-11-18

package kernel

import (
	"fmt"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
)

// settingsProblems is P1's check of every manifest's Settings, enabled or
// not: the manifest stays known while the module is disabled (06 §6.4.2).
func settingsProblems(entries []*entry) []module.Problem {
	var probs []module.Problem
	for _, e := range entries {
		probs = append(probs, module.SettingsProblems(e.man)...)
	}
	return probs
}

// rewriteProblems is P1's one-M8 rule (05-storage §5S.3.13: the store
// rewrite runs once, last, under one R2 ID): every declaring manifest after
// the first, in ID order, a disabled module's included, is a problem.
func rewriteProblems(entries []*entry) []module.Problem {
	var probs []module.Problem
	var first module.ID
	for _, e := range entries {
		rw := e.man.Rewrite
		if rw == nil {
			continue
		}
		if first != "" {
			probs = append(probs, module.Problem{Module: e.man.ID, Kind: module.ProblemMigrationOrder,
				Detail: fmt.Sprintf("module %s already declares the App's one Rewrite (M8, %q)", first, rw.ID)})
			continue
		}
		first = e.man.ID
	}
	return probs
}

// declaredSettings is what module.Setting reads at P6 (RegistrarConfig
// .Settings): every path the manifest declares, each mapped to its live
// handle where the kernel registered one, nil otherwise. No config store is
// wired into boot yet, so every value is nil and Setting hands the module a
// handle holding the section's defaults.
func declaredSettings(man module.Manifest, live map[config.Path]any) map[config.Path]any {
	out := make(map[config.Path]any, len(man.Settings))
	for _, s := range man.Settings {
		out[s.Path()] = live[s.Path()]
	}
	return out
}
