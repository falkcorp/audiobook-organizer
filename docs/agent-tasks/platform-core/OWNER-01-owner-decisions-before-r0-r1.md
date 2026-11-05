<!-- file: docs/agent-tasks/platform-core/OWNER-01-owner-decisions-before-r0-r1.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6d318a46-2f16-444d-9bfc-a89bd7547f51 -->
<!-- last-edited: 2026-11-05 -->

# OWNER-01 — Owner decisions before R0 ships and before R1's first core PR: required-check enforcement (Q-I), keep or override the R1 freeze, split-readiness thresholds (Q-G), and the P14 production read

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may answer, complete, tick or simulate it, and no agent
> runs the production read below. An agent may only prepare the text the owner reads and, after the owner has
> recorded the answers, cite them. A task that depends on this row waits for the owner's recorded answer.

| Field | Value |
|---|---|
| ID | OWNER-01 |
| Release | owner checkpoint (R0 and R1 gates) |
| Tier | **OWNER**: owner decisions and one read-only production read |
| Wave | W01 (due at once) |
| Depends on | none |
| Unblocks | **R0-05** merges only after P14 is recorded; **R1-01** (R1's first core PR, which turns the R1 freeze on) only after Q-I and the freeze decision are recorded; R1-06's readiness report uses the Q-G thresholds |
| Spec | 09 §9.2.1 (release gating; the R1 freeze), §9.4 R0 Owner, §9.4 R1 Owner, §9.7 P14; 14 §14.3 Q-I, Q-G; 12 §12.10 item 3 |

## Why now

- **P14 before R0-05 merges.** Main merges are not CI-gated and `check-rc-ordinal` auto-dispatches a production
  release at 10 RCs of a base version (09 §9.1), so a merged R0 commit can reach production within 10 RCs. R0-05
  makes the Basic gate refuse an empty stored password; an install with the gate on and no saved password would be
  locked out (09 §9.7 P14; 05-authn §11.1 item 5).
- **Q-I and the R1 freeze before R1-01.** From R1 on, tests are the main safety (D11) and every PR passes named
  required checks (09 §9.2.1); how that is enforced decides how every later wave merges. R1-01 turns the R1 freeze
  on unless the owner overrides it.
- **Q-G before R1 wires the readiness report** (12 §12.10 item 3: "the owner may tune them before R1 wires the
  readiness report, not after").

## Owner steps

1. **P14: Basic gate on without a saved password** (09 §9.7 P14). On production, with an admin API key from
   `server-bootstrap` (never paste the key anywhere):

   ```bash
   curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config \
     | jq '.data.config | {enabled: .basic_auth_enabled, password_set: ((.basic_auth_password // "") != "")}'
   ```

   (HEAD masks a set password and returns `""` for an empty one, `internal/config/update_service.go:102-104`.)
   - **Branch A** (`enabled: false`, or `password_set: true`): nothing to do.
   - **Branch B** (`enabled: true`, `password_set: false`): save a Basic password in Settings **before** R0 is
     deployed; or, after R0 is deployed, set `BASIC_AUTH_PASSWORD` and restart (05-authn §11.1 item 5 Recovery).
2. **Q-I: how the required checks are enforced** (14 §14.3 Q-I). Choose:
   - **(a)** (recommended) a ruleset or branch protection on `main` that requires the named checks of 09 §9.2.1,
     with **no bypass actors** (`enforce_admins: true`) and strict up-to-date branches or a merge queue, and merge
     tooling (the `/parallel-sweep` coordinator, CLAUDE.md's Quick Fix flow) that waits for the checks and **never
     merges with `--admin`** past them; or
   - **(b)** keep server-side merges ungated, rely on the merge tooling running `make ci` locally before every
     merge, with `auto-revert.yml` as the backstop; or
   - your own alternative, stated precisely.
   Record also whether `/parallel-sweep` may admin-merge (plan §(m) item 1). Under (a) it may not.
3. **The R1 freeze** (09 §9.2.1 "R1 freeze", a lead extension the owner may override): keep it (automatic promotion
   paused from R1's first core PR until R1 ships, so R1 is exactly one production release) or override it.
4. **Q-G: split-readiness thresholds** (14 §14.3 Q-G; 12 §12.10 item 3): accept SC1 (3 consecutive API-stable
   checkpoints over 6 weeks) and SC5 (coverage 70 % core / 60 % SDK) as proposed, or give new numbers. They are
   readiness reports, not merge gates.
5. The module name needs no answer: D13 fixed `github.com/falkcorp/mediacore` (09 §9.4 R1 Owner).

## What to record, and where

One comment on the workstream's tracking issue, counts and choices only (never a token, host, path or config
value):

```
OWNER-01 recorded <date>
P14: Branch A | Branch B (password saved on <date>)
Q-I: (a) | (b) | alternative: <one sentence>; /parallel-sweep admin-merge: allowed | not allowed
R1 freeze: kept | overridden
Q-G: accepted | SC1=<n> checkpoints over <w> weeks, SC5=<core %>/<SDK %>
```

If Q-I is (a), the owner (or an agent the owner asks, in a separate task) applies the ruleset before R1-01 merges;
the ruleset itself is not part of this plan's PRs.

## Done when

The tracking-issue comment above exists. The lead then: lets R0-05 merge (P14 recorded); lets R1-01 start its merge
gate in the recorded merge mode; passes the Q-G numbers to R1-06.

## What agents must not do

Run the P14 command, read production, guess an answer, apply a ruleset without the owner's recorded choice, or merge
R0-05 / R1-01 before this row is recorded.
