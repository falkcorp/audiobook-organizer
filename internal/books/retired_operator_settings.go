// file: internal/books/retired_operator_settings.go
// version: 1.0.0
// guid: 8d3a6e15-2f94-4b7c-9c08-4e1b7a5d0f63
// last-edited: 2026-11-28

package books

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// refuseRetiredOperatorSettings refuses a boot while the host still sets an
// operator-layer setting the flip stopped reading (cutover.RefusedOperatorPaths):
// the old openai path integrations.ai.openai.base_url and any
// integrations.metadata.<source id>.credentials.<k>, by their canonical env name
// or as a key of the operator file.
//
// Core cannot do this. RetiredName names one env name or one flat top-level file
// key, so it cannot hold a nested file path or a family of env names, and a path
// the app does not declare as passthrough is dropped by core with a warning at
// most (a file key) or a "duplicate path or env name" error that says nothing
// about what to do (a family env name with a hyphenated id or key). The setting
// would stop applying without a word, and an openai request would go, with its
// API key, to a different host than the host file says. So the same raw look as
// refuseHEADKeyedConfigFile: the environment and the --config file are read here,
// before core, and the boot is refused by key name.
//
// The refusal never copies or rewrites a value and never prints one: it names the
// env variable or the file and the path, and what to set instead. The deploy
// pre-flight (scripts/deploy_env_check.py) refuses the same env names before
// install; it reads no host, so a key of the operator file is caught only here.
func refuseRetiredOperatorSettings(configFile string, environ []string) error {
	type use struct{ what, why string }
	var uses []use
	rules := cutover.RefusedOperatorPaths()
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" || value == "null" {
			continue // unset, as core reads it (05-config section 1.3.5)
		}
		for _, r := range rules {
			if r.MatchesEnv(name) {
				uses = append(uses, use{"the environment variable " + name + " (" + r.Path + ")", r.Message})
			}
		}
	}
	for _, p := range operatorFileSettings(configFile) {
		for _, r := range rules {
			if r.MatchesPath(p) {
				uses = append(uses, use{"the operator file " + configFile + " sets " + p, r.Message})
			}
		}
	}
	if len(uses) == 0 {
		return nil
	}
	slices.SortFunc(uses, func(a, b use) int { return strings.Compare(a.what, b.what) })
	parts := make([]string, 0, len(uses))
	for _, u := range uses {
		parts = append(parts, u.what+": "+u.why)
	}
	return fmt.Errorf("books: the host sets operator settings the flipped service no longer reads, so it stops rather than run on a different setting than the host says (%w): %s",
		coreconfig.ErrRetiredName, strings.Join(parts, "; "))
}

// operatorFileSettings lists the dotted paths of every set leaf under the
// operator file's integrations key. A leaf that is null supplies nothing (a YAML
// null is unset, 05-config section 1.3.5). A file that does not exist or does not
// parse as a mapping lists none: core judges the file itself.
func operatorFileSettings(configFile string) []string {
	if configFile == "" {
		return nil
	}
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return nil
	}
	var top map[string]any
	if yaml.Unmarshal(raw, &top) != nil {
		return nil
	}
	var out []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case nil:
		case map[string]any:
			for k, c := range t {
				walk(prefix+"."+k, c)
			}
		default:
			out = append(out, prefix)
		}
	}
	walk("integrations", top["integrations"])
	slices.Sort(out)
	return out
}
