// file: internal/config/ai_endpoints.go
// version: 1.0.0
// guid: 3f0b6c2e-8a41-4d7e-9b15-6e2c7a9d4f10
// last-edited: 2026-09-13

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// AIEndpoint is one AI server row in the capability-routing settings
// (PLAN section 3). It is stored in the config blob as the ai_endpoints list.
//
// It is a plain struct with no custom UnmarshalJSON on purpose: the
// unknown-key walker (unknown_keys.go) skips any json.Unmarshaler, so a
// custom decoder would silently stop PUT /config from rejecting a typo such
// as ai_endpoints[0].capabilites.
//
// NOTHING ROUTES ON THIS YET. PR 2 only stores, validates, migrates and
// reports these rows; every AI call site still resolves its backend from the
// legacy fields, which the migration keeps writing.
type AIEndpoint struct {
	ID       string `json:"id"       mapstructure:"id"`
	Label    string `json:"label"    mapstructure:"label"`
	Protocol string `json:"protocol" mapstructure:"protocol"`
	URL      string `json:"url"      mapstructure:"url"`

	ChatModel  string `json:"chat_model"  mapstructure:"chat_model"`
	EmbedModel string `json:"embed_model" mapstructure:"embed_model"`
	// CapabilityModels overrides the kind's model for one capability ID.
	CapabilityModels map[string]string `json:"capability_models,omitempty" mapstructure:"capability_models"`

	// Priority orders candidates: lower is preferred.
	Priority int `json:"priority" mapstructure:"priority"`
	// Concurrency is the in-flight cap. 0 means 1, never "unlimited".
	Concurrency int  `json:"concurrency" mapstructure:"concurrency"`
	Enabled     bool `json:"enabled"     mapstructure:"enabled"`

	// Capabilities are the work checkboxes: registered capability IDs only,
	// default-deny, no wildcards. IDs this binary does not know (written by a
	// newer build) are kept on load and ignored by selection.
	Capabilities []string `json:"capabilities" mapstructure:"capabilities"`
	// Labels are declared hardware/placement labels ("local", "mac"). The
	// "gpu" label is MEASURED by the probe (PR 3), never declared here.
	Labels   []string `json:"labels,omitempty"   mapstructure:"labels"`
	Features []string `json:"features,omitempty" mapstructure:"features"`

	RequireGPU bool `json:"require_gpu" mapstructure:"require_gpu"`
	// AuthRef names a secret setting (e.g. "openai_api_key"). The secret
	// itself is never stored on the row.
	AuthRef string `json:"auth_ref,omitempty" mapstructure:"auth_ref"`
	// HostRoots optionally overrides this host's path for a library root_id
	// (PLAN section 3b). Keys are root IDs, so any key is accepted.
	HostRoots map[string]string `json:"host_roots,omitempty" mapstructure:"host_roots"`
}

// Endpoint row IDs and values written by migrateAIEndpointsBlob.
const (
	AIEndpointIDLocalLLM = "local-llm"
	AIEndpointIDOpenAI   = "openai"
	AIEndpointIDLocalUV  = "local-uv"

	// DefaultOpenAIBaseURL is the OpenAI API base the SDK uses when
	// openai_base_url is empty.
	DefaultOpenAIBaseURL = "https://api.openai.com/v1"
	// DefaultOpenAIChatModel is the model every OpenAI parser falls back to
	// when no per-feature model is configured.
	DefaultOpenAIChatModel = "gpt-5-mini"
	// LocalUVEndpointURL identifies the in-process uv/openai-whisper runner.
	// It is not a network address.
	LocalUVEndpointURL = "local:uv-openai-whisper"
)

// knownAIAuthRefs are the secret names an endpoint row may reference.
var knownAIAuthRefs = []string{"openai_api_key"}

// DispatchEndpoint converts the stored row into the dispatcher's input type.
// Only the status API and tests call it in PR 2; no dispatch path does.
func (e AIEndpoint) DispatchEndpoint() aidispatch.Endpoint {
	return aidispatch.Endpoint{
		ID:               e.ID,
		Label:            e.Label,
		Protocol:         aidispatch.Protocol(e.Protocol),
		URL:              e.URL,
		ChatModel:        e.ChatModel,
		EmbedModel:       e.EmbedModel,
		CapabilityModels: cloneStringMap(e.CapabilityModels),
		Priority:         e.Priority,
		Concurrency:      e.Concurrency,
		Enabled:          e.Enabled,
		Capabilities:     slices.Clone(e.Capabilities),
		Labels:           slices.Clone(e.Labels),
		Features:         slices.Clone(e.Features),
		RequireGPU:       e.RequireGPU,
		AuthRef:          e.AuthRef,
		HostRoots:        cloneStringMap(e.HostRoots),
	}
}

// DispatchEndpoints converts every row.
func DispatchEndpoints(rows []AIEndpoint) []aidispatch.Endpoint {
	out := make([]aidispatch.Endpoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.DispatchEndpoint())
	}
	return out
}

// UnknownCapabilities returns the capability IDs on the row that this binary
// does not register. Selection ignores them; the status API reports them.
func (e AIEndpoint) UnknownCapabilities() []string {
	var out []string
	for _, id := range e.Capabilities {
		if _, ok := aidispatch.ParseCapability(id); !ok {
			out = append(out, id)
		}
	}
	return out
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// normalizeEndpointURL is the form used for the (protocol, URL) uniqueness
// check: trimmed, case-folded, without a trailing slash.
func normalizeEndpointURL(u string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(u)), "/")
}

// validateAIEndpoints checks a submitted ai_endpoints list. It runs only on a
// PUT that carries the key, so rows stored by a newer binary (with capability
// IDs this one does not know) never block an unrelated settings save.
func validateAIEndpoints(rows []AIEndpoint) error {
	var errs []string
	ids := make(map[string]int, len(rows))
	pairs := make(map[string]string, len(rows))
	for i, r := range rows {
		where := fmt.Sprintf("ai_endpoints[%d]", i)
		if strings.TrimSpace(r.ID) == "" {
			errs = append(errs, where+": id is required")
		} else if j, dup := ids[r.ID]; dup {
			errs = append(errs, fmt.Sprintf("%s: duplicate id %q (also ai_endpoints[%d])", where, r.ID, j))
		} else {
			ids[r.ID] = i
		}
		if r.ID != "" {
			where = fmt.Sprintf("ai_endpoints[%d] (%s)", i, r.ID)
		}

		switch aidispatch.Protocol(r.Protocol) {
		case aidispatch.ProtocolOpenAICompat, aidispatch.ProtocolWhisperServer:
			if strings.TrimSpace(r.URL) == "" {
				errs = append(errs, where+": url is required for protocol "+r.Protocol)
			}
		case aidispatch.ProtocolLocalProcess:
		default:
			errs = append(errs, fmt.Sprintf("%s: unknown protocol %q (want %s, %s or %s)", where, r.Protocol,
				aidispatch.ProtocolOpenAICompat, aidispatch.ProtocolWhisperServer, aidispatch.ProtocolLocalProcess))
		}
		if r.URL != "" {
			if u, err := url.Parse(r.URL); err == nil && u.User != nil {
				errs = append(errs, where+": url must not embed credentials; use auth_ref")
			}
			key := r.Protocol + " " + normalizeEndpointURL(r.URL)
			if other, dup := pairs[key]; dup {
				errs = append(errs, fmt.Sprintf("%s: protocol %s and url %q are already used by %q", where, r.Protocol, r.URL, other))
			} else {
				pairs[key] = r.ID
			}
		}

		if r.Concurrency < 0 {
			errs = append(errs, fmt.Sprintf("%s: concurrency must be >= 0 (0 means 1), got %d", where, r.Concurrency))
		}
		for _, id := range r.Capabilities {
			if msg := capabilityIDProblem(id); msg != "" {
				errs = append(errs, where+": capabilities: "+msg)
			}
		}
		for id := range r.CapabilityModels {
			if msg := capabilityIDProblem(id); msg != "" {
				errs = append(errs, where+": capability_models: "+msg)
			}
		}
		if r.AuthRef != "" && !slices.Contains(knownAIAuthRefs, r.AuthRef) {
			errs = append(errs, fmt.Sprintf("%s: auth_ref %q is not a known secret (want one of %v)", where, r.AuthRef, knownAIAuthRefs))
		}
	}
	if len(errs) > 0 {
		return errors.New("invalid ai_endpoints: " + strings.Join(errs, "; "))
	}
	return nil
}

func capabilityIDProblem(id string) string {
	if strings.Contains(id, "*") {
		return fmt.Sprintf("wildcard %q is not allowed; tick each capability explicitly", id)
	}
	if _, ok := aidispatch.ParseCapability(id); !ok {
		return fmt.Sprintf("unknown capability %q (see GET /api/v1/ai/capabilities)", id)
	}
	return ""
}

// MaskAIEndpoints returns a deep copy of rows with auth_ref masked. The copy
// is required for the same reason as maskMetadataSourceCredentials: MaskSecrets
// makes a shallow struct copy, so masking in place would rewrite the live
// config's rows.
func MaskAIEndpoints(rows []AIEndpoint) []AIEndpoint {
	if rows == nil {
		return nil
	}
	out := make([]AIEndpoint, len(rows))
	for i, r := range rows {
		r.Capabilities = slices.Clone(r.Capabilities)
		r.Labels = slices.Clone(r.Labels)
		r.Features = slices.Clone(r.Features)
		r.CapabilityModels = cloneStringMap(r.CapabilityModels)
		r.HostRoots = cloneStringMap(r.HostRoots)
		if r.AuthRef != "" {
			r.AuthRef = database.MaskSecret(r.AuthRef)
		}
		out[i] = r
	}
	return out
}

// unmaskAIEndpointAuthRefs turns an auth_ref that GET masked back into the
// secret name it stands for, so a GET-then-PUT round trip validates.
func unmaskAIEndpointAuthRefs(rows []AIEndpoint) {
	for i := range rows {
		ref := rows[i].AuthRef
		if ref == "" || slices.Contains(knownAIAuthRefs, ref) {
			continue
		}
		for _, name := range knownAIAuthRefs {
			if database.MaskSecret(name) == ref {
				rows[i].AuthRef = name
				break
			}
		}
	}
}

// aiEndpointsSeed carries what the ai_endpoints migration needs beyond the
// blob itself. The blob never contains the OpenAI key, and on prod
// WHISPER_ENDPOINTS lives in the environment, which LoadConfigFromDatabase
// applies AFTER the migration chain. A migration that read only the blob
// would see no whisper servers and hand transcribe.batch to local uv.
type aiEndpointsSeed struct {
	// Base is the config the blob is unmarshalled onto (Snapshot at load),
	// so defaults for keys absent from the blob match what load produces.
	Base *Config
	// HasOpenAIKey reports a stored or env-supplied OpenAI key.
	HasOpenAIKey bool
	// EnvWhisperEndpoints / EnvWhisperRemoteURL are non-nil when the env
	// sets them; they win over the blob, as in applyEnvAuthoritativeConfig.
	EnvWhisperEndpoints *[]WhisperEndpoint
	EnvWhisperRemoteURL *string
}

// migrateAIEndpointsBlob writes the ai_endpoints list derived from the legacy
// fields (PLAN section 5) into a blob that has none. Idempotent: a blob that
// already has ai_endpoints (even an empty list the operator saved) is
// returned unchanged. A null value, which a save from before this field
// existed cannot produce but a hand edit can, is treated as absent.
//
// The legacy fields are left in place and keep being written, and nothing
// reads ai_endpoints for dispatch in this PR.
func migrateAIEndpointsBlob(blob string, seed aiEndpointsSeed) (string, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &raw); err != nil || raw == nil {
		return blob, false
	}
	if v, ok := raw["ai_endpoints"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return blob, false
	}

	cfg := &Config{}
	if seed.Base != nil {
		cfg = seed.Base.Clone()
	}
	if err := json.Unmarshal([]byte(blob), cfg); err != nil {
		return blob, false
	}
	if seed.EnvWhisperEndpoints != nil {
		cfg.WhisperEndpoints = *seed.EnvWhisperEndpoints
	}
	if seed.EnvWhisperRemoteURL != nil {
		cfg.WhisperRemoteURL = *seed.EnvWhisperRemoteURL
	}
	hasKey := seed.HasOpenAIKey || cfg.OpenAIAPIKey != ""
	if hasKey && cfg.OpenAIAPIKey == "" {
		// The effective-mode helpers look at the key field itself.
		cfg.OpenAIAPIKey = "present"
	}

	enc, err := json.Marshal(buildMigratedAIEndpoints(cfg, hasKey))
	if err != nil {
		return blob, false
	}
	raw["ai_endpoints"] = enc
	out, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(out), true
}

// tick adds c to row's capabilities if the migration is allowed to tick it.
// The migration may tick ONLY capabilities in aidispatch.MigrationBaseline; a
// capability registered later starts with no servers until an operator ticks
// it. This helper is the single enforcement point for that rule.
func tick(row *AIEndpoint, c aidispatch.Capability) {
	if !aidispatch.InMigrationBaseline(c) {
		return
	}
	if !slices.Contains(row.Capabilities, c.ID()) {
		row.Capabilities = append(row.Capabilities, c.ID())
	}
}

// buildMigratedAIEndpoints derives the rows from resolved legacy config so
// that, for every call site in PLAN section 1, selection over the rows picks
// the endpoint the site uses today. Known divergences are listed in the PR 2
// description and in ai_endpoints_migration_test.go. Never returns nil, so
// the migrated blob holds [] rather than null and the migration cannot re-run.
func buildMigratedAIEndpoints(cfg *Config, hasOpenAIKey bool) []AIEndpoint {
	rows := []AIEndpoint{}
	llmMode := cfg.EffectiveLLMMode()
	embedMode := cfg.EffectiveEmbeddingMode()
	llmLocal := llmMode == AIBackendModeLocal || llmMode == AIBackendModeOpenAIFallbackLocal
	llmCloud := llmMode == AIBackendModeOpenAI || llmMode == AIBackendModeOpenAIFallbackLocal
	embedLocal := embedMode == AIBackendModeLocal || embedMode == AIBackendModeOpenAIFallbackLocal
	embedCloud := embedMode == AIBackendModeOpenAI || embedMode == AIBackendModeOpenAIFallbackLocal

	// One local OpenAI-compatible row: chat and embeddings share the base URL
	// today (LocalBaseURL, falling back to Embedding.BaseURL).
	localURL := cfg.AIBackend.LocalBaseURL
	if localURL == "" {
		localURL = cfg.Embedding.BaseURL
	}
	if localURL != "" {
		embedModel := cfg.AIBackend.LocalEmbeddingModel
		if embedModel == "" {
			embedModel = cfg.Embedding.Model
		}
		row := AIEndpoint{
			ID:         AIEndpointIDLocalLLM,
			Label:      "Local LLM (migrated from ai_backend.local_base_url)",
			Protocol:   string(aidispatch.ProtocolOpenAICompat),
			URL:        localURL,
			ChatModel:  cfg.AIBackend.LocalLLMModel,
			EmbedModel: embedModel,
			Priority:   10,
			// Not enforced until the call sites move onto the dispatcher.
			Concurrency:  4,
			Enabled:      true,
			Capabilities: []string{},
		}
		if llmLocal {
			// The chat sites that honour llm_mode today. Batch-API work
			// (dedup_review, author_dedup_batch) is never ticked here: it
			// requires the batch_api feature, which Ollama does not have.
			tick(&row, aidispatch.LLMFilenameParse)
			tick(&row, aidispatch.LLMAuthorReview)
			tick(&row, aidispatch.LLMAuthorDiscovery)
			tick(&row, aidispatch.LLMMetadataRerank)
		}
		if embedLocal {
			tick(&row, aidispatch.EmbedText)
		}
		rows = append(rows, row)
	}

	if hasOpenAIKey {
		base := cfg.OpenAIBaseURL
		if base == "" {
			base = DefaultOpenAIBaseURL
		}
		prio := 20
		if llmMode == AIBackendModeOpenAIFallbackLocal {
			prio = 5 // cloud first, local second
		}
		models := map[string]string{}
		for id, m := range map[aidispatch.Capability]string{
			aidispatch.LLMFilenameParse:  cfg.FilenameParseModel,
			aidispatch.LLMAudiobookParse: cfg.FilenameParseModel,
			aidispatch.LLMCoverArtVision: cfg.CoverArtModel,
			aidispatch.LLMMetadataRerank: cfg.MetadataReviewModel,
			aidispatch.LLMDedupReview:    cfg.Dedup.ReviewModel,
		} {
			if m != "" {
				models[id.ID()] = m
			}
		}
		if len(models) == 0 {
			models = nil
		}
		row := AIEndpoint{
			ID:               AIEndpointIDOpenAI,
			Label:            "OpenAI (migrated from openai_api_key)",
			Protocol:         string(aidispatch.ProtocolOpenAICompat),
			URL:              base,
			ChatModel:        DefaultOpenAIChatModel,
			EmbedModel:       cfg.Embedding.Model,
			CapabilityModels: models,
			Priority:         prio,
			Concurrency:      4,
			Enabled:          true,
			Capabilities:     []string{},
			Features:         []string{aidispatch.FeatureBatchAPI, aidispatch.FeatureVision},
			AuthRef:          "openai_api_key",
		}
		// Owner decision 1: pre-tick the sites that call OpenAI directly
		// whenever a key exists, regardless of llm_mode.
		tick(&row, aidispatch.LLMFilenameParse)
		tick(&row, aidispatch.LLMAudiobookParse)
		tick(&row, aidispatch.LLMCoverArtVision) // owner decision 5: cloud only
		tick(&row, aidispatch.LLMAuthorReview)
		tick(&row, aidispatch.LLMAuthorDiscovery)
		tick(&row, aidispatch.LLMAuthorDedupBatch)
		tick(&row, aidispatch.LLMDiagnostics)
		// The intro-clip fallback is whisper-1 over OpenAI today. The
		// dispatcher refuses it on openai_compat until PR 3 adds the protocol
		// mapping; the tick records the intent.
		tick(&row, aidispatch.TranscribeIntroClip)
		if llmCloud {
			tick(&row, aidispatch.LLMMetadataRerank)
			tick(&row, aidispatch.LLMDedupReview)
		}
		if embedCloud {
			tick(&row, aidispatch.EmbedText)
		}
		if embedMode == AIBackendModeOpenAI {
			tick(&row, aidispatch.EmbedTextBatch)
		}
		rows = append(rows, row)
	}

	// Whisper servers, with poolEndpoints' precedence: a non-empty
	// whisper_endpoints list, else whisper_remote_url as one server.
	whisper := make([]AIEndpoint, 0, len(cfg.WhisperEndpoints))
	seen := map[string]bool{}
	for _, w := range cfg.WhisperEndpoints {
		if w.URL == "" || seen[w.URL] {
			continue
		}
		seen[w.URL] = true
		row := AIEndpoint{
			Label:        w.Label,
			Protocol:     string(aidispatch.ProtocolWhisperServer),
			URL:          w.URL,
			Priority:     w.Priority,
			Concurrency:  max(w.Concurrency, 1),
			Enabled:      true,
			Capabilities: []string{},
			Labels:       slices.Clone(w.Capabilities),
			RequireGPU:   w.RequireGPU,
		}
		tick(&row, aidispatch.TranscribeBatch)
		whisper = append(whisper, row)
	}
	if len(whisper) == 0 && cfg.WhisperRemoteURL != "" {
		row := AIEndpoint{
			Label:        "Whisper server (migrated from whisper_remote_url)",
			Protocol:     string(aidispatch.ProtocolWhisperServer),
			URL:          cfg.WhisperRemoteURL,
			Concurrency:  1,
			Enabled:      true,
			Capabilities: []string{},
		}
		tick(&row, aidispatch.TranscribeBatch)
		whisper = append(whisper, row)
	}
	assignWhisperIDs(whisper)
	rows = append(rows, whisper...)

	// The in-process uv runner: today's intro-clip path when uv is on PATH,
	// and the batch path only when no whisper server is configured.
	uv := AIEndpoint{
		ID:           AIEndpointIDLocalUV,
		Label:        "Local uv/openai-whisper",
		Protocol:     string(aidispatch.ProtocolLocalProcess),
		URL:          LocalUVEndpointURL,
		Priority:     100,
		Concurrency:  1,
		Enabled:      true,
		Capabilities: []string{},
	}
	tick(&uv, aidispatch.TranscribeIntroClip)
	if len(whisper) == 0 {
		tick(&uv, aidispatch.TranscribeBatch)
	}
	rows = append(rows, uv)
	return rows
}

// assignWhisperIDs names each row whisper-<port>, or whisper-<n> (1-based
// position) when the URL has no port or two rows share one.
func assignWhisperIDs(rows []AIEndpoint) {
	ports := map[string]int{}
	portOf := make([]string, len(rows))
	for i, r := range rows {
		if u, err := url.Parse(r.URL); err == nil {
			portOf[i] = u.Port()
		}
		if portOf[i] != "" {
			ports[portOf[i]]++
		}
	}
	for i := range rows {
		if p := portOf[i]; p != "" && ports[p] == 1 {
			rows[i].ID = "whisper-" + p
		} else {
			rows[i].ID = "whisper-" + strconv.Itoa(i+1)
		}
	}
}
