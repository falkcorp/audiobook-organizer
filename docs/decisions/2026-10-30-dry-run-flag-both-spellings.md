<!-- file: docs/decisions/2026-10-30-dry-run-flag-both-spellings.md -->
<!-- version: 1.0.0 -->
<!-- guid: c52a7e19-4f3b-4d08-9a61-2be85d7f10c3 -->
<!-- last-edited: 2026-10-30 -->

# Decision log: both spellings of the dry-run flag on every op

Owner instruction 2026-10-30: take the recommended option where a choice is open
and write down what was chosen and what was rejected. This note covers the sweep
for the TODO items "The dry-run flag spelling is split across the op registry"
and DURATION-BACKFILL-ONTO-OPMODE.

## What the sweep found

The TODO text (measured 2026-09-20: 11 structs tag `dryRun`, 18 tag `dry_run`)
was out of date. On main at dd525352c, among the 83 registered ops that declare a
`Preview` with a dry-run key, 73 already decoded both spellings through
`opmode.ResolveDryRun`. Ten decoded only `dry_run`. No op params struct had a
plain `bool` dry-run field (`TestGuard_NoPlainBoolDryRunParams` already forbade
it), so no op applied when the flag was dropped. The harm in the ten was the
other direction: `{"dryRun":false}` previewed silently.

## Decisions

1. **Add a second `*bool` field per struct; do not embed `opmode.DryRunParams`.**
   The existing 73 ops use two named fields (`DryRun`, `DryRunCamel`) and
   `opmode.ResolveDryRun(opID, snake, camel)`. Embedding would change every
   composite literal and the Repairs-lane / HTTP handler call sites that build
   these structs by field name (`seriesDedupOpParams{DryRun: ...}`). Rejected:
   embedding, because it churns call sites for no behavioural gain and the
   registry's reflection (`PreviewSpec.ModeKeys`) reads either shape.

2. **Resolve the mode before any work, including before the store check where the
   decode already came first.** A disagreeing body is refused with
   `<op id>: dry_run=X and dryRun=Y disagree; send one`. For
   `maintenance.series-phantom-repair` the two spellings are resolved in every
   mode, including `report`, which otherwise ignores the flag: a malformed body
   is refused rather than read. Rejected: ignoring the flag in report mode, since
   the registry's own `SameMode` treats a disagreeing body as having no mode.

3. **Keep `dryRun()` methods on `libraryStateRepairParams` and
   `rewritePathPrefixParams` returning a bare `bool`.** They call
   `opmode.ResolveDryRun` and drop the error, because the Run functions refuse a
   disagreement first and `ResolveDryRun` pairs every error with `true`
   (preview), so a caller that skips the check still fails toward preview.
   `planRewritePathPrefix` is called directly by its tests with a params value.
   Rejected: changing the methods to `(bool, error)`, which would push an error
   return through the planner for a case the Run function already closed.

4. **`maintenance.version-group-primary-repair` keeps `apply` as the older
   spelling of `dry_run:false`.** `apply:true` beside a stated `dry_run:true` or
   `dryRun:true` is still refused ("contradicts"); `apply` alone writes.

5. **Two guards, different strengths.**
   - `TestGuard_DryRunParamsAcceptBothSpellings` (go/ast, in the existing opmode
     guard file next to `TestGuard_NoPlainBoolDryRunParams`, rather than a new
     file in `internal/arch`, to share `eachInputStruct` and the output-type
     rules) flags any non-output struct with a `*bool` under exactly one
     spelling. It cannot tell an op's params from an HTTP request body, so the
     non-op structs are in `allowSingleSpelling`, each with its reason (senders
     that always set the mode, HTTP request bodies, readers of stored results).
   - `TestOpsV3_DryRunFamilyDecodesBothSpellings` walks the live registry and
     checks the type each op declares in `Preview.Params` (both spellings, both
     `*bool`). The maintenance-job family is exempt from the `*bool` half only:
     its plain bools echo a mode the dispatcher already resolved from both
     spellings.
   Rejected: a single reflection test alone, because an op whose `Preview.Params`
   names a different type than the one `Run` decodes would pass it.

6. **HTTP request bodies stay single-spelling.** The five HTTP endpoints that
   write on an omitted `dry_run` are the open owner decision
   OPS-PREVIEW-HTTP-ENDPOINTS; this sweep does not touch them. They stay in
   `allowPlainBool` / `allowSingleSpelling`.

## Frontend

Every sender in `web/src` sends `dry_run` (snake_case) and never `dryRun`, and
every op accepts `dry_run`, so no frontend change was needed. The `dryRun`
identifiers in `services/api.ts` are JavaScript parameter names, not JSON keys.
