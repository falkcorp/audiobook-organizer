// file: core/integrations/metadata/provider.go
// version: 1.0.0
// guid: 8012a3d0-c910-4294-be62-151b853ac2cf
// last-edited: 2026-11-11

package metadata

import (
	"context"
	"errors"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// Provider is one metadata instance. Q and R are app types; core never
// inspects them except through the functions the app passes to Chain and
// Cache. Ref is mandatory (no optional identity interface): a provider
// without an identity cannot be budgeted, cached or held.
type Provider[Q, R any] interface {
	integrations.Instance
	Ref() integrations.Ref
	Search(ctx context.Context, q Q) ([]R, error)
}

// Lookup is optional: fetch by a provider-native key (ASIN, TMDB id, TVDB
// id). It answers an *integrations.Error of ClassNotFound when the key is
// absent.
type Lookup[R any] interface {
	Lookup(ctx context.Context, key string) (R, error)
}

// Caps is optional: which query fields a provider can use. The chain skips a
// provider whose Accepts(q) is false instead of calling it.
type Caps[Q any] interface {
	Accepts(q Q) bool
}

// Adapter returns the descriptor for an app-defined provider adapter. It is
// the metadata kind's typed constructor (05-metadata M1.3): the kind is
// always integrations.KindMetadata, whatever m.Kind says, and the built
// instance must be a Provider[Q, R].
func Adapter[Q, R any](m integrations.Meta,
	build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Provider[Q, R], error),
) integrations.Descriptor {
	m.Kind = integrations.KindMetadata
	return integrations.NewDescriptor(m, build)
}

// Result is one answer with provenance.
type Result[R any] struct {
	Provider integrations.Ref
	Value    R
	// AskedAt is when the provider gave this answer (a cached answer keeps
	// its age).
	AskedAt   time.Time
	FromCache bool
}

// OutcomeStatus is what became of one provider in one search.
type OutcomeStatus uint8

// The outcome statuses.
const (
	// Answered: the provider returned results.
	Answered OutcomeStatus = iota
	// Empty: the provider answered that it has nothing (no results, or a
	// ClassNotFound). An answer about the query, not a failure.
	Empty
	// SkippedHeld: the gate refused before sending: a hold or a spent quota.
	SkippedHeld
	// SkippedCaps: the provider's Caps.Accepts said it cannot use the query.
	SkippedCaps
	// SkippedBreaker: the provider's circuit breaker is open.
	SkippedBreaker
	// Failed: the provider could not answer (transport, auth, bad output, the
	// per-provider deadline, an egress refusal).
	Failed
	// Canceled: the caller's context ended; says nothing about the provider.
	Canceled
)

// String names the status as the metrics label spells it.
func (s OutcomeStatus) String() string {
	switch s {
	case Answered:
		return "answered"
	case Empty:
		return "empty"
	case SkippedHeld:
		return "skipped_held"
	case SkippedCaps:
		return "skipped_caps"
	case SkippedBreaker:
		return "skipped_breaker"
	case Failed:
		return "failed"
	case Canceled:
		return "canceled"
	}
	return "unknown"
}

// Outcome is one provider's part in a search.
type Outcome struct {
	Provider integrations.Ref
	Status   OutcomeStatus
	// Class is the error's class; ClassUnknown (zero) when there was none.
	Class   integrations.Class
	Err     error
	Elapsed time.Duration
}

// answered reports whether the provider gave an answer about the query,
// results or not.
func (o Outcome) answered() bool { return o.Status == Answered || o.Status == Empty }

// ErrNoProviderAnswered is Search's error when no provider answered: every
// one failed, was held or skipped, or none was configured. "No answer" must
// never look like "no results" (05-metadata M3.5).
var ErrNoProviderAnswered = errors.New("metadata: no provider answered")
