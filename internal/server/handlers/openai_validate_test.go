// file: internal/server/handlers/openai_validate_test.go
// version: 1.0.0
// guid: 8d4b1e77-30a5-4f62-9c0d-51b6a2e7f8c3
// last-edited: 2026-09-10

package handlers

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// testKey is a placeholder that is deliberately not shaped like a real
// credential; nothing in this repo may carry a plausible live key.
const testKey = "sk-test-0001"

// runValidate drives the unexported handler against a stand-in OpenAI at
// baseURL and returns the recorded response. An optional timeout override
// keeps the timeout test from having to wait out the production deadline.
func runValidate(t *testing.T, baseURL, body string, timeout ...time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	d := validateOpenAIKeyTimeout
	if len(timeout) > 0 {
		d = timeout[0]
	}
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/setup/validate-openai-key",
		strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	validateOpenAIKey(c, baseURL, d)
	return rec
}

func decodeValidate(t *testing.T, rec *httptest.ResponseRecorder) validateOpenAIKeyResponse {
	t.Helper()
	var got validateOpenAIKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return got
}

func TestValidateOpenAIKey_ValidKey(t *testing.T) {
	var sawAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer ts.Close()

	rec := runValidate(t, ts.URL, `{"api_key":"`+testKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := decodeValidate(t, rec); !got.Valid {
		t.Fatalf("valid = false, want true (body %q)", rec.Body.String())
	}
	if sawAuth != "Bearer "+testKey {
		t.Fatalf("upstream Authorization = %q, want the bearer key", sawAuth)
	}
}

func TestValidateOpenAIKey_InvalidKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: ` + testKey + `"}}`))
	}))
	defer ts.Close()

	rec := runValidate(t, ts.URL, `{"api_key":"`+testKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a rejected key is a normal answer, not a server error)", rec.Code)
	}
	got := decodeValidate(t, rec)
	if got.Valid {
		t.Fatalf("valid = true, want false")
	}
	// The upstream body echoed the key back; ours must not.
	if strings.Contains(rec.Body.String(), testKey) {
		t.Fatalf("response echoed the api key: %q", rec.Body.String())
	}
}

func TestValidateOpenAIKey_NetworkError(t *testing.T) {
	// A 500 from OpenAI means "we could not verify", which must NOT be
	// flattened into a silent valid:false.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	rec := runValidate(t, ts.URL, `{"api_key":"`+testKey+`"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for an unverifiable upstream", rec.Code)
	}
	if got := decodeValidate(t, rec); got.Valid {
		t.Fatalf("valid = true on an upstream 5xx")
	}

	// Same for a transport-level failure (server closed).
	ts.Close()
	rec = runValidate(t, ts.URL, `{"api_key":"`+testKey+`"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("transport failure status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), testKey) {
		t.Fatalf("error response leaked the api key: %q", rec.Body.String())
	}
}

func TestValidateOpenAIKey_Timeout(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(release); ts.Close() }()

	const short = 200 * time.Millisecond
	start := time.Now()
	rec := runValidate(t, ts.URL, `{"api_key":"`+testKey+`"}`, short)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 on timeout (a slow OpenAI is unverifiable, not invalid)", rec.Code)
	}
	if got := decodeValidate(t, rec); got.Valid {
		t.Fatalf("valid = true on timeout")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("handler took %s, want it bounded by the %s deadline", elapsed, short)
	}
	if validateOpenAIKeyTimeout <= 0 {
		t.Fatalf("production timeout must be positive, got %s", validateOpenAIKeyTimeout)
	}
}

func TestValidateOpenAIKey_EmptyKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream must not be called for an empty key")
	}))
	defer ts.Close()

	for _, body := range []string{`{"api_key":""}`, `{"api_key":"   "}`, `{}`} {
		rec := runValidate(t, ts.URL, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestValidateOpenAIKey_DoesNotLogKey(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Exercise every branch: success, rejection, and upstream failure.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer unauthorized.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	for _, base := range []string{ok.URL, unauthorized.URL, broken.URL} {
		rec := runValidate(t, base, `{"api_key":"`+testKey+`"}`)
		if strings.Contains(rec.Body.String(), testKey) {
			t.Fatalf("response body leaked the key: %q", rec.Body.String())
		}
	}

	if strings.Contains(buf.String(), testKey) {
		t.Fatalf("the raw api key reached the log sink: %q", buf.String())
	}
}
