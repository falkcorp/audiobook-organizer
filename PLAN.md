<!-- file: PLAN.md -->
<!-- version: 1.1.0 -->
<!-- guid: f8f51548-8481-466d-b8e8-bc96a250ee51 -->
<!-- last-edited: 2026-09-01 -->

# Plan — collapse the two path→author parsers

Branch `refactor/unify-author-parsers`, worktree `.worktrees/author-parsers`.

Written while the user is AFK, on "fix everything you can". No approval gate was
available; the plan is recorded here so the reasoning is reviewable after the
fact. **Status: EXECUTED 2026-09-01**, with one finding that inverted the premise
— see "What the plan got wrong".

## Goal

`extractAuthorFromDirectory` and `parseFilenameForAuthor` existed as separate
copies in `internal/scanner` and `internal/metadata`. `internal/authorname`'s
package comment had been tracking them as the last outstanding duplication in
this cluster since the `personname` unification (#3029), with the note: *"Until
they are collapsed, a fix to one is not a fix to the other."*

Collapse them. Close that note.

## What was actually there — measured, not assumed

| function | scanner | metadata | real difference |
|---|---|---|---|
| `parseFilenameForAuthor` | 18 lines | 19 lines | **none** — identical modulo comments; both are a shim over `personname.ChooseAuthorSide` |
| `extractAuthorFromDirectory` | 121 lines | 60 lines | **two**: the path-split idiom, and 4 extra `skipDirs` entries |

The 121-vs-60 line gap is almost entirely comment. Stripping comment-only lines
left a diff of two hunks.

## The premise this plan started from was wrong

Memory carried the claim *"metadata runs FIRST, so a scanner-only fix is INERT."*
Both call sites are guarded by `== ""` on **different fields** (`metadata.Artist`
vs `book.Author`), so that framing did not obviously hold and had to be settled
before designing anything.

Traced: `scanner.go:1383` does `books[idx].Author = meta.Artist` when non-empty,
and two of the three `extractInfoFromPath` calls run before it. So metadata's
copy **can** overwrite scanner's result. The premise survives — but it turned out
not to matter, for the reason below.

## The finding that made this a small change

A 28-path differential corpus was run through **both** copies before anything was
moved. They disagreed on **exactly one** path:

```
/lib/Unknown Author/01.mp3   metadata => "Unknown Author"   scanner => ""
```

Every other path agreed, including all three of scanner's other extra `skipDirs`
entries (`import`, `imports`, `organized`).

**Why:** `personname.LooksLikePersonName` requires 2–4 words. Every single-word
directory name is refused by the shape gate whether or not `skipDirs` catches it
first — so 14 of the map's 15 entries **cannot change an answer**. `Unknown
Author` is the only two-word entry, hence the only live one.

So the change is a one-row behaviour delta, not the four-entry fix it looked
like. And that row is cleared by `metadata.go`'s own placeholder guard six lines
below the assignment, making it invisible to callers.

## Files changed

1. `internal/authorname/parse.go` — **new**. `ExtractAuthorFromDirectory` and
   `ParseFilenameForAuthor`, one implementation each, `skipDirs` as the union.
2. `internal/authorname/authorname.go` — the duplication NOTE closed.
3. `internal/metadata/metadata.go`, `internal/scanner/scanner.go` — copies
   deleted, 4 production call sites repointed.
4. `internal/{metadata,scanner}/author_parsers_shim_test.go` — **new**,
   test-only aliases so each package keeps its own consumer-side tests without
   churning ~40 references.
5. `internal/authorname/parse_test.go` — **new**, the corpus as a table.
6. `internal/metadata/unknown_author_directory_consumer_test.go` — **new**, the
   consumer-level assertion.
7. `changelog.d/` fragment — headerless.

## Test strategy

- **Differential over both copies, before and after.** Predicted result was an
  empty diff on the scanner side and one row on the metadata side; both confirmed.
- **Assert on the CONSUMER, not the helper.** #3029 measured a predicate and
  asserted about the consumer, and 886 bad author strings followed. So
  `ExtractMetadata` itself is driven over a real temp file under an
  `Unknown Author` directory, with a known-good twin (`Terry Pratchett`) so the
  empty-result assertion cannot pass vacuously.
- **Mutation-check the one live `skipDirs` entry** rather than trusting the green.

## Rollback

Single branch, no data migration, no config, no flags. `git revert` the merge.

## Explicitly NOT in scope

- `todo.d/20260825-directory-fallback-reads-title-as-author.md` — the scanner's
  Pratchett-036 guard. Referenced by the moved comments; not touched.
- Each call site's own guards and their ordering. The Pratchett-036 argument is
  about **when** the placeholder is cleared relative to the filename fallback;
  that is call-site-specific and deliberately stayed where it was.

---

## What the plan got wrong

**1. It expected a bug fix and found an inert divergence.** The plan was framed
around scanner's `skipDirs` being stronger and metadata's being the one that
decides production authors. Measurement inverted it: three of the four extra
entries are dead, and the fourth is masked downstream. Reasoning about the
predicate said "scanner is stronger"; running the function said "they agree on 27
of 28 paths, and the 28th is cleared anyway."

**2. It nearly shipped a test that measured the wrong string.**
`TestSkipDirsIsRedundantExceptForThePlaceholder` first asked
`LooksLikePersonName` about the map's **keys** — which are lowercased, because
lookup lowercases the directory name. The gate refuses `"unknown author"` for
starting lowercase, not for its shape, so every entry looked redundant and the
test failed on its own placeholder case. The question "could any capitalisation
of this key reach the gate?" needs the key title-cased first.

**3. The pre-existing scanner failure is not this change.**
`TestPersistChaptersForBook_MultiFileMP3s_SynthesizesFromTrackTags` fails with a
chapter-duration float mismatch. Verified byte-identical on unmodified `main`
(`9975.827` vs `9975.431111`) — an ffprobe/environment issue, unrelated.

---

## What review found that the plan and I both missed

Three review passes ran on the diff while CI was pending. Their two headline
findings were both in lines this change did not touch, and both were **false
claims I had written**.

**4. There is a THIRD path→author parser, and I wrote "nothing remains".**
`internal/metadata/folder_parser.go` — in the same package as one of the two
collapsed here — has its own container-skip map and its own shape predicate. Its
map carried no placeholder entry, so it read the organizer's own
`Unknown Author` directory back as a real author at **ConfidenceHigh**. Measured:

```
/books/Unknown Author/(Discworld 04) Mort/Mort - read by Nigel Planer
    -> Authors=[Unknown Author]  AuthorConf=3
```

Nothing downstream caught it, for a reason worth naming: `scanner.go`'s recovery
guard is `if Author == ""`. A **non-empty** placeholder *skips* the guard whose
defer would have cleared it — being wrong in a specific way let it evade the
check for being wrong. `resolveAuthorID` then mints or attaches a real
`Unknown Author` row. Fixed here and mutation-verified. The predicate divergence
is NOT fixed; it changes real answers and is tracked in `todo.d`.

**5. My consumer test's docstring claimed more than the test delivers.** It said
it would fail if a future change "drops the skipDirs entry". Mutation testing
showed it does not — the downstream clear catches the value anyway. Of the three
triggers it named, **the one this change introduced is the one it cannot see**:
the #3029 pattern, inside a comment written to warn about the #3029 pattern.
Corrected, and the honest scope of the test stated instead.

**6. Two mutants survived, both real gaps, both now killed.**
`len(parts) != 2` → `< 2` left every suite green; the exposing row needs a real
name AND a real title in the first two segments (`"a - b - c"` cannot see it,
because `ChooseAuthorSide` refuses both sides anyway). Under the mutant,
`"Neil Gaiman - Norse Mythology - 01"` files the **title** as the author. The tie
policy was likewise only caught in `internal/metadata`, through the shim — no
coverage where the argument for it lives.

**7. The translator branch is subsumed on realistic input.** Deleting it left all
three packages green; a 632-case probe and a 400,000-case fuzz found zero
differences on canonically-spaced input. Kept — 12 differences exist under
degenerate spacing and the branch gives the better answer there — but its rows no
longer count as evidence about credit parsing, and the code now says so.

**8. An unreachable guard was carried across.** `if len(parts) > 0` after
`SplitN` can never be false. `personname.go:457` refuses to write exactly that
shape, by name, because no test can kill it — so copying it here would have
imported the pattern its sibling package rejects. Removed.
