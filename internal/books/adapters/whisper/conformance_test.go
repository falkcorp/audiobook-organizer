// file: internal/books/adapters/whisper/conformance_test.go
// version: 0.1.0
// guid: 0b6b06b0-6a5f-4a57-9d2c-3f1c2f6c0d3e
// last-edited: 2026-11-21

package whisper

import (
	"net/http"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/integrations/ai/aitest"
)

// replay scripts the far side of one rig for the conformance suite: the next
// request gets the scripted outcome, every later one the worker's normal answer.
type replay struct {
	t    testing.TB
	r    *rig
	mu   sync.Mutex
	next aitest.Outcome
}

func (p *replay) Answer(_ testing.TB, o aitest.Outcome) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next = o
}

func (p *replay) Requests() int { return len(p.r.requests()) }

// take returns the outcome for this request and resets it to OK.
func (p *replay) take() aitest.Outcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	o := p.next
	p.next = aitest.OutcomeOK
	return o
}

// scripted writes the failure o as this worker says it, and reports whether it
// wrote one. A whisper worker is FastAPI: its errors are {"detail": ...}, and it
// has no quota, so the quota outcome arrives the way a proxy in front of it
// would send it.
func (p *replay) scripted(w http.ResponseWriter, o aitest.Outcome) bool {
	switch o {
	case aitest.OutcomeRateLimited:
		w.Header().Set("Retry-After", "1")
		writeJSON(p.t, w, 429, `{"detail":"slow down"}`)
	case aitest.OutcomeQuota:
		writeJSON(p.t, w, 429, `{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`)
	case aitest.OutcomeAuthRejected:
		writeJSON(p.t, w, 401, `{"detail":"Not authenticated"}`)
	case aitest.OutcomeServerError:
		writeJSON(p.t, w, 500, `{"detail":"Internal Server Error"}`)
	case aitest.OutcomeGarbage:
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not the worker</html>"))
	default:
		return false
	}
	return true
}

// TestConformance runs the shared ai adapter suite over a whisper_server
// instance on a real gate and a loopback worker: caps and health, a transcribe
// of half a second of silence, every failure class mapped and attempted once, a
// canceled caller sending nothing, and Close twice.
func TestConformance(t *testing.T) {
	t.Parallel()
	aitest.Conformance(t, func(t *testing.T) aitest.Subject {
		r := newRig(t)
		p := &replay{t: t, r: r}
		r.on("GET /health", func(w http.ResponseWriter, _ *http.Request) {
			if !p.scripted(w, p.take()) {
				writeJSON(t, w, 200, metalWorker("/mnt/gotham"))
			}
		})
		r.on("POST /transcribe", func(w http.ResponseWriter, _ *http.Request) {
			if !p.scripted(w, p.take()) {
				writeJSON(t, w, 200, map[string]any{"text": "", "error": nil})
			}
		})
		return aitest.Subject{Backend: r.inst, Models: aitest.Models{Transcribe: "whisper-large-v3-turbo"}, Replay: p}
	})
}
