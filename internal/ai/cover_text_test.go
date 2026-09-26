// file: internal/ai/cover_text_test.go
// version: 1.0.0
// guid: 4e7b1c93-6a2f-4d58-b0e9-2c5f8a1d7b36
// last-edited: 2026-09-26

package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
)

// visionServer is a fake Ollama /v1 that records every chat request body.
type visionServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []map[string]any
	reply string
}

func newVisionServer(t *testing.T, reply string) *visionServer {
	t.Helper()
	v := &visionServer{reply: reply}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		v.mu.Lock()
		v.calls = append(v.calls, body)
		v.mu.Unlock()
		content, _ := json.Marshal(v.reply)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","created":0,"model":%q,"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":%s}}]}`,
			body["model"], content)
	}))
	t.Cleanup(v.srv.Close)
	return v
}

func (v *visionServer) url() string { return v.srv.URL + "/v1" }

func (v *visionServer) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.calls)
}

var cv = aidispatch.LLMCoverArtVision.ID()

func visionEP(id, url string, prio int) aidispatch.Endpoint {
	e := ep(id, url, prio, cv)
	e.Features = []string{aidispatch.FeatureVision}
	e.CapabilityModels = map[string]string{cv: "qwen2.5vl:7b"}
	return e
}

const fencedReply = "```json\n{\"title\": \"The Long Road\", \"subtitle\": \"A Novel\", \"author\": \"Jane Writer\", \"narrators\": [\"Sam Reader\"], \"series\": \"Roads\", \"series_number\": 2, \"other_text\": \"Unabridged\"}\n```"

func TestCoverTextRequestShape(t *testing.T) {
	srv := newVisionServer(t, fencedReply)
	tp := newTestPool(visionEP("node-a", srv.url(), 10))
	img := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}

	res, err := NewRoutedCoverTextReader(tp.PoolSource).ReadCoverText(context.Background(), img, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "qwen2.5vl:7b" || res.EndpointID != "node-a" {
		t.Fatalf("model/endpoint = %s/%s", res.Model, res.EndpointID)
	}
	if res.Text.Title != "The Long Road" || res.Text.Subtitle != "A Novel" || len(res.Text.Authors) != 1 ||
		res.Text.Authors[0] != "Jane Writer" || res.Text.Narrators[0] != "Sam Reader" ||
		res.Text.SeriesNumber != "2" || res.Text.OtherText[0] != "Unabridged" {
		t.Fatalf("parsed = %+v", res.Text)
	}

	if srv.count() != 1 {
		t.Fatalf("calls = %d", srv.count())
	}
	body := srv.calls[0]
	if body["model"] != "qwen2.5vl:7b" {
		t.Fatalf("model sent = %v; want the capability_models override", body["model"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d", len(msgs))
	}
	user, _ := msgs[1].(map[string]any)
	parts, _ := user["content"].([]any)
	if user["role"] != "user" || len(parts) != 2 {
		t.Fatalf("user message = %v", user)
	}
	imgPart, _ := parts[0].(map[string]any)
	iu, _ := imgPart["image_url"].(map[string]any)
	url, _ := iu["url"].(string)
	if imgPart["type"] != "image_url" || !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image part = %v", imgPart)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
	if err != nil || !bytes.Equal(decoded, img) {
		t.Fatal("image bytes did not round-trip through the data URI")
	}
}

// Routing picks the local row that has the capability AND the vision
// feature; a row without the feature, a row without the tick, and a cloud row
// with a worse priority are not used.
func TestCoverTextRoutingPicksCapableLocalRow(t *testing.T) {
	good := newVisionServer(t, `{"title":"X"}`)
	noFeature := newVisionServer(t, `{"title":"wrong"}`)
	noTick := newVisionServer(t, `{"title":"wrong"}`)
	cloud := newVisionServer(t, `{"title":"wrong"}`)

	nf := ep("no-feature", noFeature.url(), 1, cv) // ticked, but no vision feature
	nt := ep("no-tick", noTick.url(), 2, fp)       // vision feature, no tick
	nt.Features = []string{aidispatch.FeatureVision}
	cl := visionEP("cloud", cloud.url(), 50)
	cl.AuthRef = "openai_api_key"

	tp := newTestPool(nf, nt, visionEP("pool-node", good.url(), 10), cl)
	tp.Secret = func(string) string { return "sk-test" }
	res, err := NewRoutedCoverTextReader(tp.PoolSource).ReadCoverText(context.Background(), []byte{1}, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if res.EndpointID != "pool-node" || good.count() != 1 {
		t.Fatalf("endpoint = %s, good calls = %d", res.EndpointID, good.count())
	}
	if noFeature.count()+noTick.count()+cloud.count() != 0 {
		t.Fatalf("an ineligible or lower-priority row was called: %d/%d/%d", noFeature.count(), noTick.count(), cloud.count())
	}
	if c := NewRoutedCoverTextReader(tp.PoolSource).Capacity(); c < 1 {
		t.Fatalf("capacity = %d", c)
	}
}

// A dead local row fails over to the cloud row only because the config lists it.
func TestCoverTextFailsOverToListedCloudRow(t *testing.T) {
	cloud := newVisionServer(t, `{"title":"From Cloud"}`)
	cl := visionEP("cloud", cloud.url(), 50)
	cl.AuthRef = "openai_api_key"
	tp := newTestPool(visionEP("dead", deadURL(t), 10), cl)
	tp.Secret = func(string) string { return "sk-test" }
	res, err := NewRoutedCoverTextReader(tp.PoolSource).ReadCoverText(context.Background(), []byte{1}, "image/jpeg")
	if err != nil || res.EndpointID != "cloud" {
		t.Fatalf("failover: %+v %v", res, err)
	}
}

// A cloud row with a BETTER priority than the local row (the migrated OpenAI
// row under llm_mode openai-fallback-local) is still only a fallback.
func TestCoverTextLocalFirstDespiteCloudPriority(t *testing.T) {
	local := newVisionServer(t, `{"title":"Local"}`)
	cloud := newVisionServer(t, `{"title":"Cloud"}`)
	cl := visionEP("cloud", cloud.url(), 5)
	cl.AuthRef = "openai_api_key"
	tp := newTestPool(cl, visionEP("pool-node", local.url(), 10))
	tp.Secret = func(string) string { return "sk-test" }
	res, err := NewRoutedCoverTextReader(tp.PoolSource).ReadCoverText(context.Background(), []byte{1}, "image/jpeg")
	if err != nil || res.EndpointID != "pool-node" || cloud.count() != 0 {
		t.Fatalf("got %+v %v, cloud calls %d; want the local row", res, err, cloud.count())
	}
}

// An unparsable local reply is a quality failure: it is not re-asked of the cloud.
func TestCoverTextQualityFailureDoesNotFallBackToCloud(t *testing.T) {
	local := newVisionServer(t, "I cannot read this.")
	cloud := newVisionServer(t, `{"title":"Cloud"}`)
	cl := visionEP("cloud", cloud.url(), 50)
	cl.AuthRef = "openai_api_key"
	tp := newTestPool(visionEP("pool-node", local.url(), 10), cl)
	tp.Secret = func(string) string { return "sk-test" }
	if _, err := NewRoutedCoverTextReader(tp.PoolSource).ReadCoverText(context.Background(), []byte{1}, "image/jpeg"); err == nil {
		t.Fatal("want the parse error")
	}
	if cloud.count() != 0 {
		t.Fatalf("cloud called %d times after a local quality failure", cloud.count())
	}
}

func TestCoverTextRoutingOff(t *testing.T) {
	srv := newVisionServer(t, `{}`)
	tp := newTestPool(visionEP("n", srv.url(), 10))
	tp.active.Store(false)
	r := NewRoutedCoverTextReader(tp.PoolSource)
	if _, err := r.ReadCoverText(context.Background(), []byte{1}, "image/jpeg"); !errors.Is(err, ErrCoverTextRoutingOff) {
		t.Fatalf("err = %v", err)
	}
	if r.Capacity() != 0 || srv.count() != 0 {
		t.Fatal("routing off still reached the endpoint")
	}
}

func TestCoverTextNoCapableEndpoint(t *testing.T) {
	srv := newVisionServer(t, `{}`)
	tp := newTestPool(ep("text-only", srv.url(), 10, fp))
	_, err := NewRoutedCoverTextReader(tp.PoolSource).ReadCoverText(context.Background(), []byte{1}, "image/jpeg")
	if !errors.Is(err, aidispatch.ErrNoCapableEndpoint) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCoverTextReply(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		title    string
		wantErr  bool
	}{
		{"plain", `{"title":"A"}`, "A", false},
		{"fenced", "```json\n{\"title\":\"B\"}\n```", "B", false},
		{"fence no lang", "```\n{\"title\":\"C\"}\n```", "C", false},
		{"prose around", "Here you go:\n{\"title\":\"D\"}\nDone.", "D", false},
		{"empty object", `{}`, "", false},
		{"not json", "I cannot read this image.", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCoverTextReply(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil || got.Title != tc.title {
				t.Fatalf("got %+v %v", got, err)
			}
		})
	}
}
