// file: core/web/packages/shell/src/testing/index.tsx
// version: 0.1.0
// guid: 1d6b9e40-3a72-4c58-8f13-e5c0a7d2b964
// last-edited: 2026-11-20

// @mediacore/shell/testing (07 §7.4.4): render a component inside the
// shell's providers on a memory data router, with fake auth, a fake
// realtime source and a stubbed client.

import type { ReactElement } from 'react';
import { render, type RenderResult } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { createShellClient, type ShellClient } from '../api/interim';
import { setShellAppId } from '../app/appId';
import { MountedModulesContext } from '../app/context';
import { ShellProviders } from '../app/createShell';
import { AuthClientContext, AuthContext, ModulesHintsContext } from '../auth/context';
import { grantsOf } from '../auth/hooks';
import type { AuthState } from '../contracts';
import { RealtimeProvider } from '../realtime';
import type { ModuleUI, Permission } from '../types';
import { intersect, type ServerModules } from '../version';
import { FakeRealtime } from './FakeRealtime';
import { CORE_VERSION } from '../version/coreVersion';

export { FakeRealtime };

/** Sets the page's app ID for a test that renders without renderInShell (default `test`, renderInShell's own). */
export function setTestShellAppId(id = 'test'): void {
  setShellAppId(id);
}

/** The /api/core/v1/modules payload (07 §7.5.1). */
export type ModulesResponse = ServerModules;

export interface RenderInShellOptions {
  route?: string; // initial location, default '/'
  modules?: readonly ModuleUI[]; // module contributions to mount
  auth?: Partial<AuthState> | 'anonymous'; // default: an authenticated user
  permissions?: readonly Permission[]; // the user's effective permissions
  serverModules?: ModulesResponse; // the /modules payload; default: every module enabled
  realtime?: FakeRealtime; // push topics, stream resets and closes in tests
  client?: Partial<ShellClient>; // stubbed operations
  appId?: string; // default 'test'; the app ID storageKey() and the theme key use (FE8-7)
  themeStorageKey?: string; // default `${appId}.shell.themeMode`
  defaultMode?: 'light' | 'dark'; // default 'dark'
}

/** Every module enabled at its own version, declaring exactly what it contributes. */
export function serverModulesFor(appId: string, modules: readonly ModuleUI[]): ModulesResponse {
  return {
    core: { version: CORE_VERSION },
    app: { id: appId, display_name: appId, version: '0.0.0', ui_build_id: '' },
    restart_required: false,
    modules: modules.map((m) => ({
      id: m.id,
      version: m.version,
      display_name: m.manifest.displayName,
      ui: {
        pages: [...m.manifest.pages],
        nav: [...m.manifest.nav],
        settings_panels: [...m.manifest.settingsPanels],
        widgets: [...m.manifest.widgets],
        banners: [...m.manifest.banners],
        top_bar_items: [...m.manifest.topBarItems],
        user_page: [...m.manifest.userPage],
        activity_panes: [...m.manifest.activityPanes],
        setup_steps: [...m.manifest.setupSteps],
      },
    })),
  };
}

function authState(opts: RenderInShellOptions): AuthState {
  const base: AuthState = {
    status: 'authenticated',
    user: { id: 'wayne-1', username: 'bruce', display_name: 'Bruce Wayne' },
    permissions: opts.permissions ?? [],
    method: 'session',
    ownerVerified: true,
    setupRequired: false,
    authDisabled: false,
    error: null,
    refresh: async () => {},
    logout: async () => {},
  };
  if (opts.auth === 'anonymous') return { ...base, status: 'anonymous', user: null, permissions: [], method: null };
  return { ...base, ...opts.auth };
}

export function renderInShell(
  ui: ReactElement,
  opts: RenderInShellOptions = {},
): RenderResult & { router: ReturnType<typeof createMemoryRouter>; realtime: FakeRealtime } {
  const appId = opts.appId ?? 'test';
  setShellAppId(appId);
  const themeStorageKey = opts.themeStorageKey ?? `${appId}.shell.themeMode`;
  const modules = opts.modules ?? [];
  const realtime = opts.realtime ?? new FakeRealtime();
  const base = createShellClient({ buildId: '', shell: true, fetch: () => Promise.reject(new Error('renderInShell: no stub for this call')) });
  const client = { ...base, ...opts.client } as ShellClient;
  const auth = authState(opts);
  const result = intersect(CORE_VERSION, opts.serverModules ?? serverModulesFor(appId, modules), modules, {
    isAdmin: grantsOf(auth)('core:admin'),
  });
  const mounted = { status: 'ready' as const, modules: result.modules, diagnostics: result.diagnostics };
  const router = createMemoryRouter([{ path: '*', element: ui }], { initialEntries: [opts.route ?? '/'] });
  const rendered = render(
    <ShellProviders appId={appId} displayName={appId} client={client} registry={modules} themeStorageKey={themeStorageKey} defaultMode={opts.defaultMode ?? 'dark'}>
      <AuthClientContext.Provider value={client}>
        <AuthContext.Provider value={auth}>
          <ModulesHintsContext.Provider value={modules}>
            <RealtimeProvider source={realtime}>
              <MountedModulesContext.Provider value={mounted}>
                <RouterProvider router={router} />
              </MountedModulesContext.Provider>
            </RealtimeProvider>
          </ModulesHintsContext.Provider>
        </AuthContext.Provider>
      </AuthClientContext.Provider>
    </ShellProviders>,
  );
  return Object.assign(rendered, { router, realtime });
}
