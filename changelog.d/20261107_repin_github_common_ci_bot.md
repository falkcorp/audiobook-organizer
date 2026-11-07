### Changed

- Every github-common pin moves to `016cd02` together with `.github/ghcommon-ref.txt`. Releases now collect the changelog in a job with no secrets, and push it as the CI bot app from the main-only `ci-bot` environment (`bot-environment: ci-bot`), so the push no longer fails on the merge queue (GH013).
