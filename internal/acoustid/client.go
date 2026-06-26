// file: internal/acoustid/client.go
// version: 1.2.0
// guid: 5d6e7f80-9a1b-2c3d-4e5f-607182931a2b
// last-edited: 2026-06-26

// Package acoustid is a thin client for the acoustid.org /v2/lookup API.
// We only need the smallest slice of the response — top-scoring
// MusicBrainz recording ID + score — so this is intentionally not a
// generated SDK.
//
// API docs: https://acoustid.org/webservice
//
// Rate limits: the free tier permits 3 requests/second per API key. The
// caller (acoustid.lookup-online op) is responsible for spacing requests
// — this client does not throttle internally.
package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrNoAPIKey is returned by Lookup when the env var ACOUSTID_API_KEY is unset.
var ErrNoAPIKey = errors.New("acoustid: ACOUSTID_API_KEY is not set")

// ErrRateLimited is returned when the API responds with HTTP 429 after
// the in-client retry budget is exhausted. Callers should react by
// backing off further (the lookup-online op widens its throttle for the
// remainder of the run).
var ErrRateLimited = errors.New("acoustid: rate-limited (HTTP 429)")

// retryDelays are the fallback wait intervals between automatic retries
// on 429 or 5xx when the server doesn't send Retry-After. Three attempts
// total (initial + 2 retries) — beyond that we surface ErrRateLimited so
// the caller can throttle the whole run.
var retryDelays = []time.Duration{2 * time.Second, 5 * time.Second}

// parseRetryAfter returns the duration encoded in a Retry-After header
// value. Per RFC 7231 the value is either delta-seconds (an integer) or
// an HTTP-date — we only honor the integer form because acoustid.org
// uses it. Caps at 60s so a misconfigured server can't stall an op.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	d := time.Duration(n) * time.Second
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

// LookupResult is the minimal subset of /v2/lookup's response we care about.
type LookupResult struct {
	// RecordingID is the top-scoring MusicBrainz recording ID, or "" when
	// the API has no match.
	RecordingID string
	// Score is the top match's similarity score (0..1).
	Score float64
	// Title is the recording title from the top result (populated when
	// Lookup is called with meta=recordings+titles, e.g. via LookupWithMeta).
	Title string
	// Artists contains artist names from the top result (populated when
	// Lookup is called with meta=recordings+artists).
	Artists []string
	// Status is the API's top-level status field (e.g. "ok", "error").
	Status string
	// Raw is the unmodified JSON response, for diagnostic logging. Empty
	// when the request failed before the response body arrived.
	Raw []byte
}

// Client wraps an *http.Client with the AcoustID API base URL + key.
type Client struct {
	HTTP    *http.Client
	BaseURL string // default "https://api.acoustid.org/v2/lookup"
	APIKey  string // from env ACOUSTID_API_KEY
}

// NewClient constructs a Client that reads the API key from env. The
// caller checks the returned key emptiness via ErrNoAPIKey in Lookup.
func NewClient(apiKey string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		BaseURL: "https://api.acoustid.org/v2/lookup",
		APIKey:  apiKey,
	}
}

// Lookup runs a single /v2/lookup against acoustid.org. The fingerprint
// must be the canonical chromaprint base64 form (with the 4-byte v1
// header) — internal/fingerprint.EncodeWholeFingerprint produces this
// from the raw fp bytes stored on BookFile.AcoustIDFingerprint.
//
// duration is the file's measured duration in seconds (integer); the API
// uses it to disambiguate fingerprints that overlap on short clips.
//
// Returns the top result and the raw JSON response. An API-level error
// (status != "ok") is returned as a non-nil error with the API's error
// message wrapped.
func (c *Client) Lookup(ctx context.Context, fingerprint string, durationSec int) (LookupResult, error) {
	if c.APIKey == "" {
		return LookupResult{}, ErrNoAPIKey
	}

	form := url.Values{}
	form.Set("client", c.APIKey)
	form.Set("duration", strconv.Itoa(durationSec))
	form.Set("fingerprint", fingerprint)
	form.Set("meta", "recordings recordings+titles recordings+artists")

	// Retry loop: on 429 or 5xx, honor Retry-After (or fall back to
	// retryDelays). Budget = 1 initial + len(retryDelays) attempts.
	var (
		resp        *http.Response
		body        []byte
		lastStatus  int
		rateLimited bool
	)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL,
			strings.NewReader(form.Encode()))
		if err != nil {
			return LookupResult{}, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		var doErr error
		resp, doErr = c.HTTP.Do(req)
		if doErr != nil {
			return LookupResult{}, doErr
		}

		body, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return LookupResult{}, err
		}
		lastStatus = resp.StatusCode

		retryable := lastStatus == http.StatusTooManyRequests || lastStatus/100 == 5
		if !retryable {
			break
		}
		rateLimited = rateLimited || lastStatus == http.StatusTooManyRequests
		if attempt >= len(retryDelays) {
			break
		}
		wait := parseRetryAfter(resp.Header.Get("Retry-After"))
		if wait == 0 {
			wait = retryDelays[attempt]
		}
		select {
		case <-ctx.Done():
			return LookupResult{}, ctx.Err()
		case <-time.After(wait):
		}
	}

	if lastStatus/100 != 2 {
		if rateLimited {
			return LookupResult{Raw: body}, ErrRateLimited
		}
		return LookupResult{Raw: body}, fmt.Errorf("acoustid: HTTP %d: %s", lastStatus, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Status string `json:"status"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
		Results []struct {
			ID         string  `json:"id"`
			Score      float64 `json:"score"`
			Recordings []struct {
				ID      string `json:"id"`
				Title   string `json:"title"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"artists,omitempty"`
			} `json:"recordings,omitempty"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return LookupResult{Raw: body}, fmt.Errorf("acoustid: decode response: %w", err)
	}
	if parsed.Error != nil {
		return LookupResult{Raw: body, Status: parsed.Status},
			fmt.Errorf("acoustid: API error %d: %s", parsed.Error.Code, parsed.Error.Message)
	}

	out := LookupResult{Status: parsed.Status, Raw: body}
	for _, r := range parsed.Results {
		if r.Score <= out.Score {
			continue
		}
		out.Score = r.Score
		if len(r.Recordings) > 0 {
			rec := r.Recordings[0]
			out.RecordingID = rec.ID
			out.Title = rec.Title
			for _, a := range rec.Artists {
				if a.Name != "" {
					out.Artists = append(out.Artists, a.Name)
				}
			}
		} else {
			out.RecordingID = r.ID
		}
	}
	return out, nil
}
