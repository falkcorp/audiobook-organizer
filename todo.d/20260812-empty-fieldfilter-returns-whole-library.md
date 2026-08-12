## 🔴 An empty `FieldFilter` value silently returns the WHOLE library

Measured against production 2026-08-12 on `GET /api/v1/audiobooks?filters=…`:

| `filters` value | `count` returned |
|---|---|
| `[{"field":"title","value":"Hyperion"}]` | 4 |
| `[{"field":"title","value":"zzzzz-no-such-title-exists"}]` | 0 |
| `[{"field":"title","value":""}]` | **63,870 — the entire library** |

The filter works. An **empty value is dropped**, and the request degrades to an unfiltered
list. The response carries no indication that a filter was discarded: the caller asked
"which books have a blank title?" and got back every book in the library, including ones
whose titles are plainly non-blank (`The Awakened Spark`, `Hyperion`).

**Why this is worse than a plain bug.** The failure is indistinguishable from a legitimate
answer. Anyone measuring "how many books are missing field X" gets the library size and
may well believe it — the number is large, which is exactly what a "lots of books are
missing metadata" hypothesis predicts. It is a filter that answers "everything" when it
means "I ignored you", and it will silently corrupt any audit built on it. It blocked a
real measurement while investigating the organize target-path collisions.

The same silent drop applies to the flat `?title=` query parameter, which is not a
supported parameter at all — passing it returns the unfiltered first page rather than an
"unknown parameter" error. Two separate paths, both answering confidently to a question
they never applied.

**Fix direction.** Decide explicitly what an empty value means and make the code say so:

- If empty means "match rows where this field is empty" — implement it, and it becomes the
  natural way to audit missing metadata.
- If empty is not a supported query — reject the request with 400 and name the offending
  filter. Never silently widen the result set.

Either is fine. Silently returning everything is not. Add a test that pins the chosen
behaviour, because both alternatives look identical from the outside today.

Related: this is the same family as the search post-filter pagination defect and the
"fallback that triggers only on ZERO results" — a code path that cannot distinguish
"no constraint" from "constraint I could not apply".
