<!-- file: docs/agent-tasks/platform-core/F8-03b-share-safe-bundle.md -->
<!-- version: 1.4.0 -->
<!-- guid: 3c8e5f21-7a94-4d06-b2e1-0f6d9a4c7b85 -->
<!-- last-edited: 2026-11-16 -->

# F8-03b — Share-safe diagnostics bundle: replace the path redactor in `core/health/diagnostics` with a sanitizer that meets S1, delete the F8-03b marker, its runtime refusal and its gate

| Field | Value |
|---|---|
| ID | F8-03b |
| TODO id | `TODO-F8-03B-SHARE-SAFE-BUNDLE` (`TODO.md`, folded in from `todo.d/2026-11-15-f8-03b-share-safe-bundle.md`) |
| Release | R2, flip label F8 (dark PR; it adds no route and switches nothing) |
| Tier | **T3** (Sonnet at medium effort, then an Opus review and a T3 security review before merge): a sanitizer that decides what a third party may read |
| Wave | W37 (any wave after F8-03's W30; it must merge before F8-04a's W38 and it is file-disjoint from every other wave-W37 row) |
| Depends on | F8-03 (merged: the Exporter, the marker, the gate and the open-leak list are its) |
| Blocks | F8-04a (its item 3, Health, mounts the diagnostics routes) |
| Spec | 05-health §5H.3.5 (the diagnostics bundle, layer 2 "absolute filesystem paths outside known roots"); `docs/agent-tasks/platform-core/seams.d/F8-03.md` ("S1", "Round 8: what the redactor does not meet"); the round 7 review of PR #4658 (the break-it and design review of head 7235dc265, option "3+5 hybrid") and the round 8 delta review (ba689b7bc) |

> **Done (round 13, `seams.d/F8-03.md`).** The sanitizer, the deletions and the tests below landed in one PR. The text
> that follows is the brief as it was handed out; the marker, the refusal and the gate it describes no longer exist.

**Gate:** plan, then execute autonomously in a worktree with its own PR. Dark: nothing in ABO switches, no route is
mounted. Until this row merged, `diagnostics.New` and `Exporter.Build` returned `diagnostics.ErrRedesignPendingF8_03b`
outside a test binary, and `archtest.F803bGate` failed the build if core's non-test code imports
`core/internal/healthhttp` or builds the Exporter, if the marker is `false`, or if the refusal's probe is not
`testing.Testing`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f8-03b-share-safe" -b agent/platform-core-f8-03b-share-safe origin/main
cd "$REPO/.worktrees/pc-f8-03b-share-safe"
git rebase origin/main
```

Read first: `docs/agent-tasks/platform-core/seams.d/F8-03.md` ("S1", then "Round 8" with every open leak and its input) and
`core/health/diagnostics/paths.go` (the marker, the refusal and the scanner you replace).

## Goal

A diagnostics zip can be handed to a third party. **S1 (share-safety):** no byte of the bundle names a directory, a
file, a user or a host that a configured root does not name by `<root:NAME>`. The F8-03 path redactor (`scanPaths`,
`tokenAt` and the grammar around them in `paths.go`) is a token scanner. The round 7 review found it cannot meet S1
whatever is patched into it: usernames and directory trees leak for a volume called `Macintosh HD`, a profile
`C:\Users\Bruce Wayne`, `-I/Users/...` build flags, `~bruce/`, `smb://` paths and more, and the property tests only
ever proved the scanner's own invariant. This row replaces the scanner with a structure that meets S1 by construction,
wires it to every byte that leaves, and deletes the marker, the runtime refusal and the gate.

## Obligations (the reviewer's exact list; every item is a gate of this row)

1. **Replace `paths.go`'s scanner** with the structure in the outline below:
   - `Sanitizer.Text`: split on `unicode.IsSpace`, wipe trigger tokens, the bridge rule, idempotent;
   - exact Go file-error template parsing (`*os.PathError`, `*os.LinkError`, `exec:`);
   - a validated key allowlist;
   - keys always through `Text`, with the linear collision counter (`keyNamer` in `bundle.go`).
2. **Apply it to every byte that leaves:** `Writer.JSON` values and keys, `Writer.File` lines, manifest text
   (`Errors`, `Truncated`), and zip entry names (reject a name `Text` would change, leak n1). Also the health route's
   Detail: inject `Sanitize func(string) string` into `healthhttp`'s deps, which closes leak m1.
3. **Close M1, M2, M3, M4, M6 and m1** (the leaks of the seams note's "Round 8"; the letters name leaks, not the
   mutants of earlier rounds). Turn every input listed there into an exact-output test and a fuzz seed, M6's JSONL
   input included.
4. **Tests:**
   - (a) a structural property and fuzz target with no marker tables;
   - (b) idempotence;
   - (c) a residual table that asserts exactly what survives;
   - (d) a template table built from real `os` errors on temp directories whose names hold spaces, quotes, commas,
     tabs and Unicode;
   - (e) linearity at 1 MiB, plus 50,000 colliding keys, **asserted without a clock**: count operations (as
     `TestKeyNamer_ProbesAreLinear` counts lookups), never a wall-clock bound; wall-clock bounds failed core-ci twice on
     F8-03 for a scan that was linear;
   - (f) validator negatives;
   - (g) an end-to-end `Build` over a realistic fake ring, scanning the zip bytes for planted usernames.
5. **Delete in the same change** (removal is this PR's reviewed act, and no check enforces it: the marker has two
   states, `true` and deleted, and `archtest.F803bGate` fails on `false` in any file of the package, built or not, and
   on a second declaration of it, so nothing but deleting it opens the gate):
   - `diagnostics.RedesignPendingF8_03b` (or its derived replacement);
   - `ErrRedesignPendingF8_03b` with `redesignRefusal`, the `Exporter.isTestBinary` field and `testBinary`;
   - the real-binary proof: `core/health/diagnostics/redesign_binary_test.go` (it builds `testdata/probemain/` under
     each tag set `archtest.F803bShippedTagSets` derives from `TagSets()` and the `-tags` lists of the build files, make
     variables resolved, one build per distinct file selection, and runs it) and `testdata/probemain/`,
     and `redesign_refusal_test.go`;
   - the guard files `core/archtest/f803b_gate.go`, `f803b_gate_test.go`, `f803b_probe.go` and `f803b_probe_test.go`
     (the gate with its build-constraint rule, the probe pin, `F803bShippedTagSets` and `F803bNoLinkerOverride`, which
     keeps a build file of the repo from passing `-ldflags "-X testing.testBinary=1"`);
   - `paths.go`'s grammar machinery with the tests that pin only that grammar.
   (`archtest.Package.IgnoredGoFiles` is general loader API and stays.)
6. **Update docs:**
   - the seams note: S1's status, the residual and the costs;
   - the F8-04a brief: no STOP line (the old F8-04 brief's was removed) and F8-03b stays in `Depends on`;
   - a changelog fragment;
   - check `TODO-F8-03B-SHARE-SAFE-BUNDLE` off in `TODO.md` (grep `TODO.md` first: the fragment is folded in by the
     daily job and its deletion proves nothing).
7. **Record the owner's rulings, or the defaults taken** (see "Owner decisions" below).
8. **Get an Opus review and a T3 security review before merge.** F8-04a item 3 (Health) may not start until F8-03b has
   merged.

## Outline (from the round 7 review, option "3+5 hybrid")

1. New `sanitize.go`, replacing `paths.go`: `type Sanitizer struct{ roots pathRoots }` and
   `func (s Sanitizer) Text(str string) string`. Split on `unicode.IsSpace` (so U+2028 and U+00A0 count as
   whitespace). A token is **triggered** if it contains `/`, `\`, `%`, `&#` or `~` anywhere, or, after stripping
   leading `"'([{<`, starts with `[A-Za-z]:`. A triggered token becomes `<root:NAME>` when its body, cleaned under both
   separator readings with a case-insensitive drive match, sits under a configured root, and `<path>` otherwise;
   leading opening punctuation stays outside the placeholder and a trailing run of `: , ; . ) ] } " '` stays after it.
   Placeholders hold no trigger byte, so `Text(Text(x)) == Text(x)`. **Bridge rule:** untriggered words between two
   triggered words in the same string are wiped too (this closes `HD` in `/Volumes/Macintosh HD/Users/x`).
   Untriggered tokens are kept byte for byte with their whitespace.
2. Template recognisers, applied before the token wipe, for Go file-error messages, parsed exactly: `*os.PathError` is
   `<op> <path>: <err>` with `op` one of the `os` package ops (`open`, `stat`, `lstat`, `mkdir`, `remove`, `rename`,
   `readdir`, `readlink`, `chmod`, `chown`, `chtimes`, `truncate`, `symlink`, `link`, `unlinkat`, `fdopendir`, `write`,
   `read`, `close`, `seek`, `sync`), and `<err>` the remainder after the last `": "` whose suffix is a known
   `syscall.Errno` string or the `fs.ErrNotExist`, `ErrPermission` or `ErrExist` text; `*os.LinkError` is
   `<op> <old> <new>: <err>`; `exec: "<name>": ...` follows the same pattern. Every path between the op and the errno
   is one `<path>` or `<root:NAME>`, spaces and all; wrapped errors are found by searching for ` <op> ` from the left
   and the errno suffix from the right.
3. Key allowlist in the JSON walk: `safeKeys map[string]func(string) bool` with validators for `level` (enum), `time`
   (RFC 3339), `component`, `module`, `kind`, `task`, `state`, `status`, `outcome`, `reason`
   (`^[a-z0-9_.:-]{1,64}$`), `method` (HTTP enum), `route` and `pattern` (`^/[A-Za-z0-9/{}._-]*$`, no `..` segment: a
   route template, never the raw URL), `run_id` and `id` (ULID or UUID), `version` (semver or VCS revision). A value
   that passes its key's validator is kept; anything else goes through `Text`. Keys always go through `Text`, with the
   linear collision counter. Order stays: log redactor first (secrets), then the allowlist or `Text`.
4. Use the sanitizer everywhere text leaves: `Writer.JSON`, `Writer.File` lines, manifest `Errors`, zip entry names
   (reject a name that `Text` would change), and the health route's Detail (`healthhttp` gets
   `Sanitize func(string) string` in its deps), which fixes m1.
5. Delete the grammar machinery (`scanPaths`, `tokenAt`, `pathAt`, `urlText`, `afterCut`, `routeText`, `routePath`,
   `lenientDecode`, the `judged` and `cut` bookkeeping, G1 to G9) and the tests that pin only that grammar
   (`paths_adversarial_test.go`'s grammar tables, `round4_test.go`, `round5_test.go`, the grammar parts of
   `hardening_test.go`). Keep `PathRoot`, `newPathRoots` and the root-matching helpers. Delete the marker, the runtime
   refusal and the gate in the same change.
6. Docs: S1, the residual and the costs in the seams note (inline routes and URLs, MIME types and `a/b` vanish from
   free text; structured `route` and `method` keep the useful part), and a changelog fragment.

**Superseded in round 14 (the review of `7d3057ba1`).** The rule in items 1 to 4 above (a literal bridge between
triggered words, a template parsed by operation, `Sanitize func(string) string` in the health deps, and the residual
that was described here as "the last word or words of a space-containing final segment") is replaced: a location now
wipes from its first word to the end of the line, a known error text that ends the line is the only thing kept after
it, and `healthhttp.Deps` takes `Roots`. The residual was a title or username word, so it is removed and no ruling
accepted it. `seams.d/F8-03.md`, "Round 13", is the current description.

## Owner decisions, with the defaults this row assumes

1. Accept the small residual above: **default assumed, withdrawn in round 14** (it was never an owner ruling; the residual no longer exists).
2. Library author and title paths under a configured root (leak m3): **hidden**, `<root:NAME>` only.
3. Bare usernames and hostnames outside paths (n3): **out of scope**.
4. Move the path rule into the shared log redactor: **inject it now, move it later** (a 05-logging edit).

Record each ruling, or the default taken, in the seams note.

## Files

| File | Change |
|---|---|
| `core/health/diagnostics/sanitize.go` (+ `sanitize_test.go`, a fuzz target) | new |
| `core/health/diagnostics/paths.go` | grammar deleted; `PathRoot`, `newPathRoots` and the root helpers kept (move to `roots.go` if clearer); marker, `ErrRedesignPendingF8_03b`, `redesignRefusal` deleted |
| `core/health/diagnostics/bundle.go`, `build.go`, `manifest` code | the sanitizer on every byte that leaves; `isTestBinary` and `testBinary` deleted |
| `core/health/diagnostics/*_test.go`, `testdata/probemain/` | grammar-only tests deleted; the refusal tests (`redesign_refusal_test.go`, `redesign_binary_test.go`) and `testdata/probemain/` deleted; new tests (a) to (g) |
| `core/internal/healthhttp/` | `Sanitize` injected into the deps; Detail passes through it |
| `core/archtest/f803b_gate.go`, `f803b_gate_test.go`, `f803b_probe.go`, `f803b_probe_test.go` | deleted |
| `docs/agent-tasks/platform-core/seams.d/F8-03.md`, `F8-04a-f8-flip-backup-health-metrics.md` | updated |
| `changelog.d/`, `TODO.md` | a fragment (no header, no `##` heading); the TODO entry checked off |

## Re-verify before editing (expected results at your base)

```bash
grep -n 'RedesignPendingF8_03b' core/health/diagnostics/paths.go            # the marker, const true
grep -rn 'ErrRedesignPendingF8_03b' core/health/diagnostics/*.go            # the refusal in New and Build
ls core/archtest/f803b_*.go                                                  # the gate, the probe pin and their tests
grep -n 'F8-03b' docs/agent-tasks/platform-core/F8-04a-f8-flip-backup-health-metrics.md   # Depends on (F8-04a carries no STOP line for F8-03b: it was removed when F8-03b merged)
grep -n 'TODO-F8-03B-SHARE-SAFE-BUNDLE' TODO.md todo.d/*.md                  # the entry (TODO.md once folded in)
```

## Acceptance (commands and expected output)

```bash
cd core
go vet ./health/... ./internal/healthhttp/... ./archtest/...
go test -race -count=1 ./health/diagnostics/ ./internal/healthhttp/ ./archtest/
go test -run=NONE -fuzz=FuzzSanitizerText -fuzztime=60s ./health/diagnostics/   # no failure; seed the corpus with the seams note's inputs
cd .. && make ci && make -C core ci
```

Expected: every leak of the seams note's "Round 8" list is an exact-output test, the fuzz run finds nothing, no test
reads a clock to assert linearity, and `grep -rn 'RedesignPendingF8_03b\|F803bGate\|F803bNoLinkerOverride' core docs` finds only history.

## Out of scope

- Mounting the diagnostics routes, switching ABO's `diagnostics.export` and deleting `InterimExport` (F8-04a).
- Moving the path rule into `core/log`'s `Redactor` (a later 05-logging edit).
- Bare usernames and hostnames outside paths.
- The legacy `POST /api/v1/diagnostics/export` in ABO's `internal/diagnostics`, which does not use this redactor.

## Rollback

Revert the PR. The marker, the refusal and the gate come back with it; nothing is mounted either way.

## Repo rules

- Worktree off fresh `origin/main`; headers on every file; fragments headerless.
- Conventional commit, for example `feat(core/health): share-safe diagnostics sanitizer, delete the F8-03b gate`,
  ending with the attribution lines; `gh pr merge <n> --rebase`; never squash; never `--admin`.
- Fixtures: Batman characters and comic titles only, `192.0.2.x` for addresses, `.example` for hosts; no real library
  rows.
- Targeted `-run` locally; the full suites run on CI.
