// file: web/src/test/themeBoot.equivalence.test.ts
// version: 0.1.0
// guid: d64dae19-523b-4fa2-ae44-7a0326fac587
// last-edited: 2026-11-22

// F9-03 commit 1 moves ABO's pre-paint colour-scheme script from a hand-written
// inline <script> in web/index.html to the one the mediacore() preset writes.
// This pins what the page's boot script does, so the same cases pass on both
// sides of that commit: saved light or dark wins, anything else is dark.
//
// "The page's boot script" is the inline script in index.html while it has one
// (HEAD), and otherwise the preset's own script for ABO's key and default mode.
//
// KNOWN DIFFERENCE, not asserted: HEAD's script also read a saved "system" and
// followed prefers-color-scheme; the preset's script has no such branch (core's
// themeBootScript), so "system" on a light OS paints dark until MUI's
// ThemeProvider mounts and corrects it. That belongs to core/web, not ABO. It is not
// reachable from the UI: the only two theme toggles (shell MainLayout and TopBarFrame)
// call setMode('light' | 'dark'), so "system" is stored only by editing storage by hand.

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { headTags } from '@mediacore/shell/vite';
import { SHELL_APP_ID } from '../shellAppId';
import { STORAGE_KEYS } from '../lib/storageKeys';

const ATTRIBUTE = 'data-mui-color-scheme';
const INLINE_SCRIPT = /<script\b(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/i;

/** The pre-paint script an ABO page runs, whichever side of F9-03 commit 1 this is. */
function bootScript(): string {
  const html = readFileSync(path.resolve(import.meta.dirname, '../../index.html'), 'utf8');
  const inline = INLINE_SCRIPT.exec(html);
  if (inline) return inline[1];
  const tag = headTags({
    buildId: 'test',
    appId: SHELL_APP_ID,
    themeStorageKey: STORAGE_KEYS.APP_THEME_MODE,
    defaultMode: 'dark',
  }).find((t) => t.tag === 'script');
  if (!tag?.children) throw new Error('the preset wrote no boot script');
  return tag.children;
}

function run(saved: string | null, osPrefersLight = false): string | null {
  document.documentElement.removeAttribute(ATTRIBUTE);
  localStorage.clear();
  if (saved !== null) localStorage.setItem(STORAGE_KEYS.APP_THEME_MODE, saved);
  window.matchMedia = ((query: string) => ({
    matches: osPrefersLight && query.includes('light'),
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
    onchange: null,
  })) as typeof window.matchMedia;
  new Function(bootScript())();
  return document.documentElement.getAttribute(ATTRIBUTE);
}

afterEach(() => {
  document.documentElement.removeAttribute(ATTRIBUTE);
  localStorage.clear();
});

describe('the page boot script (data-mui-color-scheme before paint)', () => {
  it('uses the key the ThemeProvider reads', () => {
    expect(STORAGE_KEYS.APP_THEME_MODE).toBe('app-theme-mode');
    expect(bootScript()).toContain(JSON.stringify(STORAGE_KEYS.APP_THEME_MODE));
  });

  it('applies a saved light', () => {
    expect(run('light')).toBe('light');
  });

  it('applies a saved dark, even on a light OS', () => {
    expect(run('dark', true)).toBe('dark');
  });

  it('is dark with nothing saved, on a light OS too', () => {
    expect(run(null)).toBe('dark');
    expect(run(null, true)).toBe('dark');
  });

  it('is dark for a saved value it does not know', () => {
    expect(run('sepia')).toBe('dark');
    expect(run('')).toBe('dark');
  });

  it('runs without a document error when storage throws', () => {
    document.documentElement.removeAttribute(ATTRIBUTE);
    const real = Object.getOwnPropertyDescriptor(window, 'localStorage');
    Object.defineProperty(window, 'localStorage', {
      configurable: true,
      get() {
        throw new Error('storage disabled');
      },
    });
    try {
      expect(() => new Function(bootScript())()).not.toThrow();
    } finally {
      if (real) Object.defineProperty(window, 'localStorage', real);
    }
  });
});
