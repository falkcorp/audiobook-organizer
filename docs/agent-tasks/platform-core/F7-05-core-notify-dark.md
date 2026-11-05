<!-- file: docs/agent-tasks/platform-core/F7-05-core-notify-dark.md -->
<!-- version: 1.0.0 -->
<!-- guid: 81c13928-3234-49a7-99a4-49cf3b0519e5 -->
<!-- last-edited: 2026-11-05 -->

# F7-05 — Core notify, dark: integrations/notify with RuleStore, notify.Importer and notifytest, the core.notify.deliver consumer, the email channel, notify/webhook in core's signed format; internal/mail and mailtest moved

| Field | Value |
|---|---|
| ID | F7-05 |
| Release | R2, flip label F7 (dark PR) |
| Tier | **T3** (Opus / high): a delivery consumer and a signed webhook format; a wrong signing string lets a captured delivery replay |
| Wave | W30 (alone). Runs inside an announced **move window** (04 keys `F7 move internal/mail -> mediacore/integrations/notify/mail`, `F7 move internal/mail/mailtest -> mediacore/coretest`) |
| Depends on | F7-01 (integrations registry: adapters, instances, `Local`, origin binding), F6-01 (core outbox: deliveries staged in the caller's batch and consumed into a task) |
| Spec | 05-notify §1 (scope; deferred-build members), §3 (Go API: `Notification`, `Notifier`, `Caps`, `Target`, `Adapter`, `Publisher`, `Service` with `Publish`/`For`/`PutRule`/`DeleteRule`/`Rules`/`SendTest`, `Rule`, `RuleStore`/`NativeRuleStore`, `Importer`/`NativeImporter`), §3.1 (the shipped channels `core.email` and `core.webhook`, the fixed body, `X-Mediacore-Delivery`, `X-Mediacore-Signature`, reserved headers), §4–§8, §9 (tests), §10 (migration: `internal/mail`, the `mailSenderOverride` seam); 04 §8.1 F7, §3.1 (`internal/mail`), §3.4 (`mailtest`) |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's webhook plugin keeps
delivering in HEAD's format through `internal/plugin` until F7-09; invites keep sending mail exactly as now (the moved
`mail.Sender` is the same code at a new path). No rule, instance or delivery exists in ABO's store.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-05-notify" -b agent/platform-core-f7-05-notify origin/main
cd "$REPO/.worktrees/pc-f7-05-notify"
git rebase origin/main
```

Announce the move window one working day ahead naming the two 04 rows above.

## Goal

1. `core/integrations/notify` (05-notify §3): types, `Publisher`, `Service`, rules with `NativeRuleStore` over
   `c/notify/rules/`, `NativeImporter`, `SendTest`, and the `core.notify.deliver` outbox consumer (deliveries staged
   one per target in the caller's batch, consumed into a task, retried on transient classes, dead-lettered on
   permanent ones and after `core.notify.max_age`, `Retry-After` honoured).
2. `core/integrations/notify/webhook`: adapter `core.webhook` with the one body format and the signed headers of
   §3.1 (D10: HEAD's `X-Audiobook-*` format is not kept); reserved headers refused on `Put`, `Test` and `Importer`.
3. `core/integrations/notify/mail`: HEAD's `internal/mail` (`Sender`, `SMTPSender`, `Message`) moved by `git mv`, and
   the `core.email` channel wrapping it with HEAD's TLS rules.
4. `core/integrations/notify/notifytest`: `RuleStoreSuite`; `coretest.FakeNotifier`, `coretest.WebhookReceiver(t)`,
   and `internal/mail/mailtest.Recorder` moved to `coretest.MailRecorder` (`core/coretest/mail.go`).
5. Re-point ABO's importers of `internal/mail` and `mailtest` (behaviour unchanged).

## Invariants (each is a 05-notify §9 test)

- **I1.** A delivery is staged in the same batch as the caller's state, one per target; no rule stages nothing
  (`TestPublish_SameBatchAsCallerState`, `TestPublish_OneDeliveryPerTargetInCallerBatch`,
  `TestPublish_NoRuleStagesNothing`).
- **I2.** Only declared, notifiable topics; a module's `Publisher` cannot publish another module's topic
  (`TestPublish_UndeclaredTopicRejected`, `TestPublish_TopicWithoutNotifyMetaRejected`,
  `TestPublisher_ForeignTopicRefused`); a bus event alone sends nothing (`TestNotifyMeta_BusEventSendsNothing`).
- **I3. Signature.** `X-Mediacore-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<delivery>.<url>.<body>")>`;
  an altered timestamp, delivery ID or destination URL fails verification (`TestWebhook_SignatureWithTimestamp`);
  the body is the one golden format (`TestWebhook_JSONBodyGolden`). These two are the **webhook delivery test** of
  09's R2 verification list, which this row owns.
- **I4.** Reserved headers (`X-Mediacore-*`, `Authorization`, `Content-Type`, any case) are refused
  (`TestWebhook_ReservedHeadersRefused`); a private URL needs `Local` (`TestWebhook_PrivateURLNeedsLocal`).
- **I5.** The publisher's ctx cancellation never cancels a delivery
  (`TestDeliver_PublisherCtxCancelDoesNotCancelDelivery`); `core.integrations.disabled` keeps entries pending without
  using up attempts (`TestDeliver_IntegrationsDisabledKeepsEntryPending`).
- **I6. Move without change.** Invite and guest-token mail behave as before (their existing tests pass with the new
  import path).
- **I7. Dark.** HEAD's webhook plugin keeps delivering in HEAD's format until F7-09.

## Files

| Action | Path |
|---|---|
| create | `core/integrations/notify/` |
| create | `core/integrations/notify/webhook/` |
| move (`git mv`) + create | `internal/mail/` → `core/integrations/notify/mail/` (plus the `core.email` channel) |
| create | `core/integrations/notify/notifytest/` |
| move (`git mv`) | `internal/mail/mailtest/recorder.go` → `core/coretest/mail.go` (`MailRecorder`) |
| modify | `internal/server/` (re-point `guest_service_tokens.go`, `invite_guest_access.go`, `server.go`, `handlers/invite_delivery.go` and their tests) |
| modify | `internal/arch/layering_test.go` (`internal/mail`, `internal/mail/mailtest` keys removed), `scripts/ci/short_test_weights.json` (only if it names them) |
| create | one `changelog.d/` fragment (headerless) |

`coretest.FakeNotifier` and `coretest.WebhookReceiver` go in `core/coretest/` beside `mail.go` (new files in a package
this row already edits).

## Re-verify before editing (expected results at your base)

```bash
M=github.com/falkcorp/audiobook-organizer
# 1. internal/mail and its importers. [evidence: mail.go, mail_test.go, mailtest/recorder.go;
#    importers: internal/server/guest_service_tokens.go, handlers/invite_delivery.go (+test), invite_guest_access.go, server.go;
#    mailtest importers: handlers/invite_delivery_test.go, guest_service_tokens_test.go]
ls internal/mail internal/mail/mailtest
grep -rl --include='*.go' "\"$M/internal/mail\"" . | grep -v '^./internal/mail/'
grep -rl --include='*.go' "\"$M/internal/mail/mailtest\"" . | grep -v '^./internal/mail/'
# 2. F6-01's outbox. Expected: ConsumeIntoTask and OutboxConsumers
grep -rn 'func ConsumeIntoTask\|OutboxConsumers' core/outbox/*.go | head -3
# 3. F7-01's registry. Expected: Registry and NewDescriptor
grep -n 'type Registry struct\|func NewDescriptor' core/integrations/*.go
# 4. HEAD's webhook format still served by the plugin (stays until F7-09). Expected: X-Audiobook-Signature-256
grep -rn 'X-Audiobook-Signature-256' --include='*.go' internal/plugins/webhook/ | head -2
# 5. Nothing of notify exists. Expected: 0
ls core/integrations/notify 2>/dev/null | wc -l
```

STOP if grep 1 finds an importer outside `internal/server/`, if F6-01's outbox or F7-01's registry is missing, or if
`core/integrations/notify` exists.

## Steps

1. Read 05-notify §3–§10 in full; copy every type and signature exactly.
2. Write the tests first (below), including the body golden and the signature cells.
3. `git mv internal/mail/mail.go core/integrations/notify/mail/mail.go` (with its test) and
   `git mv internal/mail/mailtest/recorder.go core/coretest/mail.go`; rename the recorder type to `MailRecorder`;
   re-point the importers. Commit this first and make it build alone.
4. `notify` package: `Service`, `Publish` staging through the outbox in the caller's batch, `For(module)`, rules,
   `NativeRuleStore`, `NativeImporter`, `SendTest`, the `core.notify.deliver` consumer.
5. `core.webhook` adapter (fixed body, delivery header, signature, reserved headers, `EgressConfigured`, URL change
   clears `secret` and `token`); `core.email` channel over `mail.Sender` (destination fields `host`/`port`).
6. `notifytest.RuleStoreSuite`; `coretest.FakeNotifier`, `coretest.WebhookReceiver`.
7. Layering keys, headers, fragment, acceptance.

## Code contract (05-notify §3, verbatim excerpts; copy the rest from the spec)

```go
package notify // github.com/falkcorp/mediacore/integrations/notify
type Publisher interface {
	Publish(ctx context.Context, b *storage.Batch, n Notification) error
}
func (s *Service) Publish(ctx context.Context, b *storage.Batch, n Notification) error
func (s *Service) For(module string) Publisher
func (s *Service) PutRule(ctx context.Context, r Rule) error
func (s *Service) DeleteRule(ctx context.Context, id string) error
func (s *Service) Rules(ctx context.Context) iter.Seq2[Rule, error]
type RuleStore interface {
	Load(ctx context.Context) ([]Rule, error)
	Put(ctx context.Context, b *storage.Batch, r Rule) error
	Delete(ctx context.Context, b *storage.Batch, id string) error
}
func NativeRuleStore(ks storage.Keyspace) RuleStore
type Importer interface {
	ImportRule(ctx context.Context, b *storage.Batch, r Rule) error
}
func NativeImporter(ks storage.Keyspace) Importer
func (s *Service) SendTest(ctx context.Context, inst integrations.Ref) error
```

Webhook wire format (05-notify §3.1): body
`{"schema":"mediacore.notification.v1","id","topic","severity","title","body","links","entity","fields","at","app"}`;
headers `X-Mediacore-Delivery: <notification ID>.<instance>` and, with a secret,
`X-Mediacore-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<delivery>.<url>.<body>")>`, `<url>` the configured
`url` exactly as stored.

## Tests (write first; spec names copied exactly, 05-notify §9)

`TestPublish_SameBatchAsCallerState`, `TestPublish_UndeclaredTopicRejected`,
`TestPublish_TopicWithoutNotifyMetaRejected`, `TestPublish_OneDeliveryPerTargetInCallerBatch`,
`TestPublish_NoRuleStagesNothing`, `TestPublisher_ForeignTopicRefused`, `TestNotifyMeta_BusEventSendsNothing`,
`TestPutRule_RequiresTopicPermission`, `TestDeliver_TransientRetriedPermanentDeadLettered`,
`TestDeliver_RetryAfterHonoured` (synctest), `TestDeliver_BrokenChannelAlertsOtherChannels`,
`TestDeliver_PublisherCtxCancelDoesNotCancelDelivery`, **`TestWebhook_SignatureWithTimestamp`**,
`TestWebhook_ReservedHeadersRefused`, `TestWebhook_DeliveryHeaderStable`, **`TestWebhook_JSONBodyGolden`**,
`TestWebhook_PrivateURLNeedsLocal`, `TestEmail_ReusesMailTLSRules` (the `internal/mail` tests move with it),
`TestDeliver_EntryOlderThanMaxAgeDeadLettered` (synctest), `TestNativeRuleStore_RoundTrip`,
`TestImportRule_ValidatesAndStagesInCallerBatch`, `TestDeliver_IntegrationsDisabledKeepsEntryPending`;
`notifytest.RuleStoreSuite` against `NativeRuleStore`. Not here: `TestWebhookMigration_InstancesAndRule` and
`TestWebhookMigration_SecretByValue` (F7-08).

## Acceptance (commands and expected output)

```bash
M=github.com/falkcorp/audiobook-organizer
# Core notify with the race detector. Expected: ok, no FAIL
(cd core && go test -count=1 -race ./integrations/notify/... ./coretest/...)

# The gate tests this row owns. Expected: ok
(cd core && go test -count=1 -run 'TestWebhook_JSONBodyGolden|TestWebhook_SignatureWithTimestamp' ./integrations/notify/...)

# internal/mail moved. Expected: nothing / 0
ls internal/mail 2>/dev/null
grep -rl --include='*.go' -e "\"$M/internal/mail\"" -e "\"$M/internal/mail/mailtest\"" . | wc -l

# ABO mail unchanged in behaviour; HEAD's webhook plugin still delivers. Expected: ok
go test -count=1 ./internal/server/... ./internal/plugins/webhook/...

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- Books' 12 notifiable topics and publishing through `Deps.Notify`, deleting `internal/plugin` and the webhook plugin,
  deliveries switching to core's format (F7-09).
- M6's webhook migration (F7-08).
- The notify inbox and per-user channels (deferred-build).

## Rollback

Revert the PR: `internal/mail` and `mailtest` return to their paths; nothing else in ABO changed.

## Repo rules

- Worktree off fresh `origin/main`; headers on every file; moved files keep their guid.
- PR description lists `internal/mail ==> core/integrations/notify/mail` and
  `internal/mail/mailtest ==> core/coretest` (09 §9.2.10).
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
  never squash; never `--admin`. The rename into `core/` triggers `scripts/ci/check_commits_build.py`: every commit
  builds.
