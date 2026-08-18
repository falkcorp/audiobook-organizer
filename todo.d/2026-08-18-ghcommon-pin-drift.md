### ghcommon reusable-workflow pins are a month apart — decide, don't drift

Measured 2026-08-18. Eight workflows pin `falkcorp/github-common` at
`d0c3326b` (**2026-07-19**); `ci.yml` pins `828afb50` (2026-08-18). The older pin
is **22 commits behind**.

| Pin | Date | Workflows |
|---|---|---|
| `d0c3326b` | 2026-07-19 | `frontend-ci`, `nightly`, `nightly-burndown`, `hard-burndown`, `prerelease`, `release-prod`, `security`, `triage-poll` |
| `828afb50` | 2026-08-18 | `ci` |

- [ ] Decide whether to bump the eight, and do it in **at least two PRs** —
      not one. `release-prod.yml` and `prerelease.yml` are the risk: a reusable
      release workflow that broke somewhere in those 22 commits is not
      discovered until someone cuts a release, by which point the bump is
      several PRs back and no longer the obvious suspect. Bump the
      low-consequence ones (`triage-poll`, the burndowns) first and let them run
      a nightly before touching release or security.
- [ ] Not done unattended on purpose: this was left for a human on 2026-08-18
      rather than folded into the CI-wiring PR, because verifying a release
      workflow requires actually cutting a release.

Note this is drift, not inconsistency for its own sake — the eight point at
several *different* reusable workflows (`reusable-ci`, `reusable-release`,
`reusable-security`, `reusable-burndown`, `reusable-triage-poll`), so a single
shared SHA is a convention, not a correctness requirement.
