- [ ] **DECISIONS-DEADCODE-PRUNE** PR1 of the Universal Reject plan (#4110) added the
      `internal/decisions` package with no production caller yet, so its 75
      identifiers sit in `scripts/deadcode-baseline.txt` under the "09-PR1
      one-time addition" comment (every `internal/decisions.` line). The PR that
      wires the first producer from `main` (PR2) must confirm every one of those
      names drops out of `python3 scripts/deadcode-ratchet.py`'s unreachable set,
      then run `--prune` (lower-only) so the block and its comment are removed.
      Any name still unreachable after PR2 is dead code: delete it rather than
      keep it baselined.
