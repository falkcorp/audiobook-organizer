// file: core/health/topic.go
// version: 1.0.0
// guid: a2f6c8d4-1b93-4e57-8c0a-5d7e3f9b1264
// last-edited: 2026-11-13

package health

import (
	"context"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
)

// StatusTopicName is the one catalog name of the status topic (05-health
// §5H.3.3 "Status topic").
const StatusTopicName = "core.system.status-changed.v1"

// statusKey is the topic's coalescing key: every envelope is the whole
// readiness state, so the latest one inside the window is the one to deliver.
const statusKey = "status"

// StatusTopic is the typed topic. A function, not a package variable (R5).
func StatusTopic() events.Topic[StatusPayload] {
	return events.NewTopic[StatusPayload](StatusTopicName)
}

// StatusTopicSpec is health's catalog entry: bus only, a realtime stream
// coalesced to one envelope a second, observable by core:system.status
// (seeded to editor and viewer, 05-rbac §4.8). The payload is StatusPayload,
// the /readyz body: fixed tokens, never Detail, durations, versions or counts.
// The kernel declares it with core's other topics (kernel.coreTopics).
func StatusTopicSpec() events.TopicSpec {
	return StatusTopic().Spec(events.TopicSpec{
		Permission: rbac.Need("core:system.status"),
		Realtime:   &events.RealtimeMeta{Coalesce: time.Second},
	})
}

// PublishStatus returns the Registry.SetStatusPublisher sink that publishes
// each StatusPayload on the status topic through p (the kernel's core scope).
// Publish never blocks and never fails, which is what the sink's contract
// needs: it runs under the registry's publication lock.
func PublishStatus(p events.Publisher) func(StatusPayload) {
	t := StatusTopic()
	return func(s StatusPayload) {
		events.Publish(p, context.Background(), t, statusKey, s)
	}
}
