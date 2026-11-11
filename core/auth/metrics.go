// file: core/auth/metrics.go
// version: 1.1.0
// guid: 6b1f3d97-e842-4a05-9c7d-0e5a2b8c4f61
// last-edited: 2026-11-11

package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// Metrics (05-authn §9): the "auth" core scope, mediacore.auth. Deps.Metrics
// nil records nothing. Every series whose labels are a closed set is exported
// from zero, so a dashboard sees the family before the first event; the
// method label of mediacore_auth_attempts_total also carries an extension's
// own "ext:<name>" method, created on first use.
//
// Not recorded here, and so not in testdata/series.golden: the two password
// histograms (mediacore_auth_password_hash_seconds and
// _queue_wait_seconds, which need an observer in auth/password's Hasher).
// mediacore_auth_extension_errors_total is recorded (an extension reports an
// outage as ErrExtensionUnavailable, F4-03) but its extension label is open,
// so it is created on first use and is not in the golden either. The seam
// notes say so.

// Label values of the closed sets.
const (
	outcomeSuccess  = "success"
	outcomeInvalid  = "invalid"
	outcomeExpired  = "expired"
	outcomeRevoked  = "revoked"
	outcomeInactive = "inactive"
	outcomeThrottle = "throttled"
	outcomeError    = "error"

	surfaceLogin     = "login"
	surfaceBootstrap = "bootstrap"
	surfaceSetup     = "setup"
	surfaceInvite    = "invite"
	surfaceTempLogin = "temp_login"
	surfaceOIDC      = "oidc"

	reasonInvalEpoch    = "epoch"
	reasonInvalInactive = "inactive"
	reasonInvalRevoked  = "revoked"
	reasonInvalExpired  = "expired"
	reasonInvalOther    = "other"
)

// gaugeTTL is how long the two active-count gauges reuse one store scan: a
// scrape walks every user's sessions and every key, so a scrape every few
// seconds must not.
const gaugeTTL = 30 * time.Second

// authMetrics is the service's instruments; every method is a no-op on nil.
type authMetrics struct {
	attempts      metric.Int64Counter
	throttledCtr  metric.Int64Counter
	redemptions   metric.Int64Counter
	invalidations metric.Int64Counter
	extErrors     metric.Int64Counter

	mu       sync.Mutex
	scanned  time.Time
	sessions int64
	keys     [2]int64 // native, imported
}

func newAuthMetrics(sc *metrics.Scope, s *Service) (*authMetrics, error) {
	if sc == nil {
		return nil, nil
	}
	m, prefix := sc.Meter(), sc.Prefix()
	am := &authMetrics{}
	var err error
	if am.attempts, err = m.Int64Counter(prefix+".attempts",
		metric.WithDescription("Stage 12 authentication attempts, by method and outcome (05-authn §9).")); err != nil {
		return nil, fmt.Errorf("auth: attempts counter: %w", err)
	}
	if am.throttledCtr, err = m.Int64Counter(prefix+".throttled",
		metric.WithDescription("Requests refused by a throttle or login ceiling, by surface.")); err != nil {
		return nil, fmt.Errorf("auth: throttled counter: %w", err)
	}
	if am.redemptions, err = m.Int64Counter(prefix+".bootstrap_redemptions",
		metric.WithDescription("Bootstrap token redemptions, by outcome.")); err != nil {
		return nil, fmt.Errorf("auth: bootstrap redemptions counter: %w", err)
	}
	if am.invalidations, err = m.Int64Counter(prefix+".principal_invalidations",
		metric.WithDescription("Principals Revalidate refused, by reason.")); err != nil {
		return nil, fmt.Errorf("auth: principal invalidations counter: %w", err)
	}
	if am.extErrors, err = m.Int64Counter(prefix+".extension_errors",
		metric.WithDescription("Identity extensions that could not decide a request (their IdP or backend failed), by extension. The request went on to the next method.")); err != nil {
		return nil, fmt.Errorf("auth: extension errors counter: %w", err)
	}
	sessions, err := m.Int64ObservableGauge(prefix+".sessions_active",
		metric.WithDescription("Live sessions."))
	if err != nil {
		return nil, fmt.Errorf("auth: sessions gauge: %w", err)
	}
	keys, err := m.Int64ObservableGauge(prefix+".api_keys_active",
		metric.WithDescription("Active, unexpired API keys, by whether an app imported them at its cutover."))
	if err != nil {
		return nil, fmt.Errorf("auth: api keys gauge: %w", err)
	}
	if _, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		n, err := am.counts(ctx, s)
		if err != nil {
			return err
		}
		o.ObserveInt64(sessions, n.sessions)
		o.ObserveInt64(keys, n.keys[0], metric.WithAttributes(attribute.String("imported", "false")))
		o.ObserveInt64(keys, n.keys[1], metric.WithAttributes(attribute.String("imported", "true")))
		return nil
	}, sessions, keys); err != nil {
		return nil, fmt.Errorf("auth: metrics callback: %w", err)
	}
	am.seed()
	return am, nil
}

// seed exports every closed-set series at zero.
func (m *authMetrics) seed() {
	ctx := context.Background()
	for _, method := range []Method{MethodAPIKey, MethodSession, MethodSessionDelegated} {
		for _, oc := range []string{outcomeSuccess, outcomeInvalid, outcomeExpired, outcomeRevoked, outcomeInactive, outcomeThrottle} {
			m.attempts.Add(ctx, 0, metric.WithAttributes(attribute.String("method", string(method)), attribute.String("outcome", oc)))
		}
	}
	for _, sf := range []string{surfaceLogin, surfaceBootstrap, surfaceSetup, surfaceInvite, surfaceTempLogin, surfaceOIDC} {
		m.throttledCtr.Add(ctx, 0, metric.WithAttributes(attribute.String("surface", sf)))
	}
	for _, oc := range []string{outcomeSuccess, outcomeInvalid, outcomeThrottle, outcomeError} {
		m.redemptions.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
	}
	for _, r := range []string{reasonInvalEpoch, reasonInvalInactive, reasonInvalRevoked, reasonInvalExpired, reasonInvalOther} {
		m.invalidations.Add(ctx, 0, metric.WithAttributes(attribute.String("reason", r)))
	}
}

// liveCounts is one scan's result.
type liveCounts struct {
	sessions int64
	keys     [2]int64
}

// counts is the live session and key counts, rescanned at most every
// gaugeTTL. An undecodable record is not live and is skipped; a store
// failure fails the callback, so the gauges keep their last export.
func (m *authMetrics) counts(ctx context.Context, s *Service) (liveCounts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := s.now()
	if !m.scanned.IsZero() && now.Sub(m.scanned) < gaugeTTL {
		return liveCounts{m.sessions, m.keys}, nil
	}
	var n liveCounts
	for u, err := range s.d.Store.ScanUsers(ctx) {
		if errors.Is(err, ErrUndecodableRecord) {
			continue
		}
		if err != nil {
			return liveCounts{}, fmt.Errorf("auth: count sessions: %w", err)
		}
		for rec, err := range s.d.Store.ScanUserSessions(ctx, u.ID) {
			if errors.Is(err, ErrUndecodableRecord) {
				continue
			}
			if err != nil {
				return liveCounts{}, fmt.Errorf("auth: count sessions: %w", err)
			}
			if now.Before(rec.ExpiresAt) {
				n.sessions++
			}
		}
	}
	for k, err := range s.d.Store.ScanAPIKeys(ctx, "") {
		if errors.Is(err, ErrUndecodableRecord) {
			continue
		}
		if err != nil {
			return liveCounts{}, fmt.Errorf("auth: count api keys: %w", err)
		}
		if keyUsable(k, now) != nil {
			continue
		}
		if k.Imported {
			n.keys[1]++
		} else {
			n.keys[0]++
		}
	}
	m.scanned, m.sessions, m.keys = now, n.sessions, n.keys
	return n, nil
}

// attempt counts one stage 12 verdict.
func (m *authMetrics) attempt(ctx context.Context, method Method, outcome string) {
	if m == nil {
		return
	}
	m.attempts.Add(ctx, 1, metric.WithAttributes(attribute.String("method", string(method)), attribute.String("outcome", outcome)))
}

// attemptOutcome is stage 12's outcome label for a Failure code: a password
// reset requirement is an account that may not sign in, "inactive".
func attemptOutcome(code string) string {
	switch code {
	case CodeExpired:
		return outcomeExpired
	case CodeRevoked:
		return outcomeRevoked
	case CodeInactiveUser, CodeResetRequired:
		return outcomeInactive
	}
	return outcomeInvalid
}

// extensionError counts one request an identity extension could not decide.
// The extension label is open (an app registers its own names), so the series
// is created on first use and not seeded.
func (m *authMetrics) extensionError(ctx context.Context, extension string) {
	if m == nil {
		return
	}
	m.extErrors.Add(ctx, 1, metric.WithAttributes(attribute.String("extension", extension)))
}

// throttle counts one refusal on surface.
func (m *authMetrics) throttle(surface string) {
	if m == nil {
		return
	}
	m.throttledCtr.Add(context.Background(), 1, metric.WithAttributes(attribute.String("surface", surface)))
}

// redemption counts one RedeemBootstrap result.
func (m *authMetrics) redemption(ctx context.Context, err error) {
	if m == nil {
		return
	}
	oc := outcomeSuccess
	switch {
	case err == nil:
	case errors.Is(err, ErrThrottled):
		oc = outcomeThrottle
	case errors.Is(err, ErrBootstrapInvalid):
		oc = outcomeInvalid
	default:
		oc = outcomeError
	}
	m.redemptions.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", oc)))
}

// invalidated counts a Revalidate refusal by reason; a store failure is not
// an invalidation.
func (m *authMetrics) invalidated(ctx context.Context, err error) {
	if m == nil || err == nil || errors.Is(err, ErrStoreUnavailable) {
		return
	}
	r := reasonInvalOther
	switch {
	case errors.Is(err, ErrStaleEpoch):
		r = reasonInvalEpoch
	case errors.Is(err, ErrInactiveUser), errors.Is(err, ErrResetRequired):
		r = reasonInvalInactive
	case errors.Is(err, ErrRevoked):
		r = reasonInvalRevoked
	case errors.Is(err, ErrExpired):
		r = reasonInvalExpired
	}
	m.invalidations.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", r)))
}
