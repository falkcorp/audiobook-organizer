// file: core/web/packages/shell/src/app/appId.ts
// version: 0.1.0
// guid: 1f6c8a93-4d27-4b50-9e1a-7b3d5c0f2e84
// last-edited: 2026-11-20

// The one app-ID source of a page (07 §7.10, FE7-8): the preset's
// <meta name="mediacore-app">, set once by createShell (or renderInShell in
// tests) before anything reads storageKey().

let appId: string | undefined;

/** Sets the page's app ID. createShell and renderInShell call it; nothing else does. */
export function setShellAppId(id: string): void {
  appId = id;
}

/** The page's app ID; throws before createShell or renderInShell set it. */
export function getShellAppId(): string {
  if (appId === undefined) throw new Error('@mediacore/shell: the app ID is not set yet (createShell sets it from <meta name="mediacore-app">)');
  return appId;
}

const SEGMENT = /^[A-Za-z0-9_-]{1,64}$/;

/**
 * A localStorage / sessionStorage key under the app ID (07 §7.4.2):
 * `${appId}.${scope}.${name}`. Every shell and module preference uses one,
 * so two apps on one origin never share a key.
 */
export function storageKey(scope: string, name: string): string {
  if (!SEGMENT.test(scope)) throw new Error(`storageKey: scope ${JSON.stringify(scope)} must match ${SEGMENT}`);
  if (!/^[A-Za-z0-9_.-]{1,64}$/.test(name)) throw new Error(`storageKey: name ${JSON.stringify(name)} must match /^[A-Za-z0-9_.-]{1,64}$/`);
  return `${getShellAppId()}.${scope}.${name}`;
}
