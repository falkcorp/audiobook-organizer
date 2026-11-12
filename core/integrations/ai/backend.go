// file: core/integrations/ai/backend.go
// version: 1.0.0
// guid: 0c7d3f52-8e1b-4b9a-a6d4-2f5e9c1b7a08
// last-edited: 2026-11-12

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
)

// ---- adapters (backends) ------------------------------------------------------

// Backend is what an AI adapter instance implements; the optional kind
// interfaces declare what it can do. One instance = one endpoint.
type Backend interface {
	integrations.Instance
	// Caps reports models and MEASURED features/labels (e.g. "gpu"); the
	// router merges them with the operator-declared ones. Cached by the router
	// for core.ai.caps_ttl; refreshed by the health task and by Test.
	Caps(ctx context.Context) (BackendCaps, error)
}

// BackendCaps is what an adapter measured about its endpoint.
type BackendCaps struct {
	Models   []string
	Features []Feature
	Labels   []string
	Locality Locality // what the adapter can prove (local_process is always local); LocalityUnknown otherwise
}

// TextGenerator is the text kind of a Backend.
type TextGenerator interface {
	Generate(ctx context.Context, model string, req TextRequest) (TextResponse, error)
}

// Embedder is the embedding kind of a Backend.
type Embedder interface {
	Embed(ctx context.Context, model string, req EmbedRequest) (EmbedResponse, error)
}

// Transcriber is the transcription kind of a Backend.
type Transcriber interface {
	Transcribe(ctx context.Context, model string, req TranscribeRequest) (TranscribeResponse, error)
}

// BatchBackend is the batch API of a Backend (FeatureBatch).
type BatchBackend interface {
	SubmitBatch(ctx context.Context, model string, items []BatchItem, tags map[string]string) (BatchHandle, error)
	BatchStatus(ctx context.Context, h BatchHandle) (BatchState, error)
	BatchResults(ctx context.Context, h BatchHandle) (iter.Seq2[BatchResult, error], error)
	CancelBatch(ctx context.Context, h BatchHandle) error
	FindBatches(ctx context.Context, tags map[string]string) ([]BatchHandle, error) // orphan reconcile
}

// ---- batch values ---------------------------------------------------------------

// BatchHandle is the remote batch's own identifier, opaque to core.
type BatchHandle string

// BatchJobID names one stored batch job.
type BatchJobID string

// JobTagKey is the tag SubmitBatch puts on the remote batch, with the job ID as
// its value: the orphan reconcile (BatchBackend.FindBatches) looks a batch up
// by it when a crash left the handle unrecorded.
const JobTagKey = "mediacore_job"

// BatchPhase is where a remote batch is.
type BatchPhase uint8

// The phases.
const (
	BatchInProgress BatchPhase = iota // validating, running, finalizing
	BatchCompleted                    // results are ready
	BatchFailed                       // the remote rejected or failed it
	BatchExpired                      // the remote's window passed
	BatchCanceled                     // canceled at the remote
)

// String names the phase.
func (p BatchPhase) String() string {
	switch p {
	case BatchInProgress:
		return "in_progress"
	case BatchCompleted:
		return "completed"
	case BatchFailed:
		return "failed"
	case BatchExpired:
		return "expired"
	case BatchCanceled:
		return "canceled"
	}
	return "unknown"
}

// BatchState is a remote batch's status.
type BatchState struct {
	Phase     BatchPhase
	Total     int
	Completed int
	Failed    int
}

// BatchItem is one request of a batch.
type BatchItem struct {
	CustomID string
	Request  TextRequest // one kind per batch: text
}

// BatchResult is one row of a finished batch.
type BatchResult struct {
	CustomID string
	Text     *TextResponse
	Embed    *EmbedResponse
	Err      string // per-row provider error
}

// BatchOutcome is what a BatchHandler reports about a finished apply.
type BatchOutcome struct {
	Applied int
	Skipped int
	Failed  int
}

// ---- adapter descriptor ----------------------------------------------------------

// endpointProps are the settings properties of EndpointSettings an adapter's
// schema must carry (JSON names).
func endpointProps() []string {
	return []string{"concurrency", "capabilities", "text_model", "embed_model", "transcribe_model",
		"capability_models", "labels", "features", "require_gpu", "locality", "host_roots"}
}

// Adapter wraps an AI backend descriptor for Registrar.Adapter /
// app.Options.Adapters. It is the ai kind's typed constructor (05-metadata
// M1.3): the kind is always integrations.KindAI, and the built instance must
// be a Backend.
//
// The endpoint routing fields (EndpointSettings) belong in the adapter's own
// settings schema: config.Schema has no merge, so an adapter embeds
// EndpointSettings in its Settings struct and reflects that with
// config.SchemaFor. Adapter checks the schema and, when a routing property is
// missing, replaces it with the zero Schema, which Registry.Register refuses
// ("a zero settings schema"): an adapter that cannot say how it is routed
// never registers.
func Adapter(m integrations.Meta,
	build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Backend, error),
) integrations.Descriptor {
	m.Kind = integrations.KindAI
	if missing := missingEndpointProps(m.Settings); len(missing) > 0 {
		m.Settings = config.Schema{}
	}
	return integrations.NewDescriptor(m, build)
}

// SettingsProblem names the routing properties settings lacks, or "" when it
// carries them all. It is what an adapter's own test asserts about its schema.
func SettingsProblem(settings config.Schema) string {
	missing := missingEndpointProps(settings)
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("the settings schema lacks the endpoint routing propert%s %s (embed ai.EndpointSettings in the adapter's Settings)",
		map[bool]string{true: "y", false: "ies"}[len(missing) == 1], strings.Join(missing, ", "))
}

func missingEndpointProps(s config.Schema) []string {
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(s.JSON(), &doc); err != nil {
		return endpointProps()
	}
	var out []string
	for _, p := range endpointProps() {
		if _, ok := doc.Properties[p]; !ok {
			out = append(out, p)
		}
	}
	return out
}

// EndpointSettings are the routing attributes of every AI instance, stored on
// the instance row next to the adapter's own settings. An adapter embeds it in
// its Settings struct.
type EndpointSettings struct {
	// Concurrency is the endpoint's in-flight cap, at least 1; validation
	// rejects 0 (HEAD silently treated 0 as 1).
	Concurrency int `json:"concurrency" schema:"minimum=1"`
	// Capabilities are explicit capability IDs; default-deny; "*" is rejected
	// on Put (Router.ValidateEndpoint) and never honoured.
	Capabilities     []string          `json:"capabilities,omitzero"`
	TextModel        string            `json:"text_model,omitzero"`
	EmbedModel       string            `json:"embed_model,omitzero"`
	TranscribeModel  string            `json:"transcribe_model,omitzero"`
	CapabilityModels map[string]string `json:"capability_models,omitzero"` // per-capability override
	Labels           []string          `json:"labels,omitzero"`            // declared (e.g. "unmetered"); merged with measured
	Features         []Feature         `json:"features,omitzero"`          // declared (vision, json_schema, batch_api); merged with measured
	RequireGPU       bool              `json:"require_gpu,omitzero"`
	Locality         Locality          `json:"locality,omitzero" schema:"enum=auto|local|cloud"` // auto (default) | local | cloud
	HostRoots        map[string]string `json:"host_roots,omitzero"`                              // shared-fs root mapping
}

// Locality says whether an endpoint keeps data on the operator's own network.
// It is a string kind, not an integer with text methods, because the settings
// schema reflects the Go kind: a uint8 renders as {"type":"integer"}, which
// rejects the very spelling ("local") an operator writes and, were it to admit
// a number, encoding/json cannot decode a number into a TextUnmarshaler.
type Locality string

// The localities. The zero value is LocalityAuto, so an endpoint that sets
// nothing is decided by the router's heuristic. Only "auto", "local" and
// "cloud" are settable (the settings schema's enum); LocalityUnknown is what an
// adapter reports when it can prove neither, and is never a setting.
const (
	LocalityAuto    Locality = ""        // the router's heuristic over the instance's configured URLs ("auto" is the same)
	LocalityLocal   Locality = "local"   // data stays on the operator's network
	LocalityCloud   Locality = "cloud"   // data leaves it
	LocalityUnknown Locality = "unknown" // an adapter that cannot prove either
)

// String is the locality's settings spelling; the zero value is "auto".
func (l Locality) String() string {
	if l == LocalityAuto {
		return "auto"
	}
	return string(l)
}
