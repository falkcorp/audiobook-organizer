// file: internal/scanner/ai_parser_chain_test.go
// version: 1.1.0
// guid: 2f6a8d13-b47c-4e90-a5d2-81c3f7b09e46
// last-edited: 2026-08-25

package scanner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// countingParser records that it was called at all, which is what most of these
// assertions are actually about: "the local backend was never asked" cannot be
// expressed by looking at the returned value.
type countingParser struct {
	calls  atomic.Int64
	err    error
	result []*ai.ParsedMetadata
	delay  time.Duration
}

func (p *countingParser) ParseBatch(ctx context.Context, filenames []string) ([]*ai.ParsedMetadata, error) {
	p.calls.Add(1)
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.result, nil
}

func rung(name string, p aiBatchParser) parserRung { return parserRung{name: name, parser: p} }

// TestChainFallsThroughToTheLocalBackend is the directive's main case: the
// remote is unreachable, so the local backend answers and the scan keeps its
// parsing.
func TestChainFallsThroughToTheLocalBackend(t *testing.T) {
	remote := &countingParser{err: errors.New("dial tcp 10.0.0.9:11434: connect: connection refused")}
	local := &countingParser{result: []*ai.ParsedMetadata{{Title: "Parsed Locally"}}}

	c := newParserChain(logger.New("test"), rung("remote", remote), rung("local", local))
	got, err := c.ParseBatch(t.Context(), []string{"book.m4b"})

	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "Parsed Locally", got[0].Title)
	require.Equal(t, int64(1), remote.calls.Load(), "the remote should still be tried first")
	require.Equal(t, int64(1), local.calls.Load(), "the local backend should have been asked")
	require.Equal(t, "local", c.LastRung(), "the summary must be able to say which backend answered")
}

// TestChainDoesNotFallThroughOnAPermanentError is the one that prevents a bug
// rather than adding a feature.
//
// A revoked key or an exhausted quota is the REQUEST being wrong, not the
// backend being down. Re-issuing it against the local backend cannot succeed;
// it only converts a clean immediate abort into a slow one, and on a large
// library it does that once per batch. The phase already aborts correctly on
// these -- the chain must hand the error straight back rather than swallowing
// it by trying somewhere else.
func TestChainDoesNotFallThroughOnAPermanentError(t *testing.T) {
	permanent := &ai.PermanentError{Err: errors.New("401 Unauthorized: invalid api key")}
	remote := &countingParser{err: permanent}
	local := &countingParser{result: []*ai.ParsedMetadata{{Title: "should never be reached"}}}

	c := newParserChain(logger.New("test"), rung("remote", remote), rung("local", local))
	_, err := c.ParseBatch(t.Context(), []string{"book.m4b"})

	require.Error(t, err)
	require.True(t, isPermanentAIFailure(err),
		"the permanent error must reach the phase unchanged, or the phase cannot abort on it")
	require.Equal(t, int64(0), local.calls.Load(),
		"the local backend was asked to re-run a request that is permanently wrong; "+
			"this turns one fast abort into one slow abort per batch")
}

// TestChainSkipsAnUnpreparableRung covers the directive's other half: when a
// local backend cannot be started at all, the chain must not fail loudly -- it
// runs out of rungs, and the caller defers the books.
func TestChainSkipsAnUnpreparableRung(t *testing.T) {
	remote := &countingParser{err: errors.New("no route to host")}
	var ensureCalled atomic.Bool

	c := newParserChain(logger.New("test"),
		rung("remote", remote),
		parserRung{name: "local", ensure: func(context.Context) (aiBatchParser, bool) {
			ensureCalled.Store(true)
			return nil, false // no model present / daemon would not start
		}},
	)
	_, err := c.ParseBatch(t.Context(), []string{"book.m4b"})

	require.Error(t, err)
	require.True(t, ensureCalled.Load(), "the local rung should have been offered the chance to prepare")
	require.False(t, isPermanentAIFailure(err),
		"running out of backends is transient -- marking it permanent would abort the whole phase "+
			"instead of letting the failure threshold handle it")
}

// TestChainReportsNoRungAnsweredWhenNothingWasAvailable pins the distinct error
// for "nobody was even asked", so the deferral path can tell it apart from a
// backend that answered with a failure.
func TestChainReportsNoRungAnsweredWhenNothingWasAvailable(t *testing.T) {
	c := newParserChain(logger.New("test"),
		parserRung{name: "remote", ensure: func(context.Context) (aiBatchParser, bool) { return nil, false }},
		parserRung{name: "local", ensure: func(context.Context) (aiBatchParser, bool) { return nil, false }},
	)
	_, err := c.ParseBatch(t.Context(), []string{"book.m4b"})
	require.ErrorIs(t, err, errNoRungAnswered)
}

// TestChainSkipsARungItHasNoTimeFor is the trap the budget guard exists for.
//
// runAIBatchPhase gives the whole ParseBatch call 30s. A remote that HANGS
// rather than refusing spends all of it, and handing what is left to the local
// backend does not give it a chance -- it produces a timeout attributed to the
// local backend, which then looks unhealthy in the summary and in the logs
// while being entirely fine.
func TestChainSkipsARungItHasNoTimeFor(t *testing.T) {
	// A remote that accepts the request and then hangs, burning the budget
	// down past minRungBudget before failing. The deadline must exceed
	// minRungBudget so the FIRST rung is genuinely attempted -- otherwise this
	// test would pass for the wrong reason (nothing attempted at all).
	remote := &countingParser{delay: 2 * time.Second, err: errors.New("timeout")}
	local := &countingParser{result: []*ai.ParsedMetadata{{Title: "unreachable in time"}}}

	ctx, cancel := context.WithTimeout(t.Context(), minRungBudget+1500*time.Millisecond)
	defer cancel()

	c := newParserChain(logger.New("test"), rung("remote", remote), rung("local", local))
	_, err := c.ParseBatch(ctx, []string{"book.m4b"})

	require.Error(t, err)
	require.Equal(t, int64(1), remote.calls.Load(),
		"the first rung must always be attempted regardless of budget")
	require.Equal(t, int64(0), local.calls.Load(),
		"the local backend was called with no budget left; its inevitable timeout would be "+
			"recorded as a local-backend failure when the remote is the one that hung")
	require.Equal(t, int64(1), c.SkippedForTime(),
		"a rung we never asked must be counted as skipped, not as a failure -- they say "+
			"different things about the backend's health")
}

// TestChainWithAHealthyRemoteNeverTouchesTheLocalBackend is the known-good twin
// for the fallback tests: if the local backend were called unconditionally,
// every test above would still pass, and the chain would be starting a daemon
// on every healthy scan.
func TestChainWithAHealthyRemoteNeverTouchesTheLocalBackend(t *testing.T) {
	remote := &countingParser{result: []*ai.ParsedMetadata{{Title: "Parsed Remotely"}}}
	local := &countingParser{result: []*ai.ParsedMetadata{{Title: "should never be reached"}}}

	c := newParserChain(logger.New("test"), rung("remote", remote), rung("local", local))
	got, err := c.ParseBatch(t.Context(), []string{"book.m4b"})

	require.NoError(t, err)
	require.Equal(t, "Parsed Remotely", got[0].Title)
	require.Equal(t, int64(0), local.calls.Load())
	require.Equal(t, "remote", c.LastRung())
}

// TestChainAlwaysAttemptsTheFirstRung guards a bug this suite caught in the
// budget guard's first draft.
//
// budgetRemains was applied to every rung including the first, so a caller
// whose whole deadline was under minRungBudget got NOTHING attempted: the chain
// skipped every rung and returned "no backend was available to answer" without
// having asked one. That failure is invisible in exactly the wrong way -- it
// reports a backend problem while the backends are fine, and it would arrive
// the day someone lowered the phase's 30s deadline for an unrelated reason.
//
// The budget is the caller's to spend. It governs whether falling through to
// ANOTHER backend is worthwhile; it never governs whether to make the request
// at all.
func TestChainAlwaysAttemptsTheFirstRung(t *testing.T) {
	remote := &countingParser{result: []*ai.ParsedMetadata{{Title: "Parsed Remotely"}}}

	// Deliberately far below minRungBudget.
	ctx, cancel := context.WithTimeout(t.Context(), minRungBudget/50)
	defer cancel()

	c := newParserChain(logger.New("test"), rung("remote", remote))
	got, err := c.ParseBatch(ctx, []string{"book.m4b"})

	require.NoError(t, err)
	require.Equal(t, int64(1), remote.calls.Load(),
		"a short caller deadline silently disabled AI parsing: no backend was asked, "+
			"and the chain reported a backend failure anyway")
	require.Equal(t, "Parsed Remotely", got[0].Title)
	require.Equal(t, int64(0), c.SkippedForTime(), "the first rung is not a skip")
}

// TestFallbackLocalModeActuallyBuildsAChain is the wiring assertion.
//
// AIBackendModeOpenAIFallbackLocal was a declared constant that nothing acted
// on: newAIParser's switch had no arm for it, so it fell into `default` and
// built a plain OpenAI client. Selecting the mode changed nothing, and no test
// noticed because every test asserted on parse RESULTS, which are identical
// when the remote is healthy -- the difference only shows when it is not.
//
// So this asserts on the TYPE, which is the only place the difference lives.
func TestFallbackLocalModeActuallyBuildsAChain(t *testing.T) {
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })

	config.AppConfig.EnableAIParsing = true
	config.AppConfig.OpenAIAPIKey = "sk-test-key"
	config.AppConfig.AIBackend.LLMMode = config.AIBackendModeOpenAIFallbackLocal
	config.AppConfig.AIBackend.LocalBaseURL = "http://127.0.0.1:11434/v1"
	config.AppConfig.AIBackend.LocalLLMModel = "qwen2.5:3b"

	parser, enabled := newAIParser(logger.New("test"))
	require.True(t, enabled)

	chain, ok := parser.(*parserChain)
	require.True(t, ok,
		"llm_mode=openai-fallback-local produced a %T, not a chain -- the mode is being "+
			"treated as plain openai and selecting it buys nothing", parser)
	require.Len(t, chain.rungs, 2, "expected an openai rung and a local rung")
	require.Equal(t, "openai", chain.rungs[0].name, "the remote must be tried first")
	require.Equal(t, "local", chain.rungs[1].name)
}

// TestFallbackLocalModeWithNoLocalBackendConfigured pins the directive's
// "if we can't start one locally" branch at the wiring level: the chain still
// builds and still works, it just has nothing to fall back TO. It must not
// pretend a local rung exists, and it must not refuse to parse at all.
func TestFallbackLocalModeWithNoLocalBackendConfigured(t *testing.T) {
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })

	config.AppConfig.EnableAIParsing = true
	config.AppConfig.OpenAIAPIKey = "sk-test-key"
	config.AppConfig.AIBackend.LLMMode = config.AIBackendModeOpenAIFallbackLocal
	config.AppConfig.AIBackend.LocalBaseURL = ""
	config.AppConfig.Embedding.BaseURL = ""

	parser, enabled := newAIParser(logger.New("test"))
	require.True(t, enabled, "a missing local backend must not disable AI parsing outright")

	chain, ok := parser.(*parserChain)
	require.True(t, ok)
	require.Len(t, chain.rungs, 1)
	require.Equal(t, "openai", chain.rungs[0].name)
}

// TestFallbackLocalModeWithNothingConfigured is the known-good twin for the two
// above: with neither backend available the mode must decline, not hand back an
// empty chain that fails once per batch for the whole scan.
func TestFallbackLocalModeWithNothingConfigured(t *testing.T) {
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })

	config.AppConfig.EnableAIParsing = true
	config.AppConfig.OpenAIAPIKey = ""
	config.AppConfig.AIBackend.LLMMode = config.AIBackendModeOpenAIFallbackLocal
	config.AppConfig.AIBackend.LocalBaseURL = ""
	config.AppConfig.Embedding.BaseURL = ""

	_, enabled := newAIParser(logger.New("test"))
	require.False(t, enabled)
}
