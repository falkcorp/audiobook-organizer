// file: core/integrations/ai/openai/conformance_test.go
// version: 1.0.0
// guid: 6a3f8d21-9b47-4c05-b2e8-1d7c5a0f3e96
// last-edited: 2026-11-12

package openai

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations/ai/aitest"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// fixtureRemote is aitest.Replay over a dialect directory: each route answers
// with the file of that name for that server, and the next request fails as
// the scripted outcome says.
type fixtureRemote struct {
	t    testing.TB
	kit  *coretest.ProviderHTTPKit
	dir  string
	mu   sync.Mutex
	next aitest.Outcome
}

// routes maps "METHOD path" to the fixture file that answers it; a route
// with {id} matches any value.
var routes = map[string]string{
	"GET /v1/models":                "models.json",
	"POST /v1/chat/completions":     "chat.json",
	"POST /v1/embeddings":           "embeddings.json",
	"POST /v1/audio/transcriptions": "transcription.json",
	"POST /v1/files":                "file_upload.json",
	"POST /v1/batches":              "batch_create.json",
	"GET /v1/batches":               "batch_list.json",
	"GET /v1/batches/{id}":          "batch_done.json",
	"GET /v1/files/{id}/content":    "batch_results.jsonl",
}

func (f *fixtureRemote) Answer(_ testing.TB, o aitest.Outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next = o
}

func (f *fixtureRemote) Requests() int { return int(f.kit.Hits()) }

func (f *fixtureRemote) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	o := f.next
	f.next = aitest.OutcomeOK
	f.mu.Unlock()
	switch o {
	case aitest.OutcomeRateLimited:
		w.Header().Set("Retry-After", "1")
		writeJSON(f.t, w, 429, apiError("requests", "rate_limit_exceeded", "Rate limit reached"))
		return
	case aitest.OutcomeQuota:
		writeJSON(f.t, w, 429, apiError("insufficient_quota", "insufficient_quota", "You exceeded your current quota"))
		return
	case aitest.OutcomeAuthRejected:
		writeJSON(f.t, w, 401, apiError("invalid_request_error", "invalid_api_key", "Incorrect API key provided"))
		return
	case aitest.OutcomeServerError:
		writeJSON(f.t, w, 500, apiError("server_error", "", "The server had an error"))
		return
	case aitest.OutcomeGarbage:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("<html>not the API</html>"))
		return
	}
	name := f.fixtureFor(r)
	body, err := os.ReadFile(filepath.Join(f.dir, name))
	if name == "" || err != nil {
		writeJSON(f.t, w, 404, apiError("invalid_request_error", "not_found", "no such route on this server"))
		return
	}
	if strings.HasSuffix(name, ".jsonl") {
		w.Header().Set("Content-Type", "application/jsonl")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	_, _ = w.Write(body)
}

// fixtureFor finds the fixture of r's route.
func (f *fixtureRemote) fixtureFor(r *http.Request) string {
	mux := http.NewServeMux()
	var hit string
	for pattern, file := range routes {
		mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) { hit = file })
	}
	mux.ServeHTTP(discard{}, r)
	return hit
}

type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(p []byte) (int, error) { return len(p), nil }
func (discard) WriteHeader(int)             {}

// TestConformance runs the adapter conformance over each server dialect's
// fixtures. What a dialect has no model for is skipped inside the suite, as a
// server without it would be.
func TestConformance(t *testing.T) {
	for _, d := range []struct {
		name   string
		models aitest.Models
		batch  bool
	}{
		{"openai", aitest.Models{Text: "gpt-batmobile", Embed: "text-embedding-bat", Transcribe: "whisper-1"}, true},
		{"ollama", aitest.Models{Text: "llama3.2:latest", Embed: "nomic-embed-text:latest"}, false},
		{"llamacpp", aitest.Models{Text: "gemma-3-4b-it-Q4_K_M.gguf", Embed: "gemma-3-4b-it-Q4_K_M.gguf"}, false},
		{"whispercpp", aitest.Models{Transcribe: "whisper-small"}, false},
	} {
		t.Run(d.name, func(t *testing.T) {
			t.Parallel()
			aitest.Conformance(t, func(t *testing.T) aitest.Subject {
				r := newRig(t)
				rem := &fixtureRemote{t: t, kit: r.kit, dir: filepath.Join("testdata", "dialects", d.name)}
				r.kit.Handle(rem.serve)
				return aitest.Subject{Backend: r.inst, Models: d.models, Batch: d.batch, Replay: rem}
			})
		})
	}
}

// TestConformance_Live runs the suite against a live server, with the failure
// scenarios skipped (a live server cannot be told to fail). It runs only when
// MEDIACORE_LIVE_AI=1, and reads the server from
//
//	MEDIACORE_LIVE_AI_URL               the API base, e.g. http://localhost:11434/v1
//	MEDIACORE_LIVE_AI_KEY               the API key, if the server wants one
//	MEDIACORE_LIVE_AI_TEXT_MODEL        a chat model, or empty to skip text
//	MEDIACORE_LIVE_AI_EMBED_MODEL       an embedding model, or empty to skip
//	MEDIACORE_LIVE_AI_TRANSCRIBE_MODEL  a transcription model, or empty to skip
//	MEDIACORE_LIVE_AI_BATCH=1           the server implements the batch API
//
// The server is on the operator's network, so its host is the gate's lan_allow
// for the run, as an instance with Local set would reach it. This is the run
// 05-ai section 11 item 1 relies on to measure each server version; it was NOT
// executed when this suite was written, because no live server was available.
func TestConformance_Live(t *testing.T) {
	if os.Getenv("MEDIACORE_LIVE_AI") != "1" {
		t.Skip("set MEDIACORE_LIVE_AI=1 and MEDIACORE_LIVE_AI_URL to run against a live server")
	}
	raw := os.Getenv("MEDIACORE_LIVE_AI_URL")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		t.Fatalf("MEDIACORE_LIVE_AI_URL = %q is not an absolute URL", raw)
	}
	aitest.Conformance(t, func(t *testing.T) aitest.Subject {
		kit := coretest.ProviderHTTP(t, func(o *providerhttp.Options) { o.LANAllow = func() []string { return []string{u.Host} } })
		r := newRigAt(t, kit, u.Scheme+"://"+u.Host, func(s map[string]any) { s["url"] = raw })
		if k := os.Getenv("MEDIACORE_LIVE_AI_KEY"); k != "" {
			r.setKey(k, nil)
		}
		return aitest.Subject{
			Backend: r.inst, Batch: os.Getenv("MEDIACORE_LIVE_AI_BATCH") == "1",
			Models: aitest.Models{
				Text: os.Getenv("MEDIACORE_LIVE_AI_TEXT_MODEL"), Embed: os.Getenv("MEDIACORE_LIVE_AI_EMBED_MODEL"),
				Transcribe: os.Getenv("MEDIACORE_LIVE_AI_TRANSCRIBE_MODEL"),
			},
		}
	})
}
