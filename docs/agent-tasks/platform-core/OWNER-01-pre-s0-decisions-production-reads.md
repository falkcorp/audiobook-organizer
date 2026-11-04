<!-- file: docs/agent-tasks/platform-core/OWNER-01-pre-s0-decisions-production-reads.md -->
<!-- version: 1.0.0 -->
<!-- guid: dbe7db70-6ab1-49c4-9d56-577a7fe00cc2 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-01 — Pre-S0 owner decisions and production reads: Q-H, Q-I, Q-G; P14, P15, P7, P10

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may perform these reads, take these decisions, change
> repository settings, or tick this checkpoint. An agent may only prepare the stage-record skeleton below for the
> owner to fill, and must stop there.

| Field | Value |
|---|---|
| ID | OWNER-01 |
| Tier | OWNER (no model tier): decisions and production reads only the owner can make |
| Wave | W00 (before S0) |
| Depends on | none |
| Blocks | PC-00-04 (waits for P14) and PC-01-02 (waits for Q-H); Q-I decides how every later merge is gated |
| Priority / duration | P0 / reads and decisions (no soak) |
| Owning spec | `docs/specs/platform-core/14-risks-open-questions.md` §14.3 rows Q-G, Q-H, Q-I; `09-abo-on-core.md` §9.7 rows P7, P10, P14, P15 and §9.2.1 "Soaks and owner checkpoints" |
| Digest | `docs/plans/platform-core/digests/S0.md` §9 |

## Where the results go

Record every answer in the S0 stage record `docs/plans/platform-core/stage-ledger/S0.md` (plan §(f), §(g)), in a
bookkeeping-only docs PR the owner merges. Briefs check it: PC-00-04 greps it for `P14` before merging; PC-01-02 reads
`Q-H` before creating `core/go.mod`. Suggested skeleton (an agent may create this file with every value empty):

```markdown
# S0 stage record
## Owner decisions
- Q-H (core module path): <decision> (<date>)
- Q-I (release-gating enforcement): <option (a) or (b)>; ruleset in place: <yes/no, date>
- Q-G (split readiness thresholds): <accepted / tuned values> (<date>)
## Production reads
- P14 (Basic gate on without a saved password): <result> (<date>)
- P15 (journal retention of the unit): <oldest entry date; days covered> (<date>)
- P7 (ABS surface in use): <off / on with sessions> (<date>)
- P10 (rollback inventory): <.prev levels or format; .prev and current --version-json> (<date>)
```

## Decisions (14 §14.3)

1. **Q-H — core module path.** Spec proposal: `github.com/falkcorp/mediacore` (03 §3.2); recommendation: accept,
   because renaming is cheap only before the first core package lands. PC-01-02 writes this path into `core/go.mod`
   and the root `require`/`replace`. Done when the stage record names the path.
2. **Q-I — how the release-gating requirement is enforced.** The requirement is decided: every PR to `main` passes
   the named required checks of 09 §9.2.1 (the `ci.yml` jobs mirroring root `make ci`, `extraction-gates`,
   `compat-rehearsal` on flip PRs and `release:` changes, `docker-image`, `core-ci`, `vet-darwin`, `vet-integration`,
   `unit-lifecycle` on its triggers). Options: **(a)** a ruleset or branch protection on `main` that requires those
   checks with no bypass actors (`enforce_admins: true`), strict up-to-date branches or a merge queue, rebase merges
   only, and merge tooling that never merges with `--admin` past them; **(b)** ungated server-side merges plus local
   `make ci` and `auto-revert.yml` as the backstop. Recommendation: **(a)**. Done when the stage record names the
   option and, for (a), the ruleset is in place. The required-check names that do not exist yet are created by W01's
   PRs (PC-01-02 `core-ci`, PC-01-04 `extraction-gates`, `compat-rehearsal`, `vet-darwin`, `vet-integration`,
   PC-01-05 `docker-image`, PC-02-06 `unit-lifecycle`); the owner adds each to the ruleset as it lands.
3. **Q-G — readiness thresholds for the split** (SC1: three consecutive API-stable checkpoints over six weeks; SC5:
   coverage 70 % core / 60 % SDK). They are readiness reports, not merge gates (12 §12.3.11). Recommendation: accept
   as proposed; tune before S0 wires the readiness report. Done when recorded.

## Production reads (09 §9.7; read-only commands, run by the owner)

Commands are quoted from 09 §9.7 with placeholders; substitute your own URL, host and a read-only credential, and
never paste a credential into the stage record.

4. **P14 — Basic gate on without a saved password** (before S0, before PC-00-04 merges):
   ```bash
   curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config \
     | jq '.data.config | {enabled: .basic_auth_enabled, password_set: ((.basic_auth_password // "") != "")}'
   ```
   HEAD masks a set password and returns `""` for an empty one. Gate off, or a password set: nothing to do. Gate on
   with no password: save a password before deploying S0 (or, after S0, set `BASIC_AUTH_PASSWORD` and restart).
   Done when every install reads "off" or "password set", or the owner saved one, and the result is recorded.
5. **P15 — journal retention for the soak record** (before S1):
   ```bash
   ssh <host-a> 'journalctl --disk-usage; sudo -n journalctl -u <unit> -o short-iso --reverse | tail -1'
   ```
   The unit's oldest retained entry must be more than 14 days old; if not, raise journald's `SystemMaxUse` or
   `MaxRetentionSec` (or give the unit its own journal namespace) before S1, or every soak row refuses with "journal
   does not cover the soak". Done when the oldest entry date is recorded and covers more than 14 days.
6. **P7 — ABS surface in use:**
   ```bash
   curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config | jq '.abs_api_enabled'
   ```
   plus the `abs_sess:` key count named in 09 §9.7. Off: conformance fixtures suffice. On with sessions: a live login
   and progress sync with a real ABS client after each deploy of S2, S4, S6 and S9 (and after a rollback of one of
   them) is an owner checkpoint of those releases. Done when recorded.
7. **P10 — rollback inventory:**
   ```bash
   ssh <host-a> '<bin>.prev --print-storage-levels || <bin>.prev --print-storage-format'
   ssh <host-a> '<bin>.prev --version-json'; ssh <host-a> '<bin> --version-json'
   ```
   Before S0 the binaries are older than S0, so only `--print-storage-format` answers (the projection keeps it at 1)
   and `--version-json` does not exist yet; record that. After the first S0 deploy, P10 runs after each deploy and its
   output updates `ci/prev_ref.txt` (OWNER-02). Done when recorded.

## Acceptance (owner ticks; no agent command can tick these)

- [ ] Q-H recorded (module path).
- [ ] Q-I decided and, for option (a), the ruleset in place (required checks, no bypass, rebase-only).
- [ ] Q-G recorded.
- [ ] P14: no install has the Basic gate on without a saved password, or the owner saved one; recorded.
- [ ] P15: the unit's journal reaches back more than 14 days; recorded.
- [ ] P7 and P10 recorded in the S0 stage record.

## Out of scope

The S0 owner actions that follow the code (sudoers lines, the `deploy` recipe copy into `Makefile.local`, the
bookkeeping PR creating `ci/soak.txt`, `ci/soak_health_allow.txt` and `ci/prev_ref.txt`, the S0 deploy and soak) are
OWNER-02's.

## Planner notes

- 09 S0 "Done when" and the plan place P15 "before S1"; it is listed here because the plan groups it with the pre-S0
  reads. Recording it at OWNER-01 is earlier than required and blocks nothing if deferred to OWNER-02.
