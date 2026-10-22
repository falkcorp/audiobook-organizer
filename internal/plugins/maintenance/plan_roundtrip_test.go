// file: internal/plugins/maintenance/plan_roundtrip_test.go
// version: 1.0.0
// guid: 3f0d6c2e-8b41-4a97-9d35-6e1c7a2b4f80
// last-edited: 2026-10-22

package maintenance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// roundTripPlan returns the plan as a stored one reads back: its rows are not
// part of the stored summary (they live beside the op, one key per row), so
// the fidelity a test wants is that every row survives its own JSON. Apply
// reads rows through PlanResult.Select, which serves an in-memory plan too.
func roundTripPlan(t testing.TB, res *repairs.PlanResult) repairs.PlanResult {
	t.Helper()
	stored := *res
	raw, err := json.Marshal(res.Rows)
	require.NoError(t, err)
	stored.Rows = nil
	require.NoError(t, json.Unmarshal(raw, &stored.Rows))
	return stored
}
