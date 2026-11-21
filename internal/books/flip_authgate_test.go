// file: internal/books/flip_authgate_test.go
// version: 1.0.0
// guid: 8b3e6d12-4a95-4c07-9f21-7d5c0e1a6b48
// last-edited: 2026-11-20

package books

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/app"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/storage"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// flippedOfflineABO is newOfflineABO with the config boot on and an operator
// environment the caller sets between boots. The process environment carries the
// retired ABK_STATE_DIR the sandbox sets, which the config boot refuses, so the
// boot sees only its own.
func flippedOfflineABO(t *testing.T, env *[]string) *offlineABO {
	t.Helper()
	h := &offlineABO{t: t, state: filepath.Join(t.TempDir(), "state")}
	h.path = filepath.Join(tempDir(t), "gotham.pebble")
	h.root, h.dump = gotham(t)
	h.edit = func(c *config.Config) { c.DatabasePath, c.RootDir = h.path, h.root }
	h.disk = func(o *app.Options) {
		o.StorageFS = storage.FS{}
		o.StateDir = h.state
		o.Env = append([]string{storageEnv()}, *env...)
	}
	h.marker = filepath.Join(filepath.Dir(h.path), "hnsw", "hnsw.clean-stop")
	h.boot()
	return h
}

// bootObserving boots h's store once, hands the effective config the boot
// produced to observe before the service runs, and stops it.
func (h *offlineABO) bootObserving(observe func(config.Config)) {
	h.t.Helper()
	r := runComposedIn(h.t, h.state, h.edit, nil, h.disk, func(*composed) { observe(config.Snapshot()) })
	require.NoError(h.t, r.stop())
	h.offline()
}

// TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn (05-storage 5S.3.15 "Next
// boot", SEC8-5): after a factory reset the next boot is a fresh install at core's
// defaults, and a stale operator value that disables auth or the Basic gate's
// opposite cannot loosen the default, because the more restrictive of the operator
// and the stored value wins (05-config 1.3.5): auth stays on. The reset stores
// the factory defaults of the fail-closed keys as rows for that rule to weigh.
// An install that was never reset and sets the same operator value is an
// operator's choice and is not this test's.
func TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn(t *testing.T) {
	env := []string{}
	h := flippedOfflineABO(t, &env)

	// A stale operator value from the old install: auth off.
	env = []string{"ABO_CORE_AUTH_MODE=disabled"}
	h.stageFactoryReset(nil)
	var got config.Config
	h.bootObserving(func(c config.Config) { got = c })
	require.False(t, h.slot().Busy(), "the reset did not finish: %+v", h.slot())
	// Control: the same reset boot's store, booted with no operator value, has
	// auth on, so the assertion below is about the stale value and nothing else.
	env = []string{}
	var control config.Config
	h.bootObserving(func(c config.Config) { control = c })
	require.True(t, control.EnableAuth, "auth is off with no operator value at all")
	require.True(t, got.EnableAuth, "a stale ABO_CORE_AUTH_MODE=disabled turned auth off on the boot after a factory reset")
}

// bootStoring boots h's store with the operator environment env, lets store
// change settings through the Facade (an internal write, as an admin's saved
// change lands in core's store), and stops it. It returns the effective config
// the boot produced before store ran.
func (h *offlineABO) bootStoring(store func(config.Facade)) config.Config {
	h.t.Helper()
	var got config.Config
	r := runComposedIn(h.t, h.state, h.edit, nil, h.disk, func(*composed) {
		got = config.Snapshot()
		if store != nil {
			f := config.Current()
			require.NotNil(h.t, f)
			store(f)
		}
	})
	require.NoError(h.t, r.stop())
	h.offline()
	return got
}

// TestAuthSwitch_MoreRestrictiveWins (05-authn 10 with 05-config 1.3.5): for the
// auth switch and the Basic gate's switch, whichever of the stored value and the
// operator value is more restrictive is effective. Stored required with operator
// disabled is on; stored disabled with operator required is on; and the same for
// the gate. The stored value is written by one boot and read by the next, over
// core's own store. A set retired ENABLE_AUTH or BASIC_AUTH_ENABLED refuses boot
// naming the canonical variable (05-authn 10): the names are the rename table's,
// handed to core as app.Options.RetiredConfigNames, and the refusal is core's
// *config.RetiredNameError, raised before the store opens, so the boot writes
// nothing to the store or the state directory. (The fresh-install cell is
// TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn.)
func TestAuthSwitch_MoreRestrictiveWins(t *testing.T) {
	const (
		authDisabled = "ABO_CORE_AUTH_MODE=disabled"
		authRequired = "ABO_CORE_AUTH_MODE=required"
		gateOff      = "ABO_CORE_HTTP_BASIC_GATE_ENABLED=false"
		gateOn       = "ABO_CORE_HTTP_BASIC_GATE_ENABLED=true"
	)
	type cell struct {
		name                string
		auth, gate          bool // the stored values
		operator            []string
		wantAuth, wantBasic bool
	}
	for _, c := range []cell{
		{"stored required, operator disabled", true, true, []string{authDisabled, gateOff}, true, true},
		{"stored disabled, operator required", false, false, []string{authRequired, gateOn}, true, true},
		{"stored disabled, operator disabled", false, false, []string{authDisabled, gateOff}, false, false},
		{"stored required, operator required", true, true, []string{authRequired, gateOn}, true, true},
		{"stored mixed (auth on, gate off), operator the reverse", true, false, []string{authDisabled, gateOn}, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := []string{}
			h := flippedOfflineABO(t, &env)
			h.bootStoring(func(f config.Facade) {
				// A weaker value is written as the server's own change (Mutate): the
				// API refuses to touch the auth switch while no owner is configured. A
				// value that equals the default is stored the way an admin's save
				// stores it, through Update: core keeps a fail-closed key's row even at
				// its default (C4's exception), which is what the operator value is
				// weighed against, and Mutate would see no change and write nothing.
				require.NoError(t, f.Mutate(func(cfg *config.Config) { cfg.EnableAuth, cfg.BasicAuthEnabled = c.auth, c.gate }))
				set := map[string]json.RawMessage{}
				if c.auth {
					set["enable_auth"] = json.RawMessage(strconv.FormatBool(true))
				}
				if c.gate {
					set["basic_auth_enabled"] = json.RawMessage(strconv.FormatBool(true))
				}
				if len(set) > 0 {
					_, err := f.Update(t.Context(), settingsAdmin, config.Update{Set: set})
					require.NoError(t, err)
				}
			})
			env = c.operator
			got := h.bootStoring(nil)
			require.Equal(t, c.wantAuth, got.EnableAuth, "auth switch")
			require.Equal(t, c.wantBasic, got.BasicAuthEnabled, "Basic gate switch")
		})
	}

	// A set retired name is refused, whatever it says: a stale ENABLE_AUTH=false
	// the operator's old unit still exports must stop the service, not be ignored
	// (an ignored name would leave the stored value alone, which is auth's more
	// restrictive side only by luck).
	for _, r := range []struct{ name, canonicalEnv, canonical string }{
		{"ENABLE_AUTH", "ABO_CORE_AUTH_MODE", "core.auth.mode"},
		{"BASIC_AUTH_ENABLED", "ABO_CORE_HTTP_BASIC_GATE_ENABLED", "core.http.basic_gate.enabled"},
	} {
		t.Run("a set retired "+r.name+" refuses boot naming "+r.canonicalEnv, func(t *testing.T) {
			env := []string{}
			h := flippedOfflineABO(t, &env)
			// A store with a stored switch in it, so a boot that ignored the name
			// would have something to read and rewrite.
			h.bootStoring(func(f config.Facade) {
				_, err := f.Update(t.Context(), settingsAdmin, config.Update{Set: map[string]json.RawMessage{
					"enable_auth": json.RawMessage("true"), "basic_auth_enabled": json.RawMessage("true"),
				}})
				require.NoError(t, err)
			})
			env = []string{r.name + "=false"}

			var beforeRows map[string]string
			var beforeStore, beforeState map[string]string
			err := bootOnce(t, h.state, h.edit, h.disk, func() {
				beforeRows = rawKVs(t, h.path)
				beforeStore, beforeState = dirImage(t, filepath.Dir(h.path)), dirImage(t, h.state)
			})
			require.Error(t, err, "a boot with the retired %s set succeeded", r.name)

			var retired *coreconfig.RetiredNameError
			require.True(t, errors.As(err, &retired), "the refusal is not a *config.RetiredNameError: %v", err)
			require.Len(t, retired.Uses, 1, "%v", err)
			use := retired.Uses[0]
			require.Equal(t, r.name, use.Name)
			require.False(t, use.File, "the refused use is an env name")
			require.True(t, use.Refused, "the use is reported as refused: %+v", use)
			require.Equal(t, coreconfig.Path(r.canonical), use.Canonical)
			require.Equal(t, r.canonicalEnv, use.CanonicalEnv)
			// The message an operator reads names the variable they set and the one
			// to set instead.
			require.Contains(t, err.Error(), r.name)
			require.Contains(t, err.Error(), r.canonicalEnv)

			require.Equal(t, beforeRows, rawKVs(t, h.path), "the refused boot wrote rows")
			require.Empty(t, imageDiff(beforeStore, dirImage(t, filepath.Dir(h.path))), "the refused boot wrote beside the store")
			require.Empty(t, imageDiff(beforeState, dirImage(t, h.state)), "the refused boot wrote to the state directory")
		})
	}
}

// bootOnce runs app.New once over the composition whose state directory is
// stateDir, with edit and opt applied the way a disk-backed boot applies them, and
// returns its error; a boot that succeeds is closed. prep, when set, runs after the
// composition is built and before app.New, which is where a cell reads the disk it
// will compare. Nothing is served: the refusal cells need the boot alone.
func bootOnce(t *testing.T, stateDir string, edit func(*config.Config), opt func(*app.Options), prep func()) error {
	t.Helper()
	mod, opts, _ := composedOptions(t, false, stateDir, edit, nil)
	t.Cleanup(func() { Release(mod) })
	// Options asks for the process defaults, which only a failed New or a
	// restore puts back: a boot that succeeds and is merely closed would leave
	// them taken for the next test.
	opts.ProcessDefaults = false
	if opt != nil {
		opt(&opts)
	}
	if prep != nil {
		prep()
	}
	a, err := app.New(t.Context(), opts, mod)
	if err == nil {
		require.NoError(t, a.Close(t.Context()))
	}
	return err
}

// bootServing is bootStoring that also hands the booted App to observe, which can
// serve a request and read the metrics before Run.
func (h *offlineABO) bootServing(store func(config.Facade), observe func(*composed)) {
	h.t.Helper()
	r := runComposedIn(h.t, h.state, h.edit, nil, h.disk, func(c *composed) {
		if observe != nil {
			observe(c)
		}
		if store != nil {
			f := config.Current()
			require.NotNil(h.t, f)
			store(f)
		}
	})
	require.NoError(h.t, r.stop())
	h.offline()
}

// failClosedGauge reads <ns>_config_fail_closed_overrides from the App's registry.
func failClosedGauge(t *testing.T, c *composed) float64 {
	t.Helper()
	mfs, err := c.app.Metrics().Gatherer().Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if strings.HasSuffix(mf.GetName(), "config_fail_closed_overrides") && len(mf.GetMetric()) > 0 {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatal("the registry has no config_fail_closed_overrides gauge")
	return 0
}

// TestBasicGate_MoreRestrictiveWins (05-authn 10, 05-config 1.3.5): the Basic
// gate's switch is fail-closed. Operator false with stored true, and operator true
// with stored false, both turn stage 8 on (a request with no credentials gets the
// gate's own Basic challenge, not the app's); the first is reported: the gauge,
// the startup log, and health's config_fail_closed_override. With neither set the
// gate is off. Compose cell: the shipped docker-compose.yml passes the gate's
// variables as bare names, so a host with none set passes none, and a gate enabled
// in Settings stays on instead of being overridden by a value the file sets.
func TestBasicGate_MoreRestrictiveWins(t *testing.T) {
	const gateOff, gateOn = "ABO_CORE_HTTP_BASIC_GATE_ENABLED=false", "ABO_CORE_HTTP_BASIC_GATE_ENABLED=true"
	challenged := func(c *composed) bool {
		w := c.serve(http.MethodGet, "/api/v1/audiobooks", nil)
		return w.Code == http.StatusUnauthorized && strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic realm=")
	}
	cells := []struct {
		name           string
		stored         *bool // nil: nothing stored
		operator       string
		wantOn, wantFC bool
	}{
		{"operator false, stored true", new(true), gateOff, true, true},
		{"operator true, stored false", new(false), gateOn, true, false},
		{"operator true, nothing stored", nil, gateOn, true, false},
		{"operator false, stored false", new(false), gateOff, false, false},
		{"operator false, nothing stored", nil, gateOff, false, false},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			env := []string{}
			h := flippedOfflineABO(t, &env)
			if cell.stored != nil {
				h.bootStoring(func(f config.Facade) {
					if *cell.stored {
						_, err := f.Update(t.Context(), settingsAdmin, config.Update{Set: map[string]json.RawMessage{"basic_auth_enabled": json.RawMessage("true")}})
						require.NoError(t, err)
						return
					}
					require.NoError(t, f.Mutate(func(c *config.Config) { c.BasicAuthEnabled = false }))
				})
			}
			env = []string{cell.operator}
			var on bool
			var gauge float64
			var warned bool
			h.bootServing(nil, func(c *composed) {
				on, gauge = challenged(c), failClosedGauge(t, c)
				warned = strings.Contains(c.newLog.String(), "stricter than the operator value")
			})
			require.Equal(t, cell.wantOn, on, "stage 8 on")
			require.Equal(t, cell.wantFC, gauge == 1, "gauge %v", gauge)
			require.Equal(t, cell.wantFC, warned, "the startup warning")
		})
	}

	t.Run("compose passes the gate's variables as bare names", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
		require.NoError(t, err)
		seen := 0
		for line := range strings.SplitSeq(string(raw), "\n") {
			l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
			if !strings.HasPrefix(l, "ABO_CORE_HTTP_BASIC_GATE_") && !strings.Contains(strings.ToUpper(l), "BASIC_AUTH_") {
				continue
			}
			seen++
			require.NotContains(t, l, "=", "compose sets %q: a value in the file overrides the host and can turn a gate enabled in Settings off", l)
			require.NotContains(t, l, ":", "compose sets %q", l)
		}
		require.Positive(t, seen, "docker-compose.yml names no Basic-gate variable: the cell checks nothing")
	})
}
