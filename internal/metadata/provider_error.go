// file: internal/metadata/provider_error.go
// version: 1.2.0
// guid: fa58b130-5ad4-4cfc-b726-728a75522364
// last-edited: 2026-09-03

package metadata

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// providerErrorBodyLimit bounds how much of a failing response we keep. Enough
// to carry a quota message, far short of a full error page.
const providerErrorBodyLimit = 512

// ProviderStatusError is a non-2xx response from a metadata provider, carrying
// enough of the body to tell one refusal from another.
//
// The body is the point. Google Books answers a burst overrun and an exhausted
// DAILY quota with the same 429, and only the body distinguishes them:
//
//	Quota exceeded for quota metric 'Queries' and limit 'Queries per day'
//
// Before this type the providers returned fmt.Errorf("... returned status %d")
// and threw the body away, so nothing downstream could tell a 15-minute
// problem from a 24-hour one. That is why the circuit breaker re-probed an
// exhausted daily quota every 30 seconds for hours.
type ProviderStatusError struct {
	Provider   string
	Status     int
	Body       string
	RetryAfter time.Duration // from Retry-After, 0 when absent

	// BodyUnreadable marks a response whose body could not be read, so a caller
	// can tell "the provider did not mention a quota" from "we never saw what
	// the provider said".
	BodyUnreadable bool
}

func (e *ProviderStatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s returned status %d", e.Provider, e.Status)
	}
	return fmt.Sprintf("%s returned status %d: %s", e.Provider, e.Status, e.Body)
}

// StatusError builds a ProviderStatusError from a failing response, reading a
// bounded prefix of the body. Callers must not use resp.Body afterwards.
func StatusError(provider string, resp *http.Response) *ProviderStatusError {
	e := &ProviderStatusError{Provider: provider, Status: resp.StatusCode}
	if resp.Body != nil {
		b, err := io.ReadAll(io.LimitReader(resp.Body, providerErrorBodyLimit))
		// Record a read failure instead of leaving Body empty.
		//
		// The classifier decides a 4-hour hold or a 15-minute one by looking for
		// a quota phrase in this body. Swallowing the read error made "the body
		// said nothing about a quota" indistinguishable from "we never saw the
		// body", so a truncated 429 silently became a 15-minute hold on an
		// exhausted daily quota -- and the run resumed hammering a quarter of an
		// hour later. A classification made on absent evidence must not look
		// like one made on present evidence.
		switch {
		case err != nil:
			e.BodyUnreadable = true
			e.Body = "<body unreadable: " + err.Error() + ">"
		default:
			e.Body = strings.TrimSpace(strings.Join(strings.Fields(string(b)), " "))
		}
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(strings.TrimSpace(v)); err == nil {
			if d := time.Until(t); d > 0 {
				e.RetryAfter = d
			}
		}
	}
	return e
}

// graphQLAuthMarkers are the phrases a GraphQL API uses to refuse on
// credentials, as opposed to a field or schema error.
var graphQLAuthMarkers = []string{
	"unauthorized", "unauthenticated", "invalid token", "invalid api key",
	"forbidden", "permission denied", "not authorized",
}

// GraphQLRefusal turns a GraphQL error message into a ProviderStatusError when
// the message names a refusal the throttle should act on, and returns a plain
// error otherwise.
//
// GraphQL answers 200 for everything, including "your key is wrong" and "you are
// going too fast". Without this, a provider reached over GraphQL can never be
// throttled: the classifier sees a bare fmt.Errorf and declines it. Mapping to a
// synthetic status is what lets one classifier serve both transports.
//
// A schema or field error stays a plain error on purpose -- it is a bug in our
// query, not a refusal, and throttling the provider for it would take a healthy
// source out of the chain until someone noticed.
func GraphQLRefusal(provider, message string) error {
	switch {
	case bodyNames(message, dailyQuotaMarkers), bodyNames(message, rateLimitMarkers):
		return &ProviderStatusError{Provider: provider, Status: 429, Body: message}
	case bodyNames(message, graphQLAuthMarkers):
		return &ProviderStatusError{Provider: provider, Status: 401, Body: message}
	default:
		return fmt.Errorf("%s GraphQL error: %s", provider, message)
	}
}
