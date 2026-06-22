// file: internal/ai/openai_parser.go
// version: 13.6.0
// guid: 9a0b1c2d-3e4f-5a6b-7c8d-9e0f1a2b3c4d
// last-edited: 2026-06-22

package ai

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
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
}

// Default cache TTL for AI responses (24 hours — metadata doesn't change often)
const aiResponseCacheTTL = 24 * time.Hour

// defaultModel is the fallback when cfg is nil or the per-feature field is empty.
const defaultModel = "gpt-5-mini"

// filenameParseModel returns the configured model for filename/audiobook parsing.
func (p *OpenAIParser) filenameParseModel() string {
	if p.cfg != nil && p.cfg.FilenameParseModel != "" {
		return p.cfg.FilenameParseModel
	}
	return defaultModel
}

// coverArtModel returns the configured model for cover-art parsing.
func (p *OpenAIParser) coverArtModel() string {
	if p.cfg != nil && p.cfg.CoverArtModel != "" {
		return p.cfg.CoverArtModel
	}
	return defaultModel
}

// metadataReviewModel returns the configured model for metadata review operations.
func (p *OpenAIParser) metadataReviewModel() string {
	if p.cfg != nil && p.cfg.MetadataReviewModel != "" {
		return p.cfg.MetadataReviewModel
	}
	return defaultModel
}

// NewOpenAIParser creates a new OpenAI parser.
// cfg may be nil; when provided, per-feature model fields override the default.
func NewOpenAIParser(cfg *config.Config, apiKey string, enabled bool) *OpenAIParser {
	if !enabled || apiKey == "" {
		return &OpenAIParser{enabled: false, cfg: cfg}
	}

	clientOptions := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		clientOptions = append(clientOptions, option.WithBaseURL(baseURL))
	}

	client := openai.NewClient(clientOptions...)

	return &OpenAIParser{
		client:        &client,
		cfg:           cfg,
		maxRetries:    2,
		enabled:       true,
		responseCache: cache.New[*ParsedMetadata]("ai_response", aiResponseCacheTTL),
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
		Model:               shared.ChatModel(p.filenameParseModel()), // filename parsing uses FilenameParseModel
		MaxCompletionTokens: param.NewOpt[int64](500),
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
		Model:               shared.ChatModel(p.filenameParseModel()), // audiobook context parsing uses FilenameParseModel
		MaxCompletionTokens: param.NewOpt[int64](500),
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

	// Limit batch size
	const maxBatchSize = 20
	if len(filenames) > maxBatchSize {
		filenames = filenames[:maxBatchSize]
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

	userPrompt := "Parse these audiobook filenames:\n\n"
	for i, filename := range filenames {
		userPrompt += fmt.Sprintf("%d. %s\n", i+1, filename)
	}

	jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()

	var content string
	if err := DoWithRetry(ctx, p.maxRetries+1, 2*time.Second, func() error {
		completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(systemPrompt),
				openai.UserMessage(userPrompt),
			},
			Model:               shared.ChatModel(p.filenameParseModel()), // batch filename parsing uses FilenameParseModel
			MaxCompletionTokens: param.NewOpt[int64](2000),
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
	return parseBatchMetadataFromJSON(content)
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
		Model:               shared.ChatModel(p.coverArtModel()), // cover art parsing uses CoverArtModel
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

// parseMetadataFromJSON parses a single metadata object from JSON string
func parseMetadataFromJSON(content string) (*ParsedMetadata, error) {
	var metadata ParsedMetadata
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}
	return &metadata, nil
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
			Model:               shared.ChatModel(p.metadataReviewModel()), // author dedup review uses MetadataReviewModel
			MaxCompletionTokens: param.NewOpt[int64](32000),
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
			Model:               shared.ChatModel(p.metadataReviewModel()), // author discovery review uses MetadataReviewModel
			MaxCompletionTokens: param.NewOpt[int64](16000),
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

// parseBatchMetadataFromJSON parses batch results from JSON.
// Accepts either {"results": [...]} or a bare array [...].
func parseBatchMetadataFromJSON(content string) ([]*ParsedMetadata, error) {
	// Try wrapped format first (JSON Object mode returns objects)
	var wrapped struct {
		Results []*ParsedMetadata `json:"results"`
	}
	if err := json.Unmarshal([]byte(content), &wrapped); err == nil && len(wrapped.Results) > 0 {
		return wrapped.Results, nil
	}

	// Fall back to bare array
	var results []*ParsedMetadata
	if err := json.Unmarshal([]byte(content), &results); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}
	return results, nil
}
