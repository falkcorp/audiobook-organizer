// file: core/integrations/providerhttp/ctxkeys.go
// version: 1.1.0
// guid: 9eabb08f-35bd-449d-b552-aeabd31b2469
// last-edited: 2026-11-11

package providerhttp

import "context"

type tierKey struct{}
type bypassKey struct{}
type counterKey struct{}

// WithInteractive marks ctx's requests as interactive: they may use the daily
// quota up to Total, where an unmarked (Background) request stops at
// Background (HEAD's dailyquota.WithInteractive, kept).
func WithInteractive(ctx context.Context) context.Context {
	return context.WithValue(ctx, tierKey{}, true)
}

// interactiveOf reports whether ctx carries WithInteractive.
func interactiveOf(ctx context.Context) bool {
	v, _ := ctx.Value(tierKey{}).(bool)
	return v
}

// WithBypassHold lets an operator-initiated single lookup through an active
// hold (HEAD's metadata.WithThrottleBypass, kept). It never bypasses the
// breaker's probe lease, the daily quota, the egress guard or
// core.integrations.disabled.
func WithBypassHold(ctx context.Context) context.Context {
	return context.WithValue(ctx, bypassKey{}, true)
}

func bypassOf(ctx context.Context) bool {
	v, _ := ctx.Value(bypassKey{}).(bool)
	return v
}

// GateFlags reports the parts of ctx that change what a gate does with a
// request: the interactive tier (which daily allowance it spends), whether it
// bypasses an active hold, and the counter it counts against. Two calls with
// different flags can get different answers from the same provider (one is
// refused by a spent Background quota, the other is not), so a caller that
// shares one call between concurrent askers (the metadata cache's single
// flight) must keep askers with different flags apart.
func GateFlags(ctx context.Context) (interactive, bypassHold bool, counter string) {
	return interactiveOf(ctx), bypassOf(ctx), counterOf(ctx)
}

// WithCounter names the daily counter ctx's requests count against (an
// indexer's "query" or "grab"). Unmarked requests use the first declared
// counter, "requests" by default.
func WithCounter(ctx context.Context, counter string) context.Context {
	return context.WithValue(ctx, counterKey{}, counter)
}

func counterOf(ctx context.Context) string {
	v, _ := ctx.Value(counterKey{}).(string)
	return v
}
