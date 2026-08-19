### Fixed

#### The PebbleStore split evidence is reproduced, and one headline figure corrected

`docs/plans/2026-08-19-pebblestore-struct-split-decision.md` shipped with one
open caveat: its per-method counts came from a single sitting and had not been
reproduced, so the document told its own reader to re-derive them before acting.
That re-derivation is done, with a **different instrument** — a `go/parser` AST
walk sharing no code with the original regex and brace-extraction pass.

**The structure held; one published number did not.** The count of methods
touching a domain-local field was **20 (3.6%)**; it is **14 (2.5%)**.

This was not a measurement error — both passes counted correctly, but both were
asked the wrong question. They classified `libGen` and `counterMu` as
domain-local, while the same document's "Corrections" note reverses exactly that
assignment (`libGen` crosses domains; `counterMu` guards the shared `nextID`
allocator, so both are core). The contradiction was on the page all along: the
headline said 20, the prose said 10 + 2 + 1 + 1. Six methods leave the set —
`LibraryGeneration`, `CreateBook`, `UpdateBook`, `DeleteBook`, `nextID` and
`CreateNarrator` — and the plan's own step 1 already assumed as much by placing
`libGen`, `counterMu` and `nextID` in the core struct.

The correction makes the case for the split **stronger**: even less state is
domain-local than claimed, and the remaining 14 sit in four files with ten of
them in ops-v2.

Two robustness checks were added. No unexported helper touches a genuinely
domain-local field (55 of 62 touch a field, but all core), so helper indirection
cannot add a method to the 14. And `mem()` is the only field-hiding accessor on
the struct — enumerated rather than assumed — so the census's one special case
is complete. `117 no struct fields` is now labelled an upper bound, since some
of those reach `db` transitively.

The document still does **not** recommend proceeding. The split remains proposed
and unapproved, and nothing in the struct has been touched.
