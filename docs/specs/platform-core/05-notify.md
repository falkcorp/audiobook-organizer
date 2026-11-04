<!-- file: docs/specs/platform-core/05-notify.md -->
<!-- version: 1.6.0 -->
<!-- guid: 785812fd-5ddd-4540-be24-d061c0127788 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Notifications (`integrations/notify`)

Evidence commit `748fd787a`. Raw inputs: [`raw/11-integrations.md`](../../plans/platform-core/raw/11-integrations.md) §1, §5;
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §5; [`raw/16-arr-domain-research.md`](../../plans/platform-core/raw/16-arr-domain-research.md) §7.1 row 21;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7.
Builds on [05-metadata](05-metadata.md) §M1-§M2 and on the durable outbox and the one topic catalog of
[05-events](05-events.md).

## 1. Purpose & scope

One way for core and modules to tell operators that something happened: a `Notification` value, a
`Notifier` contract for channels, **global routing rules**, and **delivery with retries through the
outbox**. The topics are **event topics** declared once in the events catalog (SIMP-5); notify keeps no
topic catalog of its own.

**v1 (SIMP-10, cross-cutting resolution 1)** is exactly what ABO uses or needs to fix today's defects
(no retry, publisher ctx, replayable HMAC, §2):

| In v1 (built in [09](09-abo-on-core.md) S7, with the instance store and `providerhttp` it needs; SIMP2-4) | Deferred (contract not frozen; built when a second app or the owner asks) |
|---|---|
| `Notification`, `Notifier`, `Publisher` (`Service.Publish`, and `module.Deps.Notify` for modules) | in-app **inbox** (`core.inbox`) and the UI bell (07) |
| `core.email` channel to a fixed address list (from `internal/mail`) | **per-user subscriptions** and per-user email to a verified address |
| `core.webhook` channel, preset `json` + the legacy ABO headers | **personal channels** (user-owned instances); when they ship, `personal_channels` defaults to **false** |
| global admin `Rule`s resolved at publish; per-target delivery through the outbox with retries | **templating** (per-instance/per-topic templates); ntfy, Gotify, Apprise presets and `custom` bodies; a dispatch stage (returns with digests or per-user delivery, §11) |

**How a module notifies (COR2-3).** A module reaches notify only through `module.Deps.Notify`, a
`notify.Publisher` scoped to the module's own catalog topics (06 owns the `Deps` field). It calls
`Publish(ctx, b, n)` with the batch of its own state change, so the notification commits with the change
it reports. `Publish` is the **only** entry point: declaring a topic with notify metadata makes it offerable
to rules but produces no notification by itself, and notify subscribes to nothing on the events bus (the bus is
lossy, and 05-events §5E.5.4 forbids external effects through it).

Out of scope: transactional mail that is not a notification (invites, password reset) — those use the
`mail.Sender` primitive directly ([05-authn](05-authn.md)); media-server sync.

### Deferred-build members

The right-hand column of the v1 table. None is in the 09 S7 build. The first column holds only package-qualified Go
identifiers, each absent from non-test files under `core/` per `archtest.DeferredMembersAbsent` (03 §3.4); adapter
IDs, routes and keys are in the second column and are not checked (03 §3.4 grammar, SIMP5-6). The PR that builds a
member deletes its row (SIMP4-1):

| Member | Also deferred (not checked) | Lands with | Contract |
|---|---|---|---|
| | adapter `core.inbox` and its read routes | a second app or the owner's request | in-app inbox and the UI bell (07) |
| `notify.Target.Recipient`, `notify.Subscription` | — | per-user subscriptions | per-user routing filtered by the recipient's permissions at delivery |
| `integrations.InstanceConfig.Owner` | key `core.notify.personal_channels` (default `false`) | personal channels | user-owned instances (removed from v1 by SIMP-10) |
| `notify.Template` | the ntfy, Gotify, Apprise and `custom` presets | templating | per-instance/per-topic templates |

## 2. Current state at HEAD

| Fact | Evidence |
|---|---|
| `plugin.Notifier` (`Notify(ctx, Event)`, `SupportedEvents()`) exists with **zero** implementors | `internal/plugin/plugin.go:98-102`; raw/11 §5 |
| The only notification-like channel is the webhook plugin: registered via `init()` into the global plugin registry, subscribes to the in-memory plugin `EventBus`, POSTs JSON to every configured URL | `internal/plugins/webhook/plugin.go:33,43-85,103-144` |
| Webhook delivery: no retry, no persistence, 10s timeout, no SSRF/egress policy, errors only logged by the bus | `plugin.go:59,116-143`; `internal/plugin/events.go:85-100` |
| Bus handlers run in goroutines with the **publisher's ctx**, so a request-scoped publish can cancel its own webhook deliveries | `internal/plugin/events.go:85-100` |
| HMAC signature `X-Audiobook-Signature-256: sha256=<hex>` over the body only — no timestamp, so a captured delivery can be replayed indefinitely | `internal/plugins/webhook/plugin.go:109-127` |
| Webhook URLs and HMAC secret come from `config.Plugins["webhook"].Settings` (plaintext config) | `internal/server/plugins_init.go:24-46`; `plugin.go:44-58` |
| Event vocabulary is audiobook-flavoured (`book.imported`, `dedup.merged`, `cover.changed`, `read_status.changed`, …) | `internal/plugin/events.go:18-31`; `plugin.go:146-159` |
| `mail.Sender` is generic and well-behaved: one recipient per `Message{To, Subject, Text, HTML}`, header-injection guard, TLS required to non-loopback hosts, AUTH only over TLS or loopback, one 15s deadline, never puts the password in an error; one implementation + `mailtest` recorder; used for invites | `internal/mail/mail.go:10-30,49-66,99-130`; `internal/mail/mailtest/recorder.go`; `internal/server/handlers/invite_delivery.go:49,217` |
| SMTP settings: `smtp_host/port/username/password/from` (env-bound) | `internal/config/invite_config.go:40-44` |
| Users have an email and a verified-email notion | `internal/database/store.go:733-742`; `internal/database/iface_user.go:14` |
| *arr "Connect" notifications are generic provider plumbing with app event payloads | raw/16 §7.1 row 21 |

## 3. Go API

```go
package notify // github.com/falkcorp/mediacore/integrations/notify

type Severity uint8

const (
	SeverityInfo Severity = iota
	SeveritySuccess
	SeverityWarning
	SeverityError
	SeverityCritical
)

type Link struct {
	Label string
	URL   string // absolute; relative app paths are resolved against core.http.public_url
}

// Notification is channel-neutral. Title/Body are plain text.
type Notification struct {
	ID       string            // ULID; idempotency key end to end (receivers dedupe on it)
	Topic    string            // an EVENT topic declared in the events catalog with notify metadata, e.g. "books.book.imported.v1"
	Severity Severity          // zero = the topic's default severity from the catalog
	Title    string
	Body     string
	Links    []Link
	Entity   *entity.Ref       // what it is about (03 §3.3 entity.Ref); deep links
	Fields   map[string]string // structured extras for webhook JSON (bounded: 32 keys, 1 KiB each)
	At       time.Time
}

// Notifier is what every channel adapter instance implements.
type Notifier interface {
	integrations.Instance
	Caps() Caps
	Send(ctx context.Context, n Notification, t Target) error
}

type Caps struct {
	HTML    bool
	MaxBody int // truncate above this (with "…")
}

// Target is one resolved destination for one delivery. v1 channels are
// global (admin-configured instances); a per-user Recipient arrives with
// per-user subscriptions (deferred, §1).
type Target struct {
	Instance integrations.Ref
}

func Adapter(m integrations.Meta,
	build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Notifier, error),
) integrations.Descriptor

// ---- publishing (the only entry point, COR2-3) -----------------------------------

// Publisher records notifications for delivery. Core subsystems call the
// *Service, which implements it for every core.* topic; a module holds the
// module-scoped Publisher the kernel builds with Service.For and hands out as
// module.Deps.Notify (06).
type Publisher interface {
	// Publish validates n, resolves the enabled global Rules for n.Topic and
	// n.Severity from the in-memory rule set, and stages ONE durable
	// core.notify.delivery.requested.v1 entry per resolved target through the outbox's
	// participant handle in b, so the entries commit atomically with the
	// caller's own state change (the reason
	// to notify and the notification cannot diverge). There is no enqueue-side
	// dedupe: n.ID is a fresh ULID per Publish, so two entries never share a
	// key, and receivers dedupe on X-Mediacore-Delivery (§3.1; SIMP3-8).
	// b == nil: Publish writes its own batch. It never blocks on channel I/O. Zero matching targets:
	// nothing is staged (counted no_rule). n.Topic must be a declared event
	// topic whose catalog entry carries notify metadata (05-events TopicSpec).
	Publish(ctx context.Context, b *storage.Batch, n Notification) error
}

type Service struct{ /* unexported */ }

var _ Publisher = (*Service)(nil)

func (s *Service) Publish(ctx context.Context, b *storage.Batch, n Notification) error

// For returns the Publisher for one module: it refuses (ErrForeignTopic) any
// topic outside "<module>.", so a module can notify only about its own topics.
// Kernel-only; modules never see the *Service.
func (s *Service) For(module string) Publisher

var (
	ErrUndeclaredTopic = errors.New("notify: topic not in the events catalog")
	ErrNoNotifyMeta    = errors.New("notify: topic has no notify metadata")
	ErrForeignTopic    = errors.New("notify: topic belongs to another module")
	ErrFieldsTooLarge  = errors.New("notify: fields exceed bounds")
)

// Rule is global routing data, written by a holder of core:integrations.manage.
// Rules are small admin data: the Service keeps them in memory, loaded at
// start and replaced when a PutRule/DeleteRule batch commits.
type Rule struct {
	ID          string
	Topics      []string // exact event topic names or "<module>.*"
	MinSeverity Severity
	Instances   []integrations.Ref // global channel instances
	Enabled     bool
}

func (s *Service) PutRule(ctx context.Context, r Rule) error
func (s *Service) DeleteRule(ctx context.Context, id string) error
func (s *Service) Rules(ctx context.Context) iter.Seq2[Rule, error]

// RuleStore persists Rules (R4-1). SDK surface, frozen by the API-stability
// gate, holding only the native contract (SIMP5-4). Core ships
// NativeRuleStore over c/notify/rules/<id> (codec.JSON[notify.Rule](1); lands
// with notify at 09 S7, SIMP4-3); an app whose routing predates core passes a
// legacy implementation as Manifest.LegacyStores.NotifyRules (06). Store
// model: ROUTED per record (05-metadata M1.1, SIMP5-1): both stores serve at
// once, and the Service sends a rule to the legacy store when that store
// covers the rule's ID, else to the native store. Coverage is the optional
// storage.Covering capability (Covers(id) with id = Rule.ID; 05-storage
// §5S.3.11 "Legacy capabilities"), detected by type assertion at P4c and
// asserted at compile time by the legacy store; a store without it covers
// every rule, so the native store implements none, and the capability goes
// with the legacy store at the integrations cutover. A legacy store refuses a
// rule its format cannot express with ErrRuleNotExpressible (422).
type RuleStore interface {
	Load(ctx context.Context) ([]Rule, error)
	Put(ctx context.Context, b *storage.Batch, r Rule) error
	Delete(ctx context.Context, b *storage.Batch, id string) error
}

func NativeRuleStore(ks storage.Keyspace) RuleStore

var ErrRuleNotExpressible = errors.New("notify: the rule's store cannot hold this rule")
func (s *Service) SendTest(ctx context.Context, inst integrations.Ref) error // core:integrations.manage; result is class + status only (05-metadata M1.3)
```

There is no `Registrar.NotificationTopic` and no `notify.TopicSpec` (SIMP-5): a module makes one of its
event topics notifiable in its single `events.TopicSpec` declaration (`Registrar.Topics`, 06), which carries
the topic's `Permission` and its notify metadata (display name, default severity, sample fields for
`SendTest`). Core's notifiable topics are declared by their owning files the same way (e.g.
`core.integration.health.changed.v1`, 05-metadata M1.4).

### 3.1 Shipped channels (v1)

| Adapter | Target | Settings (secret fields in *italics*) | Egress |
|---|---|---|---|
| `core.email` | fixed address list | `host`, `port`, `username`, *`password`*, `from`, `to` (list, required) | SMTP through `env.Gate`; private hosts need `Local` (05-metadata M1.3) |
| `core.webhook` | one URL | `url`, `method` (POST/PUT), `headers`, *`secret`* (HMAC), *`token`* (bearer), `legacy_headers` (bool; on, and only, for legacy-backed instances, §10) | `EgressConfigured`: a LAN receiver needs `Local`, which only an interactive `core:admin` holder sets (SEC2-7), or a `lan_allow` entry; a URL change clears *secret* and *token* (SEC-15) |

The webhook body is fixed (the `json` preset): `POST url`, body
`{"schema":"mediacore.notification.v1","id","topic","severity","title","body","links","entity","fields","at","app"}`.
Every webhook request carries `X-Mediacore-Delivery: <notification ID>.<instance>` (receiver-side dedupe for
at-least-once delivery) and, when a secret is set, `X-Mediacore-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret,
"<t>.<body>")>` so receivers can reject replays older than their tolerance. Legacy-backed webhook instances
(the ABO webhook plugin's URLs, §10) have `legacy_headers` on and also send `X-Audiobook-Event`/`X-Audiobook-Signature-256`
for the compatibility window (09). Email renders a fixed plain-text layout (title, body, links, fields).

## 4. Lifecycle

1. **Register**: channel adapters from `app.Options.Adapters`. Notifiable topics come from the events catalog;
   notify registers nothing of its own.
2. **Publish** (the only entry point; core via `Service`, modules via `Deps.Notify`): validate (declared event
   topic with notify metadata, own topic for a module Publisher, bounded fields) → resolve enabled global
   `Rule`s from the in-memory rule set → one durable entry `core.notify.delivery.requested.v1{Notification, Target}` per
   target, staged in the caller's batch (SIMP2-11: there is no dispatch stage). Nothing is deduplicated at
   enqueue (SIMP3-8): the same-batch stage cannot repeat a committed entry, and the receiver-side key is
   `X-Mediacore-Delivery` (§3.1), built from the payload.
   A topic's notify metadata alone produces nothing: an event published on the bus, or a durable message staged
   on a topic, never becomes a notification unless its producer also calls `Publish`.
3. **Deliver** (core outbox consumer `core.notify.deliver`, one entry per target; listed in the kernel's static
   core-consumer list, so outbox retention keeps an entry until it is acked, 05-events): render the channel's
   fixed body → `Gate.Acquire` (instance providerhttp limits) → `Send` with `core.notify.send_timeout` →
   classify → ack, or return a retryable error so the outbox retries with its backoff; permanent failures are
   dead-lettered and counted in instance health. An entry older than `core.notify.max_age` is dead-lettered
   and counted instead of sent (OPS2-20): deliveries left in `c/outbox/` by a rollback to a build without
   notify are delivered once on roll-forward if still fresh, never days late.
4. **Shutdown**: nothing in memory to flush — undelivered work is in the outbox by construction.

A rule change applies to notifications published after it commits, not to deliveries already staged.

Global channels deliver to whoever reads that channel, so who may route what is decided when the `Rule` is
written: `PutRule` requires `core:integrations.manage`, and the caller must satisfy each named topic's
declared `Permission` (a `"<module>.*"` pattern requires every matching topic's). Per-recipient RBAC
filtering at delivery time arrives with per-user subscriptions (deferred).

## 5. Error semantics

| Error path | Fail-open / closed | Why |
|---|---|---|
| `Publish` with an undeclared topic, a topic without notify metadata, or oversize fields | closed (error to caller) | Programming error; caught in tests. |
| `Publish` storage write fails | closed (caller's batch fails) | Same-batch outbox: the state change and the notification are one fact. |
| No rule matches | open (`Publish` stages nothing; counted `no_rule`) | Nobody asked to hear it. |
| Module `Publisher` given another module's (or a core) topic | closed (`ErrForeignTopic`) | A module may notify only about what it owns (COR2-3). |
| `PutRule` names a topic whose `Permission` the caller lacks | closed (403) | A rule must not route entities its author cannot see to a shared channel. |
| `ClassTransient`, `ClassRateLimited` from a channel | retry via outbox backoff (honour `RetryAfter`), up to `core.notify.max_attempts` | Receivers go down and come back. |
| `ClassAuth`, `ClassInvalid` (4xx other than 408/429), SMTP 5xx permanent reply, `mail.ErrInvalidHeader` | dead-letter after 1 attempt; instance `HealthAuthFailed`/`Degraded` (its `core.integration.health.changed.v1` reaches **other** channels through their rules) | Retrying cannot fix it; avoid a loop on the broken channel. |
| Attempts exhausted, or older than `core.notify.max_age` (including entries left pending across a rollback, OPS2-20) | dead-letter, counted; visible in UI with "retry" action | No silent loss, and no stale delivery days later. |
| Egress guard refuses a webhook URL | closed (`ClassRefusedLocal`, dead-letter) | Private and loopback targets are refused unless an interactive `core:admin` holder marked the instance `Local` or the host is in `lan_allow`; link-local always (05-metadata M1.3, SEC-15, SEC2-7). |
| SMTP to a non-loopback host without TLS | closed (refused before MAIL FROM) | Kept from `internal/mail/mail.go:16-20`. |

## 6. Concurrency contract

`Service` and adapters are goroutine-safe. Deliveries run on the outbox relay's worker pool (bounded by
05-events) — never a goroutine per handler with the publisher's ctx (the HEAD defect in
`internal/plugin/events.go:85-100`). Per instance, the providerhttp bucket (default 1 RPS, burst 5 for webhook;
0.5 RPS for email) bounds send rate; per-target outbox entries are independent, so one slow channel never
delays another. Each `Send` runs under its own timeout derived from a detached, shutdown-aware ctx.

## 7. Config keys

Channel instances are records in the one instance registry (05-metadata M1, SIMP-4), not config keys: native
rows, or legacy-backed webhooks served by the app's legacy instance store, routed per record (§10, R4-1, SIMP5-1); secrets are
`integrations.notify.<id>.<field>` (05-config §2.3 grammar). Rules are data in a `RuleStore` (§3): native rules in
`c/notify/rules/<id>`; for ABO, the one legacy-backed rule `legacy-webhooks` is served by books' legacy rule store
from the legacy webhook event list (§10).

Protection classes per 05-config §1.3.7 (SEC2-8). Channel URLs, credentials and `Local` follow the instance rules
of 05-metadata M1.3; rules need `core:integrations.manage` plus each named topic's `Permission` (§4).

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `core.notify.send_timeout` | duration | `15s` (email: `mail.DefaultTimeout`) | yes | none |
| `core.notify.max_attempts` | int | `10` | yes | none |
| `core.notify.max_age` | duration | `24h` (older undelivered, including entries pending across a rollback → dead-letter, counted; OPS2-20) | yes | none |

## 8. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_notify_published_total` | counter | `topic`, `severity` |
| `mediacore_notify_targets_total` | counter | `topic`, `result` (`resolved`, `no_rule`) |
| `mediacore_notify_deliveries_total` | counter | `instance`, `class` |
| `mediacore_notify_delivery_seconds` | histogram | `adapter` |
| `mediacore_notify_dead_letters` | gauge | `instance` |

`topic` is bounded by the events catalog.

## 9. Tests

`TestPublish_SameBatchAsCallerState`, `TestPublish_UndeclaredTopicRejected`, `TestPublish_TopicWithoutNotifyMetaRejected`,
`TestPublish_OneDeliveryPerTargetInCallerBatch`, `TestPublish_NoRuleStagesNothing`,
`TestPublisher_ForeignTopicRefused` (module Publisher; COR2-3), `TestNotifyMeta_BusEventSendsNothing`,
`TestPutRule_RequiresTopicPermission`, `TestDeliver_TransientRetriedPermanentDeadLettered`,
`TestDeliver_RetryAfterHonoured` (synctest), `TestDeliver_BrokenChannelAlertsOtherChannels`,
`TestDeliver_PublisherCtxCancelDoesNotCancelDelivery`, `TestWebhook_SignatureWithTimestamp`,
`TestWebhook_DeliveryHeaderStable`, `TestWebhook_JSONBodyGolden`, `TestWebhook_LegacyHeaders`,
`TestWebhook_PrivateURLNeedsLocal`, `TestEmail_ReusesMailTLSRules` (the `internal/mail` tests move with it),
`TestDeliver_EntryOlderThanMaxAgeDeadLettered` (synctest; the rollback case of OPS2-20),
`TestNativeRuleStore_RoundTrip` (SIMP4-3), `TestRules_LegacyCoveredIDGoesToLegacyStore` (with a fake legacy
`RuleStore` implementing `storage.Covering`: a covered ID goes only to it, every other rule only to
`c/notify/rules/`; R4-1, SIMP5-1), `TestRules_NativeStoreCoversAll` (a store without `storage.Covering` is sent every
rule; SIMP5-4), `TestPutRule_NotExpressibleRefused` (422). Store conformance: `notifytest.RuleStoreSuite(t,
factory)` (public test-support package `integrations/notify/notifytest`, 03 §3.3, MOD5-6) runs against
`NativeRuleStore`, and an app runs it against its legacy store; books'
own tests of its stores, with HEAD fixtures, cover the legacy webhook format: a Delete blanks its list entry so
later IDs are unchanged and HEAD's parser sees the remaining URLs, and the `legacy-webhooks` rule reads and writes
the legacy event list and refuses a topic outside the twelve-name mapping (§10).
Fakes: `coretest.FakeNotifier` (records `Send` calls; scriptable classes) and `coretest.WebhookReceiver(t)`
(`httptest.Server` that verifies signatures and records deliveries); `mailtest.Recorder` moves to
`coretest` as `coretest.MailRecorder`.

## 10. Migration from ABO

| ABO | Becomes |
|---|---|
| `internal/mail` (`Sender`, `SMTPSender`, `Message`) | `core/integrations/notify/mail` (public `mail.Sender` stays the primitive for transactional mail); `core.email` channel wraps it |
| `smtp_*` keys | no stored form at HEAD (environment only, 05-metadata M1.10): operator-set `smtp_*` values are the in-memory overlay that yields the effective `notify/email` instance (`Stored=false`) while set, never persisted (05-metadata M1.4 step 2, SIMP2-5); a `notify/email` saved through the API is a native row (password in `secrets` as `integrations.notify.email.password`) that older builds never read — they keep reading the environment. Like every native instance and native rule, it does nothing after a rollback, and events during the rollback are never delivered to it; only deliveries already pending in `c/outbox/` are sent on roll-forward, if fresher than `core.notify.max_age` (§4 step 3; 05-metadata M1.4 native store; OPS5-12). Invites keep their own `mail.Sender` (05-authn): before S7 it is built from the legacy SMTP settings as today; from S7 the kernel builds it from the effective `notify/email` settings, so SMTP is configured once (an S7-or-later change that 05-authn records; SIMP2-4) |
| `internal/plugins/webhook` + `config.Plugins["webhook"]` | at 09 S7 (with the instance registry; SIMP2-4) the webhook plugin is replaced by **legacy-backed** `notify/webhook-<n>` instances, served in place from `plugins.webhook` by books' legacy instance store (ABO code; no core adapter and no grant) until the integrations cutover (05-metadata M1.4 step 2a, M1.10; R4-1). `n` is the 1-based position in the comma-separated `urls` list, empty entries counted: books' store blanks the entry on a Delete and appends on a create, so under this build positions — and with them instance IDs and rule references — never shift, and HEAD's parser, which skips empty entries (`internal/plugins/webhook/plugin.go:48-53` @748fd787a), sees exactly the remaining URLs. `legacy_headers` is on for every legacy-backed webhook. The legacy `secret`, `plugins.webhook.enabled` and `events` are family-level (`Shared`): the secret moves to `secrets` on this build's first write, staged under each instance's name, and books' store replaces the blob value with the never-matching sentinel (05-config §2.2, R4-2), so after a rollback the older build signs with the sentinel and receivers reject its deliveries until the operator re-enters the secret there (stated in the release note); `events` is the stored form of the one legacy-backed global `Rule` `legacy-webhooks`, served by **books' legacy `RuleStore`** (`Manifest.LegacyStores.NotifyRules`), which targets every legacy-backed webhook and whose topics are the books topics of the mapping row below (`all` or empty = HEAD's default list, `plugin.go:146-159`); a `PutRule` on it accepts only those twelve topics and `MinSeverity` info (422 `ErrRuleNotExpressible` otherwise; other routing uses native rules). `Local` follows 05-metadata M1.10: the S7 upgrade grants none; a webhook whose host is an IP-literal private address is offered once as a pending `Local` candidate bound to the URL's origin, kept in books' extension family, which an admin accepts with `Registry.Confirm` (DATA5-9). If an older build reorders or edits the list after a rollback, the legacy-field hash no longer matches: that position's `Local` is suspended until an admin confirms it (`Registry.Confirm`), and its `secrets` copy is used only while the URL origin equals the one it was written for (05-metadata M1.4 step 2a) |
| plugin event types (`internal/plugin/events.go:18-31`) | books event topics declared notifiable in the events catalog: `book.imported`→`books.book.imported.v1`, `book.deleted`→`books.book.deleted.v1`, `metadata.applied`→`books.metadata.applied.v1`, `tags.written`→`books.tags.written.v1`, `file.organized`→`books.file.organized.v1`, `dedup.detected`→`books.dedup.detected.v1`, `dedup.merged`→`books.dedup.merged.v1`, `cover.changed`→`books.cover.changed.v1`, `read_status.changed`→`books.read-status.changed.v1`, `scan.completed`→`books.scan.completed.v1`, `book.quarantined`→`books.book.quarantined.v1`, `book.unquarantined`→`books.book.unquarantined.v1` (the last two exist at HEAD but are not in the webhook's default list, `internal/plugins/webhook/plugin.go:146-159`); books publishes through `Deps.Notify.Publish` in the same batch as the change (COR2-3) |
| `plugin.Notifier`, plugin `EventBus` (for notifications) | `plugin.Notifier` deleted (N4). The plugin `EventBus` keeps feeding the webhook plugin until S7 (S6 moves only the operations bus, SIMP3-10); at S7 the webhook plugin and its subscriptions go, and books publishes its 12 event types through `Deps.Notify` instead. `internal/plugin` itself is deleted at **S7**, in the same stage, once its last importer is rewired (18 non-test, non-mock files import it at HEAD, `git grep -l '/internal/plugin"' -- '*.go'` @748fd787a), gated by 04's stage-visibility gate (04 §2.1, §8.3; 09 S7). The notify-only bus role is `core/events` (05-events) |
| `mailSenderOverride` test seam (`internal/server/server.go:424`) | `coretest.MailRecorder` injected as the email instance |

## 11. Open items

1. Storm control: an import of 2,000 books would produce 2,000 notifications per channel. Proposed:
   per-rule `digest_window` that folds notifications of one topic into a summary — specified when the first
   high-volume topic is defined. A digest window, like per-user delivery (item 2), reintroduces a dispatch stage
   between `Publish` and delivery (one durable entry per notification, resolved and coalesced by a consumer);
   v1 has none (SIMP2-11).
2. Deferred features (§1 table) are listed for the owner; none has a consumer in S0–S10 (09 §9.3). When per-user
   delivery ships it reintroduces delivery-time RBAC filtering of each recipient against the topic's
   `Permission` for `Entity`, and personal instances are forced to `EgressPublicOnly`.

## Objections to 03

None. Layering note: `notify` (R4 L4) must not import `realtime` (L6); nothing in v1 needs it (the inbox, which
would signal through the events bus, is deferred).

## Out-of-scope defects found

- The plugin `EventBus` runs every handler with the publisher's ctx in a fresh goroutine
  (`internal/plugin/events.go:85-100`); any subscriber doing I/O is cancelled when a request-scoped publisher
  returns. Removed with `internal/plugin` (N4); listed because other subscribers than the webhook may exist in
  later code.

## Review history

Round 1 (`docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- SIMP-10 / cross-cutting resolution 1: v1 = contract + `core.email` + `core.webhook` (`json` + legacy headers)
  + global rules; inbox, per-user subscriptions, personal channels, templating and the ntfy/Gotify/Apprise/custom
  presets deferred; `personal_channels` key removed (defaults false when it ships).
- SIMP-5: notify's own topic catalog (`TopicSpec`, `Registrar.NotificationTopic`) removed; `Notification.Topic`
  is an event topic; the permission is the topic's declared `Permission`; books topic names take the
  `.v1` suffix and 03 §3.6 grammar.
- SIMP-4: channel instances in the 05-metadata M1 store; config instance row removed; secret names `integrations/notify/<id>/<field>` (superseded by the dotted form `integrations.notify.<id>.<field>` in the integration pass).
- COR-17: `*events.EntityRef` replaced by `*entity.Ref`.
- SEC-15: webhook egress `EgressConfigured` with the private-network guard; URL change clears secrets; `SendTest` returns class + status only.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-E request (SIMP-4) — secret names in dotted form; `notify/email` per 05-metadata M1.10, env/file-only SMTP values pin it while set (superseded in round 2 by the in-memory overlay, SIMP2-5).
- integration pass: R-H request — stage range S0–S10.

Round 2 (`docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- COR2-3 — `notify.Publisher` defined; modules get a topic-scoped one as `module.Deps.Notify` (`Service.For`,
  `ErrForeignTopic`); `Publish` stated as the only entry point; notify metadata alone and bus events produce
  nothing; books publishes through `Deps.Notify`; two tests.
- SIMP2-11 — dispatch stage removed: `Publish` resolves rules from the in-memory set and stages one
  `core.notify.deliver.v1` per target in the caller's batch; `TestDispatch_ResolvesAtDeliveryTime` deleted; §11
  records that digests or per-user delivery bring a dispatch stage back.
- SIMP2-4 — notify v1 (webhook import included) is built in 09 S7; invites keep `mail.Sender`, built from
  `notify/email` from S7 on (a 05-authn change), instead of reading through notify.
- OPS2-20 — `core.notify.max_age` stated as the bound for entries left pending across a rollback (dead-lettered
  and counted on roll-forward); test added. No second key: `max_age` already is that bound.
- COR2-4 — the deliver consumer is named as a kernel core consumer, so retention keeps undelivered entries.
- SIMP2-5 — SMTP env values are an in-memory overlay, never persisted (§10).
- DATA2-10 — `notify/email` and `notify/webhook-<n>` mirror back to the legacy keys until 09 C7 (§10).
  (Superseded in round 3 by R3-1: webhooks are read and written in place; `notify/email` has no legacy stored form.)
- SEC2-8 — §7 gains a Protection column.
- SIMP2-16 — the slash-form secret-name history line marked superseded by the dotted form.

Round 3 (`docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 — §10 webhook row: legacy-backed `notify/webhook-<n>` read and written in place in `plugins.webhook`
  (positional IDs kept stable by blanking on delete; family-level `secret`/`enabled`/`events`; the legacy-backed
  rule `legacy-webhooks`); secret moved on first write and blanked (rollback = re-enter); §10 SMTP row:
  `notify/email` is overlay or native row, never imported or mirrored; §3.1 and §7 wording; two tests.
- DATA3-3 / OPS3-8 — resolved by R3-1: no import, no mirror, so no C7 mirror stop and no blob deletion.
- SEC3-5 — webhook `Local` follows 05-metadata M1.10 (one-time, IP-literal, origin-bound grant).
- SIMP3-8 — `Publish` sets no enqueue idempotency key; receivers dedupe on `X-Mediacore-Delivery` (§3, §4 step 2).
- SIMP3-10 — §10: the plugin `EventBus` feeds the webhook plugin until S7; webhook plugin removed at S7; the
  `internal/plugin` deletion stage left to 04/09 with its importer count.
- SIMP3-12 — the DATA2-10 history line marked superseded.

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): §10: `internal/plugin` is deleted at S7, gated by 04's stage-visibility gate.

Round 4 (`docs/plans/platform-core/reviews/spec-round-4-dispositions.md`):

- R4-1 — `notify.RuleStore` (SDK) with `NativeRuleStore` over `c/notify/rules/` and the app's legacy store via
  `Manifest.LegacyStores.NotifyRules`; legacy-backed webhooks served by the app's legacy instance store; the
  legacy webhook format's tests move to books; §3, §7, §9, §10 webhook row (extension suspension replaces the
  origin check).
- R4-2 — §10: the legacy webhook secret is replaced by the never-matching sentinel, not blanked.
- COR4-15 — the durable delivery topic is `core.notify.delivery.requested.v1` (03 topic grammar).
- SIMP4-1 — `Deferred-build members` heading in §1 (inbox, subscriptions, personal channels, templating).
- SIMP4-3 — the native rule store's key and codec stated (§3); `TestNativeRuleStore_RoundTrip`.

Round 5 (`docs/plans/platform-core/reviews/spec-round-5-dispositions.md`):

- SIMP5-1 — §3, §7: `RuleStore` declared a routed store (both stores serve; per-rule routing by the legacy store's
  coverage of `Rule.ID`), matching 05-metadata M1.1.
- SIMP5-4 — §3: `Covers` removed from the frozen `RuleStore`; coverage is the optional `storage.Covering`
  capability (absent = covers every rule); `TestRules_NativeStoreCoversAll`.
- SIMP5-6 — §1 `Deferred-build members` table: identifier column only; `core.inbox`, the key and the presets in an
  unchecked column.
- MOD5-6 — §9: `notifytest` is the public test-support package `integrations/notify/notifytest`.
- DATA5-9 — §10 webhook row: no `Local` granted by the upgrade; pending candidate confirmed by an admin.
- OPS5-12 — §10 SMTP row: native instances and rules do nothing after a rollback; events meanwhile are not delivered.
