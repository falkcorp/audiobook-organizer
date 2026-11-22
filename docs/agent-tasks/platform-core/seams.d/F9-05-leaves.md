<!-- file: docs/agent-tasks/platform-core/seams.d/F9-05-leaves.md -->
<!-- version: 0.1.0 -->
<!-- guid: 56d2cf3f-f643-4d3f-bb0f-9fb8d571104b -->
<!-- last-edited: 2026-11-22 -->

# F9-05 leaves: what moved, what did not, and where the shell twins differ

Items 3, 7 and 8 of F9-05, the leaf slice. F9-03 owns the rest (it re-points every importer and deletes the old
`web/src` re-export files).

## Moved (old path keeps a re-export line)

| Old path | New home | Name |
|---|---|---|
| `web/src/stores/useAppStore.ts` | `core/web/packages/shell/src/providers/appStore.ts` | `useAppStore` |
| `web/src/utils/safeReturn.ts` | `core/web/packages/shell/src/auth/sanitizeReturn.ts` | `sanitizeReturn` |
| `web/src/utils/settingLocks.ts` | `core/web/packages/api-client/src/runtime/settingLocks.ts` | `describeLock` |
| `web/src/utils/apiKeyExpiry.ts` | `core/web/packages/api-client/src/runtime/apiKeyExpiry.ts` | the eight API key expiry names |
| `web/src/utils/apiResponse.ts` | `core/web/packages/api-client/src/runtime/apiResponse.ts` | `unwrapData`, `responseErrorMessage`, `describeRequestError`, `SESSION_EXPIRED_MESSAGE` |

Two consequences F9-03 should know:

- The shell package now depends on `zustand` (`^5.0.16`, the version `web/` pins), because `useAppStore` is a zustand
  store. The web app's `vitest.config.ts` and `vite.config.ts` add `zustand` to `resolve.dedupe`: without it vitest loads
  the shell's `zustand` from `core/web/node_modules` as an external module with its own React, and every test that
  reaches the store dies on `Cannot read properties of null (reading 'useCallback')`.
- The api-client's vitest config now pins `TZ=America/New_York`, as `web/vitest.config.ts` does, for the API key expiry
  tests (a UTC runner would make their daylight-saving cells blind).

## `sanitizeReturn` and `safeReturn` are two implementations, not one (left apart on purpose)

The shell already had `auth/safeReturn.ts` (F9-01). They guard the same sink and are not the same function, so merging
them is a behaviour change and not this slice's. Measured by running both over the same inputs:

| Input | `sanitizeReturn` (ABO, from `web/src/utils`) | `safeReturn` (shell) |
|---|---|---|
| `null`, `undefined`, `""`, `library`, `//host`, `/\host`, `/a\b`, `https://host/` | `""` | `"/"` |
| `/a<LF>b`, `/a<NUL>b` (control characters) | accepted unchanged | `"/"` |
| `/.//host` | **accepted unchanged** | `"/"` |
| `/%2e//host` | **accepted unchanged** | `"/"` |
| `/%5chost` | accepted unchanged | `"/"` |
| `/a/../b`, `/a/./b`, `/a b` | unchanged | normalised: `/b`, `/a/b`, `/a%20b` |

- The reject value differs (`""` against `"/"`). ABO's callers (`Login.tsx`'s `location.state.from`, `BookDetail.tsx`'s
  saved return URL) test for `""` to fall back to their own default, so the swap is not drop-in.
- **A real gap, surfaced and not fixed here:** `sanitizeReturn` accepts `/.//host`, `/%2e//host` and `/..//host`. A browser
  that navigates to such a path resolves the dot segments to `//host`, which is scheme-relative. `safeReturn` closed this
  in `6aaa04a44` ("checks the normalized path, not only the raw input"). `sanitizeReturn` is a port of the Go
  `sanitizeReturn` in `internal/server/handlers/oauth_login.go` and says the two must stay in sync, so the fix belongs to
  both at once, and the Go side is not web's. F9-03 should drop `sanitizeReturn` for `safeReturn`, with an `""`/`"/"`
  fallback change at the two call sites, once the Go side has the same fix.

## Twins left alone (an equivalence test would not have passed)

The rule was to rebind an old path to its shell or ui-kit twin only if a test that passes against the untouched ABO
version also passes against the twin. A probe run of the same event sequences against both showed the differences below,
so `web/src/components/toast/ToastProvider.tsx` and `web/src/hooks/useKeyboardShortcuts.ts` stay as they are.

### `useKeyboardShortcuts` (ABO) against `@mediacore/ui-kit`'s

| Behaviour | ABO | ui-kit twin |
|---|---|---|
| Signature | `useKeyboardShortcuts({ onShowHelp })`, calls `useNavigate()` itself, routes `/library` and `/settings` hard-coded | `useKeyboardShortcuts(table, onHelp?)`: the caller supplies the `g <key>` table; names no route |
| Chord window after `g` | 1000 ms (`g`, wait 1200 ms, `l`: no navigation) | 1500 ms (the same sequence runs the action) |
| `Ctrl+K` and `/` | focus and select the first `input[placeholder*="earch"], input[aria-label*="earch"]` | not handled (the probe left the search input unfocused) |
| `Ctrl+K` inside an input | still works | not handled |
| Modifier on the `g` | `Ctrl+g` then `l` navigates | ignored (`ctrlKey`, `metaKey` and `altKey` skip the key) |
| `defaultPrevented` events | handled | skipped |
| Input detection | `document.activeElement` | the event target |
| `?` | calls `onShowHelp` | calls `onHelp` (same) |
| `g` itself | `preventDefault()` | not prevented |

F9-03 has to supply the table from the shell routes plus each nav entry's `goKey`, and carry the search focus (`/`,
`Ctrl+K`) somewhere, or drop it deliberately: the twin has no equivalent.

### `ToastProvider` (ABO) against `@mediacore/shell`'s

| Behaviour | ABO | shell twin |
|---|---|---|
| API | `toast(message, severity?, action?)` | `show(message, severity?)`, `success(message)`, `error(message)` |
| Action button (the Undo toast) | supported, closes the toast | not supported |
| Several toasts | all stacked at once, bottom left (the probe saw two alerts) | queued, one at a time (the probe saw one) |
| Backing state | `useAppStore.notifications` (capped at 100; success and info removed after 5 s) | local component state |
| Auto-hide | 4500 ms per Snackbar, errors included (the store keeps error and warning entries until dismissed) | 5000 ms, none for `error` |
| `useToast()` outside a provider | returns a no-op `toast` | throws |

The Undo action is used by the metadata search dialogs (their tests assert `{ label: 'Undo' }`), so the twin cannot replace it until it has an action
slot. `useAppStore` moved to the shell in this slice, so the shell can host the ABO toast as is when F9-03 wants it.

## `permissionError.ts` left alone

It imports `ApiError` from `services/api`. The api-client has `ApiTimeoutError`, `ApiAuthRedirectError` and the
`Problem` type, but no class with `ApiError`'s fields, so there is no equivalent for an equivalence test to prove.
