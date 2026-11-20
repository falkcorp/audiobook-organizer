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

// restoreAppConfig pins the process-wide config for the duration of one test,
// so a test that changes it leaves the next test the config it started with.
func restoreAppConfig(t *testing.T) {
	t.Helper()
	before := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = before }) })
}
