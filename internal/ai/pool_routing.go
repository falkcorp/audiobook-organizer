// file: internal/ai/pool_routing.go
// version: 1.2.0
// guid: 146b51bb-eb0a-45ab-953b-1cc0c095646e
// last-edited: 2026-09-19

package ai

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Pool routing (design doc 2026-08-28, rollout step 2).
//
// When config.AIEndpointsRouting is on, two call sites stop resolving their
// backend from the legacy scalars and select an ai_endpoints row through
// internal/aidispatch instead:
//
//   - llm.filename_parse: RoutedFilenameParser, used by the scan and the
//     library.ai-parse op (internal/scanner newAIParser).
//   - embed.text: EmbeddingClient.WithPoolRouting, installed by the
//     embedclient service in local embedding mode.
//
// Selection is the dispatcher's: enabled rows that tick the capability
// (default-deny), priority order, free-slot assignment under each row's
// concurrency, and failover to a peer on a transport failure. Everything here
// reads the switch and the rows at CALL time, so a PUT /api/v1/config takes
// effect on the next request.

// dummyLocalAPIKey is sent to endpoints with no auth_ref. A local
// OpenAI-compatible server (Ollama) ignores the Authorization header, and the
// SDK refuses to build a client without one. Same value the legacy local
// paths use.
const dummyLocalAPIKey = "ollama"

// PoolSource supplies the current pool to routed call sites.
type PoolSource struct {
	// Active reports whether routing is on right now.
	Active func() bool
	// Endpoints returns the current rows in dispatcher form.
	Endpoints func() []aidispatch.Endpoint
	// Secret resolves an endpoint's auth_ref to its secret ("" if unset).
	Secret func(authRef string) string
	// Options are appended to every dispatcher this source builds. Production
	// passes none (process-wide slots, health and attribution); tests isolate.
	Options []aidispatch.Option
}

// ConfigPool is the production source: the switch, rows and secrets all come
// from the live config snapshot.
func ConfigPool() *PoolSource {
	return &PoolSource{
		Active: func() bool { return config.Snapshot().AIEndpointsRouting },
		Endpoints: func() []aidispatch.Endpoint {
			return config.DispatchEndpoints(config.Snapshot().AIEndpoints)
		},
		Secret: func(authRef string) string {
			if authRef == "openai_api_key" {
				return config.Snapshot().OpenAIAPIKey
			}
			return ""
		},
	}
}

// IsActive reports whether routing is on. A nil source is never active.
func (p *PoolSource) IsActive() bool { return p != nil && p.Active != nil && p.Active() }

func (p *PoolSource) dispatcher(extra ...aidispatch.Option) *aidispatch.Dispatcher {
	opts := append(append([]aidispatch.Option{}, p.Options...), extra...)
	return aidispatch.New(p.Endpoints(), opts...)
}

// apiKeyFor returns the key to send to ep. An endpoint that names an auth_ref
// whose secret is not set cannot be called; that is an endpoint failure (fail
// over to a peer), not a reason to send it an unauthenticated request.
func (p *PoolSource) apiKeyFor(ep aidispatch.Endpoint) (string, error) {
	if ep.AuthRef == "" {
		return dummyLocalAPIKey, nil
	}
	if p.Secret != nil {
		if k := p.Secret(ep.AuthRef); k != "" {
			return k, nil
		}
	}
	return "", aidispatch.EndpointFailure(fmt.Errorf("endpoint %s: auth_ref %q has no secret configured", ep.ID, ep.AuthRef))
}

// RoutedFilenameParser is a batch filename parser whose every call is routed
// through the pool for llm.filename_parse. It satisfies the scanner's
// aiBatchParser interface.
type RoutedFilenameParser struct {
	pool    *PoolSource
	llmMode string

	mu      sync.Mutex
	parsers map[string]*OpenAIParser
}

// NewRoutedFilenameParser returns a parser routed through pool. llmMode is the
// effective llm_mode: only openai and openai-fallback-local may route to a
// cloud endpoint (see aidispatch.EndpointLocality); every other mode is
// local-only.
func NewRoutedFilenameParser(pool *PoolSource, llmMode string) *RoutedFilenameParser {
	return &RoutedFilenameParser{pool: pool, llmMode: llmMode, parsers: map[string]*OpenAIParser{}}
}

// IsEnabled is always true: whether any endpoint can serve is decided per
// call, and "none can" fails closed with aidispatch.ErrNoCapableEndpoint.
func (r *RoutedFilenameParser) IsEnabled() bool { return true }

// parserFor returns the (cached) single-endpoint parser for one target. It is
// built with a nil cfg so the target's model -- the row's chat_model or its
// capability_models override -- is the model sent, never a global
// filename_parse_model meant for a different backend.
func (r *RoutedFilenameParser) parserFor(t aidispatch.Target) (*OpenAIParser, error) {
	key, err := r.pool.apiKeyFor(t.Endpoint)
	if err != nil {
		return nil, err
	}
	cacheKey := t.Endpoint.ID + "\x00" + t.Endpoint.URL + "\x00" + t.Model + "\x00" + key
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.parsers[cacheKey]; p != nil {
		return p, nil
	}
	p := newRoutedEndpointParser(key, t.Endpoint.URL, t.Model)
	r.parsers[cacheKey] = p
	return p, nil
}

// ParseBatch parses filenames on the best capable endpoint. Its result
// contract is OpenAIParser.ParseBatch's, unchanged: exactly len(filenames)
// entries, or an error.
//
// A reply that arrived but could not be used (*ReplyParseError, including
// the wrong-count *ResultCountError) is a QUALITY failure: it is returned
// as-is and never retried on a peer. The scan phase's split-and-retry owns
// that case, and asking a second endpoint the same oversized batch would only
// multiply load. It is also classified by type here because Classify would
// otherwise read the reply's model-written text and could mistake it for a
// transport error.
func (r *RoutedFilenameParser) ParseBatch(ctx context.Context, filenames []string) ([]*ParsedMetadata, error) {
	var opts []aidispatch.Option
	if r.llmMode != config.AIBackendModeOpenAI && r.llmMode != config.AIBackendModeOpenAIFallbackLocal {
		// Only the openai and openai-fallback-local modes may reach a hosted
		// API. In local mode (and any unknown mode) a cloud row is never a
		// candidate, so spillover and failover stay on the operator's network
		// exactly as legacy local mode did.
		opts = append(opts, aidispatch.WithLocalOnly())
	}
	d := r.pool.dispatcher(opts...)
	return aidispatch.Call(ctx, d, aidispatch.LLMFilenameParse, func(ctx context.Context, t aidispatch.Target) ([]*ParsedMetadata, error) {
		p, err := r.parserFor(t)
		if err != nil {
			return nil, err
		}
		res, err := p.ParseBatch(ctx, filenames)
		if err != nil {
			if _, ok := errors.AsType[*ReplyParseError](err); ok {
				return nil, aidispatch.Quality(err)
			}
			if _, ok := errors.AsType[*ResultCountError](err); ok {
				return nil, aidispatch.Quality(err)
			}
			return nil, err
		}
		return res, nil
	})
}

// WithPoolRouting makes c route its API calls through pool whenever the
// pool's switch is on, and keeps the legacy path byte-for-byte when it is
// off. The switch is read per call.
//
// The routed call is PINNED to c.Model(): only endpoints whose effective
// embed model equals it are eligible. c.Model() keys the embedding cache and
// the stored vectors, so an endpoint with a different model would silently
// mix vector spaces; with no endpoint serving it the call fails closed.
//
// Cache partitioning and the result-count check stay in EmbedBatch, around
// this hook, exactly as on the legacy path.
func (c *EmbeddingClient) WithPoolRouting(pool *PoolSource) *EmbeddingClient {
	c.pool = pool
	legacy := c.rawEmbed
	c.rawEmbed = func(ctx context.Context, texts []string) ([][]float32, error) {
		if !pool.IsActive() {
			return legacy(ctx, texts)
		}
		return c.embedRouted(ctx, texts)
	}
	return c
}

func (c *EmbeddingClient) poolRoutingActive() bool { return c.pool.IsActive() }

// embedDispatcher is the dispatcher every routed embed call uses: pinned to
// this client's model and local-only (WithPoolRouting is installed only for
// embedding_mode local, which never contacts a hosted API on the legacy path
// either). RoutedCapacity must count exactly the endpoints it can pick.
func (c *EmbeddingClient) embedDispatcher() *aidispatch.Dispatcher {
	return c.pool.dispatcher(aidispatch.WithPinnedModel(aidispatch.EmbedText, c.model), aidispatch.WithLocalOnly())
}

// RoutedCapacity is how many embed requests this client can have in flight
// across the pool right now, or 0 when its calls are not pool-routed (no pool,
// routing off, nil client). Callers that fan out embed work size their worker
// pool from it; a fixed size equal to one endpoint's slots leaves every other
// endpoint idle.
func (c *EmbeddingClient) RoutedCapacity() int {
	if c == nil || !c.pool.IsActive() {
		return 0
	}
	return c.embedDispatcher().Capacity(aidispatch.EmbedText)
}

func (c *EmbeddingClient) embedRouted(ctx context.Context, texts []string) ([][]float32, error) {
	d := c.embedDispatcher()
	return aidispatch.Call(ctx, d, aidispatch.EmbedText, func(ctx context.Context, t aidispatch.Target) ([][]float32, error) {
		key, err := c.pool.apiKeyFor(t.Endpoint)
		if err != nil {
			return nil, err
		}
		return embedViaEndpoint(ctx, t.Endpoint.URL, key, t.Model, texts, c.requestTimeout)
	})
}
