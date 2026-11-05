<!-- file: docs/agent-tasks/platform-core/OWNER-02-release-r0-cut-deploy.md -->
<!-- version: 1.1.1 -->
<!-- guid: c7905538-b04f-4d81-a543-689838294edc -->
<!-- last-edited: 2026-11-05 -->

# OWNER-02 — Release R0: cut the R0 production release, keep its tag, deploy, optional one-day smoke run

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may cut the release, deploy, run the smoke run, or tick
> this row. An agent may prepare the release-note lines below and the checklist; the owner does the rest.

| Field | Value |
|---|---|
| ID | OWNER-02 |
| Release | R0 |
| Tier | **OWNER**: production release, deploy |
| Wave | W03 |
| Depends on | R0-01, R0-02, R0-03, R0-04, R0-05, R0-06 (all merged) |
| Unblocks | **R1-35** and then **R1-01**, which merge only after this release exists, because R1-35 turns the R1 freeze on (R1-01 depends on R1-35 and does not edit `prerelease.yml`) |
| Spec | 09 §9.4 R0 (Rollback, Owner), §9.3 (the R0 tag writes R2's fixture corpus), §9.2.1 (no soaks) |

## Why the tag matters

The R0 release binary itself writes R2's fixture corpus (F2-12 drives it through its API) and `TestR0OpensR1Store`
runs it (09 §9.3 item 1, §9.4 R1 Verification). **Keep the R0 tag and its release assets**; never delete or move
them. The corpus is regenerated only if R0 is re-cut.

## Owner steps

1. **Check the content.** All six R0 PRs are merged on `main`, each green on its required checks, and R0's
   done-check passed: the six `TestLegacy…` tests and the HEAD half of `TestUserMerge_ScopedAdminKeyRefused`, the
   R0 updater tests (exit 75, kept `.prev`, older tag refused, "local build, not compared", no unattended apply),
   the pin check, `make ci`, e2e (09 §9.4 R0 Verification).
2. **P14 again, just before the deploy** (09 §9.4 R0 Owner): if OWNER-01 recorded Branch B, confirm a Basic password
   is saved now (same command as OWNER-01 step 1).
3. **Cut the production release.** Run the `Production Release` workflow (`release-prod.yml`, `workflow_dispatch`)
   on `main` at or after the last R0 merge. If `check-rc-ordinal` already auto-dispatched a production release that
   contains every R0 commit, that release is the R0 release; do not cut a second one. **Prepend** these lines to the
   generated release notes, never replacing them (CLAUDE.md "Post-Task Hygiene"; text verbatim from 09 §9.4 R0):

   > Automatic updates now only check for a new release and notify you; apply updates from Settings. A locally built
   > binary is not compared with releases.
   >
   > Security fixes: a revoked API key can no longer be re-activated; the session list no longer shows session
   > tokens; a limited API key can no longer create an unlimited one; admin-only routes honour API-key limits; Basic
   > authentication with an empty saved password is refused; a revocation can no longer be undone by a request that
   > was in flight. If Basic authentication is on without a saved password, save one before upgrading; if you are
   > locked out, set `BASIC_AUTH_PASSWORD` and restart.

4. **Deploy** with your usual target (`make deploy` from your `Makefile.local`). R0 changes no stored data and takes
   no checkpoint (09 §9.4 R0 Migrations: none).
5. **Optional one-day smoke run** (09 §9.2.1: no soaks; D11). For example: sign in; a scheduled update check shows a
   result and nothing applies itself; a revoked key stays refused; the sessions list shows handles, not tokens.
6. **Rollback if needed:** `make rollback` (09 §9.4 R0 Rollback): nothing to restore.

## What to record, and where

One tracking-issue comment: `OWNER-02 recorded <date>: R0 release <tag>; deployed; smoke run: done | skipped; kept |
rolled back`. No hosts, paths or tokens.

## Done when

The R0 production release exists with its tag kept, it is deployed (or rolled back, with the reason), and the
comment is recorded. The lead then lets R1-35, and after it R1-01, merge.
