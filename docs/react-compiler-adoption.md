<!-- file: docs/react-compiler-adoption.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8555b148-6a5c-4950-94d1-cfbdfa1e4c10 -->
<!-- last-edited: 2026-08-19 -->

# React Compiler adoption

The React Compiler (`babel-plugin-react-compiler` 1.0) is **enabled** for the web
frontend. This document records what it actually does to this codebase, what is
blocking it, and what is worth fixing.

## How it is wired

`web/vite.config.ts` runs it through `@rolldown/plugin-babel`:

```ts
babel({ presets: [reactCompilerPreset()] })
```

**This wiring is load-bearing.** Passing the plugin through `react()`'s own
`babel` option — the form every pre-Vite-8 guide shows — is a *silent no-op*
under rolldown: the build succeeds, exits 0, and emits no compiled output. Verify
by grepping an unminified build for `react.memo_cache_sentinel`, not by checking
the exit code.

`@vitejs/plugin-react` 6 also exposes `compiler: true`, backed by the Rust oxc
port. That is documented as experimental; the babel plugin is the stable 1.0
release, so we use it.

## What it is achieving

Measured 2026-08-19 with the compiler's own `logger` hook, over `src/**/*.{ts,tsx}`
excluding tests:

| | count |
|---|---|
| Components compiled | **111** across 89 files |
| Components bailed out | **218** across 79 files |

A bailout is not a bug. The compiler declines to optimize anything it cannot
prove safe rather than miscompiling it, so every one of these 218 behaves exactly
as it does today — just unmemoized.

## Why components bail out

| count | share | reason |
|---:|---:|---|
| 184 | 84.4% | `TryStatement` with a `finally` clause |
| 10 | 4.6% | value blocks (conditional / logical / optional chaining) inside `try`/`catch` |
| 8 | 3.7% | one or more React ESLint rules disabled in the file |
| 8 | 3.7% | `ThrowStatement` inside `try`/`catch` |
| 3 | 1.4% | refs accessed during render |
| 2 | 0.9% | `UpdateExpression` on a variable captured in a lambda |
| 2 | 0.9% | `TryStatement` without a `catch` clause |
| 1 | 0.5% | `TSNonNullExpression` as an object key |

**93.6% of bailouts (204/218) are unimplemented compiler features around
`try`/`catch`/`finally`, not code-quality problems.** No lint rule reports them.
They are overwhelmingly this shape:

```ts
try { ... } catch (e) { ... } finally { setLoading(false); }
```

Unblocking them means hoisting the `finally` body into both the success and
failure paths, in ~24 files. That is a real refactor with a real risk of changing
error-handling semantics, in exchange for optimization only. It is **deferred**,
not rejected.

## Relationship to the ESLint warnings

`web/eslint.config.mjs` enables the compiler rules from
`eslint-plugin-react-hooks` 7.x at `warn`. That currently reports **123
violations across 60 files** (of 151 total warnings).

**Do not treat that number as the compiler work list.** The two sets barely
overlap: the lint rules account for roughly 5% of real bailouts. Fixing all 123
would leave ~207 of 218 components still unoptimized. The lint warnings are worth
fixing on their own merits — `set-state-in-effect` (94 of them) flags derived
state that should be computed during render — but that is a React-hygiene project,
not a compiler-enablement one.

## The short list actually worth fixing

These are the bailouts that are *both* cheap and correctness-adjacent:

| file | line | reason |
|---|---:|---|
| `src/components/audiobooks/SearchBar.tsx` | 156 | refs during render |
| `src/components/bookdetail/BookDetailInfoTab.tsx` | 36 | refs during render |
| `src/components/TagComparison.tsx` | 65 | `TSNonNullExpression` object key |
| `src/pages/Library.tsx` | 133 | `UpdateExpression` captured in lambda |
| `src/components/ChangeLog.tsx` | 48 | ESLint rules disabled |
| `src/components/audiobooks/MetadataSearchDialog.tsx` | 85 | ESLint rules disabled |
| `src/components/audiobooks/VersionManagement.tsx` | 45 | ESLint rules disabled |
| `src/components/dedup/DedupAIReviewTab.tsx` | 35 | ESLint rules disabled |
| `src/components/dedup/DedupReconcileTab.tsx` | 26 | ESLint rules disabled |
| `src/pages/ActivityLog.tsx` | 173 | ESLint rules disabled |
| `src/pages/Settings.tsx` | 159 | ESLint rules disabled |

`src/components/audiobooks/MetadataReviewDialog.tsx:213` also appears in that
list and is deliberately omitted: it is being replaced by the unified review
workspace.

The "ESLint rules disabled" cases are `eslint-disable` comments. At least one —
in `src/components/settings/ITunesImport.tsx` — is stale and suppresses nothing.
Each now has a measurable cost, which it did not appear to have before.

## Reproducing these numbers

The measurement script is not committed (it is a one-shot). To rebuild it, run
`@babel/core`'s `transformAsync` over the sources with

```ts
plugins: [['babel-plugin-react-compiler', { logger, panicThreshold: 'none' }]]
```

and tally `logEvent` calls by `event.kind` (`CompileSuccess` vs
`CompileError`/`CompileSkip`), grouping the failures by
`event.detail.reason`.
