### Added

#### Impact-vs-effort quadrant chart of every open burndown task

`docs/agent-tasks/todo-completion-2026-09/QUADRANT.md` draws all 258 open rows of the priority matrix on one Mermaid quadrant chart (x = effort, y = impact) with a legend that links every label to its brief, the finding's source line, or the section's TODO.md line. The row logic moved into `state/tools/matrix_rows.py`, shared by `build_matrix.py` and the new `build_quadrant.py`, and both now drop what merged after the 2026-09-10 freeze: finding and brief ids from `state/final/done_since_0910.json`, TODO.md items from the live checkbox state after re-locating each item by text (assembly shifts every line daily). `PRIORITY-MATRIX.md` gains section C listing the 45 rows closed since the freeze.
