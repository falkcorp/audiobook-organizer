// file: internal/books/config_cutover_oracle_test.go
// version: 1.0.0
// guid: d82a4f17-3b69-4e50-a1c8-5f0e93b7d264
// last-edited: 2026-11-20

package books

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
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

// The differential oracle's HEAD side is FROZEN. testdata/config_oracle/{defaults,populated}.json
// were produced at d3d767304, while HEAD's own loaders were still in the tree, by
// saving a Config through HEAD's SaveConfigToDatabase (the "rows": the blob with its
// secrets zeroed, and the secret rows) and loading it back with
// LoadConfigFromDatabase over a copy of the store (the "config": what HEAD's serve
// would have run with). "defaults" saved the shipped defaults. "populated" saved the
// shipped defaults with every scalar moved off its value (a bool flipped, a number
// plus one, a string list lengthened) and the settings a perturbation cannot invent
// set to realistic values: OAuth sign-in on with a GitHub and a Google client id and
// secret, allowed emails, role "editor", a redirect base URL, the OpenAI key, the
// Hardcover token, the Google Books key, and credentials on the google-books and
// hardcover metadata sources. The loaders are deleted (F3-07 loader), so the goldens
// cannot be regenerated; they are read-only evidence of what HEAD produced.

type oracleRow struct {
	Key    string `json:"key"`
	Type   string `json:"type"`
	Secret bool   `json:"secret"`
	Value  string `json:"value"`
}

type oracleGolden struct {
	Rows   []oracleRow   `json:"rows"`
	Config config.Config `json:"config"`
}

func loadOracleGolden(t *testing.T, name string) oracleGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "config_oracle", name+".json"))
	require.NoError(t, err)
	var g oracleGolden
	require.NoError(t, json.Unmarshal(raw, &g))
	require.NotEmpty(t, g.Rows)
	return g
}

// seedRows writes the golden's settings rows the way HEAD's save left them (secret
// rows encrypted under the fixture's key by SetSetting).
func (g oracleGolden) seedRows(t *testing.T) func(*database.PebbleStore) {
	return func(s *database.PebbleStore) {
		for _, r := range g.Rows {
			require.NoError(t, s.SetSetting(r.Key, r.Value, r.Type, r.Secret))
		}
	}
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
	{"ai_endpoints_routing", "BEHAVIOUR CHANGE (ruling 2026-11-20, 05-ai.md:803): after the flip the one path is the router over ai_endpoints, so the base sets true; an install that never turned routing on moves from the legacy path to the routed one (asserted true on a real boot)"},
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

func TestConfigCutover_EffectiveConfigUnchanged(t *testing.T) {
	used := map[string]bool{}
	run := func(name string, after func(t *testing.T, fx *flipFixture, head, flipped config.Config)) {
		t.Run(name, func(t *testing.T) {
			golden := loadOracleGolden(t, name)
			fx := newFlipFixture(t, golden.seedRows(t), "")
			head := golden.Config
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
	run("defaults", nil)
	run("populated", func(t *testing.T, _ *flipFixture, head, flipped config.Config) {
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
