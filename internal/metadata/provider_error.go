// file: internal/metadata/provider_error.go
// version: 1.0.0
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
		if b, err := io.ReadAll(io.LimitReader(resp.Body, providerErrorBodyLimit)); err == nil {
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
