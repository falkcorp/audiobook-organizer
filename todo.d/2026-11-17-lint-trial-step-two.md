- [ ] **CI-LINT-2** Promote the GitHub-hosted lint trial (`lint-trial.yml`,
      `Lint (GitHub-hosted, trial)`) and retire Woodpecker's `checks-lint`.
      Exit criteria are in `docs/ci/woodpecker.md` ("Trial: GitHub-hosted
      lint"): N consecutive green runs that agree with `checks-lint` on the
      same commits, and a wall time at or below it. Then, in a separate PR and
      with the owner's go, add the check to the main ruleset and delete
      `.woodpecker/checks-lint.yaml`.
- [ ] **CI-TAGLIB-GH** Move `test-native-taglib` to a GitHub-hosted runner the
      same way: a non-required trial job first (build TagLib 2.0.2 via
      `scripts/ci/build_taglib_static.py`, install ffmpeg, ffprobe and fpcalc,
      run `go test -tags native_taglib -race` of `internal/metadata`,
      `internal/tagger` and `internal/metafetch`), compare with Woodpecker on
      the same commits, then make it required and retire
      `.woodpecker/test-native-taglib.yaml` in a separate PR with the owner's
      go. Put the trial in its own workflow file, not ci.yml, so a red trial
      cannot trigger auto-revert.
