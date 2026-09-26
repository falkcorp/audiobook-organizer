// file: internal/ai/cover_text.go
// version: 1.1.0
// guid: 9a4c2e71-3b8d-4f16-a5e0-8d7c1b6f2e93
// last-edited: 2026-09-26

package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/covertext"
)

// Cover text reading (llm.cover_art_vision), routed through the ai_endpoints
// pool.
//
// Owner decision 5 ("cover-art vision is cloud only") was REVERSED on
// 2026-09-26: vision runs on the local LLM nodes. Those nodes are a pool, so
// nothing here names a host. An endpoint serves this capability when its row
// ticks llm.cover_art_vision AND declares the "vision" feature; the model sent
// is the row's capability_models["llm.cover_art_vision"] (falling back to its
// chat_model). A cloud row is used only when the configuration ticks it, and
// then only as a FALLBACK: local rows are always tried first, whatever the
// priorities say (see ReadCoverText).
//
// The measured local model (a 7B vision model on Ollama's OpenAI-compatible
// /v1) takes 15-33 s per cover including a cold load, and wraps its JSON in a
// ```json fence. Hence the long per-attempt timeout and the fence stripping.

// CoverTextAttemptTimeout bounds one attempt on one endpoint. It starts after
// the endpoint slot is acquired (aidispatch.WithAttemptTimeout), so a slow
// first node does not eat the budget of the node it fails over to.
const CoverTextAttemptTimeout = 180 * time.Second

// ErrCoverTextRoutingOff is returned when ai_endpoints_routing is off. There
// is no legacy path for cover text: the legacy vision call is cloud-only.
var ErrCoverTextRoutingOff = errors.New("cover text needs ai_endpoints_routing on: llm.cover_art_vision is served only by pool rows")

// errNoCoverTextChoices is a reply with no choices: the endpoint answered, so
// it is a quality failure, not an endpoint failure.
var errNoCoverTextChoices = errors.New("cover text: no choices in reply")

const coverTextSystemPrompt = `You read the text printed on audiobook cover images.

Transcribe ONLY text that is visible on the image. Never guess or add anything that is not printed.

Reply with ONE JSON object and nothing else, using these keys (omit a key when that text is not on the cover):
{
  "title": "main title",
  "subtitle": "subtitle",
  "authors": ["each author credited"],
  "narrators": ["each narrator credited, e.g. 'Read by X' or 'Narrated by X'"],
  "series": "series name",
  "series_number": "number or label of this book in the series, as printed",
  "publisher": "publisher or imprint",
  "other_text": ["every other line of visible text, e.g. taglines, award banners, 'Unabridged'"]
}`

// CoverTextResult is one successful read.
type CoverTextResult struct {
	Text       *covertext.Text
	Raw        string
	Model      string
	EndpointID string
}

// RoutedCoverTextReader reads cover text on the best capable pool endpoint.
type RoutedCoverTextReader struct {
	pool    *PoolSource
	timeout time.Duration

	mu      sync.Mutex
	parsers map[string]*OpenAIParser
}

// NewRoutedCoverTextReader returns a reader routed through pool.
func NewRoutedCoverTextReader(pool *PoolSource) *RoutedCoverTextReader {
	return &RoutedCoverTextReader{pool: pool, timeout: CoverTextAttemptTimeout, parsers: map[string]*OpenAIParser{}}
}

// parserFor returns the cached single-endpoint client for one target, built
// with the target's model (the row's capability_models override or its
// chat_model) and no retries, as RoutedFilenameParser.parserFor does.
func (r *RoutedCoverTextReader) parserFor(t aidispatch.Target) (*OpenAIParser, error) {
	key, err := r.pool.apiKeyFor(t.Endpoint)
	if err != nil {
		return nil, err
	}
	cacheKey := t.Endpoint.ID + "\x00" + t.Endpoint.URL + "\x00" + t.Model + "\x00" + key
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.parsers[cacheKey]; p != nil {
		return p, nil
	}
	p := newRoutedEndpointParser(key, t.Endpoint.URL, t.Model)
	r.parsers[cacheKey] = p
	return p, nil
}

// dispatcher builds the dispatcher for one pass: local rows only, or cloud
// rows only (the fallback pass never re-tries a local row).
func (r *RoutedCoverTextReader) dispatcher(local bool) *aidispatch.Dispatcher {
	opts := []aidispatch.Option{aidispatch.WithAttemptTimeout(aidispatch.LLMCoverArtVision, r.timeout)}
	if local {
		opts = append(opts, aidispatch.WithLocalOnly())
	} else {
		opts = append(opts, aidispatch.WithCloudOnly())
	}
	return r.pool.dispatcher(opts...)
}

// Capacity is how many reads the pool can run at once right now, 0 when
// routing is off or no endpoint serves llm.cover_art_vision.
func (r *RoutedCoverTextReader) Capacity() int {
	if r == nil || !r.pool.IsActive() {
		return 0
	}
	// Local rows are tried first (ReadCoverText), so size to them; the
	// cloud rows' slots count only when no local row can serve at all.
	if c := r.dispatcher(true).Capacity(aidispatch.LLMCoverArtVision); c > 0 {
		return c
	}
	return r.dispatcher(false).Capacity(aidispatch.LLMCoverArtVision)
}

// ReadCoverText sends one image and returns what the model read. A reply that arrived
// but could not be parsed is a QUALITY failure (not retried on a peer).
func (r *RoutedCoverTextReader) ReadCoverText(ctx context.Context, image []byte, mimeType string) (*CoverTextResult, error) {
	if r == nil || !r.pool.IsActive() {
		return nil, ErrCoverTextRoutingOff
	}
	if len(image) == 0 {
		return nil, errors.New("cover text: empty image")
	}
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image)
	// Local first, ALWAYS, whatever the rows' priorities say: a migrated
	// OpenAI row can carry a better priority than the local row
	// (llm_mode openai-fallback-local) and is pre-ticked for vision, so
	// priority alone would send covers to the cloud first. The cloud is only
	// a FALLBACK: tried when no local row can serve or every local attempt
	// failed at the endpoint, and that pass is cloud-only so a hung local
	// node is not waited on twice. A reply that arrived but did not parse
	// (Quality) is never re-asked of the cloud. A busy local pool does not
	// fall back either: Call waits for a slot and errors only on ctx.
	res, err := aidispatch.Call(ctx, r.dispatcher(true), aidispatch.LLMCoverArtVision, r.coverTextAttempt(dataURL))
	if err == nil || ctx.Err() != nil {
		return res, err
	}
	var quality *aidispatch.QualityError
	if errors.As(err, &quality) {
		return nil, err
	}
	return aidispatch.Call(ctx, r.dispatcher(false), aidispatch.LLMCoverArtVision, r.coverTextAttempt(dataURL))
}

// coverTextAttempt is one request to one chosen endpoint.
func (r *RoutedCoverTextReader) coverTextAttempt(dataURL string) func(context.Context, aidispatch.Target) (*CoverTextResult, error) {
	return func(ctx context.Context, t aidispatch.Target) (*CoverTextResult, error) {
		p, err := r.parserFor(t)
		if err != nil {
			return nil, err
		}
		raw, err := p.readCoverTextImage(ctx, dataURL)
		if errors.Is(err, errNoCoverTextChoices) {
			return nil, aidispatch.Quality(fmt.Errorf("cover text on %s: %w", t.Endpoint.ID, err))
		}
		if err != nil {
			return nil, fmt.Errorf("cover text on %s: %w", t.Endpoint.ID, err)
		}
		text, err := ParseCoverTextReply(raw)
		if err != nil {
			return nil, aidispatch.Quality(err)
		}
		return &CoverTextResult{Text: text, Raw: raw, Model: t.Model, EndpointID: t.Endpoint.ID}, nil
	}
}

// stripJSONFence removes a surrounding Markdown code fence (```json ... ```)
// and any prose before the first "{" or after the last "}".
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if nl := strings.IndexByte(s, '\n'); nl >= 0 && !strings.Contains(s[:nl], "{") {
			s = s[nl+1:] // drop the info string ("json")
		}
		if end := strings.LastIndex(s, "```"); end >= 0 {
			s = s[:end]
		}
	}
	if i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}'); i >= 0 && j > i {
		s = s[i : j+1]
	}
	return strings.TrimSpace(s)
}

// coverTextReply is the wire form. Lists and the series number are decoded
// leniently: a model may send one string where a list is asked for, or a
// number for series_number.
type coverTextReply struct {
	Title        string          `json:"title"`
	Subtitle     string          `json:"subtitle"`
	Authors      json.RawMessage `json:"authors"`
	Author       json.RawMessage `json:"author"`
	Narrators    json.RawMessage `json:"narrators"`
	Narrator     json.RawMessage `json:"narrator"`
	Series       string          `json:"series"`
	SeriesNumber json.RawMessage `json:"series_number"`
	Publisher    string          `json:"publisher"`
	OtherText    json.RawMessage `json:"other_text"`
}

func lenientStrings(raws ...json.RawMessage) ([]string, error) {
	var out []string
	for _, raw := range raws {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var list []string
		if err := json.Unmarshal(raw, &list); err == nil {
			out = append(out, list...)
			continue
		}
		var one string
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	var clean []string
	for _, s := range out {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	return clean, nil
}

func lenientScalar(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s), nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", err
	}
	return strconv.FormatFloat(f, 'f', -1, 64), nil
}

// ParseCoverTextReply parses the model's reply into covertext.Text. An empty
// object is a valid "nothing legible" read; anything that is not a JSON
// object after fence stripping is an error carrying a sanitized excerpt.
func ParseCoverTextReply(content string) (*covertext.Text, error) {
	body := stripJSONFence(content)
	var w coverTextReply
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		return nil, newReplyParseError(fmt.Errorf("cover text reply is not a JSON object: %w", err), content)
	}
	authors, err := lenientStrings(w.Authors, w.Author)
	if err != nil {
		return nil, newReplyParseError(fmt.Errorf("cover text authors: %w", err), content)
	}
	narrators, err := lenientStrings(w.Narrators, w.Narrator)
	if err != nil {
		return nil, newReplyParseError(fmt.Errorf("cover text narrators: %w", err), content)
	}
	other, err := lenientStrings(w.OtherText)
	if err != nil {
		return nil, newReplyParseError(fmt.Errorf("cover text other_text: %w", err), content)
	}
	num, err := lenientScalar(w.SeriesNumber)
	if err != nil {
		return nil, newReplyParseError(fmt.Errorf("cover text series_number: %w", err), content)
	}
	return &covertext.Text{
		Title:        strings.TrimSpace(w.Title),
		Subtitle:     strings.TrimSpace(w.Subtitle),
		Authors:      authors,
		Narrators:    narrators,
		Series:       strings.TrimSpace(w.Series),
		SeriesNumber: num,
		Publisher:    strings.TrimSpace(w.Publisher),
		OtherText:    other,
	}, nil
}
