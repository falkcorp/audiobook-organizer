<!-- file: docs/specs/platform-core/05-notify.md -->
<!-- version: 1.13.1 -->
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

| In v1 (built for R2, in [09](09-abo-on-core.md)'s integrations flip, with the instance store and `providerhttp` it needs; SIMP2-4) | Deferred: design sketch (non-normative; the SDK freezes none of it, A-15); built when a second app or the owner asks |
|---|---|
| `Notification`, `Notifier`, `Publisher` (`Service.Publish`, and `module.Deps.Notify` for modules) | in-app **inbox** (`core.inbox`) and the UI bell (07) |
| `core.email` channel to a fixed address list (from `internal/mail`) | **per-user subscriptions** and per-user email to a verified address |
| `core.webhook` channel, preset `json` (the one wire format, §3.1) | **personal channels** (user-owned instances); when they ship, `personal_channels` defaults to **false** |
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

> **Design sketch (non-normative; A-15).** The SDK freezes none of these members; their contracts are settled when
> they are built.

The right-hand column of the v1 table. None is in the R2 build. The first column holds package-qualified Go
identifiers; adapter IDs, routes and keys are in the second column (SIMP5-6). The PR that builds a member deletes
its row (SIMP4-1):

| Member | Also deferred (not checked) | Lands with | Contract |
|---|---|---|---|
| — | adapter `core.inbox` and its read routes | a second app or the owner's request | in-app inbox and the UI bell (07) |
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
// gate. Core ships NativeRuleStore over c/notify/rules/<id>
// (codec.JSON[notify.Rule](1); lands with notify in R2, SIMP4-3); it is the
// only implementation (no app supplies its own). PutRule and DeleteRule
// commit their own batch under storage.RetryOnConflict; the mutex that
// serialises the Service's swap of its in-memory rule set is taken before the
// commit and never inside it.
type RuleStore interface {
	Load(ctx context.Context) ([]Rule, error)
	Put(ctx context.Context, b *storage.Batch, r Rule) error
	Delete(ctx context.Context, b *storage.Batch, id string) error
}

func NativeRuleStore(ks storage.Keyspace) RuleStore

// Importer is what an app's cutover step calls to write its pre-core routing
// as native rules (R2 step M6; §10). The kernel builds it over
// c/notify/rules/ and hands it to the step in module.Importers (06 §6.4.2).
// ImportRule validates r
// as PutRule does, except the author's topic permissions (the step runs as
// the app's system actor), and stages the row into b, so the rule commits with
// the step's deletion of the rows it came from.
type Importer interface {
	ImportRule(ctx context.Context, b *storage.Batch, r Rule) error
}

func NativeImporter(ks storage.Keyspace) Importer
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
| `core.email` | fixed address list | `host`, `port`, `username`, *`password`*, `from`, `to` (list, required) | SMTP through `env.Gate`; private hosts need `Local` (05-metadata M1.3); `host` and `port` are its destination fields (origin `smtp://host:port`), to which an operator-sourced *password* is bound by origin plus provenance (05-metadata M1.4 step 2, SEC7-3, R8-5); a change to `host` or `port` clears the stored *password* unless the write re-supplies it, exactly like a URL change (05-metadata M1.3 `Put`; lead decision, round 7) |
| `core.webhook` | one URL | `url`, `method` (POST/PUT), `headers` (never a reserved name, below), *`secret`* (HMAC), *`token`* (bearer) | `EgressConfigured`: a LAN receiver needs `Local`, which only an interactive `core:admin` holder sets (SEC2-7), or a `lan_allow` entry; a webhook migrated from ABO carries the `Local` that 05-metadata M1.10 gives every migrated instance; a URL change clears *secret* and *token* (SEC-15) |

The webhook body is fixed (the `json` preset): `POST url`, body
`{"schema":"mediacore.notification.v1","id","topic","severity","title","body","links","entity","fields","at","app"}`.
Every webhook request carries `X-Mediacore-Delivery: <notification ID>.<instance>` (receiver-side dedupe for
at-least-once delivery) and, when a secret is set, `X-Mediacore-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret,
"<t>.<delivery>.<url>.<body>")>`, where `<delivery>` is the `X-Mediacore-Delivery` value and `<url>` the
instance's configured `url` exactly as stored, so receivers can reject replays older than their tolerance, and a
captured delivery replayed with a fresh delivery ID, or sent to another receiver that shares the secret (M6 copies
the one family secret into every `webhook-<n>` instance, §10), fails verification (SEC-8). **Reserved headers:**
`headers` can never set `X-Mediacore-*`, `Authorization` or `Content-Type` (compared case-insensitively); a
`Put`, `Test` or `Importer` write naming one is refused (`ClassInvalid`, nothing written), so a configured header
can never shadow the delivery ID, the signature, the bearer *token* or the body type. Email renders a fixed plain-text
layout (title, body, links, fields).

**Format (D10).** This is the only webhook format; HEAD's `X-Audiobook-Event` and `X-Audiobook-Signature-256`
headers and its payload are not kept, and there is no `legacy_headers` setting. Changing the format is the cheaper
choice: keeping HEAD's would put app-named headers in a core adapter (03 R1) and keep HEAD's replayable body-only
signature (§2) as a second signing path. The cost is one owner step at the R2 deploy, reconfiguring each receiver
(the R2 release note lists the header and payload change); the HMAC secret migrates by value (§10), so a receiver
changes its header names and signing string, not its secret.

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
   and counted instead of sent (OPS2-20): a delivery held back by a long outage or a stopped service is sent
   once if still fresh, never days late.
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
| `core.integrations.disabled` true (a restored copy, R6-7; 05-metadata M1.4) | closed for sending: `Lease` gives `ErrDisabled`, nothing is sent; the entry is returned retryable, not counted against `core.notify.max_attempts`, and still dead-lettered by `core.notify.max_age`. A schedule that fires against a disabled integration fails closed, but its run is enqueued and its clock advances. The optional owner rehearsal on a restored archive sets the switch and runs with `--network none` (09) | A copy of a production store must never deliver to production receivers (OPS6-4, SEC7-6); re-enabling on a real install delivers what is still fresh. |
| `ClassTransient`, `ClassRateLimited` from a channel | retry via outbox backoff (honour `RetryAfter`), up to `core.notify.max_attempts` | Receivers go down and come back. |
| `ClassAuth`, `ClassInvalid` (4xx other than 408/429), SMTP 5xx permanent reply, `mail.ErrInvalidHeader` | dead-letter after 1 attempt; instance `HealthAuthFailed`/`Degraded` (its `core.integration.health.changed.v1` reaches **other** channels through their rules) | Retrying cannot fix it; avoid a loop on the broken channel. |
| Attempts exhausted, or older than `core.notify.max_age` (including entries held back by a long outage, OPS2-20) | dead-letter, counted; visible in UI with "retry" action | No silent loss, and no stale delivery days later. |
| Egress guard refuses a webhook URL | closed (`ClassRefusedLocal`, dead-letter) | Private and loopback targets are refused unless an interactive `core:admin` holder marked the instance `Local` or the host is in `lan_allow`; link-local always (05-metadata M1.3, SEC-15, SEC2-7). |
| SMTP to a non-loopback host without TLS | closed (refused before MAIL FROM) | Kept from `internal/mail/mail.go:16-20`. |

## 6. Concurrency contract

`Service` and adapters are goroutine-safe. Deliveries run on the outbox relay's worker pool (bounded by
05-events) — never a goroutine per handler with the publisher's ctx (the HEAD defect in
`internal/plugin/events.go:85-100`). Per instance, the providerhttp bucket (default 1 RPS, burst 5 for webhook;
0.5 RPS for email) bounds send rate; per-target outbox entries are independent, so one slow channel never
delays another. Each `Send` runs under its own timeout derived from a detached, shutdown-aware ctx.

## 7. Config keys

Channel instances are records in the one instance registry (05-metadata M1, SIMP-4): native rows only (ABO's
webhooks are migrated into them in R2, §10); secrets are `integrations.notify.<id>.<field>` (05-config §2.3
grammar). Rules are data in the `RuleStore` (§3), native rules in `c/notify/rules/<id>`; for ABO the migrated
webhook routing is the rule `webhooks` (§10).

Protection classes per 05-config §1.3.7 (SEC2-8). Channel URLs, credentials and `Local` follow the instance rules
of 05-metadata M1.3; rules need `core:integrations.manage` plus each named topic's `Permission` (§4).

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `core.notify.send_timeout` | duration | `15s` (email: `mail.DefaultTimeout`) | yes | none |
| `core.notify.max_attempts` | int | `10` | yes | none |
| `core.notify.max_age` | duration | `24h` (older undelivered entries → dead-letter, counted; OPS2-20) | yes | none |

## 8. Metrics

Names use core's default namespace `mediacore`; an app's `app.Options.MetricNamespace` replaces it (03 §3.6, A-5).
All are new with core.

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
`TestDeliver_PublisherCtxCancelDoesNotCancelDelivery`, `TestWebhook_SignatureWithTimestamp` (cells: an altered
timestamp, delivery ID or destination URL each fails verification; SEC-8), `TestWebhook_ReservedHeadersRefused`
(each reserved name, in any case, refused on `Put`, `Test` and `Importer`; SEC-8),
`TestWebhook_DeliveryHeaderStable`, `TestWebhook_JSONBodyGolden` (the one format, §3.1; it is the webhook golden
of the R2 verification list, 09), `TestWebhook_PrivateURLNeedsLocal`, `TestEmail_ReusesMailTLSRules` (the
`internal/mail` tests move with it), `TestDeliver_EntryOlderThanMaxAgeDeadLettered` (synctest; OPS2-20),
`TestNativeRuleStore_RoundTrip` (SIMP4-3), `TestImportRule_ValidatesAndStagesInCallerBatch`,
`TestDeliver_IntegrationsDisabledKeepsEntryPending` (`core.integrations.disabled`: no `Send`, the entry is retried
without using up `core.notify.max_attempts`, and `core.notify.max_age` still dead-letters it; R6-7). Store
conformance: `notifytest.RuleStoreSuite(t, factory)` (public test-support package `integrations/notify/notifytest`,
03 §3.3, MOD5-6) runs against `NativeRuleStore`.
Migration (books, R2 step M6, over HEAD-written fixtures): `TestWebhookMigration_InstancesAndRule` (each non-empty
`urls` entry becomes `notify/webhook-<n>` with HEAD's enabled flag; `events` becomes the rule `webhooks` with the
mapped topics, `all` and empty giving HEAD's default list; an unknown event name is reported and dropped; the
`plugins.webhook` rows are gone after the step), `TestWebhookMigration_SecretByValue` (a receiver verifying with
HEAD's secret verifies the first native delivery's `X-Mediacore-Signature` with the same secret value).
Fakes: `coretest.FakeNotifier` (records `Send` calls; scriptable classes) and `coretest.WebhookReceiver(t)`
(`httptest.Server` that verifies signatures and records deliveries); `mailtest.Recorder` moves to
`coretest` as `coretest.MailRecorder`.

## 10. Migration from ABO

| ABO | Becomes |
|---|---|
| `internal/mail` (`Sender`, `SMTPSender`, `Message`) | `core/integrations/notify/mail` (public `mail.Sender` stays the primitive for transactional mail); `core.email` channel wraps it |
| `smtp_*` keys | nothing to migrate: HEAD's `json:"-"` fields are read from the environment and the operator file only and never stored (`internal/config/invite_config.go:28-29` @748fd787a; 05-config §1.3.7, §2.2, 05-metadata M1.10). Operator-set `smtp_*` values are the in-memory overlay that yields the effective `notify/email` instance (`Stored=false`) while set, never persisted (05-metadata M1.4 step 2, SIMP2-5); they overlay and lock the matching fields of a stored `notify/email` row, under config's one operator lock (05-config §1.3.5). An operator-set `smtp_password` is bound to the destination `smtp://host:port` by origin plus provenance (05-metadata M1.4 step 2; SEC7-3, R8-5): it is served while the effective host and port equal the operator-set `smtp_host`/`smtp_port` (or the adapter default for one the operator does not set), or equal the row's `OperatorSecretOrigin`, which a ProtectAdmin `Put` records, so an env password beside an admin-set host works (`TestOperatorSecret_AdminStoredHostServed`, SEC8-2); beside a host or port that only a non-admin `core:integrations.manage` holder chose, the password is withheld (no `AUTH` is sent, nothing is delivered through that instance, health `operator_secret_withheld`). A `notify/email` saved through the API is a native row (password in `secrets` as `integrations.notify.email.password`). Invites keep their own `mail.Sender` (05-authn): R0 and R1 build it from the environment as HEAD does; from R2 the kernel builds it from the effective `notify/email` settings, so SMTP is configured once (a change 05-authn records; SIMP2-4) |
| `internal/plugins/webhook` + `config.Plugins["webhook"]` | replaced in R2, in the integrations flip (09), by native `notify/webhook-<n>` instances and one native rule. Books' cutover step M6 (05-metadata M1.10) decodes `plugins.webhook` once and writes through the importers (05-metadata M1.3 `Importer`, §3 `Importer`): one `notify/webhook-<n>` instance per non-empty entry of the comma-separated `urls` list (`n` = its 1-based position among the entries HEAD's parser keeps, `internal/plugins/webhook/plugin.go:48-53` @748fd787a), adapter `core.webhook`, method `POST`, `Enabled` = `plugins.webhook.enabled`; the family-level HMAC `secret` **by value** into each instance's `integrations.notify.webhook-<n>.secret` (encrypted under the keyring with the other blob secrets by the M2 secrets step, 05-config), so every receiver keeps its secret; and one native rule `webhooks` (`c/notify/rules/webhooks`) whose `Instances` are those instances, whose `Topics` are the books topics the mapping row below gives for the names in `events` (`all` or empty = HEAD's default list, `plugin.go:146-159`) and whose `MinSeverity` is info. An event name outside the twelve is listed in the step's report and left out of the rule (it never produced a delivery at HEAD either). The rows the step read are deleted in the same batch. From then on these are ordinary native instances and an ordinary rule: per-instance secrets, origin binding and the URL-change clearing of 05-metadata M1.3 apply, and nothing is family-level. Deliveries use §3.1's format (receivers are reconfigured at the R2 deploy, §3.1 "Format") |
| plugin event types (`internal/plugin/events.go:18-31`) | books event topics declared notifiable in the events catalog: `book.imported`→`books.book.imported.v1`, `book.deleted`→`books.book.deleted.v1`, `metadata.applied`→`books.metadata.applied.v1`, `tags.written`→`books.tags.written.v1`, `file.organized`→`books.file.organized.v1`, `dedup.detected`→`books.dedup.detected.v1`, `dedup.merged`→`books.dedup.merged.v1`, `cover.changed`→`books.cover.changed.v1`, `read_status.changed`→`books.read-status.changed.v1`, `scan.completed`→`books.scan.completed.v1`, `book.quarantined`→`books.book.quarantined.v1`, `book.unquarantined`→`books.book.unquarantined.v1` (the last two exist at HEAD but are not in the webhook's default list, `internal/plugins/webhook/plugin.go:146-159`); books publishes through `Deps.Notify.Publish` in the same batch as the change (COR2-3) |
| `plugin.Notifier`, plugin `EventBus` (for notifications) | `plugin.Notifier` deleted (N4). The plugin `EventBus` keeps feeding the webhook plugin until R2's integrations flip (the events flip moves only the operations bus, SIMP3-10); in that flip the webhook plugin and its subscriptions go, and books publishes its 12 event types through `Deps.Notify` instead. `internal/plugin` itself is deleted in **the same flip**, once its last importer is rewired (20 non-test files in 10 packages import it at the evidence commit, two of them the generated mocks in `internal/server/handlers/mocks`, which the deleting PR regenerates or deletes; `git grep -l '/internal/plugin"' 748fd787a -- '*.go' | grep -v _test.go`; informative, ADV-11; 09 and 04 §8.3 give the same count); the deleting PR does not build while any importer remains (04 §8.3 is the informative map; 09). The notify-only bus role is `core/events` (05-events) |
| `mailSenderOverride` test seam (`internal/server/server.go:424`) | `coretest.MailRecorder` injected as the email instance |

## 11. Open items

1. Storm control: an import of 2,000 books would produce 2,000 notifications per channel. Proposed:
   per-rule `digest_window` that folds notifications of one topic into a summary — specified when the first
   high-volume topic is defined. A digest window, like per-user delivery (item 2), reintroduces a dispatch stage
   between `Publish` and delivery (one durable entry per notification, resolved and coalesced by a consumer);
   v1 has none (SIMP2-11).
2. Deferred features (§1 table) are listed for the owner as a design sketch (non-normative, A-15); none has a
   consumer in R0–R3 (09). When per-user
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

Round 6 (`docs/plans/platform-core/reviews/spec-round-6-dispositions.md`):

- R6-3 / SIMP6-2 — §3: `storage.Covering` → `legacy.Covering`, a member of package `legacy` outside the
  API-stability gate, named by the Service through an unexported mirror checked by `export_test.go`; the legacy
  store is the `NotifyRules` field of `legacy.Stores`; §9 `TestRulesLegacyMirrorsMatch`; §10 wording.
- R6-7 / OPS6-4 — §5 row and §9 `TestDeliver_IntegrationsDisabledKeepsEntryPending`: with
  `core.integrations.disabled` nothing is sent (05-metadata M1.4).
- SEC6-5 — §10 SMTP row: SMTP env names are not `BelowStored`; the one precedence and trust rule of 05-metadata
  M1.4 step 2 applies.
- SIMP6-5 — §3, §9: only the legacy half implements `Covers` (without it, it covers every rule and the ledger
  records `legacy`); `TestRules_NativeStoreCoversAll` → `TestRules_LegacyWithoutCoveringCoversAll`.
- SIMP6-6 — §9: `legacytest.NotifyRuleStoreSuite`, the one legacy suite of `RuleStore`.
- SIMP6-8 — §1 deferred table: the `core.inbox` row's empty first cell is `—`.
- integration pass (round 6): §10 webhook row: the legacy secret is staged through `secrets`, whose books `legacy.SecretsStore.Supersede` writes the sentinel (one legacy-secret mechanism, lead decision).

Round 7 (`docs/plans/platform-core/reviews/spec-round-7-dispositions.md`):

- SEC7-3 — §3.1 `core.email` row and §10 SMTP row: `host`/`port` are the destination fields; an operator-set `smtp_password` is withheld while they are not operator-set (05-metadata M1.4 step 2).
- R7-6 / SEC7-6 / OPS7-3 — §5 `core.integrations.disabled` row: a schedule against a disabled integration fails closed but is enqueued and advances its clock; staging clears every legacy and native integration URL, webhooks included, at every stage.
- R7-7 / SIMP7-1 / COR7-2 — §3 `RuleStore` comment and §9 `TestRulesLegacyMirrorsMatch`: "export_test.go" → the mirror test (03 §3.3), asserted in `legacy_mirror_test.go` of `package notify_test`.
- SIMP7-5 — §3 `RuleStore` comment: the Service detects `legacy.Covering` once through its mirror; the kernel does not.
- integration pass (round 7): §3.1 `core.email` row: a `host` or `port` change clears the stored password unless re-supplied (lead decision, R-F item 6; 05-metadata M1.3).

Round 8 (`docs/plans/platform-core/reviews/spec-round-8-dispositions.md`):

- R8-5 / SEC8-2 — §3.1 `core.email` row and §10 SMTP row: an operator-set `smtp_password` is served while the effective host and port equal the operator's (or the default) or the row's `OperatorSecretOrigin`, recorded by a ProtectAdmin `Put` (05-metadata M1.4 step 2).
- R8-5 / SEC8-3 — §10 SMTP row: `notify/email` is native, so `ErrOperatorSecretBound` never applies to it.
- R8-3 / SIMP8-2 — §5 `core.integrations.disabled` row: staging clears legacy URLs only; native channels are covered by the switch.
- SIMP8-3 — §3 `RuleStore` comment: "the kernel asserts only the capabilities 03 §3.3 lists".

Advisor (`docs/plans/platform-core/reviews/spec-advisor-dispositions.md`):

- A-2 / ADV-S1 — §3.1, §10 webhook and SMTP rows: the webhook secret stays plaintext in the blob, read and written in place; no sentinel, extension record, pending `Local` candidate or `Registry.Confirm`; legacy-backed webhooks keep HEAD's reach and ProtectAdmin writers; `notify/email` keeps the native origin binding without `Registry.Confirm` or `ErrOperatorSecretBound`.
- A-4 / ADV-S3 — §1 "Deferred-build members": `archtest.DeferredMembersAbsent` deleted; §10 `internal/plugin` row: the stage-visibility gate replaced by the deleting PR's build.
- A-5 / ADV-S4 — §8: names under `app.Options.MetricNamespace`.
- A-12 / ADV-S6 — §5 `core.integrations.disabled` row: `staging-scrub` named.
- A-15 / ADV-S9 — §1 table, "Deferred-build members" and §11 item 2: deferred features labelled design sketch (non-normative), not frozen by the SDK.
- ADV-11 — §10 `internal/plugin` row: the importer count marked informative.
- integration pass (advisor): `OperatorSecretOrigin` named as the native row's (`notify/email` has no legacy row).
- Round 9:
  - R9-1 — §3 `RuleStore` comment: `PutRule`/`DeleteRule` commit through `storage.CommitLocked` with the legacy rule store's `legacy.WriteLock` (books: `configBlobSaveMu`); one instance per process.
  - R9-4 / SEC9-6 — §10 webhook row: creating a legacy-backed webhook or changing the family-level secret needs ProtectAdmin (05-metadata M1.3).
- integration pass (round 9): §3 `RuleStore` comment: the rule store returns the `legacy.LockConfigSettings` lock (`configBlobSaveMu`), passed to `storage.CommitLocked(ctx, b, d, stage, locks...)`; §10 `smtp_*` row: HEAD's `json:"-"` SMTP fields are environment and operator-file only, never stored, as at HEAD.
- Round 10:
  - COR10-5 — §3 `RuleStore` comment: the kernel resolves `legacy.LockConfigSettings` at P4c and hands the Service `SettingsLocks`; notify never calls `WriteLocks`.
  - R10-4 / COR10-2 — §3 `RuleStore` comment: the legacy rule store refuses a write staged into a batch without a hold (`storage.LocksHeld(b, l)`); `configBlobSaveMu` at its rank in 05-storage's total order.
  - R10-5 / DATA10-2 — §3 `RuleStore` comment: the gap-fill file step runs inside the `CommitLocked` hold.
  - DATA10-3 — §3 `RuleStore` comment: the rule-set mutex is taken before `CommitLocked`, never in a `stage`; the stage calls no books method that takes a HEAD mutex.
  - R10-10 / SEC10-2 / SEC10-3 — §10 webhook row: `Test` follows `Put`'s ProtectAdmin rule; with auth off, legacy-backed webhooks stay editable as at HEAD.
  - COR10-10 — §3 `RuleStore` comment: one instance per App.

Plan-phase consistency:

- PC-2 — §10: `internal/plugin` importers counted as 09 S7 and 04 §8.3 count them: 20 non-test files in 10 packages, two of them generated mocks.

- D10–D12 revision (2026-11-04): webhook format changed, not kept: §3.1's `json` preset with `X-Mediacore-Delivery`
  and the timestamped `X-Mediacore-Signature` is the only format; `legacy_headers` and HEAD's
  `X-Audiobook-*` headers removed (cheaper: no app-named headers in core, no second replayable signing path; one
  owner reconfiguration at the R2 deploy). The legacy `RuleStore` (routed model, `legacy.Covering`, mirror test,
  `CommitLocked` and its lock, `ErrRuleNotExpressible`, `legacytest`) deleted; `notify.Importer` added. §10: books'
  R2 cutover step migrates the webhook URLs to native `notify/webhook-<n>` instances, the HMAC secret by value
  into each, and the event list to one native rule `webhooks`; SMTP row without stage or rollback text. Stage
  references S0–S10/S7 re-keyed to R0–R3/R2; rollback and `staging-scrub` text removed from §4, §5, §7; tests
  reduced to the native suite plus two migration tests.

Round 13 (2026-11-04): SEC-8 (§3.1: the signature covers the timestamp, the delivery ID and the destination URL; reserved headers refused on write; §9 test cells).
