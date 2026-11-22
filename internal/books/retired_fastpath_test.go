// file: internal/books/retired_fastpath_test.go
// version: 0.1.0
// guid: 95180d13-c42c-4799-8862-4c42a172a1aa
// last-edited: 2026-11-22

package books

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/app"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestRetiredNameFastPathAgreesWithCheckConfig holds the kernel's step-0 fast
// path to the judgement it skips. Step 0 used to build a full scratch config
// store on every boot (every core and module section registered, then Load)
// only to see whether a set retired name refuses boot; it now asks
// config.Store.AnyRetiredNameSet first and skips that store when the answer is
// no (kernel.refuseRetiredAtStep0). That is sound only if the cheap answer is
// exactly "the full judgement lists a use": a set name the cheap check missed
// would let a boot that must be refused (a retired DATABASE_PATH, a retired
// secret) go on and create a store.
//
// Every name of ABO's retired list (flipConfigNames, what serve hands
// app.Options) is checked in each operator layer (the environment, the
// operator file, the flags), and with no layer set at all:
//
//   - every name set in one layer at once: kernel.CheckConfig must list each
//     name of that layer, and AnyRetiredNameSet, asked with that name alone
//     set, must say set; a name of the other layer (or any flag: flags name
//     canonical paths, so no retired name is ever one) must be neither listed
//     nor seen;
//   - outside -short, each name alone in each layer: AnyRetiredNameSet is true
//     exactly when CheckConfig lists a use, and never false where CheckConfig
//     refuses. That is 1,900-odd full judgements, so it runs in the
//     non-short gates with the crash matrices; the all-at-once form above
//     covers every name in every run.
//
// The run must reach both refused and warned names, so a list or a harness
// that stopped exercising one class fails here instead of passing vacuously.
func TestRetiredNameFastPathAgreesWithCheckConfig(t *testing.T) {
	retired, bindings, passthrough := flipConfigNames()
	require.NotEmpty(t, retired)
	mods := []module.Module{OfflineModule(app.Options{AppID: AppID})}
	discard := slog.New(slog.DiscardHandler)
	base := func() coreconfig.Options {
		return coreconfig.Options{
			AppID: AppID, Environ: []string{}, Retired: retired, EnvBindings: bindings,
			Passthrough: passthrough, Logger: discard,
		}
	}
	dir := t.TempDir()
	files := 0
	writeFile := func(t *testing.T, body string) string {
		t.Helper()
		files++
		p := filepath.Join(dir, fmt.Sprintf("gotham-%d.yaml", files))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		return p
	}
	fast := func(t *testing.T, o coreconfig.Options) bool {
		t.Helper()
		st, err := coreconfig.NewStore(o)
		require.NoError(t, err)
		set, err := st.AnyRetiredNameSet()
		require.NoError(t, err)
		require.NoError(t, st.Close(t.Context()))
		return set
	}
	full := func(t *testing.T, o coreconfig.Options) (uses []coreconfig.RetiredUse, refused []coreconfig.RetiredUse) {
		t.Helper()
		uses, err := kernel.CheckConfig(t.Context(), kernel.ConfigCheck{
			Config: o, StateDirDefault: config.DefaultSecureStateDir, Modules: mods,
		})
		if rerr, ok := errors.AsType[*coreconfig.RetiredNameError](err); ok {
			refused = rerr.Uses
		}
		return uses, refused
	}
	listed := func(uses []coreconfig.RetiredUse, r coreconfig.RetiredName) bool {
		return slices.ContainsFunc(uses, func(u coreconfig.RetiredUse) bool { return u.Name == r.Name && u.File == r.File })
	}

	// The value a layer sets: a string, which many keys refuse to decode. The
	// judgement runs on whether a name is set, never on its value (it runs
	// before any value is decoded), and a value error is the real store's.
	const val = "batman"
	type layer struct {
		name string
		own  func(r coreconfig.RetiredName) bool // the names this layer sets a retired name through
		set  func(t *testing.T, o *coreconfig.Options, rs ...coreconfig.RetiredName)
	}
	layers := []layer{
		{"env", func(r coreconfig.RetiredName) bool { return !r.File }, func(_ *testing.T, o *coreconfig.Options, rs ...coreconfig.RetiredName) {
			for _, r := range rs {
				o.Environ = append(o.Environ, r.Name+"="+val)
			}
		}},
		{"file", func(r coreconfig.RetiredName) bool { return r.File }, func(t *testing.T, o *coreconfig.Options, rs ...coreconfig.RetiredName) {
			seen := map[string]bool{}
			var body strings.Builder
			for _, r := range rs {
				if !seen[r.Name] { // an env name and a file key may share a spelling
					seen[r.Name] = true
					fmt.Fprintf(&body, "%q: %s\n", r.Name, val)
				}
			}
			o.FilePath = writeFile(t, body.String())
		}},
		{"flags", func(coreconfig.RetiredName) bool { return false }, func(_ *testing.T, o *coreconfig.Options, rs ...coreconfig.RetiredName) {
			o.Flags = map[coreconfig.Path]string{}
			for _, r := range rs {
				p := r.Canonical
				if p == "" {
					p = coreconfig.Path(r.Name)
				}
				o.Flags[p] = val
			}
		}},
	}

	t.Run("no layer sets a retired name", func(t *testing.T) {
		uses, refused := full(t, base())
		require.Empty(t, uses)
		require.Empty(t, refused)
		require.False(t, fast(t, base()))
	})
	t.Run("an env name set to empty or null is unset", func(t *testing.T) {
		for _, v := range []string{"", "null"} {
			o := base()
			for _, r := range retired {
				if !r.File {
					o.Environ = append(o.Environ, r.Name+"="+v)
				}
			}
			uses, _ := full(t, o)
			require.Empty(t, uses, "every retired env name set to %q", v)
			require.False(t, fast(t, o), "every retired env name set to %q", v)
		}
	})

	var nRefused, nWarned int
	for _, l := range layers {
		t.Run(l.name+"/every name at once", func(t *testing.T) {
			o := base()
			l.set(t, &o, retired...)
			uses, refused := full(t, o)
			for _, r := range retired {
				one := base()
				l.set(t, &one, r)
				seen := fast(t, one)
				if l.own(r) {
					require.True(t, listed(uses, r), "%s set in its own layer (%s) is not listed by CheckConfig", r.Name, l.name)
					require.True(t, seen, "%s set in its own layer (%s): CheckConfig lists it, AnyRetiredNameSet says nothing is set", r.Name, l.name)
					if listed(refused, r) {
						nRefused++
					} else {
						nWarned++
					}
					continue
				}
				require.False(t, seen, "%s through %s, not its own layer: AnyRetiredNameSet says set", r.Name, l.name)
				require.False(t, listed(uses, r), "%s through %s, not its own layer: CheckConfig lists it", r.Name, l.name)
			}
		})
	}
	require.Positive(t, nRefused, "no retired name refused: the run never reached the case the fast path must not skip")
	require.Positive(t, nWarned, "no retired name was only warned about")
	t.Logf("%d retired names, %d refuse boot and %d warn when set in their own layer", len(retired), nRefused, nWarned)

	if testing.Short() {
		return
	}
	for _, l := range layers {
		t.Run(l.name+"/each name alone", func(t *testing.T) {
			for _, r := range retired {
				o := base()
				l.set(t, &o, r)
				uses, refused := full(t, o)
				seen := fast(t, o)
				require.Equal(t, len(uses) > 0, seen, "%s through %s: AnyRetiredNameSet says %t, CheckConfig lists %+v", r.Name, l.name, seen, uses)
				if len(refused) > 0 {
					require.True(t, seen, "%s through %s: CheckConfig refuses, AnyRetiredNameSet says nothing is set", r.Name, l.name)
				}
				if l.own(r) {
					require.True(t, listed(uses, r), "%s set alone in its own layer (%s) is not listed", r.Name, l.name)
				}
			}
		})
	}
}
