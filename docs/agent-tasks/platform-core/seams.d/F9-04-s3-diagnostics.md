<!-- file: docs/agent-tasks/platform-core/seams.d/F9-04-s3-diagnostics.md -->
<!-- version: 0.1.0 -->
<!-- guid: 9ace089b-46b5-4271-b45b-d75944a618a2 -->
<!-- last-edited: 2026-11-21 -->

# F9-04 slice 3: Diagnostics (seams for F9-03)

Continues `F9-04.md`.

| File | What it is |
|---|---|
| `web/src/components/diagnostics/DiagnosticsFrame.tsx` | The shell half: padding box, `Diagnostics` heading and a `children` slot. No books imports. |
| `web/src/modules/books/pages/AIDiagnosticsWidget.tsx` | The brief's "AI-dedup diagnostics" widget: the intro text, the `?category=` card picker, the export and AI-suggestion flow, results and the apply dialog. Takes no props. |
| `web/src/modules/books/pages/DBHealthWidget.tsx` | The Database Health accordion (SQLite, Pebble, embeddings, AI scans, metadata cache, the on-demand expired count) with its own state. **Not named by the brief**; it reads the library database, so it is classified books. |
| `web/src/pages/Diagnostics.tsx` | ABO's binding: frame, AI widget, `AIJobsPanel`, DB health widget, `CacheStatsPanel`, in the old order. F9-03 deletes it. |

For F9-03:
- `AIJobsPanel` and `CacheStatsPanel` stay where they are (the brief lists them as components the slots wrap) and are still imported by the binding, not wrapped as widgets yet. Each is a no-prop component, so a `lazy` entry needs only a `.then(m => ({ default: m.AIJobsPanel }))`.
- The AI widget reads and writes `?category=` with `useSearchParams` and `applyOwnedParams` (a `web/src/hooks` helper). Under the shell the page is `/system/diagnostics`, so any link carrying `?category=` must be re-pointed; the shell frame has no URL state of its own.
- The shell's own diagnostics (the native `/api/core/v1/diagnostics/{run}` and `ui/diagnostics` of F8-04) have no counterpart in this page; the shell frame should list them when the page moves.
