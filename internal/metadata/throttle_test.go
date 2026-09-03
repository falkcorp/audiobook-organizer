// file: internal/metadata/throttle_test.go
// version: 1.0.0
// guid: 31722b4f-1d15-4404-a9f4-7a792efd86f0
// last-edited: 2026-09-03

package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// prodGoogleQuotaBody is the ACTUAL body prod received on 2026-09-03 while the
// bulk fetch was failing 99% of 22,934 books. Pinned verbatim: the whole feature
// turns on telling this apart from a burst 429, and a paraphrase would test the
// paraphrase.
const prodGoogleQuotaBody = `{"error":{"code":429,"message":"Quota exceeded for quota metric 'Queries' and limit 'Queries per day' of service 'books.googleapis.com' for consumer 'project_number:624717413613'.","status":"RESOURCE_EXHAUSTED"}}`

// statusErrorFrom builds a *ProviderStatusError from a synthetic response.
//
// It constructs http.Response directly rather than using httptest.NewRecorder:
// the recorder's Write implicitly calls WriteHeader(200), silently discarding a
// Code assigned beforehand. That produced a helper that reported 200 for every
// case -- every "this status throttles" assertion failed while the "this does
// not throttle" ones passed, which is the shape of a green suite testing
// nothing had the polarity been reversed.
func statusErrorFrom(t *testing.T, status int, body string, headers map[string]string) *ProviderStatusError {
	t.Helper()
	return statusErrorFor(t, "google-books", status, body, headers)
}

// statusErrorFor is the same, for a named provider — so a hold's stored Detail
// names the provider it is actually filed under.
func statusErrorFor(t *testing.T, provider string, status int, body string, headers map[string]string) *ProviderStatusError {
	t.Helper()
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	resp := &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	return StatusError(provider, resp)
}

func TestStatusError_CapturesBodyAndRetryAfter(t *testing.T) {
	se := statusErrorFrom(t, http.StatusTooManyRequests, prodGoogleQuotaBody, map[string]string{"Retry-After": "120"})
	if se.Status != 429 {
		t.Fatalf("status = %d, want 429", se.Status)
	}
	if !strings.Contains(se.Body, "Queries per day") {
		t.Fatalf("body did not survive: %q", se.Body)
	}
	if se.RetryAfter != 2*time.Minute {
		t.Fatalf("RetryAfter = %v, want 2m", se.RetryAfter)
	}
	if !strings.Contains(se.Error(), "Queries per day") {
		t.Fatalf("Error() dropped the body: %q", se.Error())
	}
}

func TestStatusError_BodyIsBounded(t *testing.T) {
	se := statusErrorFrom(t, 500, strings.Repeat("x", 5000), nil)
	if len(se.Body) > providerErrorBodyLimit {
		t.Fatalf("body len = %d, want <= %d", len(se.Body), providerErrorBodyLimit)
	}
}

func TestStatusError_RetryAfterHTTPDate(t *testing.T) {
	when := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	se := statusErrorFrom(t, 429, "slow down", map[string]string{"Retry-After": when})
	// HTTP-date has second granularity and time passes during the test, so
	// assert a band rather than an exact value.
	if se.RetryAfter < 60*time.Second || se.RetryAfter > 95*time.Second {
		t.Fatalf("RetryAfter = %v, want ~90s", se.RetryAfter)
	}
}

func TestClassifyProviderError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason ThrottleReason
		wantHold   time.Duration
		wantOK     bool
	}{
		{
			// THE case this feature exists for.
			name:       "google daily quota body",
			err:        statusErrorFrom(t, 429, prodGoogleQuotaBody, nil),
			wantReason: ThrottleDailyQuota,
			wantHold:   4 * time.Hour,
			wantOK:     true,
		},
		{
			// Same status code, different meaning. If these two collapse into
			// one duration the feature is pointless in one direction or the
			// other: a 15m hold on a day quota keeps hammering, a 4h hold on a
			// burst limit throws away most of a day's fetching.
			name:       "burst 429 without a daily marker",
			err:        statusErrorFrom(t, 429, `{"error":"too many requests, slow down"}`, nil),
			wantReason: ThrottleRateLimit,
			wantHold:   15 * time.Minute,
			wantOK:     true,
		},
		{
			name:       "burst 429 with a LONGER Retry-After is honoured",
			err:        statusErrorFrom(t, 429, "slow down", map[string]string{"Retry-After": "3600"}),
			wantReason: ThrottleRateLimit,
			wantHold:   time.Hour,
			wantOK:     true,
		},
		{
			// A provider that says "retry in 1 second" while refusing every call
			// just reproduces the hammering, so the default is the floor.
			name:       "burst 429 with a SHORTER Retry-After keeps the floor",
			err:        statusErrorFrom(t, 429, "slow down", map[string]string{"Retry-After": "1"}),
			wantReason: ThrottleRateLimit,
			wantHold:   15 * time.Minute,
			wantOK:     true,
		},
		{
			name:       "wrapped error still classifies",
			err:        fmt.Errorf("ASIN B01234: %w", statusErrorFrom(t, 429, prodGoogleQuotaBody, nil)),
			wantReason: ThrottleDailyQuota,
			wantHold:   4 * time.Hour,
			wantOK:     true,
		},
		{name: "401", err: statusErrorFrom(t, 401, "bad key", nil), wantReason: ThrottleAuth, wantHold: 6 * time.Hour, wantOK: true},
		{name: "403", err: statusErrorFrom(t, 403, "forbidden", nil), wantReason: ThrottleAuth, wantHold: 6 * time.Hour, wantOK: true},
		{name: "503", err: statusErrorFrom(t, 503, "unavailable", nil), wantReason: ThrottleUnavailable, wantHold: 30 * time.Minute, wantOK: true},
		{name: "dial failure", err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}, wantReason: ThrottleTransport, wantHold: 5 * time.Minute, wantOK: true},
		{
			// The known-good twin for the two url.Error cases below: the guard
			// must exclude OUR cancellation without also excluding every
			// transport failure that arrives in the same wrapper.
			name:       "url.Error wrapping a real dial failure",
			err:        &url.Error{Op: "Get", URL: "https://example.test", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}},
			wantReason: ThrottleTransport,
			wantHold:   5 * time.Minute,
			wantOK:     true,
		},

		// Everything below must NOT throttle.
		{name: "404 is about the query", err: statusErrorFrom(t, 404, "not found", nil)},
		{name: "400 is about the query", err: statusErrorFrom(t, 400, "bad request", nil)},
		{name: "our own cancellation", err: fmt.Errorf("fetch: %w", context.Canceled)},
		{name: "our own deadline", err: fmt.Errorf("fetch: %w", context.DeadlineExceeded)},
		// The shapes these ACTUALLY arrive in. net/http wraps a cancelled or
		// timed-out request in *url.Error, which reports Timeout() == true and
		// therefore satisfies net.Error — so without the errors.Is guards ahead
		// of the net.Error check, our own cancellation would throttle a healthy
		// provider for five minutes. A bare context.Canceled is not a net.Error
		// at all, so a test using only that would pass with the guard deleted.
		{name: "url.Error wrapping our cancellation", err: &url.Error{Op: "Get", URL: "https://example.test", Err: context.Canceled}},
		{name: "url.Error wrapping our deadline", err: &url.Error{Op: "Get", URL: "https://example.test", Err: context.DeadlineExceeded}},
		{name: "the breaker's own sentinel", err: ErrCircuitOpen},
		{name: "the throttle's own sentinel", err: ErrProviderThrottled},
		{name: "plain error", err: errors.New("decode failed")},
		{name: "nil", err: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, hold, ok := ClassifyProviderError(tc.err)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (reason=%q hold=%v)", ok, tc.wantOK, reason, hold)
			}
			if !tc.wantOK {
				return
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
			if hold != tc.wantHold {
				t.Errorf("hold = %v, want %v", hold, tc.wantHold)
			}
		})
	}
}

// TestClassify_ThrottleSentinelCannotExtendItself is the self-reference guard.
// If ErrProviderThrottled ever classified, a throttled provider's own skip
// would re-arm the hold on every call and it could never expire.
func TestClassify_ThrottleSentinelCannotExtendItself(t *testing.T) {
	if _, _, ok := ClassifyProviderError(fmt.Errorf("google-books: %w", ErrProviderThrottled)); ok {
		t.Fatal("ErrProviderThrottled classified as throttleable; a hold would extend itself forever")
	}
}

// --- registry ---------------------------------------------------------------

type fakeThrottleStore struct {
	mu      sync.Mutex
	rows    map[string][]byte
	loadErr error
	saves   int
	deletes int
}

func newFakeThrottleStore() *fakeThrottleStore {
	return &fakeThrottleStore{rows: map[string][]byte{}}
}

func (f *fakeThrottleStore) LoadProviderThrottles() (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := make(map[string][]byte, len(f.rows))
	for k, v := range f.rows {
		out[k] = v
	}
	return out, nil
}

func (f *fakeThrottleStore) SaveProviderThrottle(id string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[id] = payload
	f.saves++
	return nil
}

func (f *fakeThrottleStore) DeleteProviderThrottle(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
	f.deletes++
	return nil
}

func (f *fakeThrottleStore) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func TestRegistry_RecordFailureInstallsAndExpires(t *testing.T) {
	r := NewThrottleRegistry()
	store := newFakeThrottleStore()
	if _, err := r.AttachStore(store); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	got, wrote := r.RecordFailure("google-books", statusErrorFrom(t, 429, prodGoogleQuotaBody, nil))
	if !wrote {
		t.Fatal("first 429 did not install a throttle")
	}
	if got.Reason != ThrottleDailyQuota {
		t.Fatalf("reason = %q", got.Reason)
	}
	if !r.Throttled("google-books") {
		t.Fatal("provider not reported as throttled")
	}
	if store.rowCount() != 1 {
		t.Fatalf("persisted rows = %d, want 1", store.rowCount())
	}

	// Expire it by hand and confirm Get sweeps rather than needing a timer.
	r.mu.Lock()
	e := r.entries["google-books"]
	e.Until = time.Now().Add(-time.Second)
	r.entries["google-books"] = e
	r.mu.Unlock()

	if r.Throttled("google-books") {
		t.Fatal("expired hold still reported as active")
	}
	if store.rowCount() != 0 {
		t.Fatalf("expired hold was not swept from the store: %d rows", store.rowCount())
	}
}

// A hold must not be pushed out by the failures a bulk run's own in-flight
// calls generate, or a 4-hour hold would never end.
func TestRegistry_SameReasonDoesNotExtendTheHold(t *testing.T) {
	r := NewThrottleRegistry()
	first, _ := r.RecordFailure("google-books", statusErrorFrom(t, 429, prodGoogleQuotaBody, nil))
	time.Sleep(2 * time.Millisecond)
	_, wrote := r.RecordFailure("google-books", statusErrorFrom(t, 429, prodGoogleQuotaBody, nil))
	if wrote {
		t.Fatal("a second identical failure rewrote the hold")
	}
	cur, ok := r.Get("google-books")
	if !ok {
		t.Fatal("hold vanished")
	}
	if !cur.Until.Equal(first.Until) {
		t.Fatalf("deadline moved: %v -> %v", first.Until, cur.Until)
	}
}

// A worse failure DOES replace a milder one -- a burst limit that turns out to
// be a day quota must not stay on the 15-minute timer.
func TestRegistry_LongerReasonReplacesShorter(t *testing.T) {
	r := NewThrottleRegistry()
	r.RecordFailure("google-books", statusErrorFrom(t, 429, "slow down", nil))
	before, _ := r.Get("google-books")
	if before.Reason != ThrottleRateLimit {
		t.Fatalf("setup: reason = %q", before.Reason)
	}
	if _, wrote := r.RecordFailure("google-books", statusErrorFrom(t, 429, prodGoogleQuotaBody, nil)); !wrote {
		t.Fatal("day-quota failure did not replace the burst hold")
	}
	after, _ := r.Get("google-books")
	if after.Reason != ThrottleDailyQuota {
		t.Fatalf("reason = %q, want daily-quota", after.Reason)
	}
	if !after.Until.After(before.Until) {
		t.Fatal("day-quota hold is not longer than the burst hold it replaced")
	}
}

func TestRegistry_AttachStoreRestoresActiveAndDropsExpired(t *testing.T) {
	store := newFakeThrottleStore()
	seed := NewThrottleRegistry()
	if _, err := seed.AttachStore(store); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	seed.RecordFailure("google-books", statusErrorFrom(t, 429, prodGoogleQuotaBody, nil))
	seed.RecordFailure("hardcover", statusErrorFor(t, "hardcover", 503, "down", nil))

	// Age the hardcover row out, as a restart hours later would find it.
	raw, _ := store.LoadProviderThrottles()
	var stale ProviderThrottle
	if err := jsonUnmarshalForTest(raw["hardcover"], &stale); err != nil {
		t.Fatalf("decode: %v", err)
	}
	stale.Until = time.Now().Add(-time.Hour)
	_ = store.SaveProviderThrottle("hardcover", jsonMarshalForTest(t, stale))

	// A fresh process.
	restarted := NewThrottleRegistry()
	n, err := restarted.AttachStore(store)
	if err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	if n != 1 {
		t.Fatalf("restored = %d, want 1", n)
	}
	if !restarted.Throttled("google-books") {
		t.Fatal("an active hold did not survive the restart; this is the whole point of persisting")
	}
	if restarted.Throttled("hardcover") {
		t.Fatal("an expired hold came back to life")
	}
}

func TestRegistry_ClearAndClearAll(t *testing.T) {
	r := NewThrottleRegistry()
	store := newFakeThrottleStore()
	if _, err := r.AttachStore(store); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	r.RecordFailure("google-books", statusErrorFrom(t, 429, prodGoogleQuotaBody, nil))
	r.RecordFailure("hardcover", statusErrorFor(t, "hardcover", 401, "bad token", nil))

	if err := r.Clear("google-books"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if r.Throttled("google-books") {
		t.Fatal("manual reset did not release the hold")
	}
	if _, ok := store.rows["google-books"]; ok {
		t.Fatal("manual reset left the row on disk; it would come back on restart")
	}

	n, err := r.ClearAll()
	if err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	if n != 1 {
		t.Fatalf("ClearAll = %d, want 1", n)
	}
	if store.rowCount() != 0 {
		t.Fatalf("rows left after ClearAll: %d", store.rowCount())
	}
}

func TestRegistry_UnreadableRowIsDroppedNotFatal(t *testing.T) {
	store := newFakeThrottleStore()
	_ = store.SaveProviderThrottle("google-books", []byte("{not json"))
	r := NewThrottleRegistry()
	n, err := r.AttachStore(store)
	if err != nil {
		t.Fatalf("a corrupt row must not fail startup: %v", err)
	}
	if n != 0 {
		t.Fatalf("restored = %d, want 0", n)
	}
	if store.rowCount() != 0 {
		t.Fatal("corrupt row was left in place to fail again on the next boot")
	}
}

// jsonMarshalForTest / jsonUnmarshalForTest keep the encoding/json import out of
// the table above, where it would read as production concern.
func jsonMarshalForTest(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func jsonUnmarshalForTest(b []byte, v any) error { return json.Unmarshal(b, v) }

// --- ProtectedSource gate ---------------------------------------------------

type stubSource struct {
	id    string
	calls int
	err   error
}

func (s *stubSource) Name() string       { return s.id }
func (s *stubSource) ProviderID() string { return s.id }
func (s *stubSource) SearchByTitle(ctx context.Context, title string) ([]BookMetadata, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return []BookMetadata{{Title: title}}, nil
}

func (s *stubSource) SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]BookMetadata, error) {
	return s.SearchByTitle(ctx, title)
}

// resetDefaultThrottles clears the process-wide registry. These tests exercise
// the real global on purpose -- the user asked for a GLOBAL timer, and a test
// against a private registry would not prove the gate consults the one the
// server actually installs.
func resetDefaultThrottles(t *testing.T) {
	t.Helper()
	if _, err := DefaultThrottleRegistry().ClearAll(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(func() { _, _ = DefaultThrottleRegistry().ClearAll() })
}

// The first 429 must install the hold, and the NEXT call must not reach the
// network. Recording on breaker-trip instead would need five failures and would
// then classify ErrCircuitOpen -- which is unclassifiable -- forever.
func TestProtectedSource_ThrottlesOnFirstQuota429AndThenSkips(t *testing.T) {
	resetDefaultThrottles(t)
	stub := &stubSource{id: "google-books", err: statusErrorFrom(t, 429, prodGoogleQuotaBody, nil)}
	ps := NewChainSource(stub)

	if _, err := ps.SearchByTitle(context.Background(), "dune"); err == nil {
		t.Fatal("expected the provider error")
	}
	if stub.calls != 1 {
		t.Fatalf("calls = %d, want 1", stub.calls)
	}
	if !DefaultThrottleRegistry().Throttled("google-books") {
		t.Fatal("a daily-quota 429 on the FIRST call did not install a throttle")
	}

	_, err := ps.SearchByTitle(context.Background(), "dune")
	if !errors.Is(err, ErrProviderThrottled) {
		t.Fatalf("err = %v, want ErrProviderThrottled", err)
	}
	if stub.calls != 1 {
		t.Fatalf("a throttled provider was called again: calls = %d", stub.calls)
	}
}

// The approved exception: a user-initiated single-book lookup goes through.
func TestProtectedSource_BypassLetsAManualLookupThrough(t *testing.T) {
	resetDefaultThrottles(t)
	stub := &stubSource{id: "google-books", err: statusErrorFrom(t, 429, prodGoogleQuotaBody, nil)}
	ps := NewChainSource(stub)
	_, _ = ps.SearchByTitle(context.Background(), "dune")
	if !DefaultThrottleRegistry().Throttled("google-books") {
		t.Fatal("setup: no throttle installed")
	}

	stub.err = nil
	if _, err := ps.SearchByTitle(WithThrottleBypass(context.Background()), "dune"); err != nil {
		t.Fatalf("bypass call failed: %v", err)
	}
	if stub.calls != 2 {
		t.Fatalf("bypass did not reach the source: calls = %d", stub.calls)
	}
	// Proof of life beats a timer.
	if DefaultThrottleRegistry().Throttled("google-books") {
		t.Fatal("a successful bypass call did not release the hold")
	}
}

// A 404 is about the query, not the provider. Throttling on it would take a
// whole provider out of the chain because one book is obscure.
func TestProtectedSource_NotFoundDoesNotThrottle(t *testing.T) {
	resetDefaultThrottles(t)
	stub := &stubSource{id: "hardcover", err: statusErrorFor(t, "hardcover", 404, "no such book", nil)}
	ps := NewChainSource(stub)
	for i := 0; i < 3; i++ {
		_, _ = ps.SearchByTitle(context.Background(), "obscure")
	}
	if DefaultThrottleRegistry().Throttled("hardcover") {
		t.Fatal("a 404 throttled the whole provider")
	}
	if stub.calls != 3 {
		t.Fatalf("calls = %d, want 3", stub.calls)
	}
}

func TestUnthrottledSources_ExcludesHeldProviders(t *testing.T) {
	resetDefaultThrottles(t)
	google := NewChainSource(&stubSource{id: "google-books"})
	hardcover := NewChainSource(&stubSource{id: "hardcover"})
	chain := []MetadataSource{google, hardcover}

	live, skipped := UnthrottledSources(chain)
	if len(live) != 2 || len(skipped) != 0 {
		t.Fatalf("clean chain: live=%d skipped=%v", len(live), skipped)
	}

	DefaultThrottleRegistry().RecordFailure("google-books", statusErrorFrom(t, 429, prodGoogleQuotaBody, nil))
	live, skipped = UnthrottledSources(chain)
	if len(live) != 1 || ProviderIDOf(live[0]) != "hardcover" {
		t.Fatalf("live = %v", live)
	}
	if len(skipped) != 1 || skipped[0] != "google-books" {
		t.Fatalf("skipped = %v", skipped)
	}
	if s := ThrottleSummary(skipped); !strings.Contains(s, "daily-quota") {
		t.Fatalf("summary does not name the reason: %q", s)
	}

	// All-throttled: this empty slice is what makes the bulk op refuse to start
	// instead of ledgering a failure against every book in the library.
	DefaultThrottleRegistry().RecordFailure("hardcover", statusErrorFor(t, "hardcover", 401, "bad token", nil))
	live, skipped = UnthrottledSources(chain)
	if len(live) != 0 {
		t.Fatalf("live = %v, want empty", live)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v, want both", skipped)
	}
}
