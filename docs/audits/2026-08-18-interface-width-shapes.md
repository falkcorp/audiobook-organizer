<!-- file: docs/audits/2026-08-18-interface-width-shapes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e5b0c92-41da-4f38-b6a7-92d3f1e08c54 -->
<!-- last-edited: 2026-08-18 -->

# Not every wide interface is the same problem

Measured on `36f0819e`, from an isolated clone with a cleared golangci-lint
cache. 23 `interfacebloat` findings, classified by **structure** rather than by
count — because the count is what the linter reports and the structure is what
decides whether splitting is worth anything.

| Shape | Count | What splitting achieves |
|---|---|---|
| Flat method list | 14 | Real decomposition |
| Pure composition of embeds | 5 | **Nothing** — moves embeds between buckets |
| Mixed (embeds + 1–2 methods) | 4 | Depends; mostly composition-shaped |

## The five pure compositions

```
40  database.Store                    (internal/database/store.go)
17  itunes/service.Store              (internal/itunes/service/store.go)
12  server.bookHandlerStore           (internal/server/interfaces.go)
12  maintenance.JobStore              (internal/maintenance/job.go)
 9  organizer.Store                   (internal/organizer/service.go)
```

Every one of these is a list of embedded `database.*` interfaces and **zero
declared methods**. `interfacebloat` counts declared entries, so all five could
be turned green in twenty minutes by regrouping their embeds into buckets of
eight — `itunes/service.Store` into three groups of six, and so on.

**That would be gaming the gate.** The transitive method surface would be
identical, every consumer would still reach the same hundreds of methods, and
the resulting sub-interfaces would carry no meaning: there is no honest name for
"the first six of seventeen embeds."

The real problem with these is not their width, it is that each embed is itself
wide. The fix is to narrow them **by actual usage** — declare only the methods
the consumer calls, and let the type checker prove the rest were unused. That is
a different, larger, and more valuable piece of work, tracked as phase 2 of
`docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md`.

Two of the five have a known disposition already:

- **`maintenance.JobStore`** is scheduled for replacement by per-job interfaces
  (phase 2, item 1). Splitting it now is work thrown away.
- **`database.Store`** is the phase-2 target by definition: the goal is to make
  it unreachable, not narrower.

## Why this is written down

Because the count is the CI gate, and the gate cannot tell the difference. A
future contributor under pressure from a red build has a twenty-minute way to
make five findings disappear without improving anything, and no signal that it
is the wrong move. This file is that signal.

If you are about to split one of the five above: check whether it has any
declared methods at all. If it does not, you are regrouping embeds, and the
`.interface-width-baseline` number should stay where it is until the usage
narrowing is done properly.

## The fourteen flat lists

These are genuine decompositions and have been or are being done mechanically,
with the original name retained as the composition of its pieces so the method
set stays byte-identical and no consumer moves. Tooling:
`scripts/split_interface.py` and `scripts/verify_interface_split.py`.

Two of the four "mixed" declarations (`audiobookStore`, `audiobookUpdateStore`,
both 10 embeds + 1 method) belong with the compositions, not the flat lists.
