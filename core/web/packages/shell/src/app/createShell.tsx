// file: core/web/packages/shell/src/app/createShell.tsx
// version: 0.1.0
// guid: 7e0b4d36-2a85-4c19-b7f2-6d9c3e1a8f50
// last-edited: 2026-11-20

import { StrictMode, useMemo, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router';
import { CssBaseline, ThemeProvider, Typography } from '@mui/material';
import type { ThemeOptions } from '@mui/material/styles';
import { readPageBuildId } from '@mediacore/api-client';
import { BlockingPage, createMediaTheme, type MediaThemeInput } from '@mediacore/ui-kit';
import { createShellClient, type ShellClient } from '../api/interim';
import type { TaskRunView } from '../contracts';
import { readAppMeta, readThemeMeta } from '../meta';
import { createRealtimeClient } from '../realtime';
import { TaskTrackerProvider } from '../tasks';
import { ToastProvider } from '../toast';
import type { ModuleUI, Shell, ShellConfig } from '../types';
import { setShellAppId, storageKey } from './appId';
import { ShellContext } from './context';
import { shellRouteObjects } from './routes';
import { ShellErrorBoundary } from './ShellErrorBoundary';

/** A request the shell makes with no deadline of its own is cut off after this. */
export const SHELL_REQUEST_TIMEOUT_MS = 30_000;

export interface ShellProvidersProps {
  appId: string;
  displayName: string;
  client: ShellClient;
  registry: readonly ModuleUI[];
  theme?: Omit<MediaThemeInput, 'defaultMode'>;
  themeStorageKey: string;
  defaultMode: 'light' | 'dark';
  children: ReactNode;
}

/** GET /api/core/v1/tasks/{id}: the run the task tracker follows. */
export function taskFetcher(client: ShellClient) {
  return async (runId: string, signal: AbortSignal): Promise<TaskRunView> => {
    const rt = client.runtime;
    const res = await rt.fetch(`${rt.baseUrl}/api/core/v1/tasks/${encodeURIComponent(runId)}`, { signal, credentials: 'same-origin' });
    const body = await res.json().catch(() => null);
    if (!res.ok) throw new Error(body?.detail || body?.title || `HTTP ${res.status}`);
    return body as TaskRunView;
  };
}

/**
 * The providers outside the router, in 07 §7.4.2's order: ErrorBoundary,
 * MUI ThemeProvider (CSS variables, the preset's mode key), the client,
 * toasts and the task tracker. createShell and renderInShell share it.
 */
export function ShellProviders(p: ShellProvidersProps) {
  const theme = useMemo(
    () =>
      createMediaTheme(
        { ...p.theme, defaultMode: p.defaultMode },
        [...p.registry].sort((a, b) => a.id.localeCompare(b.id)).flatMap((m) => (m.themeExtend ? [m.themeExtend as ThemeOptions] : [])),
      ),
    [p.theme, p.defaultMode, p.registry],
  );
  const ctx = useMemo(() => ({ appId: p.appId, displayName: p.displayName, client: p.client, registry: p.registry }), [p.appId, p.displayName, p.client, p.registry]);
  const fetchRun = useMemo(() => taskFetcher(p.client), [p.client]);
  // storageKey reads the app ID setShellAppId stored for p.appId.
  const colorSchemeKey = useMemo(() => storageKey('shell', 'colorScheme'), [p.appId]);
  return (
    <ShellErrorBoundary label="The app">
      {/* MUI keeps the chosen light and dark scheme names under colorSchemeStorageKey
          (default "mui-color-scheme"), a key outside the app's namespace: two apps on one
          origin would share it (D13). */}
      <ThemeProvider theme={theme} defaultMode={p.defaultMode} modeStorageKey={p.themeStorageKey} colorSchemeStorageKey={colorSchemeKey}>
        <CssBaseline />
        <ShellContext.Provider value={ctx}>
          <ToastProvider>
            <TaskTrackerProvider fetchRun={fetchRun}>{p.children}</TaskTrackerProvider>
          </ToastProvider>
        </ShellContext.Provider>
      </ThemeProvider>
    </ShellErrorBoundary>
  );
}

/** The page's app ID by the one-source rule of 07 §7.10 (FE7-8), or the mismatch that blocks the mount. */
export function resolveAppId(configured: string): { appId: string; mismatch: string | null } {
  const meta = readAppMeta();
  if (meta === null) {
    console.warn(`@mediacore/shell: the page has no <meta name="mediacore-app">; using ShellConfig.app.id ${JSON.stringify(configured)}`);
    return { appId: configured, mismatch: null };
  }
  if (meta !== configured) {
    console.error(`@mediacore/shell: ShellConfig.app.id ${JSON.stringify(configured)} differs from the page's <meta name="mediacore-app"> ${JSON.stringify(meta)}; nothing is mounted`);
    return { appId: meta, mismatch: meta };
  }
  return { appId: meta, mismatch: null };
}

/** createShell of 07 §7.4.2. */
export function createShell(config: ShellConfig): Shell {
  let root: Root | null = null;
  return {
    mount(target) {
      const el = typeof target === 'string' ? document.querySelector(target) : target;
      if (!el) throw new Error(`createShell: mount target ${String(target)} not found`);
      if (root) throw new Error('createShell: already mounted');
      root = createRoot(el);
      const { appId, mismatch } = resolveAppId(config.app.id);
      if (mismatch !== null) {
        root.render(
          <ThemeProvider theme={createMediaTheme()}>
            <CssBaseline />
            <BlockingPage title="This page belongs to another app">
              <Typography>
                The page was built for the app {JSON.stringify(mismatch)}, but this bundle is configured as {JSON.stringify(config.app.id)}.
              </Typography>
            </BlockingPage>
          </ThemeProvider>,
        );
        return;
      }
      setShellAppId(appId);
      const themeMeta = readThemeMeta() ?? { storageKey: storageKey('shell', 'themeMode'), defaultMode: 'dark' as const };
      const client = createShellClient({ buildId: readPageBuildId(), shell: true, timeoutMs: SHELL_REQUEST_TIMEOUT_MS });
      const realtime = createRealtimeClient({ appId });
      const router = createBrowserRouter(shellRouteObjects(config.modules, realtime));
      root.render(
        <StrictMode>
          <ShellProviders
            appId={appId}
            displayName={config.app.displayName}
            client={client}
            registry={config.modules}
            theme={config.theme}
            themeStorageKey={themeMeta.storageKey}
            defaultMode={themeMeta.defaultMode}
          >
            <RouterProvider router={router} />
          </ShellProviders>
        </StrictMode>,
      );
    },
    unmount() {
      root?.unmount();
      root = null;
    },
  };
}
