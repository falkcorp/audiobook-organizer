// file: core/integrations/notify/export_test.go
// version: 1.0.0
// guid: 6e2b8d41-0c7a-4f93-b5e8-3a91d7c04f26
// last-edited: 2026-11-13

package notify

import (
	"context"

	"github.com/falkcorp/mediacore/outbox"
)

// DeadCounts is deadCounts with the page budget a test chooses, so the
// truncated path is reachable with a handful of dead letters.
var DeadCounts = deadCounts

// Deliver runs the delivery handler once, on a ctx the test controls, so the
// relay-is-stopping paths (which only a shutdown reaches) can be asserted.
func Deliver(s *Service, ctx context.Context, m outbox.Message[Delivery]) error {
	return s.deliver(ctx, m)
}
