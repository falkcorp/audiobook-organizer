<!-- file: docs/specs/platform-core/05-notify.md -->
<!-- version: 1.0.0 -->
<!-- guid: 785812fd-5ddd-4540-be24-d061c0127788 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Notifications (`integrations/notify`)

Evidence commit `748fd787a`. Raw inputs: [`raw/11-integrations.md`](../../plans/platform-core/raw/11-integrations.md) §1, §5;
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §5; [`raw/16-arr-domain-research.md`](../../plans/platform-core/raw/16-arr-domain-research.md) §7.1 row 21;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7.
Builds on [05-metadata](05-metadata.md) §M1-§M2 and on the durable outbox of [05-events](05-events.md).

## 1. Purpose & scope

One way for core and modules to tell people that something happened: a `Notification` value, a `Notifier`
contract for channels, **routing** (global admin rules and per-user subscriptions, permission-filtered),
**templating**, and **delivery with retries through the outbox**. Shipped channels, all free and
self-hostable (D9): an always-present **in-app inbox**, **email** (SMTP, from `internal/mail`), and a
**generic webhook** with presets for **ntfy**, **Gotify** and the **Apprise API** (which itself fans out to
many services), plus a raw JSON mode.

Out of scope: transactional mail that is not a notification (invites, password reset) — those use the
`mail.Sender` primitive directly ([05-authn](05-authn.md)); media-server sync; the UI bell (07, which reads
the inbox through core routes).

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

// Notification is channel-neutral. Title/Body are plain text; channels that
// support markup render through templates.
type Notification struct {
	ID       string            // ULID; idempotency key end to end (receivers dedupe on it)
	Topic    string            // registered topic "<moduleID>.<name>", e.g. "books.import.completed", "core.integration.health.degraded"
	Severity Severity
	Title    string
	Body     string
	Links    []Link
	Entity   *events.EntityRef // what it is about; drives permission filtering and deep links
	Fields   map[string]string // structured extras for templates and webhook JSON (bounded: 32 keys, 1 KiB each)
	Audience Audience
	At       time.Time
}

// Audience narrows who may receive it. Empty = everyone the topic's
// permission admits.
type Audience struct {
	Users []auth.UserID // explicit recipients (e.g. the user who requested the import)
	// No role-name filter (N5: admin is a permission, never a role-name check).
	Require rbac.Requirement // additional requirement on top of the topic's; zero = none
}

// TopicSpec is registered by a module (or core) through Registrar.NotificationTopic.
type TopicSpec struct {
	ID              string          // "<moduleID>.<name>"
	DisplayName     string
	Description     string
	Permission      rbac.Requirement // a recipient must satisfy this (e.g. rbac.Need("books:read")) for the Entity; zero rejected
	DefaultSeverity Severity
	DefaultOn       bool            // whether new users are subscribed by default (inbox channel only)
	SampleFields    map[string]string // used by the template preview and the "send test" button
}

// Target is one resolved destination for one delivery.
type Target struct {
	Instance  integrations.Ref
	Recipient *Recipient // per-user deliveries (email address, personal topic); nil for global channels
}

type Recipient struct {
	User    auth.UserID
	Address string // verified email, or channel-specific address from the user's personal instance
	Locale  string
}

// Rendered is what a channel sends, produced by the template engine.
type Rendered struct {
	Subject string
	Text    string
	HTML    string          // email only
	JSON    json.RawMessage // webhook only
	Headers map[string]string
}

// Notifier is what every channel adapter instance implements.
type Notifier interface {
	integrations.Instance
	Caps() Caps
	Send(ctx context.Context, n Notification, t Target, r Rendered) error
}

type Caps struct {
	PerUser   bool // needs a Recipient (email); global channels are false
	HTML      bool
	MaxBody   int  // truncate above this (with "…")
	Templates []TemplateSlot // which template slots the channel uses: subject, text, html, json, headers
}

type TemplateSlot string

func Adapter(m integrations.Meta,
	build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Notifier, error),
) integrations.Descriptor

// ---- service (per App) ---------------------------------------------------------

type Service struct{ /* unexported */ }

// Publish records n for delivery. With a batch, the outbox entry commits
// atomically with the caller's own state change (the reason to notify and
// the notification cannot diverge). Without one, Publish writes its own batch.
// It never blocks on channel I/O.
func (s *Service) Publish(ctx context.Context, b storage.Batch, n Notification) error

// Rules (global, admin) and Subscriptions (per user) are data.
type Rule struct {
	ID          string
	Topics      []string // exact IDs or "<module>.*"
	MinSeverity Severity
	Instances   []integrations.Ref // global channel instances
	Tags        []string           // optional: only entities whose module reports these tags (module-defined)
	Enabled     bool
}

type Subscription struct {
	User        auth.UserID
	Topics      []string
	MinSeverity Severity
	Channels    []integrations.Ref // the user's personal instances, or core inbox/email
	Enabled     bool
}

func (s *Service) PutRule(ctx context.Context, r Rule) error
func (s *Service) PutSubscription(ctx context.Context, sub Subscription) error // caller may only write their own unless core:notify.manage
func (s *Service) Preview(ctx context.Context, topic string, inst integrations.Ref) (Rendered, error)
func (s *Service) SendTest(ctx context.Context, inst integrations.Ref, to *Recipient) error

// Inbox (the core.inbox channel's store, read by the UI shell).
func (s *Service) Inbox(ctx context.Context, user auth.UserID, q InboxQuery) iter.Seq2[InboxItem, error]
func (s *Service) MarkRead(ctx context.Context, user auth.UserID, ids ...string) error
```

Registrar slice (06 owns the type): `func (r *Registrar) NotificationTopic(t ...notify.TopicSpec)` (problems in `r.Err()`). Core registers its own
topics (`core.integration.health.degraded`, `core.integration.health.recovered`, `core.task.failed`,
`core.backup.failed`, `core.update.available`, `core.auth.new_login` …; owned by the respective 05 files).

### 3.1 Shipped channels

| Adapter | Kind of target | Settings (secret fields in *italics*) | Phase |
|---|---|---|---|
| `core.inbox` | per-user, always present, cannot be deleted | none | 1 |
| `core.email` | per-user (recipient's **verified** email) or fixed address list | `host`, `port`, `username`, *`password`*, `from`, `to` (fixed list, optional) | 1 (from `internal/mail`) |
| `core.webhook` | global or personal | `url`, `method` (POST/PUT), `preset` (`json`, `ntfy`, `gotify`, `apprise`, `custom`), `headers`, *`secret`* (HMAC), *`token`* (bearer/preset token), `body_template` (custom) | 1 |

Webhook presets (each is a fixed template set; they differ only in URL, headers and body):

| Preset | Request |
|---|---|
| `json` (default) | `POST url`, body `{"schema":"mediacore.notification.v1","id","topic","severity","title","body","links","entity","fields","at","app"}` |
| `ntfy` | `POST <server>/<topic>`, body = text; headers `Title`, `Priority` (1-5 from severity), `Tags` (topic), `Click` (first link); *token* as `Authorization: Bearer` |
| `gotify` | `POST <server>/message`, JSON `{"title","message","priority"}`; *token* as `X-Gotify-Key` |
| `apprise` | `POST <apprise-api>/notify/<key>`, JSON `{"title","body","type": info\|success\|warning\|failure}` |
| `custom` | operator `body_template` + headers; content type set by the operator |

Every webhook request carries `X-Mediacore-Delivery: <notification ID>.<instance>` (receiver-side dedupe for
at-least-once delivery) and, when a secret is set, `X-Mediacore-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret,
"<t>.<body>")>` so receivers can reject replays older than their tolerance. Instances migrated from the ABO
webhook plugin also send the legacy `X-Audiobook-Event`/`X-Audiobook-Signature-256` headers for the
compatibility window (09).

## 4. Lifecycle

1. **Register**: topics from core and modules; duplicate or foreign-prefix IDs fail boot. Channel adapters
   from `app.Options.Adapters` (`core.inbox` is always on).
2. **Publish**: validate (registered topic, bounded fields) → one outbox entry
   `core.notify.dispatch.v1{Notification}` in the caller's batch.
3. **Dispatch** (outbox handler): resolve targets **at delivery time** — global `Rule`s → global instances;
   `Subscription`s of users → personal channels — then filter every per-user target through RBAC: the user
   must satisfy the topic's `Permission` (and `Audience.Require`) for `Entity` (05-rbac decides scope).
   Disabled users and unverified email addresses are dropped. One outbox entry per target,
   `core.notify.deliver.v1{NotificationID, Target}`, idempotency key `ID + target`, written in one batch.
4. **Deliver** (outbox handler per target): render templates → `Gate.Acquire` (instance providerhttp
   limits) → `Send` with `core.notify.send_timeout` → classify → ack, or return a retryable error so the
   outbox retries with its backoff; permanent failures are dead-lettered and counted in instance health.
5. **Inbox**: `core.inbox` `Send` writes `c/notify/inbox/<user>/<ulid>` and publishes
   `core.notify.inbox.added.v1` on the events bus; `realtime` (a higher layer, R4) forwards it to the user's
   SSE stream. Retention task prunes read items after `core.notify.inbox.retention`.
6. **Shutdown**: nothing in memory to flush — undelivered work is in the outbox by construction.

### 4.1 Templating

`text/template` for subject/text/JSON/headers, `html/template` for email HTML, executed with a fixed data
type `{Notification, Topic, Recipient, App{Name, PublicURL}}` and a small function set (`upper`, `lower`,
`trunc`, `default`, `date`, `json`, `severityName`, `link`) — no file, env or network access. Templates
are per instance, optionally per topic, validated on `Put` (parse + execute against `TopicSpec.SampleFields`),
with a 64 KiB size cap and a 100 ms execution budget enforced by rendering under a ctx-checked writer.
Built-in defaults exist for every channel so zero configuration produces readable output.

## 5. Error semantics

| Error path | Fail-open / closed | Why |
|---|---|---|
| `Publish` with unregistered topic or oversize fields | closed (error to caller) | Programming error; caught in tests. |
| `Publish` storage write fails | closed (caller's batch fails) | Same-batch outbox: the state change and the notification are one fact. |
| No rule or subscription matches | open (dispatch completes with zero targets, counted) | Nobody asked to hear it. |
| RBAC denies a recipient | that target dropped, audited at debug level | Notifications must never leak entities a user cannot see. |
| Template execution error | fall back to the built-in default template; health `Degraded` with the error | A broken custom template must not silence alerts. |
| `ClassTransient`, `ClassRateLimited` from a channel | retry via outbox backoff (honour `RetryAfter`), up to `core.notify.max_attempts` | Receivers go down and come back. |
| `ClassAuth`, `ClassInvalid` (4xx other than 408/429), SMTP 5xx permanent reply, `mail.ErrInvalidHeader` | dead-letter after 1 attempt; instance `HealthAuthFailed`/`Degraded`; a `core.integration.health.degraded` notification goes to **other** channels | Retrying cannot fix it; avoid a loop on the broken channel. |
| Attempts exhausted | dead-letter; visible in UI with "retry" action | No silent loss. |
| Egress guard refuses a webhook URL | closed (`ClassRefusedLocal`, dead-letter) | Webhooks default to `EgressAny` (operator-configured, may be LAN ntfy/Gotify); personal (user-owned) webhook instances are forced to `EgressPublicOnly` so a non-admin cannot probe the LAN. |
| Inbox write fails | retried by the outbox like any channel | Same path, no special case. |
| SMTP to a non-loopback host without TLS | closed (refused before MAIL FROM) | Kept from `internal/mail/mail.go:16-20`. |

## 6. Concurrency contract

`Service`, adapters and the template cache are goroutine-safe. Deliveries run on the outbox relay's worker
pool (bounded by 05-events) — never a goroutine per handler with the publisher's ctx (the HEAD defect in
`internal/plugin/events.go:85-100`). Per instance, the providerhttp bucket (default 1 RPS, burst 5 for webhook;
0.5 RPS for email) bounds send rate; per-target outbox entries are independent, so one slow channel never
delays another. Each `Send` runs under its own timeout derived from a detached, shutdown-aware ctx.

## 7. Config keys

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| `core.integrations.notify.instances.<id>.*` | instance row | `core.inbox` only | yes |
| `core.notify.send_timeout` | duration | `15s` (email: `mail.DefaultTimeout`) | yes |
| `core.notify.max_attempts` | int | `10` | yes |
| `core.notify.max_age` | duration | `24h` (older undelivered → dead-letter) | yes |
| `core.notify.inbox.retention` | duration | `90d` | yes |
| `core.notify.inbox.max_per_user` | int | `1000` | yes |
| `core.notify.template_max_bytes` | int | `65536` | yes |
| `core.notify.personal_channels` | bool | `true` (allow users to add their own webhook/ntfy channels) | yes |

## 8. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_notify_published_total` | counter | `topic`, `severity` |
| `mediacore_notify_targets_total` | counter | `topic`, `scope` (`global`, `user`), `result` (`resolved`, `rbac_denied`, `unverified`) |
| `mediacore_notify_deliveries_total` | counter | `instance`, `class` |
| `mediacore_notify_delivery_seconds` | histogram | `adapter` |
| `mediacore_notify_dead_letters` | gauge | `instance` |
| `mediacore_notify_template_fallbacks_total` | counter | `instance` |

`topic` is bounded by registration.

## 9. Tests

`TestPublish_SameBatchAsCallerState`, `TestPublish_UnregisteredTopicRejected`,
`TestDispatch_ResolvesAtDeliveryTime` (rule edited between publish and dispatch),
`TestDispatch_RBACFiltersRecipients`, `TestDispatch_UnverifiedEmailDropped`,
`TestDeliver_TransientRetriedPermanentDeadLettered`, `TestDeliver_RetryAfterHonoured` (synctest),
`TestDeliver_BrokenChannelAlertsOtherChannels`, `TestDeliver_PublisherCtxCancelDoesNotCancelDelivery`,
`TestTemplate_FallbackOnError`, `TestTemplate_SampleValidationOnPut`, `TestTemplate_NoEnvOrFileFuncs`,
`TestWebhook_SignatureWithTimestamp`, `TestWebhook_DeliveryHeaderStable`, `TestWebhook_Presets` (golden
requests for json/ntfy/gotify/apprise), `TestWebhook_PersonalInstancePublicOnly`,
`TestEmail_ReusesMailTLSRules` (the `internal/mail` tests move with it), `TestInbox_RealtimeEventAndRetention`.
Fakes: `coretest.FakeNotifier` (records `Send` calls; scriptable classes) and `coretest.WebhookReceiver(t)`
(`httptest.Server` that verifies signatures and records deliveries); `mailtest.Recorder` moves to
`coretest` as `coretest.MailRecorder`.

## 10. Migration from ABO

| ABO | Becomes |
|---|---|
| `internal/mail` (`Sender`, `SMTPSender`, `Message`) | `core/integrations/notify/mail` (public `mail.Sender` stays the primitive for transactional mail); `core.email` channel wraps it |
| `smtp_*` keys | one `notify/email` instance at first boot (password into `secrets`); auth invites read the same instance's sender through `notify` (05-authn), so SMTP is configured once |
| `internal/plugins/webhook` + `config.Plugins["webhook"]` | one `notify/webhook-<n>` instance per configured URL (preset `json`, legacy headers on), a global `Rule` for the configured event list mapped to `books.*` topics; secret into `secrets` |
| plugin event types (`internal/plugin/events.go:18-31`) | books topics: `book.imported`→`books.book.imported`, `book.deleted`→`books.book.deleted`, `metadata.applied`→`books.metadata.applied`, `tags.written`→`books.tags.written`, `file.organized`→`books.file.organized`, `dedup.detected`→`books.dedup.detected`, `dedup.merged`→`books.dedup.merged`, `cover.changed`→`books.cover.changed`, `read_status.changed`→`books.read_status.changed`, `scan.completed`→`books.scan.completed`, `book.quarantined`→`books.book.quarantined`, `book.unquarantined`→`books.book.unquarantined` (the last two exist at HEAD but are not in the webhook's default list, `internal/plugins/webhook/plugin.go:146-159`); books publishes through `notify.Publish` in the same batch as the change |
| `plugin.Notifier`, plugin `EventBus` (for notifications) | deleted (N4); the notify-only bus role is `core/events` (05-events) |
| `mailSenderOverride` test seam (`internal/server/server.go:424`) | `coretest.MailRecorder` injected as the email instance |

## 11. Open items

1. Storm control: an import of 2,000 books would produce 2,000 notifications per channel. Proposed:
   per-rule `digest_window` that folds notifications of one topic into a summary — specified when the first
   high-volume topic is defined; the outbox design already allows it (dispatch can coalesce).
2. Localization of built-in templates: `Recipient.Locale` is carried; translations are a 07 concern.
3. Whether personal channels should also exist for email to non-account addresses (e.g. a family member)
   is a product decision; the default is verified account email only.

## Objections to 03

None beyond [05-metadata](05-metadata.md) "Objections to 03". Layering note: `notify` (R4 integrations layer)
must not import `realtime` (higher layer); the inbox therefore signals through the events bus (§4 step 5).

## Out-of-scope defects found

- The plugin `EventBus` runs every handler with the publisher's ctx in a fresh goroutine
  (`internal/plugin/events.go:85-100`); any subscriber doing I/O is cancelled when a request-scoped publisher
  returns. Removed with `internal/plugin` (N4); listed because other subscribers than the webhook may exist in
  later code.
