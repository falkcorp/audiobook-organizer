<!-- file: docs/design/2026-08-12-unified-settings-architecture.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6d2f8a41-93cb-47e5-b0a2-5c7e1d94f38b -->
<!-- last-edited: 2026-08-12 -->

# Unified settings architecture — why viper/cobra did not prevent this

## The question

> "How does this keep happening? We have viper and cobra, which is supposed to give
> us one working settings system via config file or env var. Why does this keep
> happening?"

Fair question, and the answer is not "viper is bad" or "we configured it wrong".
Viper is doing exactly what it promises. It is simply **not in the path** where the
bug happens.

## What viper actually guarantees

Viper resolves a key through a precedence chain:

```
explicit Set()  >  flag  >  env  >  config file  >  key/value store  >  default
```

That guarantee applies **only to values read through viper**, at the moment you call
`viper.GetInt("scheduled.library_scan.interval")`. It is a property of the *read*, not
a property of the value.

## What this codebase actually does

```
                    ┌─────────────────────────────────────────┐
   viper defaults ──┤                                         │
   config.yaml    ──┤  viper.GetX(...)  ──►  config.AppConfig │   ← read ONCE, at boot
   env vars       ──┤                        (plain Go struct)│
   flags (cobra)  ──┘                                         │
                    └─────────────────────────────────────────┘
                                                  │
                                                  ▼
                    ┌─────────────────────────────────────────┐
   DB settings   ───┤  LoadConfigFromDatabase → applySetting   │   ← MUTATES the same
   rows             │  c.Scheduled.LibraryScan.Enabled = b     │     struct, afterwards
                    └─────────────────────────────────────────┘
                                                  │
                                                  ▼
                              every consumer reads config.AppConfig
```

There are **two writers to one struct and no precedence rule between them.** The DB
writer runs second, so the DB always wins — regardless of whether the operator set a
flag, an env var, or a config-file value. Cobra and viper never get a say, because by
the time the DB loader runs, viper is out of the picture entirely.

This is the whole bug class. It is not specific to the scheduler.

## Why it is silent

The second mechanism is Go's zero value.

`ScheduledTaskConfig{Enabled bool, Interval int, OnStartup bool}` cannot represent
"not set". `false` and `0` are simultaneously:

- a legitimate operator choice ("off", "never"), and
- what a field gets when nobody filled it in.

So when `PUT /api/v1/config` serialises the entire struct — including fields the UI
form never populated — it writes `false`/`0` for them. Those are stored as explicit
settings. Forever after, `applySetting` finds the key present and overwrites the viper
default with it.

**Measured consequence (2026-08-12, production):** every key under `scheduled.*` had
`interval: 0`, including `library_scan`, whose shipped default is `enabled: true,
interval: 360`. `library_scan` is the only unattended discovery path for new books, so
nothing had scanned automatically since the setting was last saved. PR #2315 shipped
that default and is deployed; the stored zeros defeated it. Raising a default cannot
affect any install that has ever saved settings.

This is the same shape as the other silent-success defects on the backlog: a code path
that cannot distinguish "no value" from "a value that happens to be zero", and answers
confidently either way.

## Why "just use viper harder" is not the fix

Two tempting non-fixes:

- **"Read through viper everywhere at runtime."** Viper's global is not safe for the
  hot read paths here, and it loses the typed struct that the whole codebase already
  depends on. It also does not solve the zero-value problem: `viper.GetInt` on a key
  explicitly stored as `0` still returns `0`.
- **"Stop saving settings we did not change."** Correct instinct, but as a convention
  it is unenforceable — it re-breaks the first time someone adds a field to the struct
  and a form that does not populate it.

The fix has to make the *illegal state unrepresentable*, or make viper the single
arbiter. Below are the three real options.

---

## Option A — Make the DB a viper layer (recommended)

Stop having two writers. Feed the DB settings **into viper** before the struct is
built, so viper's existing precedence chain does the arbitration:

```go
// after viper defaults/file/env are established, BEFORE building AppConfig
stored, err := loadSettingsAsMap(store)   // {"scheduled.library_scan.interval": 360, ...}
if err == nil {
    _ = viper.MergeConfigMap(stored)
}
cfg := buildAppConfigFromViper()          // the ONLY writer to the struct
```

**Wins:** one reader, one precedence chain, one place to reason about. Flags and env
vars start working for every key, which is what was expected all along. `config.AppConfig`
becomes write-once and can be made effectively read-only.

**Costs:** the DB layer sits at a fixed precedence position, so decide deliberately
where it goes. Recommended: **above** config file, **below** env and flags — so an
operator can always override a bad stored value with `ABK_SCHEDULED_LIBRARY_SCAN_INTERVAL=360`
without touching the DB. That property alone would have made the current outage a
one-line recovery.

**Does not, by itself, fix the zero-value problem** — a stored explicit `0` still wins
over the default. Pair with Option B.

## Option B — Make "unset" representable

Store settings as a **delta from default**, not as a full snapshot:

- On save, compare each field against its default. Write a row **only when it differs**;
  **delete** the row when it returns to the default.
- Or change the persisted shape to pointers (`*int`, `*bool`) so `nil` means unset, and
  only non-nil values are applied.

The delta form is preferable: it is self-healing (a default change propagates to every
install that never overrode it — exactly what was expected of #2315), the settings table
stays small and readable, and "what has this operator actually customised?" becomes
answerable by looking at it.

**This is the option that actually stops the recurrence.** Option A alone routes the
values correctly; Option B is what makes a changed default reach an existing install.

## Option C — One registry, generated accessors

The "central package with getters/setters" shape:

```go
settings.Register(settings.Def{
    Key:     "scheduled.library_scan.interval",
    Default: 360,
    Env:     "ABK_SCHEDULED_LIBRARY_SCAN_INTERVAL",
    Flag:    "scheduled-library-scan-interval",
    Doc:     "Minutes between automatic library scans. 0 disables the timer.",
})
```

with every consumer reading `settings.Int("scheduled.library_scan.interval")` and no
direct struct access anywhere.

**Wins:** a single registration point means the default, env var, flag, docs and
validation cannot drift apart; `--help` and the settings UI can both be generated from
it; an unknown key becomes an error instead of a silent zero.

**Costs:** this is a large migration — 114 top-level config keys and hundreds of read
sites. Realistically an incremental target, not a single change.

---

## Recommendation

Do **A + B** now, treat **C** as the direction of travel.

A and B together are a contained change to `internal/config` plus the settings write
path, and they fix the actual failure mode: stored values would be routed through one
precedence chain, and unset would stop masquerading as zero. C is where this should end
up, but it should be approached one subsystem at a time rather than as a big-bang
rewrite.

## Guardrails to add regardless of which option is chosen

These are cheap, and each one would independently have caught this outage:

1. **Log the effective config for anything that gates unattended work.** Already done
   for the scheduler in this PR: a task that is enabled but has no usable interval now
   WARNs and names the config key, instead of being dropped silently.
2. **A startup assertion for defaults that matter.** If `library_scan` resolves to
   "will never run", say so loudly at boot. The absence of a log line is not something
   anyone can grep for.
3. **A test that runs against a *persisted* config, not just a fresh default one.**
   `TestLibraryScanIsScheduledUnderDefaultConfig` passes and always did — it builds a
   default config, which is precisely the case that was never broken. The gap was
   "default config + a settings save". Any new default-on feature needs that second test.
4. **Reject unknown setting keys on write** rather than storing them, so a renamed key
   fails loudly instead of becoming a permanent invisible zero.

Guardrail 3 is the one that generalises: **a config test that never persists anything
cannot see this entire bug class.**
