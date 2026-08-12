## 🔴 Stored ZERO values shadow every `scheduled.*` default — nothing has been scanning

Measured on production 2026-08-12 via `GET /api/v1/config`:

```
scheduled.library_scan   = {enabled: false, interval: 0, on_startup: false}
scheduled.dedup_refresh  = {enabled: true,  interval: 0, on_startup: false}
scheduled.author_split   = {enabled: true,  interval: 0, on_startup: false}
scheduled.db_optimize    = {enabled: false, interval: 0, on_startup: false}
scheduled.label_refinement = {enabled: false, interval: 0, on_startup: false}
```

Compare the shipped viper defaults (`internal/config/config.go` ~line 1100):

| key | default | on prod |
|---|---|---|
| `scheduled.library_scan.enabled` | **true** | false |
| `scheduled.library_scan.interval` | **360** | 0 |
| `scheduled.dedup_refresh.interval` | 360 | 0 |
| `scheduled.db_optimize.interval` | 1440 | 0 |
| `scheduled.label_refinement.interval` | 10080 | 0 |

**Every interval in the block is 0.** Not one default survived.

### Consequence

`library_scan` is the only unattended discovery path for newly added books. With
`interval: 0` it never gets a ticker, so **nothing has been scanning automatically**.
PR #2315 shipped the periodic scan enabled-by-default and is deployed — the code is
right, the stored config defeats it. A book copied into an import path is still never
noticed until somebody presses Scan by hand, which is the exact bug #2315 set out to fix.

Only four tasks got tickers at the last restart (`tombstone_cleanup`, `purge_deleted`,
`isbn_enrichment`, `cleanup_activity_log`) — they read their intervals from config
fields outside the `scheduled.*` block, which is why the scheduler looked healthy.

### Mechanism

Viper's precedence chain (flag > env > file > default) only arbitrates values read
*through* viper. This codebase reads viper **once** into a plain Go struct
(`config.AppConfig`), and then a second, independent loader
(`LoadConfigFromDatabase` → `applySetting`) mutates that same struct from DB rows.
Two writers, one struct, no precedence rule between them — and the DB writer runs last.

`persistence.go` has a comment asserting this is safe: *"an absent stored setting must
leave the viper default alone. That holds here because applySetting is a per-leaf-key
switch: a key that was never stored is simply never seen."* The reasoning is correct;
the premise is not. The keys **were** stored. The tell is `dedup_refresh.enabled: true`
and `author_split.enabled: true` when both default to `false` — something wrote explicit
values for the whole block. That is the signature of a full-struct save (`PUT /config`
serialises every field), where fields the caller never populated are written as their Go
zero value.

**In Go, `0` and `false` are indistinguishable from "unset".** Once a whole-struct save
lands, the default is dead permanently — no later default change can ever take effect.
That is why raising a default (as #2315 did) had no effect on an existing install, and
it will happen again to the next default anyone changes.

### Immediate repair (one API call, no code change)

```
PUT /api/v1/config   scheduled.library_scan = {enabled: true, interval: 360}
```

⚠️ Owner call, not applied: a full library scan is expensive on this library (see the
open "prod scans take hours" item), so turning on a 6-hourly scan has a real cost.
Decide the interval before enabling.

### Durable fix — see the settings-architecture design

The repair above fixes one key on one host. It does not stop the next default from being
shadowed. The structural options are in
`docs/design/2026-08-12-unified-settings-architecture.md`.
