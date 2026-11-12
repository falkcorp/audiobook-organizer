// file: core/integrations/providerhttp/classify.go
// version: 1.0.1
// guid: c73c6078-09b3-4cb0-943f-440eb98114f5
// last-edited: 2026-11-12

package providerhttp

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// Classify maps a response or a transport error to the taxonomy, reading the
// BODY BEFORE THE STATUS: Google returns both 429 and 403 for quota and rate
// limiting, so a 403 whose body names a daily quota is ClassQuota, and a 403
// that names a rate limit is ClassRateLimited, not an auth failure
// (throttle.go:168-195, kept). ctx is the CALLER's: a done ctx yields
// ClassCanceled for any error.
//
// It classifies failures. A response below 400 with a nil err has nothing to
// classify and yields ClassUnknown, which callers read as success.
func Classify(ctx context.Context, resp *http.Response, body []byte, err error) integrations.Class {
	if err != nil {
		return classifyErr(ctx, err)
	}
	if resp == nil {
		return integrations.ClassUnknown
	}
	return classifyStatus(resp.StatusCode, body)
}

// dailyQuotaMarkers are the phrases providers use for an exhausted per-day
// allowance, as opposed to a burst limit (throttle.go:71-77). insufficient_quota
// and credit_balance_exhausted are the AI providers' codes for a spent balance:
// a 429 like a burst limit that does not clear by waiting. They are the words
// integrations/ai shares between its router and its adapters (quotaMarkers), so
// the gate holds the same endpoint for the same reason the router benches it.
func dailyQuotaMarkers() []string {
	return []string{"per day", "daily limit", "dailylimitexceeded", "quota exceeded for quota metric", "quotaexceeded",
		"insufficient_quota", "credit_balance_exhausted"}
}

// rateLimitMarkers are the machine-readable reasons for a BURST overrun
// (throttle.go:86-91): Google delivers rate limiting as 403 at least as often
// as 429.
func rateLimitMarkers() []string {
	return []string{"ratelimitexceeded", "userratelimitexceeded", "rate limit exceeded", "too many requests"}
}

func bodyNames(body []byte, markers []string) bool {
	low := strings.ToLower(string(body))
	for _, m := range markers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

func classifyStatus(status int, body []byte) integrations.Class {
	if status < 400 {
		return integrations.ClassUnknown
	}
	if status == http.StatusTooManyRequests || status == http.StatusForbidden {
		if bodyNames(body, dailyQuotaMarkers()) {
			return integrations.ClassQuota
		}
		if status == http.StatusTooManyRequests || bodyNames(body, rateLimitMarkers()) {
			return integrations.ClassRateLimited
		}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return integrations.ClassAuth
	case status == http.StatusRequestTimeout || status >= 500:
		return integrations.ClassTransient
	case status == http.StatusNotFound || status == http.StatusGone:
		return integrations.ClassNotFound
	}
	// Any other 4xx is about the query, not the remote.
	return integrations.ClassInvalid
}

func classifyErr(ctx context.Context, err error) integrations.Class {
	switch {
	case errors.Is(err, ErrBlockedAddress), errors.Is(err, ErrBlockedScheme), errors.Is(err, ErrInvalidURL),
		errors.Is(err, ErrTooManyRedirects):
		if ctx != nil && ctx.Err() != nil {
			return integrations.ClassCanceled
		}
		return integrations.ClassRefusedLocal
	case errors.Is(err, ErrResponseTooLarge):
		return integrations.ClassBadOutput
	}
	return integrations.ClassOf(ctx, err)
}

// retryAfterOf parses a Retry-After header, in either form it may take (a
// delta in seconds, an HTTP date); 0 when absent or unusable. It does not
// clamp.
func retryAfterOf(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(min(secs, 1_000_000_000)) * time.Second // a cap far above any clamp, so the product cannot overflow
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
