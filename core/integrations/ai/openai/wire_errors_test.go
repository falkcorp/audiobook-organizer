// file: core/integrations/ai/openai/wire_errors_test.go
// version: 1.0.0
// guid: b4e81f60-3a97-4d25-8c1e-7f02a5d9c643
// last-edited: 2026-11-12

package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// A 2xx answer that is not the JSON the API defines is ClassBadOutput: the
// endpoint answered and the answer is the problem. The SDK reports these as
// bare decode errors, which classed as unknown until the adapter read them by
// type, and a 200 text/html (a url that points at a web page) as a string
// error, which the response guard now types and names.
func TestResponses_UndecodableAnswersAreBadOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
		want        integrations.Class
		mention     string // text the error must carry
		never       string // text the error must not carry
	}{
		{"text served as JSON", "application/json", "not json at all", integrations.ClassBadOutput, "not the JSON the API defines", "not json at all"},
		{"an array where an object is defined", "application/json", "[1,2,3]", integrations.ClassBadOutput, "not the JSON the API defines", ""},
		{"an empty body", "application/json", "", integrations.ClassBadOutput, "not the JSON the API defines", ""},
		{"a vendor JSON content type is JSON", "application/vnd.api+json", "[1,2,3]", integrations.ClassBadOutput, "not the JSON the API defines", ""},
		{"an HTML page answered 200", "text/html; charset=utf-8", "<html><body>Sign in to Wayne Enterprises</body></html>", integrations.ClassBadOutput, `content type "text/html"`, "Sign in"},
		{"an answer with no content type", "", "{}", integrations.ClassBadOutput, "content type", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			serve := func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				} else {
					w.Header()["Content-Type"] = nil // suppress the sniffed default
				}
				_, _ = io.WriteString(w, tc.body)
			}
			r := newRig(t)
			r.on("POST /v1/chat/completions", serve)
			r.on("POST /v1/embeddings", serve)
			r.on("POST /v1/audio/transcriptions", serve)
			r.on("GET /v1/models", serve)
			r.on("GET /v1/batches/{id}", serve)
			ctx := context.Background()
			_, gen := r.inst.Generate(ctx, "m", ai.TextRequest{System: "x"})
			_, emb := r.inst.Embed(ctx, "m", ai.EmbedRequest{Inputs: []string{"a"}})
			_, tr := r.inst.Transcribe(ctx, "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: []byte("x"), MIME: "audio/mpeg"}})
			_, caps := r.inst.Caps(ctx)
			_, st := r.inst.BatchStatus(ctx, "batch_7")
			for op, err := range map[string]error{"generate": gen, "embed": emb, "transcribe": tr, "caps": caps, "batch status": st} {
				if err == nil {
					t.Errorf("%s: the answer %q was accepted", op, tc.body)
					continue
				}
				e := asIntegrationsError(t, err)
				if e.Class != tc.want {
					t.Errorf("%s: class = %v, want %v (%v)", op, e.Class, tc.want, err)
				}
				if e.Ref.Instance != "arkham" || e.Op == "" {
					t.Errorf("%s: error carries ref %v op %q; both are the operator's trail", op, e.Ref, e.Op)
				}
				if !strings.Contains(err.Error(), tc.mention) {
					t.Errorf("%s: %q lacks %q", op, err.Error(), tc.mention)
				}
				if tc.never != "" && strings.Contains(err.Error(), tc.never) {
					t.Errorf("%s: the error repeats the server's body: %v", op, err)
				}
			}
		})
	}

	t.Run("an answer cut off mid-body is transient, not bad output", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","choices":[`)
			// Returning with fewer bytes than declared makes the server drop the connection.
		})
		_, err := r.inst.Generate(context.Background(), "m", ai.TextRequest{System: "x"})
		if c := classOf(err); c != integrations.ClassTransient {
			t.Errorf("class = %v, want Transient: a dying server's partial answer may succeed elsewhere (%v)", c, err)
		}
	})

	t.Run("a result file is not held to the JSON content type", func(t *testing.T) {
		t.Parallel()
		// The batch result files are JSONL under whatever content type the
		// server picks; the guard exempts a file's content and nothing else.
		r := newRig(t)
		var (
			mu sync.Mutex
			ct string
		)
		r.on("GET /v1/batches/{id}", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 200, batchAnswer("completed", "file-out-1", ""))
		})
		r.on("GET /v1/files/{id}/content", func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			w.Header().Set("Content-Type", ct)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"id":"r","custom_id":"q1","response":null,"error":{"code":"x","message":"y"}}`+"\n")
		})
		for _, c := range []string{"application/octet-stream", "application/jsonl", "text/plain", "application/x-ndjson"} {
			mu.Lock()
			ct = c
			mu.Unlock()
			seq, err := r.inst.BatchResults(context.Background(), "batch_7")
			if err != nil {
				t.Fatalf("%s: %v", c, err)
			}
			n := 0
			for res, err := range seq {
				if err != nil || res.CustomID != "q1" {
					t.Fatalf("%s: row %+v, %v", c, res, err)
				}
				n++
			}
			if n != 1 {
				t.Errorf("%s: %d rows", c, n)
			}
		}
	})
}
