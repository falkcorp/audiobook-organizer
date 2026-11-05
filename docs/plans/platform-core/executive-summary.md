<!-- file: docs/plans/platform-core/executive-summary.md -->
<!-- version: 1.1.1 -->
<!-- guid: 85a66690-09c6-401b-8020-a2ec1acc9b74 -->
<!-- last-edited: 2026-11-05 -->

# Platform core: executive summary

Plain-language summary of the design in `docs/specs/platform-core/` as revised on 2026-11-04 and 2026-11-05.
Planning only: no production code has changed.

## The idea

A lot of audiobook-organizer is not about audiobooks: storage, background jobs, logins, settings, logs, metrics,
backups, updates and the web UI frame. The plan pulls that general part into a shared engine called **mediacore**
(`github.com/falkcorp/mediacore`, public, Apache-2.0). Audiobook-organizer becomes the engine plus a "books" module,
and later a TV app and a Movies app can reuse the same engine.

## What the owner decided

- **No backward compatibility.** Data formats, routes, setting names and metric names may change. Dashboards and
  alert rules change in the same pull request as the rename. The one frozen contract is the Audiobookshelf-compatible
  API that the phone apps use.
- **No soaks.** At most an optional one-day smoke run.
- **One cutover.** User data is converted forward once, in a single release.
- **Licences.** mediacore is Apache-2.0, so anyone may build on the engine. Audiobook-organizer keeps PolyForm
  Noncommercial 1.0.0 (free for personal use, research and non-profits) and gains a `COMMERCIAL.md` notice for paid
  commercial licences.

## Four releases

1. **R0, hotfixes:** six live security fixes, the Go toolchain bump and an updater fix. No data change.
2. **R1, core runtime:** the engine starts running the app. Your data is not touched, so rollback is a binary swap.
3. **R2, the cutover:** opens with foundation pull requests that land switched off (F2), then moves settings and
   secrets, logins, background jobs, events, integrations, and backup/updater/health/metrics onto the engine in turn
   (F3–F8). Your data is converted on the first boot of R2. Automatic updates pause until R2 is finished.
4. **R3, cleanup:** deletes the old code, and refuses to start on a database that never went through R2.

## What the cutover does to your data

| Thing | What happens |
|---|---|
| Library (books, series, authors, progress) | Untouched |
| Settings | Converted to new names; passwords and secrets move into an encrypted keyring |
| API keys | Keep working with their permissions converted; a test checks each key reaches exactly what it could before |
| Phone apps | Stay signed in |
| Web login | Resets once |
| Task and log history | Kept and converted |
| A login row that cannot be converted, or an unfinished account merge | The upgrade stops before writing anything and names the row; a command lists, fixes or deletes it |
| Any other bad row | Set aside, sealed, and reported in health; never silently dropped |

## If something goes wrong

Before converting anything, the app takes a full copy of the database (permissions 0700/0600). If R2 misbehaves,
`make rollback-restore` restores that copy and reinstalls the release you ran before. After an R2 hotfix it can fetch
that release by name and checks it matches your data first. Anything written after the upgrade is lost, so fixing
forward is the default. Credentials you revoked on R2 come back after a rollback, so revoke them again. The copy holds
plaintext secrets, so it deletes itself 7 days after it stops being needed.

## What you do at the R2 deploy

1. Take a backup and copy it off the server.
2. Rename the environment variables your unit and compose files set (the release note lists them).
3. Deploy. After the first boot, export the new keyring and keep it with the backup.
4. Switch webhook receivers to the new header and signature format.
5. Reload the shipped Grafana dashboards and Prometheus rules.
6. Sign in to the web app again and re-send any open invites.

OAuth callback and start addresses stay as they are, so identity providers and Audiobookshelf apps need no change.

## Review

The revised spec went through three review rounds (13–15). The blocker count went from 2 to 1 to 0, and every
finding was resolved. Details: `reviews/spec-rounds-13-15.md`. The implementation plan and its task briefs then went
through three more review rounds and an independent advisor review, and merged.

## Owner decisions recorded 2026-11-05

Every open question is now answered (decision D15 in the spec's decision record):

- **Merging is gated.** A rule on `main` requires the test checks to pass before anything merges, nobody can skip
  it (not even an admin), and GitHub's merge queue tests waiting changes together so a busy day does not mean hours
  of re-testing. Automated merge tooling never forces a merge past a failing check. One step is left for you: turn
  that rule and the merge queue on before the first R1 change merges.
- **R1 ships as one release.** Automatic production releases pause while R1 is built, as they will for R2.
- **Release freezes are a switch you flip.** Instead of a code change for every freeze start and end, the freeze is a
  repository setting you turn on after cutting R0, off after R1, on again before R2's switch-overs and off after R2.
  That removes one pull request and the freeze edits from three others; each flip is noted in the tracking issue
  (issue #4382).
- **Old schedule labels are deleted.** 15 background jobs carried a schedule string that the app never read. 14 of
  them already run on a real schedule and keep it; the Deluge path sync stays manual, because its cache refreshes
  itself every few minutes. Nothing about when your jobs run changes.
- **One unneeded test is dropped.** The plan no longer tests upgrading from one in-progress R2 build to the next:
  production only ever upgrades from the release before R2 to the finished R2, and that path is still fully tested.
- **The split-readiness thresholds are accepted as proposed**, and the production check for an empty Basic
  password found nothing to fix, so the first security fix can ship.

## Next

You apply the `main` rule and the merge queue. Work then starts with the R0 hotfixes (wave 1). The plan now has 133
tasks; its critical path is still about 82 working days.
