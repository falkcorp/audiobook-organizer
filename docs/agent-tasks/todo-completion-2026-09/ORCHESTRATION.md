<!-- file: docs/agent-tasks/todo-completion-2026-09/ORCHESTRATION.md -->
<!-- version: 1.6.0 -->
<!-- guid: 8a962a05-3ef5-415b-9d74-9533c72d51f6 -->
<!-- last-edited: 2026-09-10 -->

# Orchestration — todo-completion-2026-09

The package-level protocol is the parent [`../ORCHESTRATION.md`](../ORCHESTRATION.md) (coordinator owns git; workers never push; per-merge sibling rebase; conflict ladder; held review-critical PRs). This file adds only what is specific to this package.

## Dispatch order

1. Pick the cut line in [`PRIORITY-MATRIX.md`](PRIORITY-MATRIX.md) §A (risk) — the owner decides; default is every row with risk `data-loss` or `security` plus every `critical`/`high` finding.
2. Within the cut, dispatch by workstream `orchestration.md` waves (S → M → L), applying the same-file collision rule from the BREAKDOWN.
3. Hard cap: **4 concurrent worker agents**. Workers must not spawn sub-agents (three agents did so during the 2026-09-10 reconciliation and raced on shared output files).
4. Review-critical briefs (data-loss / security, marked in the brief) are held for the owner — never admin-merged.
5. After each merge: rebase every open sibling worktree; re-run the matrix generator if a brief's status changed (`state/tools/merge_verdicts.py` → `build_matrix.py`).

## State

`state/` holds the raw agent outputs (`wave1/`, `wave3/`), the merged ledger (`merged.json`), each agent's final report (`RAW-RESULTS.md`) and the generators (`tools/`). Nothing there is hand-edited.
