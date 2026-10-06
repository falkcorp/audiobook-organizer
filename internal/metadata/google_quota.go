// file: internal/metadata/google_quota.go
// version: 1.0.0
// guid: 78340ad6-8298-474b-84fe-a0dd8510ccef
// last-edited: 2026-10-06

package metadata

import (
	"context"
	"errors"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/dailyquota"
)

// Google Books' shared daily lookup budget (owner decision 2026-10-06): ONE
// counter for every Google Books caller, 1,000 a day (the key's quota),
// background lookups stopping at 800 so 200 stay reserved for a person
// waiting on the search dialog. Enforced in Google Books' HTTP transport
// (providerhttp, dailyquota.ReserveFor), so every client of the provider --
// the configured chain, the bulk fetch's own client, the test-connection
// handler's, any added later -- is counted without doing anything.
//
// An in-memory budget is installed at package init, so the count is enforced
// even in a process that never attaches a store; the serve path replaces it
// with a persisted one (AttachGoogleBooksBudgetStore) so a restart resumes
// the day's count.

// DefaultGoogleBooksDailyLimit is the shared counter's total when none is
// configured: the API key's 1,000 queries/day.
const DefaultGoogleBooksDailyLimit = 1000

// DefaultGoogleBooksBackgroundDailyLimit is where background lookups stop
// when no cap is configured.
const DefaultGoogleBooksBackgroundDailyLimit = 800

// GoogleBooksBudgetLimits returns the configured caps
// (config.GoogleBooksDailyLimit, GoogleBooksBackgroundDailyLimit, and the
// legacy GoogleBooksFallbackDailyLimit, which may only lower the background
// cap). Read on every reservation.
func GoogleBooksBudgetLimits() dailyquota.Limits {
	total := config.AppConfig.GoogleBooksDailyLimit
	switch {
	case total == 0:
		total = DefaultGoogleBooksDailyLimit
	case total < 0:
		total = 0
	}
	bg := config.AppConfig.GoogleBooksBackgroundDailyLimit
	switch {
	case bg == 0:
		bg = DefaultGoogleBooksBackgroundDailyLimit
		if legacy := config.AppConfig.GoogleBooksFallbackDailyLimit; legacy > 0 {
			bg = min(bg, legacy)
		}
	case bg < 0:
		bg = 0
	}
	return dailyquota.Limits{Total: total, Background: min(bg, total)}
}

func init() {
	dailyquota.Install(dailyquota.New(nil, SourceIDGoogleBooks, GoogleBooksBudgetLimits))
}

// GoogleBooksBudget returns the process's Google Books daily budget.
func GoogleBooksBudget() *dailyquota.DailyBudget { return dailyquota.For(SourceIDGoogleBooks) }

// AttachGoogleBooksBudgetStore replaces the process's Google Books budget with
// one persisted through store, so a restart resumes the day's count. Call it
// only from the process-scoped serve path, never from NewServer (see
// server.AttachProviderThrottleStore for why: tests close their stores).
func AttachGoogleBooksBudgetStore(store dailyquota.RawKV) {
	dailyquota.Install(dailyquota.New(store, SourceIDGoogleBooks, GoogleBooksBudgetLimits))
}

// WithInteractiveQuota marks ctx's provider lookups as made for a person
// waiting on them (dailyquota.Interactive): they may use the daily quota a
// background lookup leaves reserved.
func WithInteractiveQuota(ctx context.Context) context.Context {
	return dailyquota.WithInteractive(ctx)
}

// IsDailyBudgetSpent reports whether err is a daily-budget refusal: a request
// never sent, which says nothing about the provider.
func IsDailyBudgetSpent(err error) bool { return errors.Is(err, dailyquota.ErrDailyBudgetSpent) }
