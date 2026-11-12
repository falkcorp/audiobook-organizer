// file: core/integrations/ai/openai/generate.go
// version: 1.0.0
// guid: c6d1f8a4-3e29-4b57-a0c3-5d8e2b7f9a14
// last-edited: 2026-11-12

package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// ---- text --------------------------------------------------------------------------

// chatParams turns a text request into the SDK's chat completion params.
func chatParams(model string, req ai.TextRequest) (oai.ChatCompletionNewParams, error) {
	msgs := make([]oai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, oai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		switch m.Role {
		case ai.RoleSystem:
			msgs = append(msgs, oai.SystemMessage(joinText(m.Parts)))
		case ai.RoleAssistant:
			msgs = append(msgs, oai.AssistantMessage(joinText(m.Parts)))
		case ai.RoleUser:
			user, err := userMessage(m.Parts)
			if err != nil {
				return oai.ChatCompletionNewParams{}, err
			}
			msgs = append(msgs, user)
		default:
			return oai.ChatCompletionNewParams{}, fmt.Errorf("unknown message role %q", string(m.Role))
		}
	}
	p := oai.ChatCompletionNewParams{Model: shared.ChatModel(model), Messages: msgs}
	if req.MaxOutputTokens > 0 {
		p.MaxCompletionTokens = param.NewOpt(int64(req.MaxOutputTokens))
	}
	if req.Temperature != nil {
		p.Temperature = param.NewOpt(*req.Temperature)
	}
	if req.Seed != nil {
		p.Seed = param.NewOpt(*req.Seed)
	}
	if len(req.Stop) > 0 {
		p.Stop = oai.ChatCompletionNewParamsStopUnion{OfStringArray: req.Stop}
	}
	switch req.Output.Kind {
	case ai.OutputJSONObject:
		f := shared.NewResponseFormatJSONObjectParam()
		p.ResponseFormat = oai.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: &f}
	case ai.OutputJSONSchema:
		if len(req.Output.Schema) == 0 {
			return oai.ChatCompletionNewParams{}, fmt.Errorf("a JSON Schema output needs its schema")
		}
		js := shared.ResponseFormatJSONSchemaJSONSchemaParam{Name: req.Output.Name, Schema: req.Output.Schema}
		if req.Output.Strict {
			js.Strict = param.NewOpt(true)
		}
		p.ResponseFormat = oai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{JSONSchema: js},
		}
	}
	return p, nil
}

func joinText(parts []ai.Part) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// userMessage is a user turn: plain text when it has no image, content parts
// when it has (vision).
func userMessage(parts []ai.Part) (oai.ChatCompletionMessageParamUnion, error) {
	hasImage := false
	for _, p := range parts {
		if p.Image != nil {
			hasImage = true
		}
	}
	if !hasImage {
		return oai.UserMessage(joinText(parts)), nil
	}
	var out []oai.ChatCompletionContentPartUnionParam
	for _, p := range parts {
		switch {
		case p.Image != nil:
			if p.Image.MIME == "" || len(p.Image.Data) == 0 {
				return oai.ChatCompletionMessageParamUnion{}, fmt.Errorf("an image part needs a MIME type and data")
			}
			url := "data:" + p.Image.MIME + ";base64," + base64.StdEncoding.EncodeToString(p.Image.Data)
			out = append(out, oai.ImageContentPart(oai.ChatCompletionContentPartImageImageURLParam{URL: url}))
		case p.Text != "":
			out = append(out, oai.TextContentPart(p.Text))
		}
	}
	return oai.UserMessage(out), nil
}

// usageOf reads a completion's usage.
func usageOf(u oai.CompletionUsage) ai.Usage {
	return ai.Usage{Requests: 1, InputTokens: u.PromptTokens, CachedInputTokens: u.PromptTokensDetails.CachedTokens, OutputTokens: u.CompletionTokens}
}

// Generate runs one chat completion.
func (i *instance) Generate(ctx context.Context, model string, req ai.TextRequest) (ai.TextResponse, error) {
	p, err := chatParams(model, req)
	if err != nil {
		return ai.TextResponse{}, &integrations.Error{Ref: i.ref, Op: "generate", Class: integrations.ClassInvalid, Err: err}
	}
	comp, err := i.cl.Chat.Completions.New(ctx, p)
	if err != nil {
		return ai.TextResponse{}, i.fail(ctx, "generate", err)
	}
	if len(comp.Choices) == 0 {
		return ai.TextResponse{}, i.badOutput("generate", "the server returned no choices")
	}
	ch := comp.Choices[0]
	if ch.Message.Refusal != "" {
		return ai.TextResponse{}, i.badOutput("generate", "the model refused the request")
	}
	return ai.TextResponse{Text: ch.Message.Content, FinishReason: string(ch.FinishReason), Model: comp.Model, Usage: usageOf(comp.Usage)}, nil
}

// ---- embeddings -----------------------------------------------------------------------

// Embed runs one embeddings call. A reply with a different number of vectors
// than inputs is unusable, never padded or truncated.
func (i *instance) Embed(ctx context.Context, model string, req ai.EmbedRequest) (ai.EmbedResponse, error) {
	p := oai.EmbeddingNewParams{
		Input: oai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: req.Inputs},
		Model: oai.EmbeddingModel(model),
	}
	if req.Dimensions > 0 {
		p.Dimensions = param.NewOpt(int64(req.Dimensions))
	}
	resp, err := i.cl.Embeddings.New(ctx, p)
	if err != nil {
		return ai.EmbedResponse{}, i.fail(ctx, "embed", err)
	}
	if len(resp.Data) != len(req.Inputs) {
		return ai.EmbedResponse{}, i.badOutput("embed", fmt.Sprintf("the server returned %d vectors for %d inputs", len(resp.Data), len(req.Inputs)))
	}
	vecs := make([][]float32, len(resp.Data))
	for _, d := range resp.Data {
		if d.Index < 0 || int(d.Index) >= len(vecs) || vecs[d.Index] != nil {
			return ai.EmbedResponse{}, i.badOutput("embed", "the server returned vectors with a bad index")
		}
		v := make([]float32, len(d.Embedding))
		for j, f := range d.Embedding {
			v[j] = float32(f)
		}
		vecs[d.Index] = v
	}
	return ai.EmbedResponse{Vectors: vecs, Model: resp.Model,
		Usage: ai.Usage{Requests: 1, InputTokens: resp.Usage.PromptTokens}}, nil
}

// ---- transcription ----------------------------------------------------------------------

// defaultTranscribeModel is asked for when the endpoint names none.
const defaultTranscribeModel = "whisper-1"

// audioName gives the upload a file name whose extension carries the format
// (the server identifies the audio by it).
func audioName(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "audio.wav"
	case "audio/flac", "audio/x-flac":
		return "audio.flac"
	case "audio/ogg", "audio/opus":
		return "audio.ogg"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "audio.m4a"
	case "audio/webm":
		return "audio.webm"
	}
	return "audio.mp3"
}

// transcription is the verbose_json reply, read from the raw body so the
// adapter does not depend on the SDK's response union.
type transcription struct {
	Text     string  `json:"text"`
	Language string  `json:"language"`
	Duration float64 `json:"duration"`
	Segments []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	} `json:"segments"`
}

// Transcribe uploads the audio and returns the transcript. A library path is
// not readable here: the adapter has no access to the library, so a request
// that carries only a path belongs to an endpoint with shared-filesystem access
// (a different adapter), and is refused as unsupported.
func (i *instance) Transcribe(ctx context.Context, model string, req ai.TranscribeRequest) (ai.TranscribeResponse, error) {
	if len(req.Audio.Data) == 0 {
		return ai.TranscribeResponse{}, &integrations.Error{Ref: i.ref, Op: "transcribe", Class: integrations.ClassUnsupported,
			Err: fmt.Errorf("this adapter uploads audio bytes; it cannot read a library path")}
	}
	if model == "" {
		model = defaultTranscribeModel
	}
	p := oai.AudioTranscriptionNewParams{
		File:           oai.File(bytes.NewReader(req.Audio.Data), audioName(req.Audio.MIME), req.Audio.MIME),
		Model:          oai.AudioModel(model),
		ResponseFormat: oai.AudioResponseFormatVerboseJSON,
	}
	if req.Language != "" {
		p.Language = param.NewOpt(req.Language)
	}
	if req.Prompt != "" {
		p.Prompt = param.NewOpt(req.Prompt)
	}
	if req.Timestamps {
		p.TimestampGranularities = []string{"segment"}
	}
	resp, err := i.cl.Audio.Transcriptions.New(ctx, p)
	if err != nil {
		return ai.TranscribeResponse{}, i.fail(ctx, "transcribe", err)
	}
	var t transcription
	if err := json.Unmarshal([]byte(resp.RawJSON()), &t); err != nil {
		return ai.TranscribeResponse{}, i.badOutput("transcribe", "the server's transcript is not JSON")
	}
	out := ai.TranscribeResponse{Text: t.Text, Language: t.Language, Duration: seconds(t.Duration)}
	for _, s := range t.Segments {
		out.Segments = append(out.Segments, ai.Segment{Start: seconds(s.Start), End: seconds(s.End), Text: s.Text})
	}
	out.Usage = ai.Usage{Requests: 1, AudioSeconds: t.Duration}
	return out, nil
}

func seconds(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// drain reads and closes a response body.
func drain(rc io.ReadCloser) {
	if rc != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(rc, 1<<20))
		_ = rc.Close()
	}
}
