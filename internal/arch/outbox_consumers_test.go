// file: internal/arch/outbox_consumers_test.go
// version: 0.1.0
// guid: d3a8f5c1-6e24-4b97-a1c0-8f2b9e47d605
// last-edited: 2026-11-22

package arch

import (
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

// TestOutboxConsumersAreTasks: no package of the application references
// outbox.Consume, outbox.ConsumeTx or outbox.ConsumeRaw. Those run a handler in
// the relay's process, outside a task run; a module's consumer is a task run
// (ConsumeIntoTask), which is bounded, audited, cancelable and visible in the
// run history (05-events §5E.6). Outbox.Register refuses a module owner's plain
// consumer at boot; this fails the build first. books' relocation follow-up
// (deluge.RelocationConsumer) is the one consumer this module registers.
func TestOutboxConsumersAreTasks(t *testing.T) {
	archtest.OutboxApplicationConsumersAreTasks(t)
}
