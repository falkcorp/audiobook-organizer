// file: internal/books/config_cutover_oracle_test.go
// version: 1.0.0
// guid: d82a4f17-3b69-4e50-a1c8-5f0e93b7d264
// last-edited: 2026-11-20

package books

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
)

// headEffective is the Config HEAD's loaders produce from the fixture's store:
// the shipped defaults (ResetToDefaultsForTest), then LoadConfigFromDatabase over
// a copy of the store, as serve ran them. The fixture is not touched.
func headEffective(t *testing.T, fx *flipFixture) config.Config {
	t.Helper()
	copyDir := t.TempDir()
	require.NoError(t, copyTree(fx.dbDir, copyDir))
	store, err := database.NewPebbleStore(copyDir + "/books.pebble")
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	withConfig(t, config.Config{DatabasePath: fx.dbPath, RootDir: fx.dbDir})
	config.ResetToDefaultsForTest()
	require.NoError(t, config.LoadConfigFromDatabase(store))
	// serve validates the loaded config; Validate also normalizes (a zero minimum
	// book size and rescan age take their defaults), so the loaded value is the
	// validated one. Its errors (paths of a store that is not a real install) are
	// not the oracle's.
	c := config.Snapshot()
	_ = c.Validate()
	return c
}

// flattenConfig is a Config as dotted JSON paths to leaf values, slices and maps
// kept whole as JSON text (a list is one value: its order is part of it).
func flattenConfig(t *testing.T, c config.Config) map[string]string {
	t.Helper()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	var tree map[string]any
	require.NoError(t, json.Unmarshal(raw, &tree))
	out := map[string]string{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if m, ok := v.(map[string]any); ok && len(m) > 0 && prefix != "plugins" {
			for k, x := range m {
				walk(join(prefix, k), x)
			}
			return
		}
		b, _ := json.Marshal(v)
		out[prefix] = string(b)
	}
	walk("", tree)
	return out
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// configDiff is the paths whose values differ, sorted, as "path: head -> flipped".
func configDiff(head, flipped map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(head)) {
		if h, f := head[k], flipped[k]; h != f {
			out = append(out, fmt.Sprintf("%s: %s -> %s", k, h, f))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(flipped)) {
		if _, ok := head[k]; !ok {
			out = append(out, fmt.Sprintf("%s: (absent) -> %s", k, flipped[k]))
		}
	}
	return out
}

// headSaved seeds the store the way an R0 serve left it after a settings save:
// the shipped defaults with edit applied, written by HEAD's own SaveConfigToDatabase
// (the blob without its secrets, the secrets as encrypted rows).
func headSaved(t *testing.T, edit func(*config.Config)) func(*database.PebbleStore) {
	return func(s *database.PebbleStore) {
		withConfig(t, config.Config{})
		config.ResetToDefaultsForTest()
		if edit != nil {
			config.Mutate(edit)
		}
		require.NoError(t, config.SaveConfigToDatabase(s))
	}
}

// perturbConfig sets every scalar leaf of c to a value that is not its current
// one: a bool flips, a number moves by 7, a string gets a suffix, a string list
// gets one more element. It walks nested structs and leaves maps, pointers and
// lists of structs alone (they have their own fixtures). A leaf whose json path
// is in skip is left as is.
func perturbConfig(c *config.Config, skip map[string]bool) {
	perturbValue(reflect.ValueOf(c).Elem(), "", skip)
}

func perturbValue(v reflect.Value, prefix string, skip map[string]bool) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := join(prefix, name)
		if skip[path] || perturbSkipped(path) {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Struct:
			perturbValue(fv, path, skip)
		case reflect.Bool:
			fv.SetBool(!fv.Bool())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			fv.SetInt(fv.Int() + 1)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			fv.SetUint(fv.Uint() + 1)
		case reflect.Float32, reflect.Float64:
			fv.SetFloat(fv.Float() - 0.5)
		case reflect.String:
			if freeText[path] {
				fv.SetString(fv.String() + "-gotham")
			}
		}
	}
}

// perturbSkip are the settings the perturbation leaves at their value, by json
// path or prefix, each for a stated reason: the flipped boot, like HEAD's, refuses
// a stored value the environment decides.
var perturbSkip = []struct{ prefix, why string }{
	{"enable_auth", "the authentication switch: a flipped value gates the health probe this harness waits on; TestAuthSwitch_MoreRestrictiveWins owns it"},
	{"basic_auth", "the Basic gate: same as enable_auth; TestBasicGate_MoreRestrictiveWins owns it"},
	{"abs_", "environment-authoritative: a stored value that differs from the environment's is refused at boot"},
}

func perturbSkipped(path string) bool {
	for _, p := range perturbSkip {
		if strings.HasPrefix(path, p.prefix) {
			return true
		}
	}
	return false
}

// freeText are the string settings with no validator beyond "a string": the only
// strings the perturbation changes (every other string is an enum, a path, a URL
// or a list element with a format, and a changed one is refused by HEAD's own
// validation, which is not what the oracle measures).
var freeText = map[string]bool{}

// oracleLossy are the paths at which a flipped Config is allowed to differ from
// the one HEAD's loaders built from the same store, each with the reason and where
// the claim is checked. A path ending in "." matches everything under it. Anything
// the oracle finds outside this table, and not a pure representation difference
// (see sameValue), fails the test: a change of what the app does is a bug, and a
// change of how a value is spelled is named here before it is accepted.
var oracleLossy = []struct{ path, why string }{
	{"database_path", "HEAD's loaders leave it to the caller; the flipped boot sets it to the store the process opened (asserted equal to it)"},
	{"enabled_sort_indexes", "HEAD's loaded value is the setting; the flipped boot publishes the indexes the store built, which are the shipped pair (asserted)"},
	{"auto_update.window_start", "dropped: HEAD's updater reads and ignores it (05-updater); the base keeps the shipped 2 (TestAppConfigBaseValuesSurvive)"},
	{"auto_update.window_end", "dropped: HEAD's updater reads and ignores it (05-updater); the base keeps the shipped 5 (TestAppConfigBaseValuesSurvive)"},
	{"ai_backend.embedding_mode", "HEAD stores the empty string for derive-from-the-rest; the flip stores the derived mode; the Effective mode is asserted equal"},
	{"ai_backend.llm_mode", "HEAD stores the empty string for derive-from-the-rest; the flip stores the derived mode; the Effective mode is asserted equal"},
	{"oauth_default_role", "HEAD spells the default role viewer; with no provider there is no entry to carry it and legacyoauth.New gives an empty role viewer (asserted equal)"},
	{"ai_endpoints_routing", "OPEN: dropped by the table, one routed path reads it; the base-value decision is with the lead"},
	{"scheduled.asin_backfill.enabled", "an interval-only entry runs iff its interval is above 0 and HEAD ignores enabled; the flipped Config carries the interval (scheduled.* interval is compared)"},
	{"scheduled.candidate_fetch.enabled", "interval-only entry: see scheduled.asin_backfill.enabled"},
	{"scheduled.catalog_harvest.enabled", "interval-only entry: see scheduled.asin_backfill.enabled"},
	{"scheduled.metadata_upgrade.enabled", "interval-only entry: see scheduled.asin_backfill.enabled"},
	{"scheduled.library_scan_full.interval", "dropped (ruling 3): the due-check is fixed at one hour and the sweep period is period_hours; TestLibraryScanFullDueCheckIsFixedAndIgnoresTheDroppedInterval"},
}

// oracleLossyOnStartup: every scheduled.<task>.on_startup is a dropped row
// (ruling 2, onStartupDroppedNote); no code outside internal/config reads one
// (TestNoReaderOfADroppedField).
func oracleLossyPath(path string) (why string, ok bool) {
	if strings.HasPrefix(path, "scheduled.") && strings.HasSuffix(path, ".on_startup") {
		return "dropped (ruling 2): a kind's start trigger is declared in code, and no reader remains (TestNoReaderOfADroppedField)", true
	}
	for _, l := range oracleLossy {
		if path == l.path || (strings.HasSuffix(l.path, ".") && strings.HasPrefix(path, l.path)) {
			return l.why, true
		}
	}
	return "", false
}

// sameValue reports whether two flattened values are one value spelled two ways:
// null, [] and {} are the same empty; a map or list element holding null, "" or an
// empty container is the same as one without it (an unset secret); two durations
// are the same when they parse to the same time.Duration ("60s" and "1m").
func sameValue(a, b string) bool {
	if a == b {
		return true
	}
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	if s, ok := x.(string); ok {
		if t, ok := y.(string); ok {
			d1, e1 := time.ParseDuration(s)
			d2, e2 := time.ParseDuration(t)
			return e1 == nil && e2 == nil && d1 == d2
		}
		return false
	}
	return reflect.DeepEqual(pruneEmpty(x, true), pruneEmpty(y, true))
}

func pruneEmpty(v any, top bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			if p := pruneEmpty(x, false); !isEmptyJSON(p) {
				out[k] = p
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(t) == 0 {
			return nil
		}
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = pruneEmpty(x, false)
		}
		return out
	}
	return v
}

func isEmptyJSON(v any) bool { return v == nil || v == "" }

// oracleFixture is a Config an operator who used every settings page would have
// saved: every scalar moved off its default (perturbConfig) plus the settings the
// perturbation cannot invent, with realistic values (OAuth sign-in with its
// secrets, API keys as plain Batman-named values).
func oracleFixture(c *config.Config) {
	perturbConfig(c, nil)
	c.OAuthEnabled = true
	c.OAuthGithubClientID = "gotham-gh-client-7f3a9c41"
	c.OAuthGithubClientSecret = "gotham-gh-client-secret"
	c.OAuthGoogleClientID = "gotham-g-client-1b6e4d92.apps.example"
	c.OAuthGoogleClientSecret = "gotham-g-client-secret"
	c.OAuthAllowedEmails = "bruce@wayne.example,alfred@wayne.example"
	c.OAuthDefaultRole = "editor"
	c.OAuthRedirectBaseURL = "https://batcave.example"
	c.OpenAIAPIKey = "batman-openai-key"
	c.HardcoverAPIToken = "robin-hardcover-token"
	c.GoogleBooksAPIKey = "batgirl-google-books-key"
	for i := range c.MetadataSources {
		switch c.MetadataSources[i].ID {
		case "google-books":
			c.MetadataSources[i].Credentials = map[string]string{"apiKey": "nightwing-source-key"}
		case "hardcover":
			c.MetadataSources[i].Credentials = map[string]string{"apiKey": "oracle-source-key"}
		}
	}
}

func TestConfigCutover_EffectiveConfigUnchanged(t *testing.T) {
	used := map[string]bool{}
	run := func(name string, edit func(*config.Config), after func(t *testing.T, fx *flipFixture, head, flipped config.Config)) {
		t.Run(name, func(t *testing.T) {
			fx := newFlipFixture(t, headSaved(t, edit), "")
			head := headEffective(t, fx)
			var flipped config.Config
			fx.runFlipped(t, func(*composed) {
				flipped = config.Snapshot()
				if m, ok := config.Current().(interface{ Missing() []string }); ok {
					require.Empty(t, m.Missing(), "HEAD fields no row of the table covers")
				} else {
					t.Fatal("the installed Facade does not report Missing()")
				}
			})
			hf, ff := flattenConfig(t, head), flattenConfig(t, flipped)
			var bad []string
			for _, k := range slices.Sorted(maps.Keys(hf)) {
				if sameValue(hf[k], ff[k]) {
					continue
				}
				if _, ok := oracleLossyPath(k); ok {
					used[k] = true
					continue
				}
				bad = append(bad, fmt.Sprintf("%s: %s -> %s", k, hf[k], ff[k]))
			}
			for _, k := range slices.Sorted(maps.Keys(ff)) {
				if _, ok := hf[k]; !ok && ff[k] != "null" && ff[k] != "[]" && ff[k] != "{}" && ff[k] != `""` {
					bad = append(bad, fmt.Sprintf("%s: (absent) -> %s", k, ff[k]))
				}
			}
			if len(bad) > 0 {
				t.Errorf("a flipped boot changes the effective Config at %d paths the lossy table does not name:\n  %s",
					len(bad), strings.Join(bad, "\n  "))
			}

			// The spelled-differently modes resolve to the same backend.
			require.Equal(t, head.EffectiveEmbeddingMode(), flipped.EffectiveEmbeddingMode(), "embedding mode")
			require.Equal(t, head.EffectiveLLMMode(), flipped.EffectiveLLMMode(), "llm mode")
			require.Equal(t, legacyoauth.New(legacyoauth.Config{DefaultRole: head.OAuthDefaultRole}).DefaultRole,
				legacyoauth.New(legacyoauth.Config{DefaultRole: flipped.OAuthDefaultRole}).DefaultRole, "the role a new OAuth user gets")
			require.Equal(t, fx.dbPath, flipped.DatabasePath, "database_path is the opened store")
			require.Equal(t, []string{"year", "created_at"}, flipped.EnabledSortIndexes)
			// Nothing the planner staged was refused by core's schema: a refused value
			// is quarantined, sealed, and the setting it carried is off after the
			// flip (HEAD's OAuth sign-in was, until the fold wrote complete entries).
			for k := range rawKVs(t, fx.dbPath) {
				if strings.HasPrefix(k, "c/storage/quarantine\x00\x01") {
					t.Errorf("the flip quarantined a value: %q", k)
				}
			}
			if after != nil {
				after(t, fx, head, flipped)
			}
		})
	}
	run("factory_defaults", nil, nil)
	run("every_setting_populated", oracleFixture, func(t *testing.T, _ *flipFixture, head, flipped config.Config) {
		// The populated fixture really populated what it claims to, on both sides.
		require.Equal(t, "editor", head.OAuthDefaultRole)
		require.Equal(t, head.OpenAIAPIKey, flipped.OpenAIAPIKey)
		require.NotEmpty(t, flipped.OpenAIAPIKey, "the fixture's secret reached the flipped Config")
		for _, c := range []config.Config{head, flipped} {
			var got string
			for _, ms := range c.MetadataSources {
				if ms.ID == "google-books" {
					got = ms.Credentials["apiKey"]
				}
			}
			require.Equal(t, "nightwing-source-key", got, "a metadata source credential")
		}
		require.Equal(t, "gotham-gh-client-secret", flipped.OAuthGithubClientSecret)
	})

	for _, l := range oracleLossy {
		if !used[l.path] {
			t.Errorf("lossy table entry %q matched no difference in any fixture: delete it (or the oracle stopped measuring it)", l.path)
		}
	}
}
