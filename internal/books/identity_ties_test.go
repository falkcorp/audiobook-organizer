// file: internal/books/identity_ties_test.go
// version: 0.1.0
// guid: c4a81d60-2e97-4b35-8f12-6d90b7e3a504
// last-edited: 2026-11-21

package books

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
)

// TestIdentityTies_EachWayTheFlipCanComeApart: the predicate, whether the server
// was built under core identity and the mount's authentication agree or Register
// refuses. One row per way they can come apart.
func TestIdentityTies_EachWayTheFlipCanComeApart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		predicate bool
		server    bool
		mount     httpapi.LegacyAuth
		wantErr   bool
	}{
		{"off, as HEAD", false, false, httpapi.LegacyAuthGin, false},
		{"on, all three agree", true, true, httpapi.LegacyAuthCore, false},
		{"core identity without WithIdentity", true, false, httpapi.LegacyAuthCore, true},
		{"WithIdentity under Gin", true, true, httpapi.LegacyAuthGin, true},
		{"WithIdentity with the predicate off", false, true, httpapi.LegacyAuthGin, true},
		{"a Core mount with the predicate off", false, false, httpapi.LegacyAuthCore, true},
		{"core identity, no WithIdentity and a Gin mount", true, false, httpapi.LegacyAuthGin, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := identityTies(tc.predicate, tc.server, tc.mount)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errIdentityTie)
		})
	}
}

type fakeMigrations struct {
	done bool
	err  error
	ids  []string
}

func (f *fakeMigrations) DataMigrationDone(_ context.Context, id string) (bool, error) {
	f.ids = append(f.ids, id)
	return f.done, f.err
}

// TestIdentityStoreReady_RefusesAStoreNotAtTheIdentityLevel: Attach's check that a
// server under core identity runs over a store whose M3 is done (books.level-2 runs
// behind it), and that the server was built under core identity at all.
func TestIdentityStoreReady_RefusesAStoreNotAtTheIdentityLevel(t *testing.T) {
	ctx := context.Background()
	t.Run("done", func(t *testing.T) {
		m := &fakeMigrations{done: true}
		require.NoError(t, identityStoreReady(ctx, true, m))
		require.Equal(t, []string{cutover.IdentityCutoverID}, m.ids, "the check must ask about M3")
	})
	t.Run("M3 not done", func(t *testing.T) {
		require.ErrorIs(t, identityStoreReady(ctx, true, &fakeMigrations{done: false}), errStoreNotAtIdentityLevel)
	})
	t.Run("the read fails", func(t *testing.T) {
		boom := errors.New("Two-Face flipped the coin")
		require.ErrorIs(t, identityStoreReady(ctx, true, &fakeMigrations{err: boom}), boom)
	})
	t.Run("the server was not built under core identity", func(t *testing.T) {
		require.Error(t, identityStoreReady(ctx, false, &fakeMigrations{done: true}))
	})
	t.Run("no storage admin", func(t *testing.T) {
		require.Error(t, identityStoreReady(ctx, true, nil))
	})
}

// TestIdentityFor_RefusesAnUnflippedBuildOverIdentityHandles and the opposite: the
// two refusals of Deps that disagree with the predicate.
func TestIdentityFor_DepsMustAgreeWithThePredicate(t *testing.T) {
	m := Module().(*booksModule)
	t.Run("on, core handed nothing", func(t *testing.T) {
		t.Cleanup(SetIdentityFlipForTest(true))
		w, err := newIdentityWiring(configOwnerEmail)
		require.NoError(t, err)
		m.idw = w
		t.Cleanup(func() { m.idw = nil })
		_, err = m.identityFor(module.Deps{})
		require.ErrorIs(t, err, errIdentityDeps)
	})
	t.Run("on, Options never ran", func(t *testing.T) {
		t.Cleanup(SetIdentityFlipForTest(true))
		m.idw = nil
		_, err := m.identityFor(module.Deps{})
		require.Error(t, err)
	})
	t.Run("off, nothing handed", func(t *testing.T) {
		t.Cleanup(SetIdentityFlipForTest(false))
		id, err := m.identityFor(module.Deps{})
		require.NoError(t, err)
		require.Nil(t, id)
	})
}

// TestIdentityBoot_PredicateOffOverIdentityHandlesRefuses: core boots the identity
// flip (Options ran with the predicate on) and the predicate is off by Register:
// the boot is refused rather than served under HEAD's middleware over tables M3
// drained.
func TestIdentityBoot_PredicateOffOverIdentityHandlesRefuses(t *testing.T) {
	restore := SetIdentityFlipForTest(true)
	mod, opts, _ := composedOptions(t, false, "", func(c *config.Config) { c.EnableAuth = true }, nil)
	restore()
	t.Cleanup(SetIdentityFlipForTest(false))
	err := bootFails(t, mod, opts)
	require.ErrorIs(t, err, errIdentityDepsWithoutFlip)
}

// TestIdentityBoot_RefusesACutoverThatCouldNotBeBuilt: a failure building M3 is a
// recorded error and a refused boot, never a panic.
func TestIdentityBoot_RefusesACutoverThatCouldNotBeBuilt(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	mod, opts, _ := composedOptions(t, false, "", func(c *config.Config) { c.EnableAuth = true }, nil)
	boom := errors.New("Riddler scrambled the importers")
	mod.cutoverErr.Store(&boom)
	err := bootFails(t, mod, opts)
	require.ErrorIs(t, err, boom)
}

// TestCutoverSteps_ABuilderFailureIsRecordedNotRaised: the cutover func over
// importers that cannot stage M3 returns no steps, records the error and does not
// panic (Manifest.Cutover has no error return).
func TestCutoverSteps_ABuilderFailureIsRecordedNotRaised(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	m := Module().(*booksModule)
	m.dbPath = "fast-exit"
	w, err := newIdentityWiring(configOwnerEmail)
	require.NoError(t, err)
	m.idw = w
	var steps any
	require.NotPanics(t, func() { steps = m.cutoverSteps()(module.Importers{}) })
	require.Empty(t, steps)
	perr := m.cutoverErr.Load()
	require.NotNil(t, perr)
	require.ErrorIs(t, *perr, errNoIdentityImporters)
}

// TestIdentityBoot_M3RanAndTheStoreIsAtTheIdentityLevel: a flipped boot's store has
// M3 done, and the flipped server was built under core identity.
func TestIdentityBoot_M3RanAndTheStoreIsAtTheIdentityLevel(t *testing.T) {
	c := bootIdentity(t, nil)
	require.True(t, c.srv.UnderCoreIdentity())
	done, err := c.app.StorageAdmin().DataMigrationDone(context.Background(), cutover.IdentityCutoverID)
	require.NoError(t, err)
	require.True(t, done)
}
