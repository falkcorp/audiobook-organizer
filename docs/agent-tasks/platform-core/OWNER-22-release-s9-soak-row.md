<!-- file: docs/agent-tasks/platform-core/OWNER-22-release-s9-soak-row.md -->
<!-- version: 1.0.0 -->
<!-- guid: 44f234aa-f5e4-4155-b993-73b9f115ae32 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-22 — Release S9: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** No agent may complete, simulate or shortcut any step of this checkpoint: no agent runs
> `scripts/soak_record.py`, deploys to the reference host, reads production, runs the live ABS-client check, edits
> `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, or merges a PR that changes those files (09 §9.2.1
> "Soaks and owner checkpoints"). An agent may only remind the owner that this checkpoint blocks S10 and contracts
> C6 and C9.

| Field | Value |
|---|---|
| ID | OWNER-22 |
| Tier | OWNER — no agent may complete it |
| Wave | W18 (closes release `S9`) |
| Depends on | PC-18-02 (the S9 flip), PC-18-03 and PC-18-04 merged and deployed |
| Blocks | W20 (S10) and the W checks OWNER-26 (C6) and OWNER-29 (C9) |
| Duration | 14 days of soak from the deploy that carries PC-18-04 (plan §(b) decision 2: content after the flip) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints"; 09 §9.3 boot-time budget, "Staging", "Rollback rehearsal"; 09 §9.7 (P7 branch B, P10); 09 S9 "Done when", "Rollback" |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §6 (S9 rows 4 and 9), §7 S9, §8 |

## Owner steps

1. **Deploy** the `main` build that contains PC-18-04 to the reference host with the committed `deploy` recipe
   (`scripts/soak_record.py --deployed` after the ready check; run it by hand after a manual deploy).
2. **P10 after each deploy**: read `<bin>.prev --version-json` and `<bin> --version-json` and commit the resolved
   `PREV_REF` as `ci/prev_ref.txt` in a bookkeeping-only PR.
3. **P7 branch B — the live ABS-client check** (09 §9.7 P7; S9 is an ABS-path stage): if production has ABS enabled
   with sessions, after this deploy log in and sync progress with a real ABS client against the reference host, and
   again after any rollback of S9. Record the result in the stage record.
4. **Staging checks** on the scrubbed production-size copy: the boot-budget A/B with
   `scripts/boot_budget.py --prev <S8 bin> --cur <S9 bin> --db-copy <dir> --runs 5`.
5. **Browser checks on the reference host** (read-only, your own session): `/` serves the shell; a legacy SPA path
   such as `/library` redirects to `/books/library`; an existing `/invite/<token>` link answers 302 to
   `/invite#token=…` with `Referrer-Policy: no-referrer`; the theme stored before the upgrade is kept.
6. **Credentialed smoke scrape** of `/metrics`: 200 with `process_resident_memory_bytes`, `go_goroutines` and
   `<namespace>_build_info`.
7. **Tick the stage record** (`docs/plans/platform-core/stage-ledger/S9.md`) against 09 S9 "Done when", item by item:
   `App.tsx` has no hard-coded module routes; `createShell.meta.test.tsx`; `TestBundle_MissingMeta`; the dummy second
   contribution (G6 UI half); G3 (unknown descriptor version rejected); the e2e request log with no first-party
   request to the legacy streams; `static_embed_recovery_test.go`'s port through `app.New`; the API-only placeholder
   at `/`; the three R-test rows (reload offered after rollback to S8; preference kept; invite minted on S9 opens on
   S8); a page load before books' Start serves `index.html`; `TestLegacyInviteRedirect_SPASharedSegment`;
   `theme-boot.spec.ts` and exactly one inline script; cells a, b, c.
8. **Release notes** of the S9 production release carry rows 4 (invites) and 9 of 09 §9.2.1 verbatim, prepended to
   the generated notes: "Invite links sent on this release do not open after a rollback below the release that moved
   sign-in to the core; send a new one." and "`/books/...` bookmarks need the new release."
9. **Soak row.** After 14 days with every pass criterion met (zero `restore_*` and boot errors since `<since>`, every
   owner at level 0 in both `--print-storage-levels` and `<db>.storage-levels`, one health read with no Critical
   component Down and no token outside `ci/soak_health_allow.txt`), run from the operator's checkout:

   ```bash
   scripts/soak_record.py --release S9
   ```

   Add the printed row to `ci/soak.txt` in a bookkeeping-only PR and merge it. C9's window W (two production minors
   and 30 days since S9 shipped) starts at the S9 release, not at this row (09 §9.2.1 W (a)).

## Done when

- `ci/soak.txt` holds a current `S9` row written by `scripts/soak_record.py --release S9`.
- The stage record ticks the stage's done-check list (step 7) and records the P7 branch B result (step 3).
- P10 ran after each deploy and `ci/prev_ref.txt` was updated.

## Planner notes

None.
