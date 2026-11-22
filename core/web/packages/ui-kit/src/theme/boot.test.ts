// file: core/web/packages/ui-kit/src/theme/boot.test.ts
// version: 0.1.0
// guid: 7d2c4e91-0b38-4a65-9f17-c3a8e5b06d42
// last-edited: 2026-11-22

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MODE_ATTRIBUTE, themeBootScript } from './boot';

// The boot script runs before React mounts and must land on the scheme the
// runtime (MUI's useColorScheme, which stores the literal "light", "dark" or
// "system" under the mode storage key) will settle on, so a saved "system"
// does not paint dark and then flip.

const KEY = 'gotham.shell.themeMode';

type Prefers = { dark: boolean; light: boolean } | 'absent';

/** Runs the script in the jsdom window with the given stored value and OS preference. */
function boot(stored: string | null | 'throws', prefers: Prefers, defaultMode: 'light' | 'dark' = 'dark'): string | null {
  document.documentElement.removeAttribute(MODE_ATTRIBUTE);
  localStorage.clear();
  if (stored === 'throws') {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('storage disabled');
    });
  } else if (stored !== null) {
    localStorage.setItem(KEY, stored);
  }
  if (prefers === 'absent') {
    vi.stubGlobal('matchMedia', undefined);
  } else {
    vi.stubGlobal('matchMedia', (q: string) => ({
      matches: q.includes('prefers-color-scheme: dark') ? prefers.dark : q.includes('prefers-color-scheme: light') ? prefers.light : false,
    }));
  }
  new Function(themeBootScript(KEY, defaultMode))();
  return document.documentElement.getAttribute(MODE_ATTRIBUTE);
}

const OS_DARK = { dark: true, light: false };
const OS_LIGHT = { dark: false, light: true };

describe('themeBootScript', () => {
  beforeEach(() => vi.restoreAllMocks());
  afterEach(() => vi.unstubAllGlobals());

  it('uses a stored light', () => expect(boot('light', OS_DARK)).toBe('light'));
  it('uses a stored dark', () => expect(boot('dark', OS_LIGHT)).toBe('dark'));
  it('follows a light OS for a stored system', () => expect(boot('system', OS_LIGHT)).toBe('light'));
  it('follows a dark OS for a stored system', () => expect(boot('system', OS_DARK, 'light')).toBe('dark'));
  it('agrees with MUI when the OS states no preference: system resolves to light', () => {
    expect(boot('system', { dark: false, light: false }, 'dark')).toBe('light');
  });
  it('falls back to the default for a stored system with no matchMedia', () => {
    expect(boot('system', 'absent', 'light')).toBe('light');
    expect(boot('system', 'absent', 'dark')).toBe('dark');
  });
  it('falls back to the default for nothing stored', () => {
    expect(boot(null, OS_LIGHT, 'dark')).toBe('dark');
    expect(boot(null, OS_DARK, 'light')).toBe('light');
  });
  it('falls back to the default for a garbage value', () => {
    expect(boot('purple', OS_LIGHT, 'dark')).toBe('dark');
    expect(boot('System', OS_LIGHT, 'dark')).toBe('dark');
  });
  it('falls back to the default when storage throws', () => expect(boot('throws', OS_LIGHT, 'dark')).toBe('dark'));
  it('falls back to the default when matchMedia throws', () => {
    document.documentElement.removeAttribute(MODE_ATTRIBUTE);
    localStorage.setItem(KEY, 'system');
    vi.stubGlobal('matchMedia', () => {
      throw new Error('boom');
    });
    new Function(themeBootScript(KEY, 'light'))();
    expect(document.documentElement.getAttribute(MODE_ATTRIBUTE)).toBe('light');
  });
});
