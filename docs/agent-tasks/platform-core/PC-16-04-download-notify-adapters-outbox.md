<!-- file: docs/agent-tasks/platform-core/PC-16-04-download-notify-adapters-outbox.md -->
<!-- version: 1.0.0 -->
<!-- guid: cca0095a-96df-4f05-8883-aa1fc7a74652 -->
<!-- last-edited: 2026-11-04 -->

# PC-16-04 — Download and notify adapters: Deluge into `integrations/download/deluge`; mail and webhook into `integrations/notify`; notify through the outbox

| Field | Value |
|---|---|
| ID | PC-16-04 |
| Title | Download and notify adapters: Deluge into integrations/download/deluge; mail and webhook into integrations/notify; notify through the outbox |
| Tier | **T3 — Opus / high.** Webhook bytes must stay HEAD's for `legacy_headers` instances, and the notify path must not double-deliver while HEAD's webhook plugin still runs. |
| Wave | W16 (stage S7, release `S7`) |
| Depends on | PC-16-03 |
| Trailer | `Stage-Release: S7 1` |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §3 (rows `integrations/download`, `integrations/notify`), §4 (05-download, 05-notify §3), §5 rules 7, 9, 11, 15 |
| Owning spec | 05-download §3 (phase 1 only), §9, §10; 05-notify §3, §3.1, §4–§10; 09 S7 "Notify", "Moves" |

**Gate.** Merges only after the `S6` soak row exists (`check_soak.py` check (b)).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-16-04" -b feat/pc-16-04-download-notify origin/main
cd "$REPO/../abo-pc-16-04"
git worktree list
make web-deps
```

## Goal

1. **Download (phase 1 only).** `integrations/download` with `Client`, `FileLister`, `Relocator`, `Adapter`,
   `Pool` (`Instances`, `Lease`, `Items`, `Files`, `Relocate`); the Deluge client becomes the `core.deluge` adapter in
   `integrations/download/deluge` (RPC and re-login code reused, every method takes ctx, HTTP from `env.HTTP`). The
   package global client goes; books calls `Pool` (as `module.Deps.Downloads`). The legacy-backed instance
   `download/deluge` is served by PC-16-01's books store. No grab, monitor or pending-recovery code exists.
2. **Notify v1.** `integrations/notify` (`Service`, `Publisher`, `Rule`, `RuleStore`, `NativeRuleStore`, the
   outbox consumer `core.notify.deliver`), channels `core.email` (wrapping `internal/mail`, which moves to
   `integrations/notify/mail`) and `core.webhook`, plus `notifytest`. Books' legacy `RuleStore` serves the one rule
   `legacy-webhooks` from the legacy webhook event list.
3. **No double delivery.** In this PR no books code calls `Deps.Notify.Publish` for the 12 legacy event types:
   HEAD's webhook plugin stays the only deliverer until PC-16-06 switches the publishers and deletes it. The notify
   path is complete and tested but carries no production notification yet (05-notify §4: notify metadata alone
   produces nothing).

## Files

Create:
- `core/integrations/download/`, `core/integrations/download/deluge/`
- `core/integrations/notify/`, `core/integrations/notify/mail/`, `core/integrations/notify/webhook/`,
  `core/integrations/notify/notifytest/`

Modify:
- `internal/deluge/` — `client.go` moves to the adapter; `GetClient`, `ResetGlobalClientForTest`,
  `SetGlobalClientForTest` are deleted; `ProtectedPathCache`, discovery, import and mark-in-place stay (books) and
  use `Pool.Items`/`Pool.Files` across all download instances.
- `internal/mail/` — moves to `core/integrations/notify/mail`; `mailtest.Recorder` → `coretest.MailRecorder`.
- `internal/plugins/webhook/` — **not deleted here**; touch it only to capture the HEAD golden (step 3).
- `internal/server/handlers/` — `organize.go` (Deluge "follow the book" → `Pool.Relocate`), `invite_delivery.go`
  (mail import path).

See Planner notes for other files this PR must touch.

## Re-verify before editing

Counts at `748fd787a` (unchanged at this plan's HEAD). Zero hits or a moved count: STOP.

```bash
git grep -n 'func GetClient\|func ResetGlobalClientForTest\|func SetGlobalClientForTest' -- internal/deluge/integration.go   # 3 (:34, :77, :85)
git grep -n 'deluge.GetClient()' -- '*.go' | grep -v _test | wc -l          # 3
git grep -n 'req.Header.Set("X-Audiobook-Event"\|req.Header.Set("X-Audiobook-Signature-256"' -- internal/plugins/webhook/plugin.go   # 2 (:124, :126)
git grep -n 'func allEventTypes' -- internal/plugins/webhook/plugin.go       # 1 (:146; the default list, 10 types)
git grep -c 'EventType = ' -- internal/plugin/events.go                     # 12 (the 12 legacy event types)
git grep -n 'mailSenderOverride imail.Sender' -- internal/server/server.go   # 1 (:424)
ls internal/mail/mail.go internal/mail/mailtest/recorder.go                 # both exist

M=github.com/falkcorp/audiobook-organizer
git grep -l "\"$M/internal/deluge\"" -- '*.go' | grep -v _test.go | wc -l   # 9 (internal/maintenance/jobs, internal/plugins/deluge, internal/server, internal/server/handlers)
git grep -l "\"$M/internal/mail\"" -- '*.go' | grep -v _test.go | wc -l     # 5 (internal/mail/mailtest, internal/server, internal/server/handlers)

# Prerequisites (expect >= 1): the outbox (S6) and PC-16-01..03
git grep -n 'func Stage\[T any\]' -- core/outbox | head -1
git grep -n 'func NativeInstanceStore' -- core/integrations | head -1
git grep -n 'func (r \*Registry) Client' -- core/integrations/providerhttp | head -1
```

## Steps

1. Read 05-download §1, §3 (phase-1 part only; everything marked phase 2 or design sketch is **not** built),
   §4.1, §4.2, §4.4, §5–§10; 05-notify §1–§10; 05-events 5E.4 (outbox `Stage`, `Consume`, the kernel's static
   core-consumer list); 05-metadata M1.4 step 2a "Family-level fields" and the M1.10 table rows for Deluge and
   webhooks.
2. Write the tests below first.
3. **Golden first.** Before moving anything, capture the HEAD webhook delivery for a fixed event and secret from
   `internal/plugins/webhook` (fixed clock): exact body bytes, `Content-Type`, `X-Audiobook-Event`,
   `X-Audiobook-Signature-256` (`sha256=<hex HMAC-SHA256(secret, body)>`). Commit it as a testdata golden. The
   `core.webhook` channel with `legacy_headers` on must reproduce those bytes and headers exactly (09 S7 Done when);
   it may add `X-Mediacore-Delivery` and `X-Mediacore-Signature`, which HEAD receivers ignore. See Planner notes for
   the body rule.
4. Download: build `Pool` and the `core.deluge` adapter. `core.integrations.disabled`: a fake client sees no
   request and no snapshot changes. Books keeps `BookVersion.TorrentHash`/`BookFile.DelugeHash` unchanged and
   builds `download.ItemRef{Instance: <download/deluge ref>, ID: ItemID(hash)}` at each call site (no books row
   rewrite). `deluge_move_enabled`, `deluge_discovery_label`, `deluge_discovery_enabled` become
   `modules.books.download.*` settings with window aliases. `plugin.DownloadClient` is **not** implemented (it is
   deleted in PC-16-06).
5. Notify: `Publish` validates (declared topic with notify metadata, own topic for a module `Publisher`, bounded
   fields), resolves enabled global rules in memory and stages one durable entry
   `core.notify.delivery.requested.v1{Notification, Target}` per target **in the caller's batch**; nothing is
   deduplicated at enqueue. The consumer `core.notify.deliver` (add it to the kernel's static core-consumer list)
   renders the channel body, `Gate.Acquire`s, `Send`s under `core.notify.send_timeout` with a detached,
   shutdown-aware ctx, classifies, acks or returns retryable; dead-letters after `core.notify.max_attempts` or past
   `core.notify.max_age` (default 24 h). `core.integrations.disabled`: no `Send`, the entry is retried without
   consuming `max_attempts`, and `max_age` still dead-letters it.
6. Books' legacy `RuleStore` (`legacy.Stores.NotifyRules`, `legacy.Covering` asserted at compile time): the one
   rule `legacy-webhooks` targets every legacy-backed webhook; its topics are the books topics of 05-notify §10's
   mapping (`all` or empty = HEAD's default list); `PutRule` on it accepts only those twelve topics and
   `MinSeverity` info, else 422 `ErrRuleNotExpressible`. Declare the 12 `books.*.v1` topics notifiable in the
   events catalog (names verbatim from 05-notify §10).
7. Mail: `core.email` wraps `mail.Sender`; TLS and AUTH rules kept (`TestEmail_ReusesMailTLSRules`); the `smtp_*`
   operator values are the in-memory overlay that yields `notify/email` (`Stored=false`); a stored `notify/email` is
   a native row. Invites keep their own `mail.Sender`, built by the kernel from the effective `notify/email` settings
   from S7 (05-notify §10); rewire `invite_delivery.go`, `guest_service_tokens.go`, `invite_guest_access.go` and
   `server.go`'s test seam to `coretest.MailRecorder`.
8. Webhook instances are legacy-backed `notify/webhook-<n>` (PC-16-01's store). `legacy_headers` is on for every
   legacy-backed webhook and off for native ones; native webhooks use the fixed `mediacore.notification.v1` body.
9. Keep HEAD's plugin delivering. Assert in a test that, with this PR's wiring, a book event produces exactly one
   delivery per URL (from the plugin) and no outbox entry (name coined here:
   `TestS7Notify_NoDoubleDeliveryBeforeFlip`).

## Code contract (copy exactly; full declarations in the owning files)

```go
package download // 05-download §3, phase 1
type Client interface {
	integrations.Instance
	Caps(ctx context.Context) (Caps, error)
	Status(ctx context.Context) (ClientStatus, error)
	Items(ctx context.Context, f ItemFilter) ([]Item, error)
}
type FileLister interface { Files(ctx context.Context, id ItemID) ([]File, error) }
type Relocator interface { SetLocation(ctx context.Context, ids []ItemID, remoteDir string) error }
func Adapter(m integrations.Meta, build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Client, error)) integrations.Descriptor
func (p *Pool) Instances(proto Protocol) []integrations.Ref
func (p *Pool) Lease(ctx context.Context, ref integrations.Ref) (Client, func(), error)
func (p *Pool) Items(ctx context.Context, ref integrations.Ref, f ItemFilter) ([]Item, error)
func (p *Pool) Files(ctx context.Context, item ItemRef) ([]File, error)
func (p *Pool) Relocate(ctx context.Context, items []ItemRef, localDir library.Path) error

package notify // 05-notify §3
type Notifier interface { integrations.Instance; Caps() Caps; Send(ctx context.Context, n Notification, t Target) error }
func (s *Service) Publish(ctx context.Context, b *storage.Batch, n Notification) error
func (s *Service) For(module string) Publisher           // refuses foreign topics: ErrForeignTopic
func (s *Service) PutRule(ctx context.Context, r Rule) error
func (s *Service) DeleteRule(ctx context.Context, id string) error
func (s *Service) Rules(ctx context.Context) iter.Seq2[Rule, error]
func NativeRuleStore(ks storage.Keyspace) RuleStore      // c/notify/rules/<id>
func (s *Service) SendTest(ctx context.Context, inst integrations.Ref) error
```

Config keys (05-notify §7): `core.notify.send_timeout` 15s, `core.notify.max_attempts` 10, `core.notify.max_age`
24h. Metrics (05-notify §8, 05-download §8) are new with core, under ABO's namespace.

## Tests to write first

Download (05-download §9, phase 1): `TestPool_ItemsFillsLocalPath`, `TestPool_RelocateGroupsByInstance`,
`TestPaths_UnmappedRelocateRefused`, `TestPaths_WindowsRemote`, `TestDeluge_PrivateHostNeedsLocal`,
`TestPool_IntegrationsDisabledNoClientCall`, `TestDelugeInstance_LegacyBackedReadInPlace`, and
`coretest.DownloadConformance` (phase-1 cases) passed by `core.deluge` with recorded fixtures; live mode only behind
`MEDIACORE_LIVE_DOWNLOAD=1`, never in CI. Phase-2 names (`TestGrab_*`, `TestMonitor_*`) are **not** written.

Notify (05-notify §9): `TestPublish_SameBatchAsCallerState`, `TestPublish_UndeclaredTopicRejected`,
`TestPublish_TopicWithoutNotifyMetaRejected`, `TestPublish_OneDeliveryPerTargetInCallerBatch`,
`TestPublish_NoRuleStagesNothing`, `TestPublisher_ForeignTopicRefused`, `TestNotifyMeta_BusEventSendsNothing`,
`TestPutRule_RequiresTopicPermission`, `TestDeliver_TransientRetriedPermanentDeadLettered`,
`TestDeliver_RetryAfterHonoured`, `TestDeliver_BrokenChannelAlertsOtherChannels`,
`TestDeliver_PublisherCtxCancelDoesNotCancelDelivery`, `TestWebhook_SignatureWithTimestamp`,
`TestWebhook_DeliveryHeaderStable`, `TestWebhook_JSONBodyGolden`, `TestWebhook_LegacyHeaders` (byte-equal to the
step-3 golden), `TestWebhook_PrivateURLNeedsLocal`, `TestEmail_ReusesMailTLSRules`,
`TestDeliver_EntryOlderThanMaxAgeDeadLettered`, `TestNativeRuleStore_RoundTrip`,
`TestRules_LegacyCoveredIDGoesToLegacyStore`, `TestRules_LegacyWithoutCoveringCoversAll`,
`TestRulesLegacyMirrorsMatch`, `TestDeliver_IntegrationsDisabledKeepsEntryPending`,
`TestPutRule_NotExpressibleRefused`, and `notifytest.RuleStoreSuite` on both rule stores plus
`legacytest.NotifyRuleStoreSuite` on books' store (under `TestLegacyStoreSuites`). Books' own HEAD-fixture tests:
a Delete blanks its list entry so later IDs are unchanged and HEAD's parser sees the remaining URLs; the
`legacy-webhooks` rule reads and writes the legacy event list and refuses a topic outside the twelve.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                  # exit 0
(cd core && go test ./integrations/download/... ./integrations/notify/... ./coretest/... -race -count=1)   # all "ok"
go test ./internal/deluge/... ./internal/plugins/deluge/... ./internal/maintenance/jobs/... ./internal/server/... ./internal/books/... -race -count=1   # all "ok"
git grep -n 'func GetClient\|SetGlobalClientForTest\|ResetGlobalClientForTest' -- internal/deluge | grep -v _test    # 0 lines
find core/integrations/download \( -iname '*grab*' -o -iname '*monitor*' -o -iname '*pending*' \) -print   # no output
git grep -n -E 'func .*(Grab|Monitor|MarkImported|Tracked|Refresh)\b' -- core/integrations/download | grep -v _test.go | wc -l   # 0 (phase 2 is not built)
git grep -n 'Deps.Notify.Publish\|\.Notify\.Publish(' -- internal | grep -v _test.go | wc -l   # 0 (publishers switch in PC-16-06)
make ci                                                                        # exit 0
make -C core ci                                                                # exit 0
```

## Out of scope

Phase-2 download (grab, monitor, pending recovery, `Adder`/`Remover`/`Categorizer`); deleting `internal/plugin`
and the webhook plugin, and switching books' publishers to `Deps.Notify` (PC-16-06); the generic integration routes
and aliases (PC-16-06); per-user notification channels and digest windows (05-notify §11); storing `ItemRef` on
books rows (phase 2).

## Rollback

Binary swap. Deluge settings and its password stay in HEAD's blob fields; webhook URLs, secret and events stay in
`plugins.webhook`; no outbox entry is produced by this PR, so nothing is pending at rollback. A native rule or a
stored `notify/email` is inert on S6 (09 §9.2.1 row 8).

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on new or changed files; one headerless `changelog.d/` fragment.
- Conventional commits, each with `Stage-Release: S7 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- CLAUDE.md concurrency: deliveries run on the outbox relay's bounded pool (`core.outbox.relay_workers`), never a
  goroutine per handler; `Pool.Items` across instances stays sequential per instance unless a later PR measures it.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- **Spec conflict on the legacy webhook body.** 09 S7 Done when requires the "webhook golden (legacy headers and
  body) byte-equal to S6 for a `legacy_headers` instance", and 09 §9.6 row 29 keeps HEAD's event names per instance
  while `legacy_headers` is set; but 05-notify §3.1 says "the webhook body is fixed (the `json` preset)"
  (`mediacore.notification.v1`) and describes `legacy_headers` as adding only the two headers. HEAD's body is
  `json.Marshal(plugin.Event)` (`type`, `timestamp`, `book_id`, `user_id`, `data`) and its signature covers that
  body, so the two readings cannot both hold. This brief follows 09's done-check and the plan's tier reason (HEAD's
  bytes for `legacy_headers`); the worker states the difference in the PR description. The spec owner should amend
  05-notify §3.1.
- The row omits files this PR must change: `internal/server/` (`deluge_integration.go`, `deluge_discovery.go`,
  `server.go`'s mail seam, `guest_service_tokens.go`, `invite_guest_access.go`), `internal/plugins/deluge/` and
  `internal/maintenance/jobs/` (callers of the Deluge global), `internal/books/` (`legacy.Stores.NotifyRules`,
  `Deps.Downloads` wiring), `core/legacy/` (`legacytest.NotifyRuleStoreSuite`), `core/coretest/` (`MailRecorder`,
  `FakeNotifier`, `WebhookReceiver`, `FakeDownloadClient`, `DownloadConformance`) and the kernel's static outbox
  consumer list. `internal/server/` is shared with PC-16-03 and PC-16-06; the chain orders them.
