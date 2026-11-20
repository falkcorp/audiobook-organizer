// file: core/web/packages/shell/test/createShell.meta.test.tsx
// version: 0.1.0
// guid: 2d9e4b70-6f13-4c85-a8e2-1b7c0f5d9e36
// last-edited: 2026-11-20

// 07 §7.18 (FE7-8, FE8-7): one app-ID source, the preset's mediacore-app meta.

import { act, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createShell } from '../src/app/createShell';
import { getShellAppId, storageKey } from '../src/app/appId';
import { readThemeMeta } from '../src/meta';
import { renderInShell } from '../src/testing';
import { mediacore } from '../vite/index.mjs';

function setMeta(name: string, content: string | null) {
  document.head.querySelector(`meta[name="${name}"]`)?.remove();
  if (content === null) return;
  const m = document.createElement('meta');
  m.setAttribute('name', name);
  m.setAttribute('content', content);
  document.head.appendChild(m);
}

function host(): HTMLElement {
  const el = document.createElement('div');
  document.body.appendChild(el);
  return el;
}

/** A fetch that answers the probes a signed-out page makes. */
function serverFetch(appId: string) {
  return vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    if (url.endsWith('/api/core/v1/auth/status')) return json({ has_users: true, auth_disabled: false, setup_required: false });
    if (url.endsWith('/api/core/v1/me')) return json({ user: { id: 'wayne-1', username: 'bruce' }, permissions: ['core:admin'], method: 'session', owner_verified: true });
    if (url.endsWith('/api/core/v1/modules'))
      return json({ core: { version: MEDIACORE_CORE_VERSION }, app: { id: appId, display_name: 'Gotham', version: '1', ui_build_id: '' }, restart_required: false, modules: [] });
    if (url.endsWith('/api/core/v1/ui/diagnostics')) return new Response(null, { status: 204 });
    if (url.includes('/api/core/v1/events')) return new Promise<Response>(() => {});
    return json({ title: 'not found' }, 404);
  });
}

afterEach(() => {
  setMeta('mediacore-app', null);
  setMeta('mediacore-theme', null);
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('createShell and the app-ID meta', () => {
  it('a ShellConfig.app.id that differs from the meta gives the blocking page and mounts nothing', () => {
    setMeta('mediacore-app', 'gotham');
    const error = vi.spyOn(console, 'error').mockImplementation(() => {});
    const fetch = serverFetch('gotham');
    vi.stubGlobal('fetch', fetch);
    const shell = createShell({ app: { id: 'metropolis', displayName: 'Metropolis' }, modules: [] });
    act(() => shell.mount(host()));
    expect(screen.getByText('This page belongs to another app')).toBeInTheDocument();
    expect(error).toHaveBeenCalledWith(expect.stringMatching(/differs from the page's <meta name="mediacore-app">/));
    expect(fetch).not.toHaveBeenCalled();
    act(() => shell.unmount());
  });

  it('without the meta the shell mounts with config.app.id and logs one warning', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    vi.stubGlobal('fetch', serverFetch('gotham'));
    const shell = createShell({ app: { id: 'gotham', displayName: 'Gotham' }, modules: [] });
    act(() => shell.mount(host()));
    expect(getShellAppId()).toBe('gotham');
    expect(warn.mock.calls.filter((c) => /no <meta name="mediacore-app">/.test(String(c[0])))).toHaveLength(1);
    act(() => shell.unmount());
  });

  it('a server app.id that differs reports app-id-mismatch', async () => {
    setMeta('mediacore-app', 'gotham');
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const fetch = serverFetch('metropolis');
    vi.stubGlobal('fetch', fetch);
    window.history.replaceState(null, '', '/');
    const shell = createShell({ app: { id: 'gotham', displayName: 'Gotham' }, modules: [] });
    act(() => shell.mount(host()));
    await waitFor(() => expect(warn).toHaveBeenCalledWith(expect.stringMatching(/app-id-mismatch/)));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([u, init]) => String(u).endsWith('/api/core/v1/ui/diagnostics') && String(init?.body).includes('app-id-mismatch'))).toBe(true),
    );
    act(() => shell.unmount());
  });

  it("without the theme meta the shell uses storageKey('shell','themeMode') and 'dark'", () => {
    expect(readThemeMeta()).toBeNull();
    setMeta('mediacore-theme', 'gotham.shell.themeMode light');
    expect(readThemeMeta()).toEqual({ storageKey: 'gotham.shell.themeMode', defaultMode: 'light' });
    setMeta('mediacore-theme', 'bad key dark');
    expect(readThemeMeta()).toBeNull();
    renderInShell(<div />, { appId: 'gotham' });
    expect(storageKey('shell', 'themeMode')).toBe('gotham.shell.themeMode');
  });

  it('the preset refuses an appId or themeStorageKey outside its pattern', () => {
    expect(() => mediacore({ appId: 'Gotham' })).toThrow(/appId/);
    expect(() => mediacore({ appId: 'gotham', themeStorageKey: 'two words' })).toThrow(/themeStorageKey/);
    expect(() => mediacore({ appId: 'gotham' })).not.toThrow();
  });

  it('renderInShell({ appId, themeStorageKey, defaultMode }) sets the keys a test reads', () => {
    localStorage.setItem('gotham.theme', 'light');
    renderInShell(<div data-testid="x" />, { appId: 'gotham', themeStorageKey: 'gotham.theme', defaultMode: 'dark' });
    expect(getShellAppId()).toBe('gotham');
    expect(storageKey('batcave', 'columns')).toBe('gotham.batcave.columns');
    expect(document.documentElement.getAttribute('data-mui-color-scheme')).toBe('light');
  });
});
