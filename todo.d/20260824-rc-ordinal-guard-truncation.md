- [ ] **Fix the RC-ordinal guard's 200-release truncation window.**
      `prerelease.yml`'s `check-rc-ordinal` job enumerates with
      `gh release list --repo "$REPO" --limit 200`, but the repo has 464 releases.
      It has only ever reported correctly because `gh release list` is newest-first
      and the base being counted is always the newest one, so the window happens to
      contain it. `v0.218.1` reached 180 RCs -- 90% of the way to silently
      under-counting on the one job whose entire purpose is counting. This is the
      same truncation pattern already replaced with `gh api --paginate` in
      `cleanup-rc-releases.yml`.
      Not urgent: after the backlog purge, counts drop to <=3 per base.
      Cost is more than a one-line swap -- `gh api` returns `tag_name`/`prerelease`
      where `gh release list --json` returns `tagName`/`isPrerelease`, so
      `.github/scripts/check-rc-ordinal.sh` and its tests move with it.
      Found while hardening the purge (#2877); the guard became load-bearing for
      the first time in #2875, having been skipped for months.
