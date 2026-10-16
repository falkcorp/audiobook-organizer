Release workflows now pin github-common at `1f0d70e` (github-common#374), in
both `prerelease.yml`/`release-prod.yml` and `.github/ghcommon-ref.txt`. The
v0.222.2 production release failed in "Create GitHub Release" with "Argument
list too long", after the release was already published. Its notes were about
125 KB and were passed to the release action through an env var. Notes now go
through a file, capped under GitHub's 125,000-character body limit, so the
CHANGELOG collection and draft cleanup steps that release skipped will run.
