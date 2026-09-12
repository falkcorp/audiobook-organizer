// file: internal/ai/openai_parser.go
// version: 13.14.0
// guid: 9a0b1c2d-3e4f-5a6b-7c8d-9e0f1a2b3c4d
// last-edited: 2026-09-12

package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// ParsedMetadata represents structured metadata extracted from a filename
type ParsedMetadata struct {
	Title      string `json:"title"`
	Author     string `json:"author"`
	Series     string `json:"series,omitempty"`
	SeriesNum  int    `json:"series_number,omitempty"`
	Narrator   string `json:"narrator,omitempty"`
	Publisher  string `json:"publisher,omitempty"`
	Year       int    `json:"year,omitempty"`
	Confidence string `json:"confidence"` // high, medium, low
}

// OpenAIParser handles AI-powered metadata parsing using OpenAI
type OpenAIParser struct {
	client        *openai.Client
	cfg           *config.Config // Per-feature model selection; may be nil (falls back to gpt-5-mini)
	maxRetries    int
	enabled       bool
	responseCache *cache.Cache[*ParsedMetadata] // Application-level response cache

	// defaultModelOverride replaces the package-level defaultModel constant as
	// the fallback when a per-feature model field is empty. Set by
	// NewOpenAIParserWithBaseURL so a local backend (e.g. Ollama) uses its own
	// model name (e.g. "qwen2.5:7b-instruct") instead of the OpenAI-only
	// "gpt-5-mini" default. Empty means fall back to defaultModel.
	defaultModelOverride string
}

// Default cache TTL for AI responses (24 hours — metadata doesn't change often)
const aiResponseCacheTTL = 24 * time.Hour

// defaultModel is the fallback when cfg is nil or the per-feature field is empty.
const defaultModel = "gpt-5-mini"

// fallbackModel returns the model to use when a per-feature field is empty:
// the per-client override (set for local backends) if present, else the
// package-level OpenAI default.
func (p *OpenAIParser) fallbackModel() string {
	if p.defaultModelOverride != "" {
		return p.defaultModelOverride
	}
	return defaultModel
}

// filenameParseModel returns the configured model for filename/audiobook parsing.
func (p *OpenAIParser) filenameParseModel() string {
	if p.cfg != nil && p.cfg.FilenameParseModel != "" {
		return p.cfg.FilenameParseModel
	}
	return p.fallbackModel()
}

// coverArtModel returns the configured model for cover-art parsing.
func (p *OpenAIParser) coverArtModel() string {
	if p.cfg != nil && p.cfg.CoverArtModel != "" {
		return p.cfg.CoverArtModel
	}
	return p.fallbackModel()
}

// metadataReviewModel returns the configured model for metadata review operations.
func (p *OpenAIParser) metadataReviewModel() string {
	if p.cfg != nil && p.cfg.MetadataReviewModel != "" {
		return p.cfg.MetadataReviewModel
	}
	return p.fallbackModel()
}

// NewOpenAIParser creates a new OpenAI parser using cfg.OpenAIBaseURL (if
// set) and the OpenAI default model. Preserved for backward compatibility
// with existing callers; new callers that need a per-client base URL / model
// (e.g. a local Ollama backend) should use NewOpenAIParserWithBaseURL, which
// never consults cfg.OpenAIBaseURL.
//
// cfg may be nil; when provided, per-feature model fields override the
// default. baseURL always falls back to the process-wide
// config.AppConfig.OpenAIBaseURL when cfg is nil or cfg.OpenAIBaseURL is
// unset — OPENAI_BASE_URL was historically a process-wide env var read
// unconditionally regardless of which cfg instance a caller passed, and this
// preserves that behavior now that it is viper-bound instead of read live.
func NewOpenAIParser(cfg *config.Config, apiKey string, enabled bool) *OpenAIParser {
	baseURL := config.AppConfig.OpenAIBaseURL
	if cfg != nil && cfg.OpenAIBaseURL != "" {
		baseURL = cfg.OpenAIBaseURL
	}
	return NewOpenAIParserWithBaseURL(cfg, apiKey, baseURL, defaultModel, enabled)
}

// NewOpenAIParserWithBaseURL creates an OpenAI parser pinned to an explicit
// base URL and fallback model, without consulting the process-wide
// OPENAI_BASE_URL env.
//
// baseURL scopes an OpenAI-compatible endpoint override to THIS parser only
// (e.g. "http://127.0.0.1:11434/v1" for a local Ollama). This is deliberately a
// parameter rather than the OPENAI_BASE_URL env, because that env applies to
// every default OpenAI client in the process — including the embedding client,
// which must NOT be silently redirected. An empty baseURL uses the OpenAI
// default endpoint.
//
// model is the fallback model used when a per-feature model field on cfg is
// empty; an empty model keeps the OpenAI-only defaultModel. cfg may be nil.
func NewOpenAIParserWithBaseURL(cfg *config.Config, apiKey, baseURL, model string, enabled bool) *OpenAIParser {
	if !enabled || apiKey == "" {
		return &OpenAIParser{enabled: false, cfg: cfg, defaultModelOverride: model}
	}

	clientOptions := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		clientOptions = append(clientOptions, option.WithBaseURL(baseURL))
	}

	client := openai.NewClient(clientOptions...)

	return &OpenAIParser{
		client:               &client,
		cfg:                  cfg,
		maxRetries:           2,
		enabled:              true,
		responseCache:        cache.New[*ParsedMetadata]("ai_response", aiResponseCacheTTL),
		defaultModelOverride: model,
	}
}

// cacheKey generates a deterministic hash key for caching AI responses.
func cacheKey(prefix string, input string) string {
	h := sha256.Sum256([]byte(prefix + ":" + input))
	return hex.EncodeToString(h[:16]) // 128-bit key is plenty
}

// CacheStats returns the number of cached entries (for diagnostics).
func (p *OpenAIParser) CacheStats() int {
	if p.responseCache == nil {
		return 0
	}
	return p.responseCache.Len()
}

// InvalidateCache clears the AI response cache.
func (p *OpenAIParser) InvalidateCache() {
	if p.responseCache != nil {
		p.responseCache.InvalidateAll()
	}
}

// IsEnabled returns whether the parser is enabled
func (p *OpenAIParser) IsEnabled() bool {
	return p.enabled
}

// ParseFilename uses OpenAI to parse a filename into structured metadata.
// Results are cached locally for 24h and uses OpenAI prompt caching for cost reduction.
// PRIORITY: Interactive
func (p *OpenAIParser) ParseFilename(ctx context.Context, filename string) (*ParsedMetadata, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}

	// Check application-level cache
	key := cacheKey("filename", filename)
	if cached, ok := p.responseCache.Get(key); ok {
		return cached, nil
	}

	systemPrompt := `You are an expert at parsing audiobook filenames. Extract structured metadata from the filename.

Common patterns:
- "Title - Author" or "Author - Title"
- "Author - Series Name Book N - Title"
- "Title (Series Name #N)" or "Title (Series Name, Book N)"
- May include narrator: "Title - Author - Narrator"
- May include year: "Title (2020)" or "Title - Author (2020)"

Return ONLY valid JSON with these fields (omit if not found):
{
  "title": "book title",
  "author": "author name",
  "series": "series name",
  "series_number": 1,
  "narrator": "narrator name",
  "publisher": "publisher name",
  "year": 2020,
  "confidence": "high|medium|low"
}

Set confidence based on clarity of the filename structure.`

	userPrompt := fmt.Sprintf("Parse this audiobook filename:\n\n%s", filename)

	jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()

	completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userPrompt),
		},
		Model:                shared.ChatModel(p.filenameParseModel()), // filename parsing uses FilenameParseModel
		MaxCompletionTokens:  param.NewOpt[int64](500),
		PromptCacheKey:       param.NewOpt("audiobook-filename-parser-v1"),
		PromptCacheRetention: openai.ChatCompletionNewParamsPromptCacheRetention24h,
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &jsonObjectFormat,
		},
		User: openai.String("ao-metadata"),
	})

	if err != nil {
		return nil, fmt.Errorf("OpenAI API call failed: %w", err)
	}

	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	content := completion.Choices[0].Message.Content
	result, err := parseMetadataFromJSON(content)
	if err != nil {
		return nil, err
	}

	// Cache the result
	p.responseCache.Set(key, result)
	return result, nil
}

// AudiobookContext provides rich context for AI parsing beyond just a filename.
type AudiobookContext struct {
	FilePath      string `json:"file_path"`                // Full path including folder hierarchy
	Title         string `json:"title,omitempty"`          // Existing title from DB
	AuthorName    string `json:"author_name,omitempty"`    // Existing author from DB
	Narrator      string `json:"narrator,omitempty"`       // Existing narrator from DB
	FileCount     int    `json:"file_count,omitempty"`     // Number of files in the book
	TotalDuration int    `json:"total_duration,omitempty"` // Total duration in seconds
}

// ParseAudiobook uses OpenAI to parse audiobook metadata from rich context
// (full file path, existing metadata, file count, duration) rather than just a filename.
// Results are cached locally for 24h by file path.
// PRIORITY: Interactive
func (p *OpenAIParser) ParseAudiobook(ctx context.Context, abCtx AudiobookContext) (*ParsedMetadata, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}

	// Check application-level cache by file path
	key := cacheKey("audiobook", abCtx.FilePath)
	if cached, ok := p.responseCache.Get(key); ok {
		return cached, nil
	}

	systemPrompt := `You are an expert at identifying audiobook metadata. Extract structured metadata from ALL available context: folder hierarchy, existing metadata, file details.

Key strategies:
- Folder paths often encode: /Author/Series/Title/ or /Author/Title/
- Existing metadata may be partially correct — improve or correct it
- Multi-file books (file_count > 1) are common; individual filenames like "01 Part 1.mp3" are not useful
- When author and narrator are the same, still include both
- If multiple authors or narrators, separate them with " & " (ampersand with spaces)

Return ONLY valid JSON with these fields (omit if not found):
{
  "title": "book title",
  "author": "author name (use ' & ' to separate multiple)",
  "series": "series name",
  "series_number": 1,
  "narrator": "narrator name (use ' & ' to separate multiple)",
  "publisher": "publisher name",
  "year": 2020,
  "confidence": "high|medium|low"
}

Set confidence based on how much context was available and how unambiguous it is.`

	// Build a rich user prompt with all available context
	userPrompt := fmt.Sprintf("Parse this audiobook's metadata from the following context:\n\nFull file path: %s", abCtx.FilePath)

	if abCtx.Title != "" {
		userPrompt += fmt.Sprintf("\nExisting title: %s", abCtx.Title)
	}
	if abCtx.AuthorName != "" {
		userPrompt += fmt.Sprintf("\nExisting author: %s", abCtx.AuthorName)
	}
	if abCtx.Narrator != "" {
		userPrompt += fmt.Sprintf("\nExisting narrator: %s", abCtx.Narrator)
	}
	if abCtx.FileCount > 0 {
		userPrompt += fmt.Sprintf("\nFile count: %d files", abCtx.FileCount)
	}
	if abCtx.TotalDuration > 0 {
		hours := abCtx.TotalDuration / 3600
		minutes := (abCtx.TotalDuration % 3600) / 60
		userPrompt += fmt.Sprintf("\nTotal duration: %dh %dm", hours, minutes)
	}

	jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()

	completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userPrompt),
		},
		Model:                shared.ChatModel(p.filenameParseModel()), // audiobook context parsing uses FilenameParseModel
		MaxCompletionTokens:  param.NewOpt[int64](500),
		PromptCacheKey:       param.NewOpt("audiobook-context-parser-v1"),
		PromptCacheRetention: openai.ChatCompletionNewParamsPromptCacheRetention24h,
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &jsonObjectFormat,
		},
		User: openai.String("ao-metadata"),
	})

	if err != nil {
		return nil, fmt.Errorf("OpenAI API call failed: %w", err)
	}

	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	content := completion.Choices[0].Message.Content
	result, err := parseMetadataFromJSON(content)
	if err != nil {
		return nil, err
	}

	p.responseCache.Set(key, result)
	return result, nil
}

// ParseBatch parses multiple filenames in a single request (more efficient).
// Retries with exponential backoff on failure to handle rate limiting.
func (p *OpenAIParser) ParseBatch(ctx context.Context, filenames []string) ([]*ParsedMetadata, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}

	if len(filenames) == 0 {
		return []*ParsedMetadata{}, nil
	}

	// This cap used to be its own `const maxBatchSize = 20`, silently truncating
	// anything larger -- and it was independent of the scanner phase's own
	// `const batchSize = 20`, so the two only agreed by coincidence. The moment
	// batch size became configurable (2026-09-09) that coincidence would have
	// broken: an operator setting 50 would get 20 parsed and 30 dropped with no
	// error anywhere, and the phase counts a batch as OK by the absence of an
	// error, so the summary would have reported success for a batch that
	// discarded 60% of its input.
	//
	// So it now shares the config ceiling, and going over it is an ERROR rather
	// than a truncation. A caller asking for more than can be served is a
	// misconfiguration, and the one thing it must not do is look like it worked.
	if len(filenames) > config.AIParseBatchSizeCeiling {
		return nil, fmt.Errorf("ParseBatch: %d filenames exceeds the %d-filename ceiling; lower ai_backend.parse_batch_size",
			len(filenames), config.AIParseBatchSizeCeiling)
	}

	systemPrompt := `You are an expert at parsing audiobook filenames. Extract structured metadata from each filename.

Common patterns:
- "Title - Author" or "Author - Title"
- "Author - Series Name Book N - Title"
- "Title (Series Name #N)" or "Title (Series Name, Book N)"
- May include narrator: "Title - Author - Narrator"
- May include year: "Title (2020)" or "Title - Author (2020)"

Return ONLY valid JSON with a "results" key containing an array with these fields for each file (omit if not found):
{"results": [
  {
    "title": "book title",
    "author": "author name",
    "series": "series name",
    "series_number": 1,
    "narrator": "narrator name",
    "publisher": "publisher name",
    "year": 2020,
    "confidence": "high|medium|low"
  }
]}

Set confidence based on clarity of the filename structure.`

	var userPrompt strings.Builder
	userPrompt.WriteString("Parse these audiobook filenames:\n\n")
	for i, filename := range filenames {
		userPrompt.WriteString(fmt.Sprintf("%d. %s\n", i+1, filename))
	}

	jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()

	var content string
	if err := DoWithRetry(ctx, p.maxRetries+1, 2*time.Second, func() error {
		completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(systemPrompt),
				openai.UserMessage(userPrompt.String()),
			},
			Model:                shared.ChatModel(p.filenameParseModel()), // batch filename parsing uses FilenameParseModel
			MaxCompletionTokens:  param.NewOpt[int64](2000),
			PromptCacheKey:       param.NewOpt("audiobook-batch-parser-v1"),
			PromptCacheRetention: openai.ChatCompletionNewParamsPromptCacheRetention24h,
			ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONObject: &jsonObjectFormat,
			},
			User: openai.String("ao-metadata"),
		})
		if err != nil {
			return fmt.Errorf("OpenAI API call failed: %w", err)
		}
		if len(completion.Choices) == 0 {
			return fmt.Errorf("no response from OpenAI")
		}
		content = completion.Choices[0].Message.Content
		return nil
	}); err != nil {
		return nil, err
	}
	// Always exactly len(filenames) entries (nil where the model had nothing),
	// or an error: the caller assigns results by position.
	return parseBatchMetadataFromJSON(content, len(filenames))
}

// ParseCoverArt uses OpenAI vision to extract metadata from audiobook cover art.
// It sends the cover image to gpt-5-mini and asks it to read the title, author,
// series, and other information visible on the cover.
func (p *OpenAIParser) ParseCoverArt(ctx context.Context, imageBytes []byte, mimeType string) (*ParsedMetadata, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}
	if len(imageBytes) == 0 {
		return nil, fmt.Errorf("empty image data")
	}

	b64 := base64.StdEncoding.EncodeToString(imageBytes)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

	systemPrompt := `You are an expert at reading audiobook cover art. Extract the title, author, series name, and any other metadata visible on the cover image.

Return ONLY valid JSON with these fields (omit if not visible):
{
  "title": "book title",
  "author": "author name",
  "series": "series name",
  "series_number": 1,
  "narrator": "narrator name",
  "publisher": "publisher name",
  "year": 2020,
  "confidence": "high|medium|low"
}

Set confidence based on how clearly the text is readable on the cover.`

	jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()

	completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{
				openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
					URL: dataURL,
				}),
				openai.TextContentPart("Read the metadata from this audiobook cover image."),
			}),
		},
		Model:                shared.ChatModel(p.coverArtModel()), // cover art parsing uses CoverArtModel
		PromptCacheKey:       param.NewOpt("audiobook-cover-parser-v1"),
		PromptCacheRetention: openai.ChatCompletionNewParamsPromptCacheRetention24h,
		MaxCompletionTokens:  param.NewOpt[int64](500),
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &jsonObjectFormat,
		},
		User: openai.String("ao-metadata"),
	})

	if err != nil {
		return nil, fmt.Errorf("OpenAI API call failed: %w", err)
	}

	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	content := completion.Choices[0].Message.Content
	return parseMetadataFromJSON(content)
}

// TestConnection tests the OpenAI API connection
func (p *OpenAIParser) TestConnection(ctx context.Context) error {
	if !p.enabled {
		return fmt.Errorf("OpenAI parser is not enabled")
	}

	// Set timeout for test
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Simple test parse
	_, err := p.ParseFilename(ctx, "The Hobbit - J.R.R. Tolkien")
	return err
}

// parseMetadataFromJSON parses a single metadata object from an LLM reply.
//
// This used to be a bare json.Unmarshal into ParsedMetadata, which has the
// batch path's fragility in a worse form: encoding/json ignores unknown keys,
// so a reply wrapped the way the batch prompt asks for -- {"results": [{...}]},
// which the local model has been seen to emit even for one item -- decoded to
// an all-empty ParsedMetadata with NO error, a silent empty parse. `null`
// decoded the same way.
//
// Now: a metadata object is decoded as before; `{}` is still an empty result;
// an object with none of ParsedMetadata's keys is accepted only as a wrapper
// with exactly one key holding one result (an object, or an array of at most
// one element, where an empty array is an empty result, matching the batch
// path). What the wrapper holds goes through decodeResultElement, so an error
// payload such as {"error": {"message": "..."}} is an error and not an empty
// parse, and a key other than "results" must hold something metadata-shaped
// to count as a wrapper at all. Anything else is a *ReplyParseError that
// carries a sanitized excerpt of the reply.
func parseMetadataFromJSON(content string) (*ParsedMetadata, error) {
	m, err := extractSingleMetadata([]byte(content))
	if err != nil {
		return nil, newReplyParseError(err, content)
	}
	return m, nil
}

func extractSingleMetadata(raw []byte) (*ParsedMetadata, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, errors.New("response is not a JSON object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}

	if len(obj) == 0 || hasParsedMetadataKey(obj) {
		// decodeResultElement, not a bare Unmarshal, so a top-level
		// {"title": null, "error": "x"} is an error here too.
		m, _, err := decodeResultElement(raw)
		if err != nil {
			return nil, fmt.Errorf("the reply object %w", err)
		}
		return m, nil
	}
	// The one-key wrapper below never looks at the key's name, so check it
	// here: {"error": {"title": "Solo"}} is an error report, and "Solo"
	// must not be saved as the book's title.
	if key, ok := errorReportKey(obj); ok {
		return nil, fmt.Errorf("object carries an %q key, so it is an error report", sanitizeReplyText(key))
	}
	if len(obj) != 1 {
		return nil, fmt.Errorf("object has %d keys and none of them is a metadata field", len(obj))
	}

	for key, value := range obj {
		m, isMetadata, err := unwrapSingleResult(value)
		if err != nil {
			return nil, fmt.Errorf("%q %w", sanitizeReplyText(key), err)
		}
		if key != resultsKey && !isMetadata {
			return nil, fmt.Errorf("object's only key %q is not a metadata field and holds no metadata object, so it is not a results wrapper",
				sanitizeReplyText(key))
		}
		if m == nil {
			// {"results": []} or {"results": [null]}: the model found nothing.
			m = &ParsedMetadata{}
		}
		return m, nil
	}
	return nil, errors.New("unreachable: one-key object had no key")
}

// unwrapSingleResult decodes what a single-key wrapper holds in a
// single-result reply: one object, or an array of at most one element. The
// returned error is a phrase meant to follow the key in a message.
func unwrapSingleResult(value json.RawMessage) (*ParsedMetadata, bool, error) {
	inner := bytes.TrimSpace(value)
	switch {
	case len(inner) > 0 && inner[0] == '{':
		m, isMetadata, err := decodeResultElement(inner)
		if err != nil {
			return nil, false, fmt.Errorf("holds an object that %w", err)
		}
		return m, isMetadata, nil
	case len(inner) > 0 && inner[0] == '[':
		var elems []json.RawMessage
		if err := json.Unmarshal(inner, &elems); err != nil {
			return nil, false, fmt.Errorf("holds an invalid array: %w", err)
		}
		switch len(elems) {
		case 0:
			return nil, false, nil
		case 1:
			m, isMetadata, err := decodeResultElement(elems[0])
			if err != nil {
				return nil, false, fmt.Errorf("holds an element that %w", err)
			}
			return m, isMetadata, nil
		default:
			return nil, false, fmt.Errorf("holds %d results where one was asked for", len(elems))
		}
	default:
		return nil, false, errors.New("is not a metadata field and holds neither an object nor an array")
	}
}

// decodeResultElement decodes one result the model returned for one input.
//
//   - JSON null is "no result": nil, and not an error. Observed from
//     qwen2.5:7b-instruct as a placeholder for a filename it could not parse.
//   - {} is an empty result.
//   - An object carrying at least one ParsedMetadata key is decoded. Presence
//     is what counts, not value: the observed replies send every key with null
//     or empty values for "found nothing". Keys match case-insensitively, as
//     encoding/json matches field names. Unrelated extra keys next to them,
//     such as "filename", are ignored.
//   - An object carrying an "error" or "errors" key (any case) is an error
//     report, even when metadata keys are present too:
//     {"title": null, "error": "rate limited"} would otherwise decode to an
//     all-empty result, and {"title": "Solo", "error": "x"} would save a
//     title the backend itself flagged.
//   - Anything else is an error. In particular an object whose keys are all
//     foreign -- {"error": "x"}, {"code": 500, "message": "overloaded"} -- is
//     not a result. encoding/json would decode it to an all-empty
//     ParsedMetadata with no error, the scanner would save that as the book's
//     AI parse and stamp it in the scan cache, and the book would never be
//     sent to the model again.
//
// isMetadata reports whether the element carried a metadata key, which is the
// only evidence that an unfamiliar wrapper key really holds results.
//
// The returned error is a phrase meant to follow "result N" or "holds an
// element that" in a message.
func decodeResultElement(raw json.RawMessage) (m *ParsedMetadata, isMetadata bool, err error) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false, errors.New("is not a JSON object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false, fmt.Errorf("is not a valid JSON object: %w", err)
	}
	if len(obj) > 0 && !hasParsedMetadataKey(obj) {
		return nil, false, fmt.Errorf("has %d key(s) and none of them is a metadata field", len(obj))
	}
	if key, ok := errorReportKey(obj); ok {
		return nil, false, fmt.Errorf("carries an %q key, so it is an error report and not a result", sanitizeReplyText(key))
	}
	var metadata ParsedMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, false, fmt.Errorf("does not decode as metadata: %w", err)
	}
	return &metadata, len(obj) > 0, nil
}

// SuggestionRole represents a detected role (author, narrator, or publisher) in an AI suggestion.
type SuggestionRole struct {
	Name     string   `json:"name,omitempty"`
	IDs      []int    `json:"ids,omitempty"`
	Variants []string `json:"variants,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// SuggestionRoles contains the structured role decomposition for an AI suggestion.
type SuggestionRoles struct {
	Author    *SuggestionRole `json:"author,omitempty"`
	Narrator  *SuggestionRole `json:"narrator,omitempty"`
	Publisher *SuggestionRole `json:"publisher,omitempty"`
}

// AuthorDedupInput represents a group of potential duplicate authors for AI review.
type AuthorDedupInput struct {
	Index         int      `json:"index"`
	CanonicalName string   `json:"canonical_name"`
	VariantNames  []string `json:"variant_names"`
	BookCount     int      `json:"book_count"`
	SampleTitles  []string `json:"sample_titles"`
}

// AuthorDedupSuggestion represents an AI suggestion for handling a duplicate author group.
type AuthorDedupSuggestion struct {
	GroupIndex    int              `json:"group_index"`
	Action        string           `json:"action"`
	CanonicalName string           `json:"canonical_name"`
	Reason        string           `json:"reason"`
	Confidence    string           `json:"confidence"`
	IsNarrator    []int            `json:"is_narrator,omitempty"`  // Deprecated: use Roles
	IsPublisher   []int            `json:"is_publisher,omitempty"` // Deprecated: use Roles
	Roles         *SuggestionRoles `json:"roles,omitempty"`
}

// ReviewAuthorDuplicates sends duplicate author groups to OpenAI for review.
// Groups are batched in chunks of 50 to manage token limits.
func (p *OpenAIParser) ReviewAuthorDuplicates(ctx context.Context, groups []AuthorDedupInput) ([]AuthorDedupSuggestion, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}
	if len(groups) == 0 {
		return []AuthorDedupSuggestion{}, nil
	}

	return p.reviewAuthorBatch(ctx, groups)
}

func (p *OpenAIParser) reviewAuthorBatch(ctx context.Context, batch []AuthorDedupInput) ([]AuthorDedupSuggestion, error) {
	systemPrompt := `You are an expert audiobook metadata reviewer. You will receive groups of potentially duplicate author names. For each group, determine the correct action:

- "merge": The variants are the same single author with different name formats. Provide the correct canonical name.
- "split": An entry contains multiple different people combined into one string. ALWAYS use split when a single entry contains two or more real people separated by commas, ampersands, "and", brackets, etc. Examples: "V. A. Lewis, Azrie" is TWO people and must be split. "Stephen King & Peter Straub" must be split. "Graphic Audio [R.A. Salvatore]" must be split into author + publisher.
- "rename": The canonical name needs correction (e.g., "TOLKIEN, J.R.R." → "J.R.R. Tolkien").
- "skip": The group is fine as-is or you're unsure.
- "alias": Pen names or stage names for the same person (e.g., "Mark Twain" / "Samuel Clemens"). The canonical_name should be the real/professional name.
- "reclassify": Entry is not an author at all (narrator/publisher misclassified as author).

COMPOUND ENTRIES — THIS IS CRITICAL:
- If a SINGLE entry contains two or more real people, the action MUST be "split", NEVER "merge".
- "V. A. Lewis, Azrie" → action: "split" — these are two separate authors.
- "Patterson, James" → action: "merge" or "rename" — this is ONE person in "Last, First" format.
- To distinguish: check if the parts after the comma are a first name matching the surname before it (one person) vs a completely different name (two people). When in doubt, check the sample_titles.
- For compound entries with publishers (Graphic Audio, Marvel, DC, Brilliance Audio, Full Cast Audio), use "split" and populate the roles object.

INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee", "J. R. R. Tolkien" not "J.R.R. Tolkien".

SELF-NARRATING AUTHORS: Some authors narrate their own audiobooks (e.g., Neil Gaiman, Stephen Fry, David Sedaris). If a name appears as both author and narrator, do NOT reclassify them as a narrator. Only reclassify as narrator if the person EXCLUSIVELY narrates other people's books and never writes their own. Check sample_titles — if their name appears as author on the books they narrate, they are a self-narrating author and should be kept as an author.

ROLE DECOMPOSITION: For every suggestion, populate the "roles" object to classify each name:
- "author": the actual book author with name variants
- "narrator": a voice actor identified by reading many different authors' books (NOT self-narrating authors)
- "publisher": a production company or publisher (e.g., Graphic Audio, Brilliance Audio, Marvel)

Return ONLY valid JSON: {"suggestions": [{"group_index": N, "action": "merge|split|rename|skip|alias|reclassify", "canonical_name": "Correct Name", "reason": "brief explanation", "confidence": "high|medium|low", "is_narrator": [indices], "is_publisher": [indices], "roles": {"author": {"name": "Author Name", "variants": ["Variant1"], "reason": "why"}, "narrator": {"name": "Narrator Name", "ids": [indices], "reason": "why"}, "publisher": {"name": "Publisher Name", "ids": [indices], "reason": "why"}}}]}

The roles object fields are all optional — only include roles that are detected.`

	batchJSON, err := json.Marshal(batch)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal batch: %w", err)
	}

	userPrompt := fmt.Sprintf("Review these duplicate author groups:\n\n%s", string(batchJSON))

	jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()

	var suggestions []AuthorDedupSuggestion
	if err := DoWithRetry(ctx, p.maxRetries+1, 2*time.Second, func() error {
		completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(systemPrompt),
				openai.UserMessage(userPrompt),
			},
			Model:                shared.ChatModel(p.metadataReviewModel()), // author dedup review uses MetadataReviewModel
			MaxCompletionTokens:  param.NewOpt[int64](32000),
			PromptCacheKey:       param.NewOpt("audiobook-author-dedup-v4"),
			PromptCacheRetention: openai.ChatCompletionNewParamsPromptCacheRetention24h,
			ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONObject: &jsonObjectFormat,
			},
			User: openai.String("ao-metadata"),
		})
		if err != nil {
			return fmt.Errorf("OpenAI API call failed: %w", err)
		}
		if len(completion.Choices) == 0 {
			return fmt.Errorf("no response from OpenAI")
		}
		var result struct {
			Suggestions []AuthorDedupSuggestion `json:"suggestions"`
		}
		if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}
		suggestions = result.Suggestions
		return nil
	}); err != nil {
		return nil, err
	}
	return suggestions, nil
}

// AuthorDiscoveryInput represents a single author for AI-driven duplicate discovery (Full mode).
type AuthorDiscoveryInput struct {
	ID           int      `json:"id"`
	Name         string   `json:"name"`
	BookCount    int      `json:"book_count"`
	SampleTitles []string `json:"sample_titles"`
}

// AuthorDiscoverySuggestion represents an AI suggestion from full-discovery mode.
type AuthorDiscoverySuggestion struct {
	AuthorIDs     []int            `json:"author_ids"`
	Action        string           `json:"action"`
	CanonicalName string           `json:"canonical_name"`
	Reason        string           `json:"reason"`
	Confidence    string           `json:"confidence"`
	IsNarrator    []int            `json:"is_narrator,omitempty"`  // Deprecated: use Roles
	IsPublisher   []int            `json:"is_publisher,omitempty"` // Deprecated: use Roles
	Roles         *SuggestionRoles `json:"roles,omitempty"`
}

// DiscoverAuthorDuplicates sends the full list of authors to OpenAI in a single request
// so the AI can see cross-author relationships and avoid nonsensical matches.
func (p *OpenAIParser) DiscoverAuthorDuplicates(ctx context.Context, inputs []AuthorDiscoveryInput) ([]AuthorDiscoverySuggestion, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}
	if len(inputs) == 0 {
		return []AuthorDiscoverySuggestion{}, nil
	}

	return p.discoverAuthorBatch(ctx, inputs)
}

func (p *OpenAIParser) discoverAuthorBatch(ctx context.Context, batch []AuthorDiscoveryInput) ([]AuthorDiscoverySuggestion, error) {
	systemPrompt := `You are an expert audiobook metadata reviewer. You will receive a list of authors with their IDs, book counts, and sample book titles. Find groups of authors that are likely the same person, and identify compound entries, misclassified narrators/publishers, etc.

ACTIONS:
- "merge": Two or more entries are the same single person with different name formats. Provide the correct canonical name.
- "split": A SINGLE entry contains multiple different people combined into one string. ALWAYS use split for these — NEVER merge.
- "rename": A single entry needs its name corrected (e.g., "TOLKIEN, J.R.R." → "J.R.R. Tolkien").
- "skip": Fine as-is or you're unsure.
- "alias": Pen names or stage names for the same person. canonical_name = real/professional name.
- "reclassify": Entry is not an author (narrator/publisher misclassified as author).

COMPOUND ENTRIES — THIS IS THE MOST IMPORTANT RULE:
- If a SINGLE author entry contains two or more real people, the action MUST be "split", NEVER "merge".
- "V. A. Lewis, Azrie" → TWO different people → action: "split", list both names in reason.
- "Stephen King & Peter Straub" → TWO people → action: "split".
- "Graphic Audio [R.A. Salvatore]" → author + publisher → action: "split", populate roles.
- "Patterson, James" → ONE person in "Last, First" format → NOT a compound. action: "rename" to "James Patterson".
- HOW TO TELL: If the part after the comma/ampersand is a first name that belongs with the surname before it, it's one person. If it's a completely different name, it's two people. Use sample_titles to verify — do both names appear as separate authors elsewhere?
- Even if one component matches an existing individual author entry, the compound entry itself must be SPLIT, not merged. The individual components can then be merged separately after splitting.

CRITICAL RULES:
- Use sample_titles to distinguish authors from narrators. A narrator reads many different authors' books.
- SELF-NARRATING AUTHORS: Some authors narrate their own audiobooks (e.g., Neil Gaiman, Stephen Fry, David Sedaris). If a name appears as both author and narrator, do NOT reclassify them as a narrator. Only reclassify as narrator if the person EXCLUSIVELY narrates other people's books and never writes their own.
- NEVER merge two genuinely different people.
- Only merge when names clearly refer to the same person: e.g. "J.R.R. Tolkien" and "Tolkien, J.R.R."
- If unsure, use action "skip" — false negatives are far better than false positives.
- INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".
- Identify narrators or publishers incorrectly listed as authors (but NOT self-narrating authors).

COMPOUND ENTRIES WITH PUBLISHERS:
- "Graphic Audio [John Smith]" → action: "split", Author: John Smith, Publisher: Graphic Audio
- "Name, Marvel" or "Name, DC" → action: "split", Author: Name, Publisher: Marvel/DC
- "Full Cast Audio" alone → action: "reclassify" (publisher, not author)
- Populate the roles object with structured data.

ROLE DECOMPOSITION: For every suggestion, populate the "roles" object:
- "author": the actual book author with name variants
- "narrator": a voice actor identified by reading many different authors' books (NOT self-narrating authors)
- "publisher": a production company or publisher (e.g., Graphic Audio, Brilliance Audio, Marvel)

Return ONLY valid JSON: {"suggestions": [{"author_ids": [1, 42], "action": "merge|rename|split|skip|alias|reclassify", "canonical_name": "Correct Name", "reason": "brief explanation", "confidence": "high|medium|low", "is_narrator": [ids], "is_publisher": [ids], "roles": {"author": {"name": "Author Name", "variants": ["Variant1"], "reason": "why"}, "narrator": {"name": "Narrator Name", "ids": [ids], "reason": "why"}, "publisher": {"name": "Publisher Name", "ids": [ids], "reason": "why"}}}]}

The roles object fields are all optional — only include roles that are detected. Only include groups where you find actual duplicates or issues. Do not return entries for authors that look fine.`

	batchJSON, err := json.Marshal(batch)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal batch: %w", err)
	}

	userPrompt := fmt.Sprintf("Find duplicate authors in this list:\n\n%s", string(batchJSON))

	jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()

	var suggestions []AuthorDiscoverySuggestion
	if err := DoWithRetry(ctx, p.maxRetries+1, 2*time.Second, func() error {
		completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(systemPrompt),
				openai.UserMessage(userPrompt),
			},
			Model:                shared.ChatModel(p.metadataReviewModel()), // author discovery review uses MetadataReviewModel
			MaxCompletionTokens:  param.NewOpt[int64](16000),
			PromptCacheKey:       param.NewOpt("audiobook-author-discover-v4"),
			PromptCacheRetention: openai.ChatCompletionNewParamsPromptCacheRetention24h,
			ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONObject: &jsonObjectFormat,
			},
			User: openai.String("ao-metadata"),
		})
		if err != nil {
			return fmt.Errorf("OpenAI API call failed: %w", err)
		}
		if len(completion.Choices) == 0 {
			return fmt.Errorf("no response from OpenAI")
		}
		var result struct {
			Suggestions []AuthorDiscoverySuggestion `json:"suggestions"`
		}
		if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}
		suggestions = result.Suggestions
		return nil
	}); err != nil {
		return nil, err
	}
	return suggestions, nil
}

// parseBatchMetadataFromJSON parses a ParseBatch reply into exactly `expected`
// entries, one per input filename in input order, nil where the model had
// nothing for that filename.
//
// The caller (scanner.runAIBatchPhase) assigns results[i] to the i-th book, so
// the one thing this must never do is return a list whose positions do not
// line up with the inputs. Every accepted shape below either has exactly
// `expected` entries or has none; anything else is an error.
//
// Accepted shapes, and whether each has been seen from a real backend:
//
//   - {"results": [...]}                -- observed; the shape the prompt asks for.
//     Entries may be null or have null fields (both observed from
//     qwen2.5:7b-instruct); they decode to nil / zero values. Accepted only
//     when "results" is the object's ONLY key: {"results": [], "error": "..."}
//     is an error, not zero results.
//   - {"results": []}                   -- observed, and the cause of the
//     2026-09-12 library.ai-parse aborts. See below.
//   - [...]                             -- bare array; accepted since before
//     this change.
//   - {"<any single key>": [...]}       -- unobserved; a wrapper under a key
//     other than "results". Accepted only when it is the object's ONLY key,
//     so an unrelated array can never be mistaken for the results, and only
//     when at least one element is a metadata object, so {"error": []} or
//     {"error": [{"code": 500}]} is never taken for a results list.
//   - {"title": ..., ...}               -- unobserved; a bare metadata object.
//     Accepted only when the batch had exactly one filename, where position
//     cannot be ambiguous.
//
// In every array shape each element must be null, {}, or an object carrying
// at least one ParsedMetadata key (decodeResultElement); an element whose keys
// are all foreign, e.g. {"error": "x"}, fails the whole reply. Every failure is
// a *ReplyParseError.
//
// {"results": []} is ZERO RESULTS, not an error. qwen2.5:7b-instruct returns it
// for inputs that carry no metadata at all ("Disc 1".."Disc 8", "Season 2");
// for the identical 8-disc input it has also returned 8 entries whose fields
// are all null, which this function has always accepted. Both mean "found
// nothing", and both must reach the caller the same way -- each book saved
// unchanged and stamped as attempted. Treating the empty rendering as an error
// did not protect anything: it counted toward the phase's 3-failure abort
// threshold, which exists to detect a dead backend, and one op aborted with
// 74 of 106 books unparsed because three batches of folder names like these
// "failed". An empty list also cannot misassign anything, which is the only
// hazard the count check below exists for.
func parseBatchMetadataFromJSON(content string, expected int) ([]*ParsedMetadata, error) {
	items, err := extractBatchItems([]byte(content), expected)
	if err != nil {
		return nil, newReplyParseError(err, content)
	}

	if len(items) == 0 {
		return make([]*ParsedMetadata, expected), nil
	}
	if len(items) != expected {
		// A short list means the model dropped entries somewhere, and nothing
		// says which: results[2] could belong to filename 5. Positional
		// assignment would silently write one book's title onto another, so
		// this is an error and the batch's books are left for the next scan.
		return nil, newReplyParseError(fmt.Errorf(
			"got %d result(s) for %d filename(s), and results are matched to filenames by position",
			len(items), expected), content)
	}
	return items, nil
}

// extractBatchItems finds the results array in a batch reply. See
// parseBatchMetadataFromJSON for which shapes are accepted and why.
func extractBatchItems(raw []byte, expected int) ([]*ParsedMetadata, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errors.New("empty response")
	}

	switch raw[0] {
	case '[':
		items, _, err := decodeMetadataArray(raw)
		return items, err
	case '{':
	default:
		return nil, errors.New("response is neither a JSON object nor a JSON array")
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}

	if results, ok := obj[resultsKey]; ok {
		if len(obj) != 1 {
			// {"results": [], "error": "context length exceeded"}: the
			// sibling says the list is not an answer. Taking the empty list
			// as "found nothing" would save every book in the batch as
			// parsed-and-empty and stamp it in the scan cache, so none of
			// them would ever be sent to the model again.
			return nil, fmt.Errorf(`"results" is present alongside %d other key(s); only a reply whose sole key is "results" is taken as the results`,
				len(obj)-1)
		}
		if !isJSONArray(results) {
			// {"results": null} and {"results": {...}} are not the shape
			// asked for and neither has been seen; fail rather than guess.
			return nil, errors.New(`"results" is not a JSON array`)
		}
		items, _, err := decodeMetadataArray(results)
		return items, err
	}

	if expected == 1 && hasParsedMetadataKey(obj) {
		// Same element rules as every other shape: a bare object that also
		// carries "error" is an error report, not the one result.
		m, _, err := decodeResultElement(raw)
		if err != nil {
			return nil, fmt.Errorf("the reply object %w", err)
		}
		return []*ParsedMetadata{m}, nil
	}

	// The one-key wrapper below takes any key holding metadata objects, so
	// the key's name is checked here. JSON:API and RFC 7807 error bodies
	// look like {"errors": [{"status": "429", "title": "Too Many Requests"}]},
	// and that title must not be saved as a book's title.
	if key, ok := errorReportKey(obj); ok {
		return nil, fmt.Errorf("object carries an %q key, so it is an error report", sanitizeReplyText(key))
	}

	if len(obj) == 1 {
		for key, value := range obj {
			if !isJSONArray(value) {
				return nil, fmt.Errorf(`object's only key %q is not a JSON array and there is no "results" key`,
					sanitizeReplyText(key))
			}
			items, anyMetadata, err := decodeMetadataArray(value)
			if err != nil {
				return nil, err
			}
			// Only "results" is trusted to mean "results" when it holds
			// nothing: {"error": []} or {"errors": [null]} carries no evidence
			// that it is a results list, and taking it as one would be the
			// same silent empty parse as an error payload.
			if !anyMetadata {
				return nil, fmt.Errorf(`object's only key %q holds no metadata object, so it is not a results wrapper`,
					sanitizeReplyText(key))
			}
			return items, nil
		}
	}
	return nil, fmt.Errorf(`object has %d keys and no "results" array`, len(obj))
}

// resultsKey is the wrapper key the batch prompt asks the model to use.
const resultsKey = "results"

// decodeMetadataArray decodes a JSON array of results, one per input in
// order, validating every element with decodeResultElement. anyMetadata
// reports whether at least one element carried a metadata key.
func decodeMetadataArray(raw []byte) (items []*ParsedMetadata, anyMetadata bool, err error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, false, err
	}
	items = make([]*ParsedMetadata, len(elems))
	for i, elem := range elems {
		m, isMetadata, err := decodeResultElement(elem)
		if err != nil {
			return nil, false, fmt.Errorf("result %d %w", i, err)
		}
		items[i] = m
		anyMetadata = anyMetadata || isMetadata
	}
	return items, anyMetadata, nil
}

func isJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}

// parsedMetadataKeys are the JSON keys of ParsedMetadata. An object carrying
// any of them is a metadata object; one carrying none of them is something
// else (a wrapper, an error payload) and must not be decoded as metadata,
// because encoding/json ignores unknown keys and would return an all-empty
// ParsedMetadata with no error.
var parsedMetadataKeys = []string{"title", "author", "series", "series_number", "narrator", "publisher", "year", "confidence"}

// hasParsedMetadataKey matches keys case-insensitively because encoding/json
// does: {"Title": "Capital"} decodes into ParsedMetadata.Title, so an exact
// match here would reject a reply the decoder reads correctly.
func hasParsedMetadataKey(obj map[string]json.RawMessage) bool {
	for key := range obj {
		for _, k := range parsedMetadataKeys {
			if strings.EqualFold(key, k) {
				return true
			}
		}
	}
	return false
}

// errorReportKeys mark an object as an error report from the backend or the
// model rather than a result, whatever else it carries.
var errorReportKeys = []string{"error", "errors"}

// errorReportKey returns the first key of obj that is "error" or "errors" in
// any case.
func errorReportKey(obj map[string]json.RawMessage) (string, bool) {
	for key := range obj {
		for _, k := range errorReportKeys {
			if strings.EqualFold(key, k) {
				return key, true
			}
		}
	}
	return "", false
}

// maxResponseExcerptBytes bounds how much of an unparseable LLM reply is copied
// into an error. The error lands in the library.ai-parse operation record
// (AIBatchFailure.Err), and that store's growth is a measured production
// problem, so the excerpt is enough to recognise the shape, not the whole
// reply.
const maxResponseExcerptBytes = 300

// ReplyParseError reports a reply from the model that could not be turned into
// metadata: the provider answered, and what it said was not a usable result.
//
// It exists so that callers classify the failure by TYPE and never by text.
// Error() includes Excerpt, up to maxResponseExcerptBytes of model-written
// text, because the operation log needs it to diagnose the next bad reply. That
// makes the message unsafe to string-match: a filename echoed into a malformed
// reply can contain "permission_error" or "invalid_api_key", and
// internal/scanner's isPermanentAIFailure used to abort the whole AI phase on
// such a substring. A reply we could not decode is by definition not an auth
// or quota failure from the provider, so that classifier returns false for
// anything that unwraps to *ReplyParseError before it looks at any text.
type ReplyParseError struct {
	// Err says what was wrong with the reply. Its message can quote
	// model-written JSON keys, sanitized the same way as Excerpt.
	Err error
	// Excerpt is a truncated, log-safe copy of the reply (responseExcerpt).
	Excerpt string
}

func (e *ReplyParseError) Error() string {
	return fmt.Sprintf("failed to parse OpenAI response: %v; response: %s", e.Err, e.Excerpt)
}

func (e *ReplyParseError) Unwrap() error {
	return e.Err
}

func newReplyParseError(err error, content string) *ReplyParseError {
	return &ReplyParseError{Err: err, Excerpt: responseExcerpt(content)}
}

// responseExcerpt returns a truncated, log-safe copy of an LLM reply for use in
// an error message, so the next parse failure can be diagnosed from the
// operation log instead of by reproducing it against the backend.
func responseExcerpt(content string) string {
	s := strings.TrimSpace(content)
	if len(s) > maxResponseExcerptBytes {
		cut := maxResponseExcerptBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = fmt.Sprintf("%s... (%d bytes total)", s[:cut], len(s))
	}
	return sanitizeReplyText(s)
}

// sanitizeReplyText makes model-written text safe to put in a log line or an
// operation record. logger.SanitizeLogValue escapes C0 controls and DEL only;
// on top of that this escapes the characters that rearrange or break a line
// without being C0: the Unicode format characters (category Cf, which includes
// the bidi embeddings and overrides U+202A-U+202E and the isolates
// U+2066-U+2069, plus zero-width characters) and the line and paragraph
// separators U+2028 and U+2029 (categories Zl/Zp, not Cf). The shared
// sanitizer is deliberately left alone; this is local to model output.
func sanitizeReplyText(s string) string {
	s = logger.SanitizeLogValue(s)
	if !strings.ContainsFunc(s, isInvisibleReplyRune) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case !isInvisibleReplyRune(r):
			b.WriteRune(r)
		case r > 0xFFFF:
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

func isInvisibleReplyRune(r rune) bool {
	return unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
}
