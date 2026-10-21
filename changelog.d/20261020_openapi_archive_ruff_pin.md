`docs/archive/openapi.yaml` is deleted (owner approved 2026-10-20). Nothing read it: the repo has no OpenAPI spec, and the contract is the Go DTOs plus `web/src/types/generated/`. It was the last OpenAPI file in the repo, so `FIX_OPENAPI=true` is dropped from `super-linter-pr.env`; it had been kept only because this file still existed.

The repo's own copy of `.github/workflows/scripts/ci_workflow.py` is deleted. No workflow ran it: the Python lint that CI runs is github-common's reusable-ci, which checks out and runs github-common's own `ci_workflow.py`. The local copy had already drifted (it still ran black, while upstream and pre-commit are ruff-only), and editing it changed nothing in CI.

The pre-commit ruff hook moves from v0.14.3 to v0.17.0 (and to the canonical `astral-sh/ruff-pre-commit` URL), so pre-commit and local runs use the same ruff the repo is formatted with.

CI's ruff version comes from the github-common script checkout at the `.github/ghcommon-ref.txt` SHA, now 861d4e83 with ruff 0.17.0.
