// file: core/integrations/ai/openai/adapter.go
// version: 1.0.0
// guid: 92a5e3f7-6c18-4b04-8d2a-1f7b9c4e0a63
// last-edited: 2026-11-12

// Package openai is the OpenAI-compatible adapter (core.openai_compat) of the ai
// kind (05-ai section 3): the client half of HEAD's OpenAIParser and
// EmbeddingClient, behind ai.Backend, ai.TextGenerator, ai.Embedder,
// ai.Transcriber and ai.BatchBackend. It speaks POST /v1/chat/completions,
// /v1/embeddings, /v1/audio/transcriptions, /v1/files plus /v1/batches, and
// GET /v1/models for discovery, to OpenAI itself and to the local servers that
// copy its wire (Ollama, llama.cpp, vLLM, whisper servers).
//
// This package and its subpackages are the ONLY place the openai-go SDK may be
// imported (archtest NoAISDKOutsideAdapters); the SDK's types never leave it.
// Every SDK error is mapped to *integrations.Error here, so the router never
// reads a provider's error shape. The adapter is dark until the integrations
// flip (F7-09): nothing registers it, an app does through app.Options.Adapters.
package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/secrets"
)

// AdapterID is the adapter's ID.
const AdapterID integrations.AdapterID = "core.openai_compat"

// secretField is the settings field that holds the API key's reference.
const secretField = "api_key"

// callTimeout is the per-attempt transport timeout of the adapter's instances;
// the router bounds each call with its own capability timeout (transcription
// runs up to 30 minutes), so this only has to be larger than any of them.
const callTimeout = 35 * time.Minute

// Settings are an instance's settings: the routing attributes every ai
// instance carries (ai.EndpointSettings), the server's base URL and the API
// key's reference. A local server that needs no key leaves api_key unset; the
// key is read at each request, never at build time, so a rotation takes effect
// on the next call.
type Settings struct {
	ai.EndpointSettings
	// URL is the API base, e.g. https://api.openai.com/v1 or a local server's
	// /v1 root.
	URL    string      `json:"url" schema:"format=uri"`
	APIKey secrets.Ref `json:"api_key,omitzero"`
}

// Descriptor is the adapter for app.Options.Adapters. It returns an error only
// if Settings cannot be reflected, which a test of this package rules out.
func Descriptor() (integrations.Descriptor, error) {
	schema, err := config.SchemaFor[Settings]()
	if err != nil {
		return integrations.Descriptor{}, fmt.Errorf("openai: settings schema: %w", err)
	}
	return ai.Adapter(integrations.Meta{
		ID:          AdapterID,
		DisplayName: "OpenAI-compatible",
		Version:     "1.0.0",
		InfoURL:     "https://platform.openai.com/docs/api-reference",
		Settings:    schema,
		Egress:      integrations.EgressConfigured,
		// MaxRetries -1: no transport retry. The router owns failover and the
		// bench, and a transport that retried a 429 or a 5xx on the same
		// endpoint would hide the failure from both.
		Limits: integrations.Limits{RPS: 20, Burst: 20, MaxRetries: -1, Timeout: callTimeout},
	}, build), nil
}

// instance is one OpenAI-compatible endpoint.
type instance struct {
	ref integrations.Ref
	cl  oai.Client
}

var (
	_ ai.Backend          = (*instance)(nil)
	_ ai.TextGenerator    = (*instance)(nil)
	_ ai.Embedder         = (*instance)(nil)
	_ ai.Transcriber      = (*instance)(nil)
	_ ai.BatchBackend     = (*instance)(nil)
	_ integrations.Tester = (*instance)(nil)
)

// build makes an instance. It does no I/O.
func build(_ context.Context, env integrations.Env, cfg integrations.InstanceConfig) (ai.Backend, error) {
	raw, _ := cfg.Settings["url"].(string)
	base, err := baseURL(raw)
	if err != nil {
		return nil, &integrations.Error{Ref: env.Ref, Op: "build", Class: integrations.ClassInvalid, Err: err}
	}
	opts := []option.RequestOption{
		option.WithBaseURL(base),
		// The SDK's own retry would silently retry a 429 or a 5xx before the
		// router ever saw it, defeating its failover and bench.
		option.WithMaxRetries(0),
		option.WithHTTPClient(env.HTTP.Client()),
	}
	if hasKey(cfg.Settings) {
		opts = append(opts, option.WithMiddleware(keyMiddleware(env)))
	}
	return &instance{ref: env.Ref, cl: oai.NewClient(opts...)}, nil
}

// baseURL validates the configured URL and gives it the trailing slash the SDK
// joins paths onto.
func baseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("the url setting must be an absolute http or https URL")
	}
	if u.User != nil {
		return "", errors.New("the url setting must not carry credentials; set api_key instead")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

// hasKey reports whether the instance names an API key.
func hasKey(settings map[string]any) bool {
	v, ok := settings[secretField]
	if !ok || v == nil {
		return false
	}
	if s, isStr := v.(string); isStr {
		return s != ""
	}
	return true
}

// keyMiddleware sets the Authorization header from the secret at each request.
// A key that cannot be read stops the request before it is sent: never an
// unauthenticated request to a server that expects a key.
func keyMiddleware(env integrations.Env) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		v, err := env.Secrets.Secret(req.Context(), secretField)
		if err != nil {
			class := integrations.ClassOf(req.Context(), err)
			if class == integrations.ClassUnknown {
				class = integrations.ClassAuth // a referenced key that is unset or unreadable
			}
			return nil, &integrations.Error{Ref: env.Ref, Op: "secret", Class: class, Err: errors.New("the API key could not be read")}
		}
		req.Header.Set("Authorization", "Bearer "+v.Reveal())
		return next(req)
	}
}

// Close releases nothing: the connection pool belongs to the instance's HTTP.
func (*instance) Close() error { return nil }

// Caps lists the server's models (GET /v1/models). The adapter measures no
// feature: structured output, vision and batch support differ by server
// version, so the operator declares them on the endpoint (05-ai section 11,
// item 1). Locality is left for the router to decide.
func (i *instance) Caps(ctx context.Context) (ai.BackendCaps, error) {
	page, err := i.cl.Models.List(ctx)
	if err != nil {
		return ai.BackendCaps{}, i.fail(ctx, "caps", err)
	}
	var caps ai.BackendCaps
	for _, m := range page.Data {
		caps.Models = append(caps.Models, m.ID)
	}
	return caps, nil
}

// Health asks for the model list: one request, no tokens spent, no remote
// state changed.
func (i *instance) Health(ctx context.Context) integrations.Health {
	if _, err := i.Caps(ctx); err != nil {
		switch integrations.ClassOf(ctx, err) {
		case integrations.ClassAuth:
			return integrations.Health{Status: integrations.HealthAuthFailed, Message: "the server rejected the API key"}
		case integrations.ClassRefusedLocal:
			return integrations.Health{Status: integrations.HealthMisconfigured, Message: integrations.ReasonSecretMissing}
		}
		return integrations.Health{Status: integrations.HealthDown, Message: "the server did not answer usefully"}
	}
	return integrations.Health{Status: integrations.HealthOK}
}

// Test is the extra test of Registry.Test: the caps fetch.
func (i *instance) Test(ctx context.Context) error {
	_, err := i.Caps(ctx)
	return err
}

// ---- errors ------------------------------------------------------------------------

// detail is a bounded, one-line excerpt of a provider's error body for the
// instance's own log (integrations.Error.Detail). The key travels in a header,
// never in a body, so the excerpt holds none.
func detail(raw string) string {
	raw = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, raw)
	if len(raw) > integrations.MaxDetail {
		raw = raw[:integrations.MaxDetail]
	}
	return raw
}

// fail maps whatever the SDK or the transport returned to an
// *integrations.Error: an error that already is one (the gate's refusal, the
// egress guard, the key middleware) keeps its class; a provider answer is
// classified by its status and by the type and code of its body (a 429 with a
// quota marker in either is ClassQuota); a done parent is ClassCanceled; the
// rest goes through integrations.ClassOf. The SDK's error text names the
// request and the body, so it is not wrapped: Err carries a fixed phrase and
// Detail the bounded body excerpt.
func (i *instance) fail(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*integrations.Error](err); ok {
		if e.Ref == (integrations.Ref{}) {
			c := *e
			c.Ref = i.ref
			return &c
		}
		return e
	}
	if api, ok := errors.AsType[*oai.Error](err); ok {
		e := &integrations.Error{
			Ref: i.ref, Op: op, Status: api.StatusCode, Detail: detail(api.RawJSON()),
			Class: ai.StatusClass(api.StatusCode, api.Type, api.Code),
			Err:   fmt.Errorf("the server answered %d", api.StatusCode),
		}
		if api.Response != nil {
			if s := api.Response.Header.Get("Retry-After"); s != "" {
				if secs, perr := strconv.Atoi(strings.TrimSpace(s)); perr == nil && secs >= 0 {
					e.RetryAfter = time.Duration(secs) * time.Second
				}
			}
		}
		return e
	}
	return &integrations.Error{Ref: i.ref, Op: op, Class: integrations.ClassOf(ctx, err), Err: scrubbed(err)}
}

// scrubbed is err's class text without the URL a *url.Error names.
func scrubbed(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// badOutput is the error of an answer the endpoint gave and that is unusable.
func (i *instance) badOutput(op, why string) error {
	return &integrations.Error{Ref: i.ref, Op: op, Class: integrations.ClassBadOutput, Err: errors.New(why)}
}
