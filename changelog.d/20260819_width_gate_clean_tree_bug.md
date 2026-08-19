### Fixed

- `scripts/check-interface-width.sh` aborted silently with exit 1 whenever the
  tree was **clean**. Its stale-path check piped `grep` under `set -euo pipefail`
  without a guard, and `grep` exits 1 when it matches nothing — so zero findings
  killed the script before it printed a count, a comparison, or any message. The
  bug was unreachable until now because the baseline had never been below 1.
  Exit 1 is also this gate's own "width went UP/DOWN" code, so a clean tree would
  have reported in CI as a ratchet violation with no explanation.
