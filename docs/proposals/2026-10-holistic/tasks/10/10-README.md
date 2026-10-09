<!-- file: docs/proposals/2026-10-holistic/tasks/10/10-README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4ebed018-afbd-4c8b-9e23-316a6c751eaa -->
<!-- last-edited: 2026-10-09 -->

# Task briefs for doc 10 (Deluge cleanup after organize)

Source: `docs/proposals/2026-10-holistic/10-deluge-cleanup-after-organize.md` §4. Template: `../00-TEMPLATE.md`.

| Id | Title | Wave | Model | Size | Depends on | Brief |
|---|---|---|---|---|---|---|
| 10-PR0 | Deluge client: remove-with-data, ratio, seed time, files | 1 | sonnet | S | none | `10-PR0.md` |
| 10-PR1 | Write the torrent-to-book link on import | 1 | sonnet | M | 10-PR0 | `10-PR1.md` |
| 10-PR2 | `deluge.link-backfill` op | 1 | opus | M | 10-PR0, 10-PR1 | `10-PR2.md` |
| 10-PR3 | The cleanup fixer (trial, approval, journal, remove) | 3 | not briefed | L | 10-PR0, 10-PR2, 05 PR5/6/7 | doc 10 §4 PR 3 |
| 10-PR4 | Metrics, alert and panel for the cleanup | 3 | not briefed | S | 10-PR3, 11-PR1 | doc 10 §4 PR 4 |
| 10-PR5 | Repairs row parts and settings UI | 3 | not briefed | S | 10-PR3, 05 PR10 | doc 10 §4 PR 5 |
| 10-PR6 | Docs and executive summary | 3 | not briefed | S | 10-PR3 | doc 10 §4 PR 6 |

Notes:
- Run 10-PR0, then 10-PR1, then 10-PR2 in order; they touch overlapping Deluge files.
- 10-PR1 and 10-PR2 make rows eligible for the (manual-only) bulk Deluge import candidate set; each brief says so and pins it with a test.
- `deluge_move_enabled` stays false throughout (D57).
