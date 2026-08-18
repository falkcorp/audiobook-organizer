<!-- file: docs/audits/2026-08-18-interface-width-shapes.md -->
<!-- version: 2.0.0 -->
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

---

# Part 2 — what measurement showed

Added 2026-08-18, after the flat-list sweep landed (#2542, #2545, #2546, #2547,
#2549, #2550, #2553) and the count went **28 → 6**. Everything below is measured,
not estimated; the method that produced each number is stated so it can be
re-run and disagreed with.

## 1. The width gate cannot see transitive width

`interfacebloat` counts **declared entries**. An embed counts as one, whatever it
drags in. So one embed of a 51-method interface costs a single slot, while
listing fifteen real methods costs fifteen.

The gate therefore scores the wide-embed style *better* than the honest form:

| form of `organizer.Store` | declared entries | transitive surface |
|---|---|---|
| 9 embedded `database.*` interfaces (as found) | **9** — near the limit | **179 methods** |
| the honest narrow form: 16 methods + 2 embeds | **18** — fails the gate | ~45 methods |

Narrowing that interface *by usage* makes its gate score more than twice as bad
while cutting its real surface by three quarters. The only shape that satisfies
both is to narrow by usage **and then group** the resulting methods into named
sub-interfaces — which is the right answer, but note that the gate arrived at it
by accident rather than by pointing at it.

**This is worth knowing before trusting the number.** The width ratchet is a
real guard against the flat-list shape — a 30-method interface cannot hide from
it — and it caught every one of the fourteen. It is blind to the composition
shape, which is where the actual method count lives.

## 2. Parameter types are what propagate width

The reason `organizer.Store` embedded a 30-method `database.OperationStore` was
not that the organizer needs 30 operation methods. It was that the organizer
passes its store to `operations.SaveParams` and `operations.ClearState`, and
those two functions *declared* `database.OperationStore`.

Both use exactly one method. So did the other five helpers in that file, and
`LoadParams` declared `database.Store` — the full 398-method type — to call one
getter. Fixed in #2552: seven signatures, five one-method interfaces, no caller
changes, and `state.go` no longer imports `internal/database` at all.

**A wide parameter is more expensive than a wide interface declaration.** A
caller must hold something satisfying the declared type, so a wide parameter
forces every caller — and every interface those callers declare — to carry the
whole surface. This is the mechanism by which width spreads, and it is invisible
to the linter.

When narrowing a wide consumer interface, look at what its store is *passed to*
before listing what it calls directly. The parameter may be the whole reason.

## 3. Measured usage of the remaining wide compositions

Method: empty the interface, then `go build -gcflags=-e ./...` plus
`go vet -gcflags=-e` and read the compiler's enumeration of unresolved calls.
This is exhaustive rather than estimated — the type checker sees transitively
reachable helpers that call-site greps cannot.

| interface | transitive surface | actually called | also assignable to |
|---|---|---|---|
| `bookHandlerStore` | 182 | **0** | nothing — **deleted in #2554** |
| `organizer.Store` | 179 | 16 | `database.OperationStore` (removed by #2552), in-package `OrganizerStore` |
| `audiobookStore` | 171 | 44 | 3 in-package narrow interfaces |
| `itunes/service.Store` | — | 24 | 7 interfaces, incl. `database.OperationStore` |

Two things fall out of this table.

**`bookHandlerStore` was dead.** Twelve embeds, 182 reachable methods, and zero
references outside its own file — it existed only to be asserted against
`PebbleStore`. So did its three siblings. The honest fix for a dead wide
interface is deletion, and the gate had been reporting it as a code-quality
finding to be *split*.

**`audiobookStore` is wide because the service is wide.** 44 of 171 is a real
74% cut, but 44 methods cannot be grouped into eight entries without groups of
five or six, and the honest narrow form scores worse on the gate than the ten
embeds that currently hide it. The number to react to there is 44 — that is a
service with 44 store dependencies, and *that* is the finding, not the interface.

## 4. Landing exactly on the limit is a latent failure

Sixteen interfaces sit at exactly 8 declared entries. They pass today and fail on
the next method added — a one-line change, by an author who did not write the
grouping and whose only outs are restructuring someone else's split or reaching
for a `nolint`.

Several were created by this very sweep, because the splitter was aimed at
"get under the limit," which reliably parks things *on* it. Corrected for
`MetadataStore` (#2550) and `abs.Store` (#2553); **split to ≤6 entries, not ≤8.**

## 5. Instrument notes

Three counting bugs in one afternoon, all of which produced plausible numbers:

- **Counting lines is not counting entries.** A wrapped signature spans several
  lines. `TranscriptionRunners` reported 12 and has 2 methods. Track paren depth.
- **A trailing comment on the brace line hides a declaration.**
  `type BookReader interface { //nolint:interfacebloat` did not match a regex
  anchored on `interface {\n`, so `BookReader` was silently absent from the
  expansion and `BookStore` came out as 16 instead of 51. Same failure family as
  the splitter's embed bug. Do not anchor on the newline.
- **`go build` stops at 10 errors.** The first usage probe of `organizer.Store`
  reported 5 methods; the real answer was 16. Use `-gcflags=-e`.

Each was caught by cross-checking against something independent — golangci-lint's
own count, and the separately-established figure of 51 for `BookStore`. A census
that only agrees with itself is not a census.

**The honest count is "reported + suppressed."** Two interfaces carry
`//nolint:interfacebloat` (`database.BookReader`, and one in
`internal/plugins/maintenance/deps.go`) and are invisible to the reported number.
