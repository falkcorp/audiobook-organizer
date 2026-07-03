// file: internal/server/handlers/aibackends/aibackends.go
// version: 1.0.0
// guid: 7c3d9e21-4a5b-4f6c-9d8e-1a2b3c4d5e6f
// last-edited: 2026-07-03

// Package aibackendshandler provides HTTP handlers for the AI backend-mode
// toggle (TASK-10's AIBackendConfig): a status probe that reports the
// effective embedding/LLM mode plus whether the configured local models are
// pulled into Ollama, and a pull-model endpoint that shells out to the
// managed Ollama binary (reusing internal/tools.ToolRegistry, the same
// lifecycle used by the "Managed External-Tool Lifecycle" /api/v1/tools
// endpoints) to pull a model on demand.
package aibackendshandler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/tools"
)

// pullTimeout bounds a single `ollama pull` invocation. Model pulls can be
// several GB; 30 minutes is generous enough for a slow connection while still
// giving up rather than hanging the handler goroutine forever.
const pullTimeout = 30 * time.Minute

// statusProbeTimeout bounds the GET /api/tags probe used to determine
// reachability and list pulled models. Kept short so Settings page loads
// don't stall waiting on a down local endpoint.
const statusProbeTimeout = 3 * time.Second

// Handler provides HTTP handlers for /api/v1/ai/backends.
type Handler struct {
	registry *tools.ToolRegistry
	daemon   *tools.OllamaDaemon
}

// New constructs a Handler. registry may be nil in tests that only exercise
// Status (pull-model requires a non-nil registry to resolve the ollama
// binary). daemon may also be nil (e.g. Ollama tool mode disabled); PullModel
// then falls back to invoking the CLI directly without ensuring a managed
// daemon is running.
func New(registry *tools.ToolRegistry, daemon *tools.OllamaDaemon) *Handler {
	return &Handler{registry: registry, daemon: daemon}
}

// ModelStatus reports whether a single named model has been pulled into the
// probed local endpoint.
type ModelStatus struct {
	Name   string `json:"name"`
	Pulled bool   `json:"pulled"`
}

// StatusResponse is the response body for GET /api/v1/ai/backends/status.
type StatusResponse struct {
	EmbeddingMode  string       `json:"embedding_mode"`
	LLMMode        string       `json:"llm_mode"`
	LocalBaseURL   string       `json:"local_base_url"`
	LocalReachable bool         `json:"local_reachable"`
	EmbeddingModel *ModelStatus `json:"embedding_model,omitempty"`
	LLMModel       *ModelStatus `json:"llm_model,omitempty"`
	FallbackReason string       `json:"fallback_reason,omitempty"`
}

// usesLocal reports whether mode requires a reachable local endpoint.
func usesLocal(mode string) bool {
	return mode == config.AIBackendModeLocal || mode == config.AIBackendModeOpenAIFallbackLocal
}

// Status probes the configured local backend (if either pipeline is in a
// local-involving mode) and reports the effective mode plus per-model pulled
// state, so the Settings UI can decide whether to prompt for a model pull.
//
//	GET /api/v1/ai/backends/status
func (h *Handler) Status(c *gin.Context) {
	cfg := &config.AppConfig
	resp := StatusResponse{
		EmbeddingMode: cfg.EffectiveEmbeddingMode(),
		LLMMode:       cfg.EffectiveLLMMode(),
		LocalBaseURL:  cfg.AIBackend.LocalBaseURL,
	}

	needsLocal := usesLocal(resp.EmbeddingMode) || usesLocal(resp.LLMMode)
	if !needsLocal || cfg.AIBackend.LocalBaseURL == "" {
		httputil.RespondWithOK(c, resp)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), statusProbeTimeout)
	defer cancel()

	pulled, err := listPulledModels(ctx, cfg.AIBackend.LocalBaseURL)
	resp.LocalReachable = err == nil
	if err != nil {
		resp.FallbackReason = "local endpoint unreachable: " + err.Error()
	}

	if usesLocal(resp.EmbeddingMode) && cfg.AIBackend.LocalEmbeddingModel != "" {
		resp.EmbeddingModel = &ModelStatus{
			Name:   cfg.AIBackend.LocalEmbeddingModel,
			Pulled: modelPresent(pulled, cfg.AIBackend.LocalEmbeddingModel),
		}
	}
	if usesLocal(resp.LLMMode) && cfg.AIBackend.LocalLLMModel != "" {
		resp.LLMModel = &ModelStatus{
			Name:   cfg.AIBackend.LocalLLMModel,
			Pulled: modelPresent(pulled, cfg.AIBackend.LocalLLMModel),
		}
	}

	httputil.RespondWithOK(c, resp)
}

// pullModelRequest is the request body for POST /api/v1/ai/backends/pull-model.
type pullModelRequest struct {
	Model string `json:"model" binding:"required"`
}

// PullModel shells out to the managed `ollama` binary (resolved through
// ToolRegistry, the same lifecycle backing /api/v1/tools/:name/install) to
// pull req.Model. It runs synchronously and returns once the pull completes
// or pullTimeout elapses — there is no op-registry/streaming progress channel
// for this endpoint; the frontend re-polls Status after this call returns,
// mirroring the existing ToolsPanel install-then-refetch pattern.
//
//	POST /api/v1/ai/backends/pull-model
func (h *Handler) PullModel(c *gin.Context) {
	var req pullModelRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Model == "" {
		httputil.RespondWithBadRequest(c, "model is required")
		return
	}

	if h.registry == nil {
		httputil.RespondWithServiceUnavailable(c, "tool registry not configured")
		return
	}

	binPath, err := h.registry.Resolve("ollama")
	if err != nil {
		httputil.RespondWithServiceUnavailable(c, "ollama is not available: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), pullTimeout)
	defer cancel()

	// The ollama CLI's "pull" subcommand talks to a running "ollama serve"
	// daemon over the local API, so ensure the managed daemon is up first
	// (same pre-flight as drainEmbedQueue). Stop it back down once the pull
	// finishes so an on-demand pull doesn't leave the daemon resident.
	if h.daemon != nil {
		if err := h.daemon.EnsureRunningOrAdopt(ctx); err != nil {
			httputil.RespondWithServiceUnavailable(c, "failed to start ollama: "+err.Error())
			return
		}
		defer h.daemon.StopWhenIdle(ctx) //nolint:errcheck
	}

	cmd := exec.CommandContext(ctx, binPath, "pull", req.Model)
	out, err := cmd.CombinedOutput()
	if err != nil {
		httputil.RespondWithInternalError(c, "ollama pull failed: "+err.Error()+": "+string(out))
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"model":  req.Model,
		"pulled": true,
	})
}

// ollamaTagsResponse mirrors the subset of Ollama's native GET /api/tags
// response body this package needs.
type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// listPulledModels fetches GET {baseURL}/api/tags (stripping a trailing "/v1"
// since Ollama's native tags endpoint is unversioned, matching
// ai.ProbeOllamaAvailable's URL handling) and returns the pulled model names.
func listPulledModels(ctx context.Context, baseURL string) ([]string, error) {
	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	url := strings.TrimRight(root, "/") + "/api/tags"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// modelPresent reports whether want matches any pulled model name. Ollama tag
// names commonly include a ":tag" suffix (e.g. "bge-m3:latest"); a bare model
// name configured without a tag should still match the tagged entry, so this
// compares both the exact name and the name with its tag suffix stripped.
func modelPresent(pulled []string, want string) bool {
	for _, name := range pulled {
		if name == want {
			return true
		}
		if base, _, ok := strings.Cut(name, ":"); ok && base == want {
			return true
		}
	}
	return false
}
