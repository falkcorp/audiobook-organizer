<!-- file: docs/plans/platform-core/reviews/spec-rounds-13-15.md -->
<!-- version: 1.1.0 -->
<!-- guid: 95aa8749-4bc7-4064-afe3-3203c6c1a73f -->
<!-- last-edited: 2026-11-05 -->

# Spec review rounds 13–16 (after owner decisions D10–D14)

The D10–D14 revision rewrote the design around one cutover release (R2), so the review loop restarted against the
revised files. Every finding below was dispositioned by the lead and applied to the spec on the same branch.

| Round | Lenses | BLOCKER | MAJOR | MINOR | Outcome |
|---|---|---|---|---|---|
| 13 | conformance to D10–D14, data, security, cross-file, implementability | 2 | 33 | 35 | all accepted; 22 lead rulings; eight revisers on disjoint files plus cross-file follow-ups |
| 14 | ruling conformance, cross-file references, data and security | 1 | 13 | 19 | all accepted; 28 dispositions; five revisers |
| 15 | re-check of the round-14 fixes | 0 | 4 | 7 | all accepted and applied; exit criterion (0 BLOCKER) met |
| 16 | review of the 17 gap fixes the plan rebuild needed | 0 | 3 | 7 | all accepted and applied (tasklint lands at F2 with a shrink-only exemption list; tasks starts on any registered kind or stored runs; the journal's live tail lands at F6) |

## Round 13 BLOCKERs

1. The R0 hotfix base and the rollback binary were ambiguous across files. Now the rollback target is always "the
   pre-freeze release", the last production release before F3 merges.
2. The Audiobookshelf-compatible routes reached the full auth service. Now books holds a narrow `auth.Compat` handle
   (`VerifyPassword`, `AuthenticateKey`, `PrincipalFor`, `CheckCredentialFor`).

## Round 14 BLOCKER

1. A crash between M3's user and key chunks would make the next boot's cutover preflight report live API keys as
   orphans, and the blocker tool could then delete them. Now the preflight runs only on a store whose cutover has
   not started (books below M7's level and no cutover step record); a resumed cutover skips it. A crash-test cell
   covers the case (05-storage §5S.3.13).

## Decisions taken in these rounds worth knowing

- R2 opens with the F2 foundation PRs, landed dark: the data-migration runner, the tasks engine, the audit store, the
  full scope catalog, every core config section and the health registry.
- One read-only cutover preflight runs before the checkpoint. An identity row that cannot be converted, or an
  unfinished account merge, refuses the boot and writes nothing; `storage cutover-blockers` (list, fix, delete)
  repairs it offline.
- HEAD's OAuth start path is kept at its exact path, because an Audiobookshelf client starts sign-in there (D12 (c)).
- With auth off, settings routes keep their reach; only the credential-metadata and audit routes need a real user.
- After an R2 hotfix, `make rollback-restore RELEASE=<pre-freeze tag>` fetches that release, checks its storage
  levels and only then installs it as `.prev`. There is no arbitrary-binary override.
- Checkpoints, dry-run scratch copies and rehearsal copies are 0700/0600 and are deleted 7 days after they stop being
  needed.
