// file: core/integrations/ai/types.go
// version: 1.0.0
// guid: 5b0a8d9e-31c4-4a6f-9e72-8d4c1f6a2b03
// last-edited: 2026-11-12

package ai

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/library"
)

// ---- kinds, features, capabilities -----------------------------------------

// Kind is the kind of work a capability asks for.
type Kind string

// The kinds of work.
const (
	KindText       Kind = "text"       // chat/generation; vision via image parts
	KindEmbed      Kind = "embed"      // embeddings
	KindTranscribe Kind = "transcribe" // speech to text
)

// Valid reports whether k is one of the three kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindText, KindEmbed, KindTranscribe:
		return true
	}
	return false
}

// Feature is an endpoint property a capability can require.
type Feature string

// The features.
const (
	FeatureJSONSchema Feature = "json_schema" // response_format with a strict JSON Schema
	FeatureJSONMode   Feature = "json_mode"   // response_format json_object
	FeatureVision     Feature = "vision"
	FeatureBatch      Feature = "batch_api"
	FeatureTools      Feature = "tools"      // reserved; no core caller yet
	FeatureTimestamps Feature = "timestamps" // transcription segments with times
	// FeatureSharedFS is a prefix: FeatureSharedFS + a library root ID says the
	// endpoint reads the file itself (measured).
	FeatureSharedFS Feature = "shared_fs:"
)

// CapabilitySpec is what a module registers (Registrar.AICapability calls
// Router.RegisterCapability with the module's ID).
type CapabilitySpec struct {
	ID               string    // "<moduleID>.<name>", e.g. "books.filename_parse"; validated against the module ID
	Kind             Kind      //
	RequiredFeatures []Feature // contains-all
	OptionalFeatures []Feature // change delivery, never eligibility
	Locality         LocalityFloor
	AttemptTimeout   time.Duration // default per kind: text 120s, embed 60s, transcribe 30m
	Description      string        // all three texts required
	DataSent         string        // privacy disclosure shown next to every endpoint tick box
	TypicalRequest   string
}

// LocalityFloor is the module's privacy floor for a capability; a call may
// narrow it further (WithLocalOnly), never widen it.
type LocalityFloor uint8

// The floors.
const (
	AnyLocality LocalityFloor = iota
	LocalOnly                 // e.g. a capability whose DataSent is library or log content
)

// Capability is the unforgeable handle a registration returns. Its fields are
// unexported, so only RegisterCapability can mint one; it names the Router
// that minted it, so a handle of another App's Router is refused as
// unregistered and BatchApplyTask reaches its own Router with no global.
type Capability struct {
	id string
	r  *Router
}

// ID is the catalog ID ("books.filename_parse"); "" for the zero handle.
func (c Capability) ID() string { return c.id }

// IsZero reports the zero handle, which registration returns on error.
func (c Capability) IsZero() bool { return c.id == "" && c.r == nil }

// ---- requests ----------------------------------------------------------------

// Role is "system", "user" or "assistant".
type Role string

// The roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Part is one piece of a message: text or an image.
type Part struct {
	Text  string
	Image *Image // requires FeatureVision on the endpoint
}

// Image is image input.
type Image struct {
	MIME string
	Data []byte
}

// Message is one turn.
type Message struct {
	Role  Role
	Parts []Part
}

// OutputKind is the response format asked for.
type OutputKind uint8

// The output kinds.
const (
	OutputText OutputKind = iota
	OutputJSONObject
	OutputJSONSchema
)

// OutputFormat says what the answer should look like.
type OutputFormat struct {
	Kind   OutputKind      // OutputText | OutputJSONObject | OutputJSONSchema
	Name   string          // schema name for OutputJSONSchema
	Schema json.RawMessage // JSON Schema (2020-12 subset accepted by OpenAI-compatible servers)
	Strict bool
}

// TextRequest is one text generation call.
type TextRequest struct {
	System          string
	Messages        []Message
	Output          OutputFormat
	MaxOutputTokens int
	Temperature     *float64
	Seed            *int64
	Stop            []string
}

// TextResponse is its answer.
type TextResponse struct {
	Text         string
	FinishReason string // "stop", "length", "content_filter", ...
	Model        string // model the server reports
	Usage        Usage
	Served       Served // which endpoint answered (attribution; never a metric label in histograms)
}

// EmbedRequest is one embedding call.
type EmbedRequest struct {
	Inputs     []string
	Dimensions int // 0 = model default
}

// EmbedResponse is its answer.
type EmbedResponse struct {
	Vectors [][]float32
	Model   string
	Usage   Usage
	Served  Served
}

// AudioSource is the audio of a transcription: bytes, or a library path.
type AudioSource struct {
	Data []byte       // bytes to upload, or
	Path library.Path // a library path, used only when the endpoint has FeatureSharedFS for its root
	MIME string
}

// TranscribeRequest is one transcription call.
type TranscribeRequest struct {
	Audio      AudioSource
	Language   string // BCP-47 or "" for auto
	Prompt     string
	Timestamps bool
}

// Segment is one timed piece of a transcript.
type Segment struct {
	Start, End time.Duration
	Text       string
}

// TranscribeResponse is its answer.
type TranscribeResponse struct {
	Text     string
	Language string
	Duration time.Duration
	Segments []Segment
	Usage    Usage
	Served   Served
}

// Usage is what a call consumed.
type Usage struct {
	Requests          int
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	AudioSeconds      float64
}

// Served says which endpoint answered.
type Served struct {
	Endpoint integrations.Ref
	Model    string
	Attempts int
}
