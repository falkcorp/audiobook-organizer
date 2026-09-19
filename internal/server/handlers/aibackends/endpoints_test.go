// file: internal/server/handlers/aibackends/endpoints_test.go
// version: 1.1.0
// guid: 5a7c9e13-2b4d-4f86-a0c1-7d3e5b9f2a68
// last-edited: 2026-09-19

package aibackendshandler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	aibackendshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/aibackends"
)

func serve(t *testing.T, path string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET(path, h)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return w
}

func TestCapabilities_ServesRegistry(t *testing.T) {
	h := aibackendshandler.New(nil, nil)
	w := serve(t, "/ai/capabilities", h.Capabilities)
	var body struct {
		Data struct {
			Capabilities []aibackendshandler.CapabilityInfo `json:"capabilities"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data.Capabilities, len(aidispatch.Registry()))
	for _, c := range body.Data.Capabilities {
		require.NotEmpty(t, c.Description, c.ID)
		require.NotEmpty(t, c.DataSent, c.ID)
		require.NotNil(t, c.RequiredFeatures, c.ID)
	}
	require.Equal(t, aidispatch.LLMFilenameParse.ID(), body.Data.Capabilities[0].ID)
}

func TestEndpointsStatus_RowsCoverageAndMasking(t *testing.T) {
	orig := config.Snapshot().AIEndpoints
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.AIEndpoints = orig }) })
	config.Mutate(func(c *config.Config) {
		c.AIEndpoints = []config.AIEndpoint{
			{ID: "local-llm", Protocol: "openai_compat", URL: "http://127.0.0.1:11434/v1", ChatModel: "m",
				Priority: 10, Enabled: true, Capabilities: []string{"llm.filename_parse", "llm.from_the_future"}},
			{ID: "cloud", Protocol: "openai_compat", URL: "https://api.openai.com/v1", ChatModel: "gpt-5-mini",
				Priority: 20, Enabled: true, AuthRef: "openai_api_key", Capabilities: []string{"llm.filename_parse"}},
		}
	})

	h := aibackendshandler.New(nil, nil)
	w := serve(t, "/ai/endpoints/status", h.EndpointsStatus)
	var body struct {
		Data aibackendshandler.EndpointsStatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	d := body.Data
	require.False(t, d.RoutingActive)
	require.Contains(t, d.Note, "not yet used for routing")
	require.Len(t, d.Endpoints, 2)
	require.Equal(t, "not_probed", d.Endpoints[0].Probe.Status)
	require.Equal(t, []string{"llm.from_the_future"}, d.Endpoints[0].UnknownCapabilities)
	require.Equal(t, 1, d.Endpoints[0].EffectiveConcurrency)
	require.Equal(t, database.MaskSecret("openai_api_key"), d.Endpoints[1].Endpoint.AuthRef)
	require.Equal(t, "openai_api_key", config.Snapshot().AIEndpoints[1].AuthRef, "handler masked the live config")

	var fp *aibackendshandler.CapabilityCoverage
	for i := range d.Coverage {
		if d.Coverage[i].Capability == aidispatch.LLMFilenameParse.ID() {
			fp = &d.Coverage[i]
		}
	}
	require.NotNil(t, fp)
	require.Equal(t, []string{"local-llm", "cloud"}, fp.Candidates)
	require.Len(t, d.Coverage, len(aidispatch.Registry()))
}

// routing_active reflects the ai_endpoints_routing switch, and each endpoint
// carries the process-wide attribution counters, so an operator can prove
// work landed on a given endpoint.
func TestEndpointsStatus_RoutingSwitchAndAttribution(t *testing.T) {
	prev := config.Snapshot()
	t.Cleanup(func() {
		config.Mutate(func(c *config.Config) {
			c.AIEndpoints = prev.AIEndpoints
			c.AIEndpointsRouting = prev.AIEndpointsRouting
		})
	})
	const id = "attr-test-endpoint"
	config.Mutate(func(c *config.Config) {
		c.AIEndpoints = []config.AIEndpoint{{ID: id, Protocol: "openai_compat", URL: "http://192.0.2.9:11434/v1",
			ChatModel: "m", Priority: 1, Enabled: true, Capabilities: []string{"llm.filename_parse"}}}
		c.AIEndpointsRouting = true
	})
	aidispatch.DefaultAttribution().Record(id, "llm.filename_parse", aidispatch.ClassOK, time.Now())
	aidispatch.DefaultAttribution().Record(id, "llm.filename_parse", aidispatch.ClassFailover, time.Now())

	h := aibackendshandler.New(nil, nil)
	w := serve(t, "/ai/endpoints/status", h.EndpointsStatus)
	var body struct {
		Data aibackendshandler.EndpointsStatusResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	d := body.Data
	require.True(t, d.RoutingActive)
	require.NotContains(t, d.Note, "not yet used for routing")
	require.Len(t, d.Endpoints, 1)
	a := d.Endpoints[0].Attribution
	require.EqualValues(t, 2, a.Requests)
	require.EqualValues(t, 1, a.Failures)
	require.EqualValues(t, 2, a.ByCapability["llm.filename_parse"])
	require.False(t, a.LastUsed.IsZero())

	config.Mutate(func(c *config.Config) { c.AIEndpointsRouting = false })
	w = serve(t, "/ai/endpoints/status", h.EndpointsStatus)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.False(t, body.Data.RoutingActive)
}
