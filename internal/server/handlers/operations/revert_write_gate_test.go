// file: internal/server/handlers/operations/revert_write_gate_test.go
// version: 1.0.0
// guid: 5e2d8b17-9a40-4c63-b1f7-0d6a3e92c485
// last-edited: 2026-11-18

package operations_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	operationsmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A revert is an HTTP request, so the dispatcher's write-set gate never saw it.
// These tests pin the handler claiming the write-set its operation's definition
// declares for the length of the revert: refused with 409 while another writer
// holds an overlapping write, released when the revert ends however it ends.

// fakeRevertGate records the claims the handler makes.
type fakeRevertGate struct {
	declared  map[string][]opsregistry.Resource
	conflict  error // HoldWriteSet's refusal
	holds     []string
	held      int
	released  int
	holdCalls int
}

func (g *fakeRevertGate) WriteSetOfDef(defID string) []opsregistry.Resource { return g.declared[defID] }

func (g *fakeRevertGate) HoldWriteSet(holderID, reason string, writes []opsregistry.Resource) (func(), error) {
	g.holdCalls++
	if g.conflict != nil {
		return nil, g.conflict
	}
	g.holds = append(g.holds, holderID)
	g.held++
	return func() { g.released++ }, nil
}

// gatedRevertHandler builds a handler over an op row of defID, whose revert func
// reports whether the claim was held while it ran.
func gatedRevertHandler(t *testing.T, defID string, gate *fakeRevertGate, revertErr error, ran *bool, heldDuring *int) *operations.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := operationsmocks.NewMockOperationsStore(t)
	store.EXPECT().GetOperationV2("op-1").Return(&database.OperationV2Row{ID: "op-1", DefID: defID}, nil)
	store.EXPECT().GetOperationChanges("op-1").Return(nil, nil)
	return operations.New(store, nil, nil, nil, nil, nil, nil,
		func(changes []*database.OperationChange) (*undo.UndoConflictReport, error) { return nil, nil },
		func(id string, _ audiobooks.RevertActor) (*audiobooks.RevertResult, error) {
			*ran = true
			*heldDuring = gate.held - gate.released
			if revertErr != nil {
				return nil, revertErr
			}
			return &audiobooks.RevertResult{OperationID: id, Total: 1, Restored: 1}, nil
		},
	).WithRevertWriteGate(gate)
}

func TestRevertOperation_ClaimsTheOperationsWriteSetAndReleasesIt(t *testing.T) {
	gate := &fakeRevertGate{declared: map[string][]opsregistry.Resource{
		"maintenance.series-denumber": {opsregistry.ResBooks, opsregistry.ResSeries},
	}}
	var ran bool
	var heldDuring int
	h := gatedRevertHandler(t, "maintenance.series-denumber", gate, nil, &ran, &heldDuring)

	w := postRevert(h)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.True(t, ran)
	assert.Equal(t, []string{"revert:op-1"}, gate.holds)
	assert.Equal(t, 1, heldDuring, "the claim is held while the revert runs")
	assert.Equal(t, 1, gate.released, "and released when it ends")
}

func TestRevertOperation_ReleasesTheClaimWhenTheRevertFails(t *testing.T) {
	gate := &fakeRevertGate{declared: map[string][]opsregistry.Resource{"maintenance.series-denumber": {opsregistry.ResSeries}}}
	var ran bool
	var heldDuring int
	h := gatedRevertHandler(t, "maintenance.series-denumber", gate, errors.New("restore failed"), &ran, &heldDuring)

	w := postRevert(h)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.True(t, ran)
	assert.Equal(t, 1, gate.released, "a failed revert must not leave the write-set held")
}

// The refusal: nothing is reverted, and the answer names what is writing.
func TestRevertOperation_RefusedWhileAnotherWriterHoldsTheWriteSet(t *testing.T) {
	gate := &fakeRevertGate{
		declared: map[string][]opsregistry.Resource{"maintenance.series-denumber": {opsregistry.ResBooks, opsregistry.ResSeries}},
		conflict: &opsregistry.WriteSetConflict{BlockerOpID: "op-prune", BlockerDefID: "dedup.series-prune", Resources: []opsregistry.Resource{opsregistry.ResSeries}},
	}
	var ran bool
	var heldDuring int
	h := gatedRevertHandler(t, "maintenance.series-denumber", gate, nil, &ran, &heldDuring)

	w := postRevert(h)

	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.False(t, ran, "a refused revert writes nothing")
	assert.Contains(t, w.Body.String(), "op-prune")
	assert.Contains(t, w.Body.String(), "dedup.series-prune")
	assert.Contains(t, w.Body.String(), "series")
	assert.Zero(t, gate.released, "nothing was held, so nothing is released")
}

// A gate that fails for another reason refuses too: a revert that cannot be
// fenced does not run unfenced.
func TestRevertOperation_RefusedWhenTheGateCannotSay(t *testing.T) {
	gate := &fakeRevertGate{
		declared: map[string][]opsregistry.Resource{"maintenance.series-denumber": {opsregistry.ResSeries}},
		conflict: errors.New("registry shutting down"),
	}
	var ran bool
	var heldDuring int
	h := gatedRevertHandler(t, "maintenance.series-denumber", gate, nil, &ran, &heldDuring)

	w := postRevert(h)

	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.False(t, ran)
}

// An operation whose definition declares no Writes is invisible to the gate, in
// both directions, exactly as the dispatcher treats it.
func TestRevertOperation_AnOperationWithNoDeclaredWritesIsNotGated(t *testing.T) {
	gate := &fakeRevertGate{declared: map[string][]opsregistry.Resource{}}
	var ran bool
	var heldDuring int
	h := gatedRevertHandler(t, "dedup.book-combine", gate, nil, &ran, &heldDuring)

	w := postRevert(h)

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.True(t, ran)
	assert.Zero(t, gate.holdCalls, "no claim is made for an operation that declares none")
}
