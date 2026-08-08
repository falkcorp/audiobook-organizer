### Fixed

#### The Library no longer claims to be empty when it simply cannot reach the server

Restarting the backend made the Library announce **"No Audiobooks Found"** — to
someone with a 44,000-book library. It was never true; the request had just
failed.

Two things combined to produce it. `useLibraryQuery` responded to a failed load
by calling `setAudiobooks([])`, actively discarding whatever was on screen, and
its `finally` block then set `loading` to false. `LibraryBookGrid` in turn chose
its empty state with:

    audiobooks.length === 0 && !loading && !searchQuery

with no error branch anywhere — and the component was not even passed one; its
props carried only `loading: boolean`, so it was structurally incapable of
telling a failed fetch from an empty library. A failed request produced exactly
the state that condition looks for.

This is not an edge case. The backend is unreachable for roughly **40 seconds
after every deploy** while memdb warms over the whole library (measured
2026-08-08: `/api/v1/system/status` refused connections for ~40s after a
restart, then answered normally). Every single deploy hit this window.

Three changes:

- **Failed loads no longer wipe the list.** The last known-good page stays on
  screen, so a mid-session blip leaves the shelf intact instead of blanking it.
- **The empty state is gated on a load that actually succeeded.** The decision
  moved into a pure `libraryContentState()` helper whose branch order *is* the
  fix: `reconnecting` is checked before `empty`, so only a request that resolved
  with zero results may claim the library is empty.
- **Failed loads retry automatically** with exponential backoff from 500ms,
  capped at 5s — chosen well under the warmup window so the Library repopulates
  within seconds of the backend answering. Retries continue indefinitely for
  transient failures (network error, connection refused, 5xx) because those all
  resolve on their own; a 4xx is a real client-side fault and is not retried. An
  explicit cancel stops the retry loop, and the timer is cleared on unmount.

While the library is unreachable the user now sees "Loading your library…" with
a note that this is normal for up to a minute after an update, instead of being
told their collection is gone.

Also moves `isSubItemSelected` out of `Sidebar.tsx` into its own module,
resolving a `react-refresh/only-export-components` warning introduced with the
In Progress filter fix. Net lint warnings drop from 25 to 24.

Covered by `libraryContentState.test.ts` (10 cases), including an exhaustive
sweep asserting that `empty` is reachable **only** from a clean, settled,
genuinely-zero result.
