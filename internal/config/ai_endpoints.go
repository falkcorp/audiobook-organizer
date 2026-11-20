// file: internal/config/ai_endpoints.go
// version: 1.2.2
// guid: 3f0b6c2e-8a41-4d7e-9b15-6e2c7a9d4f10
// last-edited: 2026-11-09

package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
)

// AIEndpoint is one AI server row in the capability-routing settings
// (PLAN section 3). It is stored in the config blob as the ai_endpoints list.
//
// It is a plain struct with no custom UnmarshalJSON on purpose: the
// unknown-key walker (unknown_keys.go) skips any json.Unmarshaler, so a
// custom decoder would silently stop PUT /config from rejecting a typo such
// as ai_endpoints[0].capabilites.
//
// Routing on these rows is gated by Config.AIEndpointsRouting (default off).
// With it off, every AI call site resolves its backend from the legacy
// fields, which the migration keeps writing; with it on, only the call sites
// listed on that field route through the pool.
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

// DispatchEndpoint converts the stored row into the dispatcher's input type.
// Used by the status API and, when ai_endpoints_routing is on, by the routed
// call sites in internal/ai.
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
	maps.Copy(out, m)
	return out
}

// knownAIAuthRefs are the secret names an endpoint row may reference.
var knownAIAuthRefs = []string{"openai_api_key"}

// normalizeEndpointURL is the form used for the (protocol, URL) uniqueness
// check: trimmed, case-folded, without a trailing slash.
func normalizeEndpointURL(u string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(u)), "/")
}

// ValidateAIEndpoints checks a submitted ai_endpoints list. It runs only on a
// PUT that carries the key, so rows stored by a newer binary (with capability
// IDs this one does not know) never block an unrelated settings save. The
// config Engine calls it on the list a write carries; HEAD called it from the
// update service this replaced.
func ValidateAIEndpoints(rows []AIEndpoint) error {
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
