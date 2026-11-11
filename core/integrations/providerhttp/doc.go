// file: core/integrations/providerhttp/doc.go
// version: 1.0.0
// guid: 28642cb9-f8ea-461a-99c3-c80be49fb21f
// last-edited: 2026-11-11

// Package providerhttp is the instance-scoped outbound HTTP layer of
// 05-metadata M2. Every integration adapter's outbound request passes through
// one per-App Registry, which composes in the TRANSPORT (so no call site can
// bypass it, including an SDK that copies the *http.Client Client returns and
// sets its own CheckRedirect) a reason-aware persisted hold, a circuit breaker
// with a probe lease, a token bucket shared per scope, a daily quota with
// background and interactive tiers, the egress guard, bounded retry honouring
// Retry-After, response size limits and secret redaction. Non-HTTP adapters
// use the same Gate without the transport.
//
// The egress guard is the SSRF boundary for every adapter: the dial checks the
// RESOLVED address (never the name), so a hostname that resolves to a private
// address, a rebinding answer and a redirect into the private range are all
// refused at the socket. Private ranges are reachable only through the
// instance's Policy.Private (its bound, admin-set origins and the operator's
// lan_allow list), never for data-derived URLs, and link-local addresses
// (cloud metadata included) never.
//
// The package also holds core.integrations.release-holds (holds.go): the
// kind's final TaskSpec over the one-method InterimHolds backend, and the
// native body, which is the Registry's ReleaseHolds method.
//
// Layer L4 (03 R4). It holds no package-level mutable state (R5): HEAD's
// limiter, client, override, throttle and daily-quota maps are Registry
// fields.
package providerhttp
