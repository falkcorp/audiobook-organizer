// file: internal/plugins/maintenance/throttle_reset_test.go
// version: 1.0.0
// guid: 35d137e1-f6b2-47a4-804c-b123ee3208bc
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func heldRegistry(t *testing.T) *metadata.ThrottleRegistry {
	t.Helper()
	reg := metadata.NewThrottleRegistry()
	_, ok := reg.RecordFailure("googlebooks", &metadata.ProviderStatusError{
		Provider: "googlebooks", Status: 429, Body: "Quota exceeded ... Queries per day"})
	require.True(t, ok)
	_, ok = reg.RecordFailure("hardcover", &metadata.ProviderStatusError{
		Provider: "hardcover", Status: 401, Body: "bad key"})
	require.True(t, ok)
	require.Len(t, reg.List(), 2)
	return reg
}

func runThrottleReset(t *testing.T, reg *metadata.ThrottleRegistry, params string) (*throttleResetResult, error) {
	t.Helper()
	rep := &resultReporter{}
	err := runThrottleResetOn(context.Background(), reg, json.RawMessage(params), rep)
	res, _ := rep.result.(throttleResetResult)
	return &res, err
}

func TestThrottleReset_PreviewListsAndChangesNothing(t *testing.T) {
	for _, params := range []string{"", "{}", "null", `{"dry_run":true}`, `{"dryRun":true}`} {
		reg := heldRegistry(t)
		res, err := runThrottleReset(t, reg, params)
		require.NoError(t, err, params)
		require.True(t, res.DryRun)
		require.Equal(t, 2, res.Matched)
		require.Equal(t, 0, res.Released)
		require.Len(t, res.Rows, 2)
		require.Equal(t, "googlebooks", res.Rows[0].Provider)
		require.Equal(t, "daily-quota", res.Rows[0].Kind)
		require.NotEmpty(t, res.Rows[0].Until)
		require.False(t, res.Rows[0].Released)
		require.Len(t, reg.List(), 2, "preview must not release (params=%q)", params)
	}
}

func TestThrottleReset_ApplyReleasesOnlyNamedProvider(t *testing.T) {
	reg := heldRegistry(t)
	res, err := runThrottleReset(t, reg, `{"dry_run":false,"provider":"googlebooks"}`)
	require.NoError(t, err)
	require.Equal(t, 1, res.Released)
	require.True(t, res.Rows[0].Released)
	require.False(t, reg.Throttled("googlebooks"))
	require.True(t, reg.Throttled("hardcover"))
}

func TestThrottleReset_ApplyEmptyProviderReleasesAll(t *testing.T) {
	reg := heldRegistry(t)
	res, err := runThrottleReset(t, reg, `{"dry_run":false}`)
	require.NoError(t, err)
	require.Equal(t, 2, res.Released)
	require.Empty(t, reg.List())
}

func TestThrottleReset_UnknownProviderIsAClearError(t *testing.T) {
	for _, params := range []string{`{"provider":"nope"}`, `{"dry_run":false,"provider":"nope"}`} {
		reg := heldRegistry(t)
		_, err := runThrottleReset(t, reg, params)
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), `"nope"`) && strings.Contains(err.Error(), "googlebooks"), err.Error())
		require.Len(t, reg.List(), 2)
	}
}

func TestThrottleReset_ConflictingModeKeysRefuse(t *testing.T) {
	reg := heldRegistry(t)
	_, err := runThrottleReset(t, reg, `{"dry_run":false,"dryRun":true}`)
	require.Error(t, err)
	require.Len(t, reg.List(), 2)
}
