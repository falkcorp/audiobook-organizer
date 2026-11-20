// file: core/web/packages/shell/test/shellStorageKeys.test.tsx
// version: 0.1.0
// guid: 5b0f3e86-1c47-4d92-9a65-e8d2c7b4a019
// last-edited: 2026-11-20

// 07 §7.18 (D13, FE6-2): every key the shell and ui-kit read or write is a
// storageKey() name under the app ID, or the preset's theme key; no
// pre-shell key (table_config, dismissed_announcements, app-theme-mode under
// its own name) is read.

import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { act, fireEvent, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createShell } from '../src/app/createShell';

const PRE_SHELL_KEYS = ['table_config', 'dismissed_announcements', 'activity-source-prefs', 'activity-ops-pinned', 'welcome_wizard_completed', 'ops-indicator-collapse-v1', 'settings.showAdvanced', 'app-theme-mode'];

function recordStorage() {
  const keys: string[] = [];
  for (const store of [Storage.prototype]) {
    for (const fn of ['getItem', 'setItem', 'removeItem'] as const) {
      const orig = store[fn];
      vi.spyOn(store, fn).mockImplementation(function (this: Storage, ...args: [string, string?]) {
        keys.push(args[0]);
        return (orig as (...a: unknown[]) => unknown).apply(this, args) as never;
      });
    }
  }
  return keys;
}

function setMeta(name: string, content: string) {
  const m = document.createElement('meta');
  m.setAttribute('name', name);
  m.setAttribute('content', content);
  document.head.appendChild(m);
}

function signedInFetch() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    if (url.endsWith('/api/core/v1/auth/status')) return json({ has_users: true, auth_disabled: false, setup_required: false });
    if (url.endsWith('/api/core/v1/me')) return json({ user: { id: 'wayne-1', username: 'bruce' }, permissions: ['core:admin'], method: 'session', owner_verified: true });
    if (url.endsWith('/api/core/v1/modules'))
      return json({ core: { version: MEDIACORE_CORE_VERSION }, app: { id: 'gotham', display_name: 'Gotham', version: '1', ui_build_id: '' }, restart_required: false, modules: [] });
    if (url.includes('/api/core/v1/events')) return new Promise<Response>(() => {});
    return new Response(null, { status: 204 });
  });
}

afterEach(() => {
  document.head.querySelectorAll('meta[name^="mediacore-"]').forEach((m) => m.remove());
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('shell storage keys', () => {
  it('a mounted, signed-in shell touches only keys under the app ID, plus the preset theme key', async () => {
    for (const k of PRE_SHELL_KEYS) localStorage.setItem(k, 'stale');
    setMeta('mediacore-app', 'gotham');
    setMeta('mediacore-theme', 'app-theme-mode dark'); // ABO passes its old key to the preset (FE6-2)
    vi.stubGlobal('fetch', signedInFetch());
    const keys = recordStorage();
    const el = document.createElement('div');
    document.body.appendChild(el);
    const shell = createShell({ app: { id: 'gotham', displayName: 'Gotham' }, modules: [] });
    act(() => shell.mount(el));
    const toggle = await screen.findByLabelText('toggle color mode');
    act(() => fireEvent.click(toggle));
    expect(localStorage.getItem('app-theme-mode')).toBe('light');
    act(() => shell.unmount());
    // react-router's data router keeps the view transitions it applied in
    // sessionStorage under its own name; it is the library's, per tab, and
    // no app preference.
    const outside = keys.filter((k) => !k.startsWith('gotham.') && k !== 'app-theme-mode' && k !== 'remix-router-transitions');
    expect(outside).toEqual([]);
    expect(keys.filter((k) => PRE_SHELL_KEYS.includes(k) && k !== 'app-theme-mode')).toEqual([]);
  });

  it('no shell or ui-kit source names a storage key literally', () => {
    const roots = [join(import.meta.dirname, '../src'), join(import.meta.dirname, '../../ui-kit/src')];
    const offenders: string[] = [];
    for (const root of roots) {
      for (const f of readdirSync(root, { recursive: true }).map(String)) {
        if (!/\.(ts|tsx)$/.test(f) || /\.test\./.test(f)) continue;
        const src = readFileSync(join(root, f), 'utf8');
        if (/(local|session)Storage\.(get|set|remove)Item\(\s*['"`]/.test(src)) offenders.push(f);
        for (const k of PRE_SHELL_KEYS) if (src.includes(`'${k}'`) || src.includes(`"${k}"`)) offenders.push(`${f}: ${k}`);
      }
    }
    expect(offenders).toEqual([]);
  });
});
