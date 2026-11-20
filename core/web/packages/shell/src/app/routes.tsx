// file: core/web/packages/shell/src/app/routes.tsx
// version: 0.1.0
// guid: 3c7a0e58-9b26-4f41-a6d3-8e2f5b1c0d79
// last-edited: 2026-11-20

import { useEffect, useMemo, type ComponentType, type ReactNode } from 'react';
import { Outlet, useNavigate, type RouteObject } from 'react-router';
import { DataState } from '@mediacore/ui-kit';
import { AuthProvider, InvitePage, LoginPage, ModulesHintsContext, RequireAuth, SetupPage, TempLoginPage, useAuth } from '../auth';
import { holds } from '../nav/navModel';
import { RealtimeProvider } from '../realtime';
import type { RealtimeSource } from '../contracts';
import type { ModuleManifest, ModuleUI, RouteContribution } from '../types';
import { useMountedModules, useShellContext } from './context';
import { LazySlot, MainLayout } from './MainLayout';
import { ModulesGate } from './ModulesGate';
import { StaleBuildBanner } from './StaleBuildBanner';
import { ActivityPage, HomePage, NoAccessPage, NotFoundPage, SettingsPage, SystemPage, UsersPage } from './shellPages';

/** A source that never connects: the realtime provider of a signed-out tab. */
const INERT_SOURCE: RealtimeSource = { subscribe: () => () => {}, setTopics() {}, start() {}, stop() {} };

/** Opens the realtime stream only for a signed-in user (the stream needs a session). */
function SessionRealtime({ source, children }: { source: RealtimeSource; children: ReactNode }) {
  const auth = useAuth();
  return (
    <RealtimeProvider key={auth.status === 'authenticated' ? 'live' : 'inert'} source={auth.status === 'authenticated' ? source : INERT_SOURCE}>
      {children}
    </RealtimeProvider>
  );
}

/** Module-wide providers, outermost first in module ID order (07 §7.4.2). */
function ModuleProviders({ children }: { children: ReactNode }) {
  const { modules } = useMountedModules();
  const providers = useMemo(
    () => [...modules].sort((a, b) => a.id.localeCompare(b.id)).flatMap((m) => (m.providers ?? []) as ComponentType<{ children: ReactNode }>[]),
    [modules],
  );
  return providers.reduceRight<ReactNode>((inner, P) => <P>{inner}</P>, children);
}

/**
 * The root route element: the providers that navigate or read the location
 * sit inside the data router (AuthProvider sends a signed-out session to
 * /login), then the realtime stream, the intersection and the module
 * providers. Everything that needs neither (client, theme, toasts, tasks) is
 * outside the router (createShell).
 */
export function ShellFrame({ realtime }: { realtime: RealtimeSource }) {
  const { client, registry } = useShellContext();
  const navigate = useNavigate();
  return (
    <AuthProvider client={client} onSignedOut={() => navigate('/login')}>
      <ModulesHintsContext.Provider value={registry}>
        <SessionRealtime source={realtime}>
          <ModulesGate>
            <ModuleProviders>
              {/* Above every route, the sign-in pages included: a stale tab learns it at /login too. */}
              <StaleBuildBanner />
              <Outlet />
            </ModuleProviders>
          </ModulesGate>
        </SessionRealtime>
      </ModulesHintsContext.Provider>
    </AuthProvider>
  );
}

/** A module page: waits for the intersection, 404s an unmounted module, guards its permission. */
function ModuleRoute({ moduleId, route }: { moduleId: string; route: RouteContribution<ModuleManifest> }) {
  const { status, modules } = useMountedModules();
  const { permissions } = useAuth();
  const mounted = modules.find((m) => m.id === moduleId);
  const live = mounted?.routes.find((r) => r.id === route.id);
  useEffect(() => {
    if (live?.title) document.title = live.title;
  }, [live?.title]);
  if (status === 'loading' || status === 'idle') return <DataState status="loading" />;
  if (status === 'error' && !live) return <DataState status="error" error="The module list did not load, so this page cannot be shown." />;
  if (!live) return <NotFoundPage />;
  if (!holds(permissions, live.permission)) return <NoAccessPage />;
  return <LazySlot load={live.lazy} label={live.title ?? `${moduleId} ${live.id}`} />;
}

/**
 * The data router's route table: the shell routes of 07 §7.4.3 (their first
 * segments are ui.ReservedSegments()) and every compiled-in module's routes
 * under /<module id>/. The table is fixed at createShell; whether a module
 * page renders is decided per render by the intersection (ModuleRoute).
 */
export function shellRouteObjects(registry: readonly ModuleUI[], realtime: RealtimeSource): RouteObject[] {
  const moduleRoutes: RouteObject[] = registry.flatMap((m) =>
    m.routes.map((r) => ({
      path: `${m.id}/${r.path}`,
      element: <ModuleRoute moduleId={m.id} route={r as RouteContribution<ModuleManifest>} />,
    })),
  );
  return [
    {
      element: <ShellFrame realtime={realtime} />,
      children: [
        { path: '/login', element: <LoginPage /> },
        { path: '/login/temp', element: <TempLoginPage /> },
        { path: '/setup', element: <SetupPage /> },
        { path: '/invite', element: <InvitePage /> },
        {
          element: (
            <RequireAuth>
              <MainLayout />
            </RequireAuth>
          ),
          children: [
            { index: true, element: <HomePage /> },
            { path: 'activity', element: <ActivityPage /> },
            { path: 'settings/*', element: <SettingsPage /> },
            { path: 'system/*', element: <SystemPage /> },
            { path: 'users', element: <UsersPage /> },
            ...moduleRoutes,
            { path: '*', element: <NotFoundPage /> },
          ],
        },
      ],
    },
  ];
}
