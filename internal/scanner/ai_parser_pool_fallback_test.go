// file: internal/scanner/ai_parser_pool_fallback_test.go
// version: 1.0.0
// guid: e82f0ef3-fe59-4184-8d82-d6c5cfdd5b9e
// last-edited: 2026-09-19

package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Pins the openai-fallback-local semantics of the ROUTED parser against the
// phase's abort rule (isPermanentAIFailure). This deliberately differs from
// the legacy parserChain, which returned a permanent cloud error as-is and
// aborted the phase: routed, a quota/401/403 on the cloud row fails over to
// the local row, and the phase aborts only when every endpoint failed and a
// permanent error is among them.

// statusServer answers every chat call with status/body, or, when status is
// 200, with one result per numbered filename.
type statusServer struct {
	calls  atomic.Int64
	status int
	body   string
	url    string
}

func newStatusServer(t *testing.T, status int, body string) *statusServer {
	t.Helper()
	s := &statusServer{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if s.status != http.StatusOK {
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(s.body))
			return
		}
		content, _ := json.Marshal(`{"results":[{"title":"t","author":"a","confidence":"high"}]}`)
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":%s}}]}`, content)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL + "/v1"
	return s
}

const (
	quotaBody    = `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`
	badModelBody = `{"error":{"message":"The model does not exist","type":"invalid_request_error","code":"model_not_found"}}`
)

func fallbackPool(health *aidispatch.Health, eps ...aidispatch.Endpoint) *ai.PoolSource {
	return &ai.PoolSource{
		Active:    func() bool { return true },
		Endpoints: func() []aidispatch.Endpoint { return eps },
		Secret:    func(string) string { return "sk-test" },
		Options: []aidispatch.Option{aidispatch.WithSlots(aidispatch.NewSlots()), aidispatch.WithHealth(health),
			aidispatch.WithAttribution(aidispatch.NewAttribution())},
	}
}

func poolEP(id, url string, prio int, cloud bool) aidispatch.Endpoint {
	e := aidispatch.Endpoint{ID: id, Protocol: aidispatch.ProtocolOpenAICompat, URL: url, ChatModel: "m",
		Priority: prio, Enabled: true, Capabilities: []string{aidispatch.LLMFilenameParse.ID()}}
	if cloud {
		e.AuthRef = "openai_api_key"
	}
	return e
}

// (a) Quota exhausted on the cloud row: the local row answers and the phase
// continues (no error, so nothing for isPermanentAIFailure to abort on).
func TestRoutedFallback_CloudQuotaFailsOverToLocal(t *testing.T) {
	cloud := newStatusServer(t, http.StatusTooManyRequests, quotaBody)
	local := newStatusServer(t, http.StatusOK, "")
	health := aidispatch.NewHealth()
	p := ai.NewRoutedFilenameParser(fallbackPool(health, poolEP("openai", cloud.url, 5, true),
		poolEP("local-llm", local.url, 10, false)), config.AIBackendModeOpenAIFallbackLocal)

	res, err := p.ParseBatch(context.Background(), []string{"A - B"})
	if err != nil {
		t.Fatalf("quota on cloud should fail over to local; got %v", err)
	}
	if len(res) != 1 || local.calls.Load() != 1 || cloud.calls.Load() != 1 {
		t.Fatalf("res %d, local calls %d, cloud calls %d; want 1/1/1", len(res), local.calls.Load(), cloud.calls.Load())
	}
	if !health.InCooldown("openai") {
		t.Fatal("quota-exhausted cloud row was not benched")
	}
}

// (b) Every endpoint fails permanently: the ExhaustedError still carries the
// *ai.PermanentError, so the phase aborts exactly as it does today.
func TestRoutedFallback_AllPermanentAbortsPhase(t *testing.T) {
	cloud := newStatusServer(t, http.StatusTooManyRequests, quotaBody)
	local := newStatusServer(t, http.StatusUnauthorized, `{"error":{"message":"bad key","type":"invalid_request_error","code":"invalid_api_key"}}`)
	p := ai.NewRoutedFilenameParser(fallbackPool(aidispatch.NewHealth(), poolEP("openai", cloud.url, 5, true),
		poolEP("local-llm", local.url, 10, false)), config.AIBackendModeOpenAIFallbackLocal)

	_, err := p.ParseBatch(context.Background(), []string{"A - B"})
	if _, ok := err.(*aidispatch.ExhaustedError); !ok {
		t.Fatalf("err = %T %v, want *aidispatch.ExhaustedError", err, err)
	}
	if !isPermanentAIFailure(err) {
		t.Fatalf("all-endpoints-permanent error is not permanent to the phase: %v", err)
	}
}

// (c) A bad model (404) is a request error, not an endpoint failure: one
// request, no failover to the peer, no benching, and the phase aborts once.
func TestRoutedFallback_BadModelSingleAbortNoBench(t *testing.T) {
	cloud := newStatusServer(t, http.StatusNotFound, badModelBody)
	local := newStatusServer(t, http.StatusOK, "")
	health := aidispatch.NewHealth()
	p := ai.NewRoutedFilenameParser(fallbackPool(health, poolEP("openai", cloud.url, 5, true),
		poolEP("local-llm", local.url, 10, false)), config.AIBackendModeOpenAIFallbackLocal)

	_, err := p.ParseBatch(context.Background(), []string{"A - B"})
	if err == nil || !isPermanentAIFailure(err) {
		t.Fatalf("err = %v, want a permanent failure", err)
	}
	if cloud.calls.Load() != 1 || local.calls.Load() != 0 {
		t.Fatalf("cloud calls %d, local calls %d; want 1 and 0", cloud.calls.Load(), local.calls.Load())
	}
	if health.InCooldown("openai") {
		t.Fatal("a bad-model request benched the endpoint")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the 404 surfaced", err)
	}
}
