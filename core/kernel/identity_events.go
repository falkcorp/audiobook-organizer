// file: core/kernel/identity_events.go
// version: 0.1.0
// guid: 3f81b6d2-0c47-4e95-a2d8-6b19e7c5f430
// last-edited: 2026-11-21

package kernel

import (
	"context"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
)

// The identity control topics (F4-08 A11b). realtime refuses to build its
// subscriptions unless the catalog declares both (realtime.Hub.Subscriptions;
// the kernel never sets AllowNoControlTopics), because without them a revoked
// credential or a rights change would reach an open stream only on its next
// heartbeat (05-realtime §4.6).
//
//   - core.rbac.changed.v1 {generation}: rbac bumps its generation on every
//     role write and calls rbac.Deps.OnChange, which is publishRBACChanged
//     (05-rbac §5 step 6). rbac never imports events (05-rbac R-C; events
//     imports rbac), so core declares and publishes it here.
//   - core.principal.invalidated.v1 {user_id, credential_id?, reason}: every
//     auth revocation publishes it after its batch commits (05-authn §4.12),
//     through publishPrincipalInvalidated (auth's side is F4-08 A15).
//
// Neither carries Realtime, Notify or Durable metadata: they are control
// topics, read by core's own raw subscriptions, never streamed or notified.
// The spec names no observe permission for either, so both take the
// narrowest core scope, core:system.manage, for the surfaces that list or
// read catalog topics. A publish before the bus freezes (a revocation at boot,
// before P7) is refused, counted and logged once by the bus: no stream is open
// then, so nothing is lost.
const (
	rbacChangedTopicName          = "core.rbac.changed.v1"
	principalInvalidatedTopicName = "core.principal.invalidated.v1"
)

// identityTopicPermission is who may observe the two control topics.
const identityTopicPermission rbac.Permission = "core:system.manage"

// rbacChangedKey is core.rbac.changed.v1's key: every envelope is the whole
// state (the latest generation).
const rbacChangedKey = "rbac"

// rbacChangedPayload is core.rbac.changed.v1's payload.
type rbacChangedPayload struct {
	Generation uint64 `json:"generation"`
}

// principalInvalidatedPayload is core.principal.invalidated.v1's payload. The
// keys are the ones realtime decodes (core/realtime's invalidation): user_id,
// and credential_id, omitted when the revocation covers every credential of
// the user. Reason is a fixed token naming the revocation.
type principalInvalidatedPayload struct {
	UserID       string `json:"user_id"`
	CredentialID string `json:"credential_id,omitempty"`
	Reason       string `json:"reason"`
}

// rbacChangedTopic is the typed topic. A function, not a package variable (R5).
func rbacChangedTopic() events.Topic[rbacChangedPayload] {
	return events.NewTopic[rbacChangedPayload](rbacChangedTopicName)
}

// principalInvalidatedTopic is the typed topic.
func principalInvalidatedTopic() events.Topic[principalInvalidatedPayload] {
	return events.NewTopic[principalInvalidatedPayload](principalInvalidatedTopicName)
}

// identityTopicSpecs are the two control topics' catalog entries; coreTopics
// lists them.
func identityTopicSpecs() []events.TopicSpec {
	need := rbac.Need(identityTopicPermission)
	return []events.TopicSpec{
		rbacChangedTopic().Spec(events.TopicSpec{Permission: need}),
		principalInvalidatedTopic().Spec(events.TopicSpec{Permission: need}),
	}
}

// publishRBACChanged returns rbac.Deps.OnChange (identityInputs.OnRoleChange):
// it publishes core.rbac.changed.v1 through p, the kernel's bus. Publish never
// blocks and never fails, so it is safe where rbac calls it, after a role
// write. OnChange carries no context, so the envelope has no actor.
func publishRBACChanged(p events.Publisher) func(generation uint64) {
	t := rbacChangedTopic()
	return func(generation uint64) {
		events.Publish(p, context.Background(), t, rbacChangedKey, rbacChangedPayload{Generation: generation})
	}
}

// publishPrincipalInvalidated returns the publisher auth's revoking writes
// call after their batch commits (F4-08 A15 adds the auth.Deps callback; B3
// binds it to this). Primitives, not an auth type, so this file and auth's
// side land independently. Keyed by user ID. An empty userID publishes
// nothing: realtime would drop it anyway, and every auth revocation names a
// user.
func publishPrincipalInvalidated(p events.Publisher) func(ctx context.Context, userID, credentialID, reason string) {
	t := principalInvalidatedTopic()
	return func(ctx context.Context, userID, credentialID, reason string) {
		if userID == "" {
			return
		}
		events.Publish(p, ctx, t, userID, principalInvalidatedPayload{UserID: userID, CredentialID: credentialID, Reason: reason})
	}
}
