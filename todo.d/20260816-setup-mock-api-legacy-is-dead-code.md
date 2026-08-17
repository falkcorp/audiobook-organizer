### `setupMockApiLegacy` in the E2E helpers is ~1,000 lines of dead code

`web/tests/e2e/utils/test-helpers.ts:1957` exports `setupMockApiLegacy`, a
second full mock API implementation (roughly lines 1957–2958). It has **zero
callers** — `grep -rn setupMockApiLegacy web/ --exclude-dir=node_modules`
returns only its own definition.

It is worse than merely unused: it is a decoy. It carries its own
`/api/v1/operations/{scan,organize,active}` handlers, so anyone auditing the
mocks for a stale endpoint finds two copies and reasonably updates both. That
happened during the 2026-08-16 v2-endpoint repair; the second copy was
correctly identified as dead only after the grep.

Delete it in a standalone PR, so the diff reads as a deletion rather than
getting mixed into a behavioural change.
