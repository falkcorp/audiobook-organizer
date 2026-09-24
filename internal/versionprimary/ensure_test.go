// file: internal/versionprimary/ensure_test.go
// version: 1.0.0
// guid: 3e8a1b64-2f9c-4d07-b5a3-91c6e0d4f728
// last-edited: 2026-09-24

package versionprimary_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func ensure(t *testing.T, f *vptest.Fixture, gid string) versionprimary.HandoffResult {
	t.Helper()
	res, err := versionprimary.EnsureSinglePrimary(context.Background(), f.S, gid, versionprimary.Env{RootDir: f.Root})
	require.NoError(t, err)
	return res
}

func TestEnsureSinglePrimary_HealthyKeepsIncumbentAndDemotesNilSiblings(t *testing.T) {
	f := vptest.New(t)
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "true", Created: time.Unix(200, 0)})
	b := f.Book(t, vptest.Spec{ID: "b", Group: "g", Primary: "nil", Created: time.Unix(100, 0)})
	res := ensure(t, f, "g")
	require.Equal(t, versionprimary.OutcomeHealthy, res.Outcome)
	f.RequireSinglePrimary(t, "g", a)
	require.Equal(t, "false", f.Flag(t, b))

	// A second run writes nothing.
	res = ensure(t, f, "g")
	require.Equal(t, versionprimary.OutcomeHealthy, res.Outcome)
	require.Empty(t, res.Writes)
}

func TestEnsureSinglePrimary_ZeroPrimariesElects(t *testing.T) {
	f := vptest.New(t)
	f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "false"})
	f.Book(t, vptest.Spec{ID: "b", Group: "g", Primary: "nil"})
	res := ensure(t, f, "g")
	require.Equal(t, versionprimary.OutcomeElected, res.Outcome)
	f.RequireSinglePrimary(t, "g", res.PrimaryID)
}

func TestEnsureSinglePrimary_TwoPrimariesElectsOne(t *testing.T) {
	f := vptest.New(t)
	f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "true"})
	f.Book(t, vptest.Spec{ID: "b", Group: "g", Primary: "true"})
	res := ensure(t, f, "g")
	require.Equal(t, versionprimary.OutcomeElected, res.Outcome)
	f.RequireSinglePrimary(t, "g", res.PrimaryID)
}

func TestEnsureSinglePrimary_IneligibleIncumbentHandsOn(t *testing.T) {
	f := vptest.New(t)
	inc := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "true", State: "imported"})
	lib := f.Book(t, vptest.Spec{ID: "b", Group: "g", Primary: "false"})
	res := ensure(t, f, "g")
	require.Equal(t, versionprimary.OutcomeElected, res.Outcome)
	f.RequireSinglePrimary(t, "g", lib)
	require.Equal(t, "false", f.Flag(t, inc))
}

func TestEnsureSinglePrimary_SoftDeletedPrimaryHandsOn(t *testing.T) {
	f := vptest.New(t)
	old := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "true"})
	next := f.Book(t, vptest.Spec{ID: "b", Group: "g", Primary: "false"})
	f.SoftDelete(t, old)
	ensure(t, f, "g")
	f.RequireSinglePrimary(t, "g", next)
}

func TestEnsureSinglePrimary_HeldWritesNothing(t *testing.T) {
	f := vptest.New(t)
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "false", State: "imported"})
	b := f.Book(t, vptest.Spec{ID: "b", Group: "g", Primary: "nil", Outside: true})
	res := ensure(t, f, "g")
	require.Equal(t, versionprimary.OutcomeHeld, res.Outcome)
	require.Empty(t, res.Writes)
	require.Equal(t, "false", f.Flag(t, a))
	require.Equal(t, "nil", f.Flag(t, b))
}

func TestEnsureSinglePrimary_EmptyGroupID(t *testing.T) {
	f := vptest.New(t)
	res := ensure(t, f, "")
	require.Equal(t, versionprimary.OutcomeEmpty, res.Outcome)
}

func TestCrown_WritesChoiceAndDemotesRest(t *testing.T) {
	f := vptest.New(t)
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "true"})
	b := f.Book(t, vptest.Spec{ID: "b", Group: "g", Primary: "nil", State: "imported"})
	res, err := versionprimary.Crown(f.S, "g", b)
	require.NoError(t, err)
	require.Equal(t, versionprimary.OutcomeCrowned, res.Outcome)
	f.RequireSinglePrimary(t, "g", b)
	require.Equal(t, "false", f.Flag(t, a))
}

func TestCrown_RefusesSoftDeletedOrForeignBook(t *testing.T) {
	f := vptest.New(t)
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "true"})
	b := f.Book(t, vptest.Spec{ID: "b", Group: "g", Primary: "false"})
	f.SoftDelete(t, b)
	res, err := versionprimary.Crown(f.S, "g", b)
	require.NoError(t, err)
	require.Equal(t, versionprimary.OutcomeCrownNotMember, res.Outcome)
	f.RequireSinglePrimary(t, "g", a)
}
