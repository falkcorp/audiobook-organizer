// file: core/integrations/providerhttp/bucket.go
// version: 1.0.0
// guid: 9a558073-47dd-4119-bd3a-734824d03325
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"math"
	"sync"
	"time"
)

// bucket is one scope's token bucket (05-metadata M2.6): one per Scope,
// shared by every Client and worker of it, and resized IN PLACE by set, so a
// limit change can never leave two buckets serving one provider (HEAD defect
// 1). It is a reservation bucket as rate.Limiter is: a taker that finds no
// token owes a delay, the tokens go negative, and a taker that gives up
// returns its token.
type bucket struct {
	mu     sync.Mutex
	now    func() time.Time
	rps    float64
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(now func() time.Time, rps float64, burst int) *bucket {
	return &bucket{now: now, rps: rps, burst: float64(burst), tokens: float64(burst), last: now()}
}

// refill adds the tokens earned up to t. Caller holds mu.
func (b *bucket) refill(t time.Time) {
	if t.After(b.last) {
		b.tokens = math.Min(b.burst, b.tokens+t.Sub(b.last).Seconds()*b.rps)
		b.last = t
	}
}

// reserve takes one token and returns how long its taker must wait for it.
func (b *bucket) reserve() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(b.now())
	b.tokens--
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(math.Ceil(-b.tokens / b.rps * float64(time.Second)))
}

// giveBack returns a reserved token whose taker gave up.
func (b *bucket) giveBack() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens = math.Min(b.burst, b.tokens+1)
}

// set changes the rate and the burst in place.
func (b *bucket) set(rps float64, burst int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(b.now())
	b.rps, b.burst = rps, float64(burst)
	b.tokens = math.Min(b.tokens, b.burst)
}

// available is the tokens a taker would find now (negative: queued takers).
func (b *bucket) available() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(b.now())
	return b.tokens
}

// wait takes a token, sleeping through sleep until it is earned. A ctx that
// ends first returns the token and ctx's error: a canceled fetch stops paying
// for tokens.
func (b *bucket) wait(ctx context.Context, sleep func(context.Context, time.Duration) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d := b.reserve()
	if d <= 0 {
		return nil
	}
	if err := sleep(ctx, d); err != nil {
		b.giveBack()
		return err
	}
	return nil
}
