// file: internal/config/mutate_persist_test.go
// version: 1.0.0
// guid: 8a4d6e12-5c37-4b90-9f2a-1d7e3b05c68f
// last-edited: 2026-11-19

package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMutatePersist_NoFacadeIsHEADsPath: with no Facade the change is made in
// memory and the blob is saved, as the scheduler handlers did.
func TestMutatePersist_NoFacadeIsHEADsPath(t *testing.T) {
	installForTest(t, nil)
	prev := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = prev }) })
	store := newMockSettingsStore()
	require.NoError(t, MutatePersist(store, func(c *Config) { c.FilenameParseModel = "alfred-pennyworth" }))
	require.Equal(t, "alfred-pennyworth", Snapshot().FilenameParseModel)
	require.Contains(t, store.settings, "config_blob")
}

// TestMutatePersist_FacadeRefusalIsAnError: with a Facade installed a refused change
// is returned, nothing is saved to the blob, and the caller can answer with it.
func TestMutatePersist_FacadeRefusalIsAnError(t *testing.T) {
	refusal := errors.New("alfred says no")
	f := &stubFacade{mutateErr: refusal}
	installForTest(t, f)
	store := newMockSettingsStore()
	err := MutatePersist(store, func(c *Config) { c.FilenameParseModel = "x" })
	require.ErrorIs(t, err, refusal)
	require.Empty(t, store.settings, "a refused change wrote the blob")
	require.Equal(t, 1, f.mutated)
}

// TestMutatePersist_FacadeWriteIsNotSavedAgain: the Facade's write is the durable
// one; the blob is never written beside it, whatever the Config holds.
func TestMutatePersist_FacadeWriteIsNotSavedAgain(t *testing.T) {
	f := &stubFacade{}
	installForTest(t, f)
	store := newMockSettingsStore()
	require.NoError(t, MutatePersist(store, func(c *Config) { c.FilenameParseModel = "x" }))
	require.Empty(t, store.settings)
}

// TestSaveConfigToDatabase_RefusedWithAFacade: the whole-Config blob is never
// written once the Facade is installed. The Facade's projection fills the Config's
// secret fields with their plaintext; saving it would put every credential the
// migration moved out back on disk.
func TestSaveConfigToDatabase_RefusedWithAFacade(t *testing.T) {
	installForTest(t, &stubFacade{})
	store := newMockSettingsStore()
	err := SaveConfigToDatabase(store)
	require.ErrorIs(t, err, ErrBlobSaveWithFacade)
	require.Empty(t, store.settings, "the refused save wrote something")
}

// TestMutate_ReportsARefusalAndMutateCheckedReturnsIt: Mutate logs the refusal once;
// MutateChecked returns it.
func TestMutate_ReportsARefusalAndMutateCheckedReturnsIt(t *testing.T) {
	var reported []error
	old := mutateRefused
	mutateRefused = func(err error) { reported = append(reported, err) }
	t.Cleanup(func() { mutateRefused = old })
	refusal := errors.New("no")
	installForTest(t, &stubFacade{mutateErr: refusal})
	Mutate(func(c *Config) {})
	require.Len(t, reported, 1)
	require.ErrorIs(t, MutateChecked(func(c *Config) {}), refusal)
	require.Len(t, reported, 1, "MutateChecked reported as well as returned")
}
