// file: internal/server/library_ai_parse_op_test.go
// version: 1.2.0
// guid: 32147e60-f02b-47a1-8b05-cf56ca320f50
// last-edited: 2026-08-24

package server

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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
	prev := scanner.SetEnqueueAIParse(nil)
	t.Cleanup(func() { scanner.SetEnqueueAIParse(prev) })
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

	reg := aiParseTestReg(t)
	s := &Server{opRegistry: reg}
	require.NoError(t, s.RegisterLibraryAIParseOp(reg))

	require.True(t, scanner.EnqueueAIParseWired(),
		"the scan has no way to queue AI parsing and will block on the LLM inline")
}

func TestLibraryAIParseOpSkipsWiringForAForeignRegistry(t *testing.T) {
	restoreEnqueueHook(t)

	// A zero-value Server, as the params-decode contract table uses.
	require.NoError(t, (&Server{}).RegisterLibraryAIParseOp(aiParseTestReg(t)))

	require.False(t, scanner.EnqueueAIParseWired(),
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

// aiParseStubReporter records the progress messages it is given.
type aiParseStubReporter struct {
	opsregistry.Reporter
	mu       sync.Mutex
	messages []string
	logs     []string
}

func (r *aiParseStubReporter) UpdateProgress(_, _ int, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
	return nil
}

// Log records what the op writes into the OPERATION record, as opposed to the
// process log. The distinction is the whole point of the summary reporting: the
// AI phase's own failures are log.Warn + nil return, so without reporter.Log an
// aborted run finishes green with the evidence only in journalctl.
func (r *aiParseStubReporter) Log(_ slog.Level, message string, _ ...slog.Attr) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, message)
	return nil
}

func (r *aiParseStubReporter) logged() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.logs...)
}

func (r *aiParseStubReporter) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.messages...)
}

// TestLibraryAIParseOpReportsPerBatchProgressToTheReporter guards the op against
// the registry watchdog.
//
// runAIBatchPhase stamps progress per LLM batch through log.UpdateProgress. If
// the op hands it a bare logger.New instead of a reporter-backed one, those
// stamps go to stdout and the registry never sees them -- and the watchdog
// cancels an op that reports nothing for ProgressTimeout. A 200-book batch is
// ~10 LLM calls of up to 30s each, comfortably past it. This phase already
// caused exactly that failure once: it is why library.scan could finish its
// whole file walk and still be canceled for inactivity.
//
// The backend points at a closed port, so the batch fails fast. That is fine --
// the stamp under test is emitted BEFORE the call, deliberately, for this reason.
func TestLibraryAIParseOpReportsPerBatchProgressToTheReporter(t *testing.T) {
	restoreEnqueueHook(t)

	oldAI := config.AppConfig.EnableAIParsing
	oldBackend := config.AppConfig.AIBackend
	t.Cleanup(func() {
		config.AppConfig.EnableAIParsing = oldAI
		config.AppConfig.AIBackend = oldBackend
	})
	config.AppConfig.EnableAIParsing = true
	config.AppConfig.AIBackend.LLMMode = config.AIBackendModeLocal
	config.AppConfig.AIBackend.LocalBaseURL = "http://127.0.0.1:1"
	config.AppConfig.AIBackend.LocalLLMModel = "test-model"

	reg := aiParseTestReg(t)
	require.NoError(t, (&Server{opRegistry: reg}).RegisterLibraryAIParseOp(reg))
	def, ok := reg.Def("library.ai-parse")
	require.True(t, ok)

	rep := &aiParseStubReporter{}
	require.NoError(t, def.Run(context.Background(),
		[]byte(`{"books":[{"id":"bk1","file_path":"/lib/a.m4b"}]}`), rep))

	var sawBatch bool
	for _, m := range rep.seen() {
		if strings.Contains(m, "AI parsing batch") {
			sawBatch = true
		}
	}
	require.True(t, sawBatch,
		"per-batch progress never reached the reporter; the watchdog will cancel long batches. seen=%v", rep.seen())

	// The same run must leave a truthful summary in the OPERATION record.
	//
	// The backend is a closed port, so every batch failed. Before this, the op
	// finished by stamping "Parsed 1 filename(s)" regardless -- because every
	// failure inside the AI phase is a log.Warn and a nil return, and
	// LoggerFromReporter only forwards UpdateProgress, so the warnings went to
	// the process log. A wedged LLM produced a green operation claiming it had
	// parsed everything.
	var sawSummary bool
	for _, m := range rep.logged() {
		if strings.Contains(m, "ai parse summary:") && strings.Contains(m, "batch failure") {
			sawSummary = true
		}
	}
	require.True(t, sawSummary,
		"the operation record has no summary of what actually happened; a failed run is indistinguishable from a clean one. logged=%v", rep.logged())

	// And nothing may claim a parse that did not happen.
	for _, m := range rep.seen() {
		require.NotContains(t, m, "Parsed 1 filename(s)",
			"the op reported a fabricated success count after every batch failed")
	}
}
