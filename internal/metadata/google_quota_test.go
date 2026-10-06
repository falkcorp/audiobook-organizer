// file: internal/metadata/google_quota_test.go
// version: 1.1.0
// guid: d79d70eb-b6e8-442b-8889-7f4d6132ff49
// last-edited: 2026-10-06

package metadata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/dailyquota"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
)

// googleQuotaHarness installs a fresh in-memory Google Books budget with the
// configured (default) limits, lifts the request-rate limiter so a full day's
// quota runs in a second, and serves Google's search endpoint, counting the
// requests that actually reach it.
func googleQuotaHarness(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	restore := dailyquota.Install(dailyquota.New(nil, SourceIDGoogleBooks, GoogleBooksBudgetLimits))
	t.Cleanup(restore)
	providerhttp.SetLimits(SourceIDGoogleBooks, providerhttp.Limits{RPS: 1e6, Burst: 1e6, MaxRetries: 0, Timeout: 5 * time.Second})
	providerhttp.ResetProvider(SourceIDGoogleBooks)
	t.Cleanup(func() {
		providerhttp.SetLimits(SourceIDGoogleBooks, providerhttp.BuiltinLimitsFor(SourceIDGoogleBooks))
		providerhttp.ResetProvider(SourceIDGoogleBooks)
	})
	t.Cleanup(ResetThrottlesForTesting)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalItems":0,"items":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The defaults are the owner's numbers: one 1,000/day counter, background
// stopping at 800.
func TestGoogleBooksBudgetLimits_Defaults(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.GoogleBooksDailyLimit, config.AppConfig.GoogleBooksBackgroundDailyLimit = 0, 0
	config.AppConfig.GoogleBooksFallbackDailyLimit = 0
	if got := GoogleBooksBudgetLimits(); got != (dailyquota.Limits{Total: 1000, Background: 800}) {
		t.Fatalf("defaults = %+v, want {1000 800}", got)
	}
	// The pre-2026-10-06 fallback limit may lower the background cap, never
	// raise it.
	config.AppConfig.GoogleBooksFallbackDailyLimit = 950
	if got := GoogleBooksBudgetLimits().Background; got != 800 {
		t.Fatalf("legacy 950 raised the background cap to %d", got)
	}
	config.AppConfig.GoogleBooksFallbackDailyLimit = 300
	if got := GoogleBooksBudgetLimits().Background; got != 300 {
		t.Fatalf("legacy 300: background cap %d, want 300", got)
	}
}

// A bare Google Books client -- the kind a new caller would construct -- is
// counted without doing anything: background requests stop at 800 (request
// 801 never reaches Google), interactive requests go on to 1,000 on the SAME
// counter, and the 800 background refusals neither open the circuit breaker
// nor install a throttle hold that would lock the interactive lookups out.
func TestGoogleBooksClient_SharedBudgetCannotBeBypassed(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.GoogleBooksDailyLimit, config.AppConfig.GoogleBooksBackgroundDailyLimit = 0, 0
	config.AppConfig.GoogleBooksFallbackDailyLimit = 0
	srv, hits := googleQuotaHarness(t)

	// Two independent clients (the chain's and, say, the bulk fetch's own):
	// one counter.
	bg1 := NewChainSource(NewGoogleBooksClientWithBaseURL(srv.URL))
	bg2 := NewGoogleBooksClientWithBaseURL(srv.URL)
	ctx := context.Background()
	for i := range 800 {
		c := MetadataSource(bg1)
		if i%2 == 1 {
			c = bg2
		}
		if _, err := c.SearchByTitle(ctx, "Some Title"); err != nil {
			t.Fatalf("background lookup %d: %v", i+1, err)
		}
	}
	for i := range 6 { // more than the breaker's threshold of 5
		_, err := bg1.SearchByTitle(ctx, "Some Title")
		if !IsDailyBudgetSpent(err) {
			t.Fatalf("background lookup %d past 800: err = %v, want a daily-budget refusal", 801+i, err)
		}
	}
	if got := hits.Load(); got != 800 {
		t.Fatalf("Google received %d requests, want 800 (a refused request is never sent)", got)
	}
	if _, held := DefaultThrottleRegistry().Get(SourceIDGoogleBooks); held {
		t.Fatal("budget refusals installed a throttle hold on Google Books")
	}

	ictx := WithInteractiveQuota(ctx)
	for i := range 200 {
		if _, err := bg1.SearchByTitle(ictx, "Some Title"); err != nil {
			t.Fatalf("interactive lookup %d: %v", i+1, err)
		}
	}
	if _, err := bg2.SearchByTitle(ictx, "Some Title"); !IsDailyBudgetSpent(err) {
		t.Fatalf("interactive lookup 1,001: err = %v, want refused", err)
	}
	if got := hits.Load(); got != 1000 {
		t.Fatalf("Google received %d requests, want 1000", got)
	}
	if got := GoogleBooksBudget().Used(); got != 1000 {
		t.Fatalf("budget used = %d, want 1000", got)
	}
}

// A budget refusal reaches callers as net/http's *url.Error -- a net.Error --
// and must still not classify as transport trouble: a hold would lock out
// every caller, the interactive ones the budget reserves quota for included.
func TestClassifyProviderError_BudgetRefusalIsNotAFailure(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://example.invalid", Err: fmt.Errorf("providerhttp google-books: %w",
		&dailyquota.SpentError{Provider: SourceIDGoogleBooks, Used: 800, Limit: 800})}
	if reason, hold, ok := ClassifyProviderError(err); ok {
		t.Fatalf("budget refusal classified as %s (hold %s), want no hold", reason, hold)
	}
}

// N2: a budget whose store fails refuses without a throttle hold.
func TestClassifyProviderError_BudgetStoreFailureIsNotAFailure(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://example.invalid", Err: fmt.Errorf("providerhttp google-books: %w",
		fmt.Errorf("%w: save google-books daily budget: disk full", dailyquota.ErrBudgetUnavailable))}
	if reason, hold, ok := ClassifyProviderError(err); ok {
		t.Fatalf("budget store failure classified as %s (hold %s), want no hold", reason, hold)
	}
}
