- [ ] **Never accumulate more than 10 RCs on a version — cut the stable release
      instead.** Owner directive, 2026-08-08: *"we are never to get above 10
      RCs. Right now we have massive changes all bunched together. Doing it that
      way we have consistent releases."*

      **What triggered this.** On 2026-08-08 the repo was sitting on
      **`v0.217.9-rc.87`** — eighty-seven release candidates on a single
      version — while the last actual stable release was **`v0.217.4`, cut on
      2026-07-06**. A month of work, including several data-loss fixes and a
      library-wide reparse, had piled into one undifferentiated blob that nobody
      could review, bisect, or roll back to a known-good point. Three duplicate
      broken draft releases had also accumulated against the unused `v0.217.9`
      tag. (Cut as `v0.218.0` that night; drafts deleted.)

      **The rule.** When the RC counter for a version reaches **10**, the next
      step is a stable release, not `rc.11`. Every merge to `main` already mints
      an RC via `.github/workflows/prerelease.yml`, so ten RCs is roughly ten
      merged PRs — a reviewable unit.

      **Make it enforced, not remembered.** A rule that depends on someone
      noticing a counter is the same class of failure that let it reach 87:

      - **Fail or warn in CI at the threshold.** Have the prerelease workflow
        check the RC ordinal it just minted and, at `>= 10`, either fail loudly
        or open/refresh a "cut a release" issue. A passive dashboard will be
        ignored; the signal has to appear where the work is happening.
      - **Consider auto-promoting.** If ten RCs are green, cutting the stable
        release is mechanical — `release-prod.yml` already takes
        `release-type` and `previous-version`. Weigh auto-promotion against
        wanting a human gate; if a human gate is kept, the reminder must be
        unmissable.
      - **Clean up RCs on promotion.** `cleanup-rc-releases.yml` exists; verify
        it actually runs on stable promotion and prunes the superseded RC
        releases and tags, or 87 stale prereleases will keep cluttering the
        releases page.
      - **Watch for the duplicate-draft bug.** Three identical broken drafts for
        `v0.217.9` accumulated because the release path created a new draft
        rather than updating the existing one. Fixed for this repo by pinning
        `.github/ghcommon-ref.txt`, but confirm the draft path specifically.

      **Why it matters beyond tidiness.** With one stable release a month, "roll
      back to the last good version" means discarding a month of fixes. With a
      release every ~10 merges, a bad change is bounded by a handful of PRs, the
      release notes are short enough to actually read, and `git bisect` between
      two stable tags is a tractable search rather than 87 candidates.

      **Acceptance:** the RC ordinal never exceeds 10 without either a stable
      release being cut or CI complaining; and the releases page does not
      accumulate superseded RC entries.
