<!-- file: docs/agent-tasks/2026-08-30-m16-investigation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3a7c1f92-6d84-4b1e-9f05-72c8ae4d1b60 -->
<!-- last-edited: 2026-08-30 -->

# M16 "SURVIVED" on PR #2987 — the harness was lying

**Verdict: (a). The mutation was never applied to the code it names.** M16 is not a
coverage gap, not a fixture problem, and there is no missing test to write. With the
harness bug fixed, M16 is KILLED by exactly the test that should catch it.

Branch under investigation: `perf/activity-index-limit-pushdown`, head `8c40c5313`.
Work branch: `fix/activity-pushdown-m16`.

## The evidence that settles it

M16 targets the four-field refusal in `pactIndexPushdownEligible`
(`internal/database/pebble_activity_store.go:2030`):

```go
if f.Type != "" || f.Level != "" || f.Source != "" || f.Search != "" {
    return false
}
```

`scripts/mutation-matrix.sh` documented `\x7c` as the escape for a literal `|` inside
a mutation expression (the table parser splits fields on `|`), and then **un-escaped
it in the shell before handing the expression to perl** — line 223 of v1.2.0:

```bash
expr="$(echo "${expr:-}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | sed 's/\\x7c/|/g')"
```

In a perl **pattern** a bare `|` is alternation. `A || B` is therefore alternation with
two **empty branches**, and an empty branch matches the zero-length string at offset 0.
`s///` fires there instead of on the gate.

Reproduced by replaying the harness's own field parsing under real `bash` and applying
the result:

```
$ perl -0777 -pi -e "$EXPR" internal/database/pebble_activity_store.go
$ git diff -- internal/database/pebble_activity_store.go
@@ -1,3 +1,4 @@
+<TAB>
 // file: internal/database/pebble_activity_store.go
 // version: 1.10.0
```

The gate at line 2034 came through **byte-for-byte unchanged**. The whole "mutation"
was a tab and a newline prepended to line 1. That is why the suite stayed green.

Guard 3 (`git diff --quiet`) asks *did the file change*, not *did the intended text
change*, so it scored the entry APPLIED. The file still compiled. The suite still
passed. Result: `SURVIVED`.

## Confirmation after the fix

`--run` narrowing can only manufacture false **survivors**, never false kills, so a
KILL under `--run` is conclusive.

```
$ scripts/mutation-matrix.sh --pkg ./internal/database/ \
    --table <M10,M11,M16 only> --run 'IndexPushdown|ActivityFilterFieldCount'
# baseline GREEN
KILLED | M10 an orphaned ref consumes an offset slot | TestIndexPushdownBoundaryNilVsEmpty,TestIndexPushdownOrphansScatteredThroughoutAgreeWithFullPath,...
KILLED | M11 off-by-one in the offset skip          | TestIndexPushdownBookIndexAgreesWithFullPath,TestIndexPushdownBoundaryNilVsEmpty,TestIndexPushdownForeignRefBeforeOffsetShiftsPage
KILLED | M16 push down a filter the index cannot decide | TestIndexPushdownEligibilityRefusesUndecidableFilters,.../refused/level,.../refused/search
# score : 3/3 killed (100%)
```

M16 is killed by `TestIndexPushdownEligibilityRefusesUndecidableFilters` on the
`level` and `search` subtests — the named asserting test, not unrelated noise.

## Hypotheses ruled out, and how

1. **(b) "applied but the fixture cannot observe it"** — ruled out by the diff above.
   The gate's bytes are identical to HEAD, so there is nothing for any fixture to
   observe. This was the most plausible story going in (the pushdown re-applies
   `matchesFilter` per decoded page row and decrements `total`, so a small fixture with
   `Limit >= len(rows)` really would agree on both paths). It is *not* what happened.
2. **(c) "genuine coverage hole"** — ruled out by the KILLED line above. The
   `panic("M16 reached")` probe was therefore never needed: a kill proves the branch is
   reached, which is strictly stronger than a panic firing.
3. **"the table entry is malformed"** — ruled out. The entry is correct as written;
   `perl -0777 -pe 's/if a \x7c\x7c b \{\nreturn false\n\}/GATE-DELETED/'` matches a
   literal `||` and rewrites it. The bug was entirely in the shell un-escaping.

## Two further instrument defects found

**Defect 2 — a table's LAST mutation is silently dropped.**
`while IFS='|' read -r ... done < "$TABLE"` exits *without running the body* when the
final line has no trailing newline. `activity-index-pushdown.muts` ended without one,
so **M18 has never run, not once**. M18 is the entry documented as a KNOWN EQUIVALENT
MUTANT expected to SURVIVE, with a note asking the reader to "confirm it stays
survived" — and a mutation that is simply *absent* from a report is indistinguishable
from one that ran and survived. Every run looked like the confirmation.
`mutations attempted` is counted inside the loop, so the total was short by one too.

Measured: `grep -cE '^M[0-9]+ '` = 18; the harness's parse loop yielded 17. Fixed with
`|| [[ -n "${name:-}${file:-}${expr:-}" ]]` on the `read`, verified against a two-line
unterminated fixture, and the table's newline restored. Both halves fixed, because
either alone leaves the next table author exposed.

**Defect 3 — the escape's blast radius.** `missing-file-census.muts` (merged, 22
entries) also uses `\x7c`, but only in a *replacement* half, where a bare `|` is
harmless. Verified by execution that it still yields `if decisive || true {` after the
fix, and ran the new sentinel guard over **all 39 entries of both tables**: 39 accepted,
0 rejected. No merged table was disarmed.

## New guard

Guard 5: before the source file is touched, the expression is run against a **one-byte**
sentinel and the output must be byte-identical. No real mutation pattern matches one
arbitrary byte; a zero-width one matches every input there is. Verified both ways
(broken pattern rejected, correct pattern accepted).

The sentinel is one byte and **not** the empty string: under `-0777 -p` perl's read loop
never executes on a zero-length stream, so an empty sentinel produces empty output for a
broken pattern and a working one alike, and the check would pass for both.

## The gate fails CLOSED on prefixes and OPEN on fields

Settled by adding a dummy `Category string` field to `ActivityFilter` in a throwaway
worktree:

- `go build ./...` → exit 0. `go vet ./internal/database/` → exit 0. **No build break.**
- `pactIndexPushdownEligible("act:op:X:", ActivityFilter{OperationID:"X", Limit:10,
  Category:"anything"})` → **true**. An unclassified field is accepted by omission and
  silently enables the pushdown.
- `TestActivityFilterFieldCountIsPinned` fails: `expected: 15, actual: 16`, with a
  message naming the hazard.

So the `reflect.TypeOf(ActivityFilter{}).NumField()` pin **is** a working tripwire, but
it is a *test* failure, not a build break. Unknown index-key families do fail closed
(`default: return false`, pinned by M17).

**Not a blocker for #2987** — this is the PR's own documented deny-list design, the pin
fires, and the test's doc comment already records the Since/Until dependency.

**Follow-up worth considering (not done, deliberately out of scope):** convert the gate
to fail closed by construction rather than by pin. Copy the filter, zero the fields the
index *can* decide, and refuse if anything remains:

```go
rest := f
rest.Limit, rest.Offset = 0, 0
rest.OperationID, rest.BookID = "", ""
rest.Since, rest.Until = nil, nil
if !reflect.DeepEqual(rest, ActivityFilter{}) { return false }
```

A new field then refuses the pushdown unless someone explicitly zeroes it. Cost: one
`reflect.DeepEqual` per query (negligible against a Pebble scan) and rewriting the
gate's central function inside a performance PR. User's call.

## `cd91bed8d` has a false commit message

It describes renaming the page loop's counter `rank` → `skipped` and dropping its dead
increment. **The commit contains no such change** — it touches only
`activity-index-pushdown.muts`. The source edit was eaten by a concurrent
`mutation-matrix.sh` run: the harness restores with `git checkout -- <file>`, which
cannot tell a mutation from an uncommitted edit to the same file.

The rename is redone in `9d295e611`, whose message says plainly that `cd91bed8d`'s was
inaccurate and why. History was **not** rewritten; `cd91bed8d`'s message stays wrong
where it is.

This also explains why M10 and M11 reported NOT-APPLIED: both referenced the `skipped`
identifier the lost edit would have introduced. M10 had a *second*, independent reason —
it anchored on a sentence of **comment prose that was never in the file at all**. It now
anchors on `if !alive[i] { continue }`, executable text a reword cannot silently disarm.

## What I was doing when I stopped

Running the full 18-entry matrix without `--run` (`matrix-clean.txt`, at `1099c17aa`).
It reached `baseline GREEN`, `M01 KILLED`, `M02 KILLED` and was stopped on a timebox.
The package suite is ~327 s per mutant, so a full pass is ~2 h.

**Single next step:** re-run that full pass to completion. Expect
`17 killed / 1 survived / 0 not-applied`, the survivor being **M18** — which will be the
first time M18 has ever executed. If anything other than M18 survives, that IS a real
coverage gap and should be treated as one.

Everything M16-related is already conclusive; the full pass is confirmation breadth, not
the merge decision.

## Surprising things worth keeping

- A guard that asks "did the file change" is not a guard that the *intended* change
  happened. Any zero-width-capable pattern splits those two apart.
- A mutation missing from a report and a mutation that survived look identical. Count
  the entries against the report before reading the score.
- The harness's own header warns that editing a running shell script corrupts it (bash
  reads by byte offset). The same applies to the **table**, which is read via
  `done < "$TABLE"` — do not edit a `.muts` file while a run is consuming it.
- `--run` narrowing can only produce false *survivors*, never false kills. A KILL under
  `--run` needs no confirmation; a SURVIVED does.
