// file: internal/operations/registry/reporter_result_test.go
// version: 1.0.0
// guid: 5d92f038-71b4-42ce-9a67-04e1b83fd7a2
// last-edited: 2026-08-22

package registry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// stubResultlessReporter satisfies Reporter and deliberately does NOT implement
// ResultSetter. It stands in for the many adapter reporters across the codebase
// that have no v2 row to write to, and exists so the "unsupported" branch of
// ReporterSetResult is exercised against a real type rather than a nil interface.
//
// Do not give this type a SetResult method. Its whole purpose is the absence.
type stubResultlessReporter struct{}

func (stubResultlessReporter) UpdateProgress(int, int, string) error      { return nil }
func (stubResultlessReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (stubResultlessReporter) Logger() *slog.Logger                       { return slog.Default() }
func (stubResultlessReporter) Checkpoint(any) error                       { return nil }
func (stubResultlessReporter) IsCanceled() bool                           { return false }
func (stubResultlessReporter) Trigger(context.Context, string, any) error { return nil }
func (stubResultlessReporter) SetCurrentItem(string)                      {}
func (stubResultlessReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, Reporter) error) error {
	return fn(ctx, stubResultlessReporter{})
}

// Compile-time proof of the two properties the test depends on: it IS a Reporter,
// and it is NOT a ResultSetter. Without the second assertion, someone adding a
// SetResult method would turn TestReporterSetResult_UnsupportedReporterErrors into a
// test that silently checks nothing.
var _ Reporter = stubResultlessReporter{}

func init() {
	if _, isSetter := any(stubResultlessReporter{}).(ResultSetter); isSetter {
		panic("stubResultlessReporter must NOT implement ResultSetter; " +
			"its absence is what TestReporterSetResult_UnsupportedReporterErrors verifies")
	}
}

// capturedResult records what a reporter actually handed the store. A pointer to
// the written string (rather than the string itself) distinguishes "wrote an empty
// payload" from "never wrote at all" — the difference several of these tests turn on.
type capturedResult struct {
	id      string
	payload *string
	calls   int
}

// newResultReporter builds a dbReporter backed by database.MockStore, returning the
// capture so a test can assert on what was persisted.
//
// It uses MockStore rather than the package's fakeStore because fakeStore lives in
// package registry_test, and these tests need the unexported dbReporter.
func newResultReporter(t *testing.T, opID string) (*dbReporter, *capturedResult) {
	t.Helper()
	capture := &capturedResult{}
	ms := &database.MockStore{
		SetOperationV2ResultFunc: func(id string, resultData string) error {
			capture.id = id
			capture.payload = &resultData
			capture.calls++
			return nil
		},
	}
	return &dbReporter{
		opID:    opID,
		defID:   "test.result",
		store:   ms,
		flushCh: make(chan struct{}, 1),
		runCtx:  context.Background(),
	}, capture
}

// TestReporterSetResult_UnsupportedReporterErrors is the central guarantee.
//
// ReporterSetResult uses the same optional-interface assertion as ReporterOpID, but
// must NOT share its tolerance of absence: ReporterOpID returning "" leaves an
// activity entry merely uncorrelated, whereas a swallowed result is the operation's
// entire output vanishing with nothing recorded.
//
// stubResultlessReporter deliberately implements Reporter and NOT ResultSetter.
func TestReporterSetResult_UnsupportedReporterErrors(t *testing.T) {
	var rep Reporter = stubResultlessReporter{}

	err := ReporterSetResult(rep, map[string]int{"x": 1})

	require.Error(t, err, "a reporter that cannot persist results must not silently drop them")
	require.Contains(t, err.Error(), "cannot persist results")
	require.Contains(t, err.Error(), "stubResultlessReporter",
		"the error must name the offending type, or it is unactionable")
}

// TestReporterSetResult_DelegatesToResultSetter verifies the happy path reaches the
// store, with a positive control: the asserted value appears nowhere else, so the
// test cannot pass against an empty or defaulted write.
func TestReporterSetResult_DelegatesToResultSetter(t *testing.T) {
	r, capture := newResultReporter(t, "op-set-1")

	payload := map[string]any{"suggestions": 41879, "mode": "groups"}
	require.NoError(t, ReporterSetResult(r, payload))

	require.Equal(t, 1, capture.calls, "the store must be written exactly once")
	require.Equal(t, "op-set-1", capture.id, "the result must be keyed by the reporter's own op id")
	require.NotNil(t, capture.payload, "the payload must reach the store")
	require.Contains(t, *capture.payload, "41879")
	require.Contains(t, *capture.payload, "groups")
}

// TestDBReporterSetResult_MarshalsRatherThanStoringRaw pins why SetResult takes
// `any` instead of a pre-encoded string: the stored blob is guaranteed to be valid
// JSON, so a reader cannot be handed something that will not decode.
func TestDBReporterSetResult_MarshalsRatherThanStoringRaw(t *testing.T) {
	r, capture := newResultReporter(t, "op-set-2")

	require.NoError(t, r.SetResult([]string{"a", "b"}))

	require.NotNil(t, capture.payload)
	require.Equal(t, `["a","b"]`, *capture.payload)
}

// TestDBReporterSetResult_UnmarshalableValueErrors verifies a marshal failure is
// reported rather than writing a partial or empty result.
func TestDBReporterSetResult_UnmarshalableValueErrors(t *testing.T) {
	r, capture := newResultReporter(t, "op-set-3")

	// A channel cannot be marshalled to JSON.
	err := r.SetResult(make(chan int))

	require.Error(t, err)
	require.Contains(t, err.Error(), "marshal result")
	require.Equal(t, 0, capture.calls, "a failed marshal must not reach the store at all")
	require.Nil(t, capture.payload, "a failed marshal must not leave a partial result behind")
}

// TestDBReporterSetResult_StoreErrorPropagates verifies the store's "not found" is
// surfaced to the op rather than absorbed — PebbleStore returns exactly this for a
// row that does not exist (see TestSetOperationV2Result_MissingRowErrors).
func TestDBReporterSetResult_StoreErrorPropagates(t *testing.T) {
	ms := &database.MockStore{
		SetOperationV2ResultFunc: func(id string, _ string) error {
			return fmt.Errorf("opv2: operation not found: %s", id)
		},
	}
	r := &dbReporter{
		opID:    "op-missing",
		defID:   "test.result",
		store:   ms,
		flushCh: make(chan struct{}, 1),
		runCtx:  context.Background(),
	}

	err := r.SetResult(map[string]int{"x": 1})

	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "op-missing"),
		"the error must name the op whose result was lost, got: %v", err)
}

// TestDBReporterSetResult_NilStoreErrors covers the reporter shape used by tests
// that build a bare dbReporter with no store (see newBareReporter). It must fail
// loudly rather than panic or no-op.
func TestDBReporterSetResult_NilStoreErrors(t *testing.T) {
	r := newBareReporter()

	err := r.SetResult(map[string]int{"x": 1})

	require.Error(t, err)
	require.Contains(t, err.Error(), "no store")
}
