// file: internal/scanner/ai_parser_chain.go
// version: 1.0.0
// guid: 7b3c9e41-52a8-4d16-b0f7-9c8e2a4d6f31
// last-edited: 2026-08-25

package scanner

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// parserChain tries several AI backends in order so that one being unreachable
// does not cost the scan its filename parsing.
//
// It implements aiBatchParser, so runAIBatchPhase does not change shape: from
// the phase's point of view a chain is just a parser that happens to have more
// than one way to answer.
//
// The distinction that makes falling through safe is UNREACHABLE vs REFUSED.
// A connection failure, DNS failure or timeout means the request never got a
// verdict, so asking a different backend is worth doing. A revoked key,
// exhausted quota or a malformed request means the request itself is wrong;
// re-issuing it against another backend just burns time and, worse, converts a
// clean permanent abort into a slow one. isPermanentAIFailure already draws
// exactly this line for the phase's own abort logic, so the chain reuses it
// rather than inventing a second classifier that could drift from it.
type parserChain struct {
	rungs []parserRung
	log   logger.Logger

	// lastRung names the rung that most recently answered, for the summary.
	// Written from several batch workers at once, so it is atomic.
	lastRung atomic.Value

	// skippedForTime counts rungs that were never attempted because the
	// caller's deadline had already been spent by an earlier rung. Counted
	// separately from failures because "we did not ask" and "we asked and it
	// failed" are different facts and only one of them says anything about
	// the backend's health.
	skippedForTime atomic.Int64
}

// parserRung is one backend in the chain.
type parserRung struct {
	// name identifies the rung in logs and in the phase summary.
	name string

	// parser does the work. Built lazily by ensure, because constructing a
	// local backend can mean starting a daemon and we must not pay that on a
	// scan where the remote answered.
	parser aiBatchParser

	// ensure prepares the rung and reports whether it can be used at all.
	// Nil means "always available". A rung that cannot be prepared -- no local
	// model present, daemon refused to start -- returns false, and the chain
	// moves on. That is the directive's "if we can't start one locally we just
	// kick them into a queue", expressed one level down.
	ensure func(ctx context.Context) (aiBatchParser, bool)
}

// minRungBudget is the least time a rung is worth offering.
//
// runAIBatchPhase gives ParseBatch a 30s deadline for the whole call, and that
// budget is shared by every rung the chain tries. An UNREACHABLE remote is
// cheap -- a refused connection or a DNS failure returns in milliseconds, so
// the next rung inherits almost the full budget, which is the case this chain
// exists for. A remote that accepts the connection and then HANGS is the
// expensive one: it can spend the entire deadline before failing, and calling
// the next rung with 200ms left does not give it a chance, it just relabels
// the same timeout as the local backend's fault.
//
// So a rung with less than this left is recorded as skipped rather than tried.
// The books land in the deferred queue with an honest reason instead of a
// fabricated local-backend failure.
const minRungBudget = 5 * time.Second

// errNoRungAnswered is returned when every rung was unavailable or skipped and
// none of them actually produced an error to report. It is deliberately NOT a
// permanent failure: nothing about it says the request was wrong, so the
// phase's retry/threshold logic should treat it like any other transient miss.
var errNoRungAnswered = errors.New("ai parser chain: no backend was available to answer")

// newParserChain builds a chain. Rungs are tried in the order given.
func newParserChain(log logger.Logger, rungs ...parserRung) *parserChain {
	return &parserChain{rungs: rungs, log: log}
}

// ParseBatch satisfies aiBatchParser.
func (c *parserChain) ParseBatch(ctx context.Context, filenames []string) ([]*ai.ParsedMetadata, error) {
	var lastErr error

	attempted := false
	for _, rung := range c.rungs {
		// Stop before pretending to try a rung we have no time for. Checked
		// before ensure, not after, because ensure itself can be the
		// expensive part (starting a daemon).
		//
		// Only ever applied to FALLING THROUGH, never to the first attempt.
		// The budget belongs to the caller, and a caller that allows less than
		// minRungBudget still wants its request made -- gating the first rung
		// on it would silently turn AI parsing off entirely if the phase's 30s
		// deadline were ever lowered, with every batch reporting "no backend
		// was available" and no backend having been asked.
		if attempted && !budgetRemains(ctx) {
			c.skippedForTime.Add(1)
			c.log.Warn("ai parser chain: skipping the %q backend, the deadline was already spent by an earlier backend", rung.name)
			continue
		}

		parser := rung.parser
		if rung.ensure != nil {
			p, ok := rung.ensure(ctx)
			if !ok {
				c.log.Info("ai parser chain: the %q backend is unavailable, trying the next one", rung.name)
				continue
			}
			parser = p
		}
		if parser == nil {
			continue
		}

		attempted = true
		results, err := parser.ParseBatch(ctx, filenames)
		if err == nil {
			c.lastRung.Store(rung.name)
			return results, nil
		}

		// A permanent failure is the request's fault, not the backend's.
		// Return it as-is so the phase aborts exactly as it does today --
		// falling through here is the bug this branch prevents.
		if isPermanentAIFailure(err) {
			c.lastRung.Store(rung.name)
			return nil, err
		}

		lastErr = err
		c.log.Warn("ai parser chain: the %q backend did not answer (%v), trying the next one", rung.name, err)
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errNoRungAnswered
}

// budgetRemains reports whether ctx has enough time left to be worth handing to
// another backend. A ctx with no deadline always does.
func budgetRemains(ctx context.Context) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) >= minRungBudget
}

// LastRung reports which backend most recently answered, or "" if none has.
// Used by the phase summary so a run served entirely by the local fallback
// cannot be mistaken for a normal one.
func (c *parserChain) LastRung() string {
	if v := c.lastRung.Load(); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// SkippedForTime reports how many rung attempts were abandoned because the
// caller's deadline was already spent.
func (c *parserChain) SkippedForTime() int64 { return c.skippedForTime.Load() }
