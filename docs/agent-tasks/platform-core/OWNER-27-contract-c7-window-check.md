<!-- file: docs/agent-tasks/platform-core/OWNER-27-contract-c7-window-check.md -->
<!-- version: 1.0.0 -->
<!-- guid: 01ea996d-0897-4bb4-a1f5-7c18bb172cf3 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-27 — Contract C7: W's owner check on the reference host (P5 callers, P8 counter readings)

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may complete it, tick it, run its commands
> against production or the reference host, run `scripts/soak_record.py`, edit `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, or merge a PR that changes them (09 §9.2.1 "Soaks and owner
> checkpoints"; plan §(g)). An agent that is handed this file stops and reports.

| Field | Value |
|---|---|
| ID | `OWNER-27` |
| Tier | OWNER — NOT AGENT WORK |
| Wave | W19 (contracts) |
| Depends on | `OWNER-20` (the soak row of the release that shipped the expand step: S7) |
| Unblocks | `PC-19-05` (the C7 contract PR) |
| Duration | W ≥ 30 days and two production minors after S7 shipped, then 14 consecutive days of readings |
| Spec | 09 §9.2.1 W (a)–(c); 09 §9.5 C7; 09 §9.7 P5, P8 |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §8 "Contract checkpoints" |

## What this checkpoint covers

the S7-section generic aliases `/api/v1/ai`, `/api/v1/deluge` and `/api/v1/metadata-sources` (surface `http`). W is dates plus an owner check (09 §9.2.1); nothing is persisted, there is no usage record or route, and only the reference store is measured.

## W's three conditions (all required, 09 §9.2.1)

- **(a) Dates.** Two production minor releases **and** 30 days after S7 shipped in production. Minors auto-promote every 10 RCs (09 §9.1), so the count alone can be short; the 30-day floor always applies.
- **(b) Release notes.** Every production release in that period names each surface as deprecated, with its canonical replacement. A release that omitted one restarts the count for that surface.
- **(c) Readings.** Before each deploy of the reference host in the last 14 days, and again at the contract PR, read the process-local counter and the process start time; together the readings cover 14 consecutive days with no use of any surface of this contract. A restart between readings is covered only if a reading was taken just before it (the counter is process-local).

## Procedure

1. Record the dates for (a) and the release tags checked for (b) in the stage record `docs/plans/platform-core/stage-ledger/<stage>.md` (doc-only; 09 §9.2.9).
2. Find external callers (P5) and list them in the stage record (09 §9.2.9; OPS9-12). A caller found keeps the window open until P8 shows no use:

```bash
# P5 callers (09 §9.7): external API callers over the last 30 days; counts only.
ssh <host-a> "journalctl -u <unit> --since -30d -o cat" \
  | grep -oE '(GET|POST|PUT|PATCH|DELETE) /api/[a-z0-9/_-]*' \
  | sed -E 's#/[0-9A-Za-z]{20,}#/:id#g' | sort | uniq -c | sort -rn | head -50
```

3. Take the P8 readings on the schedule of (c):

```bash
# P8 (09 §9.7): run on the operator's machine against the reference host. Placeholders only; paste counts, never values.
curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/metrics \
  | grep '^audiobook_organizer_compat_legacy_use_total' | grep -E 'surface="(http)"'
ssh <host-a> 'systemctl show <unit> -p ActiveEnterTimestamp'      # the process start time, recorded beside each reading
```

- Count only `http` samples whose pattern starts with `/api/v1/ai`, `/api/v1/deluge` or `/api/v1/metadata-sources`.

4. **Decide.** No sample for any surface of this contract across 14 consecutive days (branch A): hand the coordinator the readings block below for `PC-19-05`'s PR description. A non-zero count (branch B): the window extends; the `name` label names the surface, and the next release notes name the caller to fix.

## Readings block (paste into the contract PR description)

```text
W check for C7 (OWNER-27)
expand: S7, in production since <YYYY-MM-DD>; minors since: <tag>, <tag>; days: <n>
release notes naming the surfaces as deprecated: <tag list>
external callers (P5): <none | list>
readings (audiobook_organizer_compat_legacy_use_total):
  <YYYY-MM-DDTHH:MMZ>  process start <timestamp>  samples for this contract: none
  ... (one line per reading; first and last at least 14 days apart, no gap across a restart)
```

## Done when

- two production minors and 30 days since S7 shipped;
- every production release in the period names the surfaces as deprecated;
- 14 consecutive days of readings with no use, with process start times, recorded in the contract PR.

## Never

- Print a session token, API key, secret or any `sess:`/`invite:` value; count keys only (09 §9.7).
- Let an agent take a reading, or treat a reading an agent produced as evidence (09 §9.2.1 (c)).

