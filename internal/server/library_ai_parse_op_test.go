// file: internal/server/library_ai_parse_op_test.go
// version: 1.0.0
// guid: 32147e60-f02b-47a1-8b05-cf56ca320f50
// last-edited: 2026-08-24

package server

import (
	"context"
	"log/slog"
	"testing"

	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// aiParseTestReg builds a registry backed by a mock store. RegisterOp persists
// the definition row, so the store cannot be nil.
func aiParseTestReg(t *testing.T) *opsregistry.Registry {
	t.Helper()
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	return opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
}

// restoreEnqueueHook guards the rest of the package's tests from the process
// global this registration writes.
func restoreEnqueueHook(t *testing.T) {
	t.Helper()
	prev := scanner.EnqueueAIParseFn
	t.Cleanup(func() { scanner.EnqueueAIParseFn = prev })
}

// TestLibraryAIParseOpWiresTheScannerHookForTheServersOwnRegistry is the other
// half of the gate in RegisterLibraryAIParseOp.
//
// op_params_decode_test.go proves the hook is NOT written for a foreign
// registry. Without this test that gate could be inverted, or made
// unconditionally false, and the only symptom would be scans quietly going back
// to blocking on the LLM -- which no existing test observes.
func TestLibraryAIParseOpWiresTheScannerHookForTheServersOwnRegistry(t *testing.T) {
	restoreEnqueueHook(t)
	scanner.EnqueueAIParseFn = nil

	reg := aiParseTestReg(t)
	s := &Server{opRegistry: reg}
	require.NoError(t, s.RegisterLibraryAIParseOp(reg))

	require.NotNil(t, scanner.EnqueueAIParseFn,
		"the scan has no way to queue AI parsing and will block on the LLM inline")
}

func TestLibraryAIParseOpSkipsWiringForAForeignRegistry(t *testing.T) {
	restoreEnqueueHook(t)
	scanner.EnqueueAIParseFn = nil

	// A zero-value Server, as the params-decode contract table uses.
	require.NoError(t, (&Server{}).RegisterLibraryAIParseOp(aiParseTestReg(t)))

	require.Nil(t, scanner.EnqueueAIParseFn,
		"registering against a throwaway registry left the live hook pointing at it")
}

// TestLibraryAIParseOpQueuesRatherThanSerializingWithTheScan pins the two
// scheduling properties the op exists for.
func TestLibraryAIParseOpQueuesRatherThanSerializingWithTheScan(t *testing.T) {
	restoreEnqueueHook(t)

	reg := aiParseTestReg(t)
	require.NoError(t, (&Server{opRegistry: reg}).RegisterLibraryAIParseOp(reg))

	def, ok := reg.Def("library.ai-parse")
	require.True(t, ok, "library.ai-parse was not registered")

	// Its own key: sharing "library.scan" would make AI parsing block the next
	// scan, which is the coupling this op removes.
	require.Equal(t, "library.ai-parse", def.ConcurrencyKey)

	// DedupeQueuedRuns must stay false. The registry reuses an active op's ID
	// instead of queueing when this is set, so a scan that enqueued several
	// batches would have every batch after the first collapse into the first
	// one -- silently dropping most of the candidates.
	require.False(t, def.DedupeQueuedRuns,
		"batches would collapse into the first active op instead of queueing behind it")

	// ResumeDrop: a dropped batch costs nothing permanent, because the books
	// keep their empty fields and the next scan re-nominates them. A restart
	// would re-send LLM requests for books the batch already finished.
	require.Equal(t, opsregistry.ResumeDrop, def.ResumePolicy)
}

// TestLibraryAIParseOpAcceptsAnEmptyBatchWithoutFailing covers the batch that
// arrives with nothing to do -- it must not fail the operation.
func TestLibraryAIParseOpAcceptsAnEmptyBatchWithoutFailing(t *testing.T) {
	restoreEnqueueHook(t)

	reg := aiParseTestReg(t)
	require.NoError(t, (&Server{opRegistry: reg}).RegisterLibraryAIParseOp(reg))
	def, ok := reg.Def("library.ai-parse")
	require.True(t, ok)

	require.NoError(t, def.Run(context.Background(), []byte(`{"books":[]}`), &aiParseStubReporter{}))
}

// aiParseStubReporter records nothing; the empty-batch path only calls
// UpdateProgress.
type aiParseStubReporter struct{ opsregistry.Reporter }

func (aiParseStubReporter) UpdateProgress(int, int, string) error { return nil }
