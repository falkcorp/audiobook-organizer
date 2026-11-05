<!-- file: docs/agent-tasks/platform-core/OWNER-04-pre-r2-production-reads.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5dbb01a6-8a15-49e4-a5f1-45022bf2338c -->
<!-- last-edited: 2026-11-05 -->

# OWNER-04 — Pre-R2 production reads and the never-run-crons decision: P1, P2, P3, P4, P5, P7, P9 (counts only) and Q-C

> **NOT AGENT WORK.** This is an owner checkpoint. No agent reads production, runs these commands, guesses a count,
> or ticks this row. Agents consume only the counts the owner records.

| Field | Value |
|---|---|
| ID | OWNER-04 |
| Release | owner checkpoint (inputs to R2) |
| Tier | **OWNER**: read-only production reads, counts only; one decision |
| Wave | W04 |
| Depends on | OWNER-02 (R0 deployed) |
| Unblocks | **F2-14** (P1 sizes the production-shaped store), **F3-07** (P1: no unregistered prefix; P4: no unmapped env name), **F5-14** (Q-C) |
| Spec | 09 §9.7 P1–P5, P7, P9; 14 §14.3 Q-C; 05-storage §5S.9.2 (what migrates, resets or stays) |

## Rules for every read (09 §9.7)

Run on the deployed R0 or R1 production host, read-only. **Paste counts only, never values.** Never print `sess:` or
secret values; count keys only. `<abo-url>`, `<redacted-token>` (an admin API key from `server-bootstrap`),
`<unit>`, `<scratch>` and `<db>` are placeholders. Offline commands run on a **restored backup copy** under
`<scratch>`, so production is never stopped.

## Owner steps

| ID | Question | Command (verbatim from 09 §9.7) | Branch A | Branch B |
|---|---|---|---|---|
| P1 | Key counts and bytes per family | `curl -sS -H "Authorization: Bearer <redacted-token>" "<abo-url>/api/v1/diagnostics/db-census?fresh=true" \| jq '.data.families[] \| select(.prefix \| test("^(u\|role\|apikey\|invite\|inviteallow\|oauthid\|sess\|idx\|setting\|opv2\|opv3\|act):")) \| {prefix,keys,raw_key_bytes,raw_value_bytes}'` | every key is in a registered family: the generator takes the counts | `(unregistered)` keys: list them **by prefix** before F3 |
| P2 | Custom (non-seed) roles exist? | `audiobook-organizer diagnostics query --raw --db <scratch>/audiobooks.pebble --prefix role: --limit 1000 \| grep -c '"is_seed":true'` vs the total of `^Key:` lines | none | some: each converts through the atom table; an atom outside it grants nothing and the role is reported |
| P3 | API keys with empty scopes | the same with `--prefix apikey:` and `grep -c '"scopes":\[\]'` | none | some: M3 converts each per 05-rbac §6, never empty |
| P4 | Env names actually set | `systemctl show <unit> -p Environment \| tr ' ' '\n' \| cut -d= -f1`; `systemctl cat <unit> \| grep -o 'Environment="[A-Z_0-9]*'`; Docker: `docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' <container> \| cut -d= -f1`; launchd: `plutil -extract EnvironmentVariables xml1 -o - <plist> \| grep -o '<key>[A-Z_0-9]*'`; compose: `docker compose config \| grep -oE '^\s+[A-Z_0-9]+:'` | only names the rename table maps | a name the table does not map: it must be added before F3 |
| P5 | Open invites; external callers of core routes | `... --prefix invite: ... \| grep -c '^Key:'`; callers: `journalctl -u <unit> --since -30d -o cat \| grep -oE '(GET\|POST\|PUT\|PATCH\|DELETE) /api/[a-z0-9/_-]*' \| sed -E 's#/[0-9A-Za-z]{20,}#/:id#g' \| sort \| uniq -c \| sort -rn \| head -50` | no open invite; only the SPA and the repo's scripts call core routes | open invites: re-send them after R2; an outside caller: the R2 release note and runbook name it |
| P7 | ABS surface in use | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config \| jq '.abs_api_enabled'`; `... --prefix abs_sess: ... \| grep -c '^Key:'` | off | on with sessions: a live phone-app login and progress sync go into R2's smoke run |
| P9 | Webhooks configured | `curl … /api/v1/config \| jq '.plugins.webhook // empty \| keys'` | none | some: M6 migrates each with its secret; switch each receiver at the R2 deploy |

P4 prints env **names**, not values; P5's caller list prints methods and paths only. Paste nothing else.

**Q-C** (14 §14.3 Q-C): the 15 v2 cron strings that never ran (for example `deluge.protected-paths-sync` every
30 min): **delete the declarations** (the spec default and recommendation; re-add any one later as a real
schedule), or enable them as real schedules.

## What to record, and where

One tracking-issue comment, counts and names of prefixes or env variables only:

```
OWNER-04 recorded <date>
P1: <prefix>=<keys>/<key bytes>/<value bytes> per family; unregistered prefixes: none | <prefix list>
P2: custom roles <n>   P3: empty-scope keys <n>   P4: unmapped env names: none | <name list>
P5: open invites <n>; outside callers: none | <method path list>   P7: ABS <on|off>, abs_sess <n>   P9: webhooks <n>
Q-C: delete | enable
```

## Done when

The comment exists. F2-14 reads P1; F3-07 waits for P1 and P4 Branch A (or the rename table extended); F5-14 reads Q-C.

## Planner notes

- P2, P3, P5 (invites) and P7 (`abs_sess:` count) run `diagnostics query --raw --db <scratch>/…` on a restored
  backup copy, and 09 §9.7 restores it with `storage restore-offline --into`, which does not exist until R1
  (R1-25); `grep -rn 'restore-offline' cmd internal` finds nothing at HEAD. On an R0-only host the owner needs
  another way to get a scratch copy, or these four reads wait for the R1 deploy. Every consumer (F2-14, F3-07,
  F5-14) runs after OWNER-03 anyway, so making this row depend on OWNER-03 would cost no time.
