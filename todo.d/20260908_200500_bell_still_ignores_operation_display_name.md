## The bell still ignores `displayName` — the surface #3145 was named for

`ba0948244` ("one operation display name for the bell and the Activity page")
added `operationDisplayName(op)` to `web/src/components/layout/operationsFormat.ts`,
whose own doc comment says it "is what render code should call". The Activity page
calls it (`ActivityLog.tsx:744`, `:1466`). **The bell never does.** All three row
sites in `OperationsIndicator.tsx` (`:335`, `:512`, `:598`) still call
`formatOperationType(op.type)`, which sees only the bare type and never the
server's curated name.

So the two surfaces still disagree, on any op whose `displayName` is not
reproducible from its type via `OPERATION_LABELS`:

| op | Activity page | Bell |
|---|---|---|
| `library.ai-parse` (`displayName: "AI Filename Parsing"`) | AI Filename Parsing | **Ai Parse** |

Observed directly, not inferred: `OperationsIndicator.grouping.test.tsx` seeds an op
carrying `displayName: 'AI Filename Parsing'` and has to match `/Ai Parse/` for the
row to be found at all.

- [ ] Point the three `formatOperationType(op.type)` sites in
      `OperationsIndicator.tsx` at `operationDisplayName(op)` and update
      `OperationsIndicator.grouping.test.tsx` to match the display name instead of
      the type. Check the popover's other type-derived text at the same time —
      grep the file for `op.type` rather than fixing only the three found here.

Worth noting for whoever picks this up: the reason it survived is that
`formatOperationType` never fails. It title-cases whatever it is handed, so the
wrong name looks like a real name, and no test or type error could point at it.
The map lookup succeeding for `sql-migration` — the one op the original bug report
was about — is what made the fix look complete.
