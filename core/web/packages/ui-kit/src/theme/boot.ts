// file: core/web/packages/ui-kit/src/theme/boot.ts
// version: 0.1.0
// guid: 3e9a7b15-6c42-4f08-a1d3-8b5e0c7f2a96
// last-edited: 2026-11-20

// Dependency-free on purpose: presets.test.mjs loads this file with Node's
// type stripping and asserts the vite preset writes exactly this script.

/** The attribute the mode lives in: flipping it changes the scheme with no re-render. */
export const MODE_ATTRIBUTE = 'data-mui-color-scheme';

const KEY_PATTERN = /^[A-Za-z0-9._:-]{1,128}$/;

/**
 * The body of the pre-mount inline script the vite preset writes into
 * index.html (07 §7.7): it reads the stored mode and sets the mode attribute
 * before any module runs, so a user who chose light sees no dark flash. The Go
 * handler hashes it into the CSP. `storageKey` is checked against the preset's
 * pattern because it lands inside a script literal.
 */
export function themeBootScript(storageKey: string, defaultMode: 'light' | 'dark'): string {
  if (!KEY_PATTERN.test(storageKey)) throw new Error(`theme storage key ${JSON.stringify(storageKey)} must match ${KEY_PATTERN}`);
  if (defaultMode !== 'light' && defaultMode !== 'dark') throw new Error(`default mode must be light or dark`);
  return (
    `(function(){var m=${JSON.stringify(defaultMode)};try{var s=localStorage.getItem(${JSON.stringify(storageKey)});` +
    `if(s==="light"||s==="dark")m=s}catch(e){}document.documentElement.setAttribute(${JSON.stringify(MODE_ATTRIBUTE)},m)})();`
  );
}
