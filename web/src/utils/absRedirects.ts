// file: web/src/utils/absRedirects.ts
// version: 1.0.0
// guid: 7e2b4c90-1d3f-4a58-9b6e-c0a8f5d27e41
// last-edited: 2026-10-19

/**
 * Audiobookshelf web-client URLs that users reach from ABS apps and docs, mapped
 * to the equivalent page here. Only paths with an obvious equivalent belong in
 * this list; everything else falls through to the NotFound page. `/config/users`
 * is deliberately absent: our Security tab is not a user manager.
 *
 * React Router ignores trailing slashes when matching, so `/config/api-keys/`
 * is covered by the `/config/api-keys` row. The target's hash is what Settings
 * reads (`tabFromHash`) to pick its initial tab, and Settings is mounted fresh
 * by the redirect, so the hash is honoured.
 */
export const ABS_REDIRECTS: ReadonlyArray<{ from: string; to: string }> = [
  { from: '/config', to: '/settings' },
  { from: '/config/api-keys', to: '/settings#api-keys' },
];
