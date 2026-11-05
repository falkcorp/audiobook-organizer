// file: web/src/utils/absRedirects.ts
// version: 1.2.0
// guid: 7e2b4c90-1d3f-4a58-9b6e-c0a8f5d27e41
// last-edited: 2026-11-05

/**
 * Audiobookshelf web-client URLs that users reach from ABS apps and docs, mapped
 * to the equivalent page here. Only paths with an obvious equivalent belong in
 * this list; everything else falls through to the NotFound page. `/config/users`
 * is deliberately absent: our Security tab is not a user manager.
 *
 * React Router ignores trailing slashes when matching, so `/config/api-keys/`
 * is covered by the `/config/api-keys` row. The target's `?tab=` is what Settings
 * reads to pick its tab (it still reads the older `#slug` form too).
 */
export const ABS_REDIRECTS: ReadonlyArray<{ from: string; to: string }> = [
  { from: '/config', to: '/settings' },
  { from: '/config/api-keys', to: '/settings?tab=api-keys' },
  // ABS served behind its router base (ROUTER_BASE_PATH=/audiobookshelf), which
  // its docs and bookmarks commonly use.
  { from: '/audiobookshelf', to: '/dashboard' },
  { from: '/audiobookshelf/config', to: '/settings' },
  { from: '/audiobookshelf/config/api-keys', to: '/settings?tab=api-keys' },
];
