// file: core/web/packages/shell/test/fixtures.ts
// version: 0.1.0
// guid: 8a2e5c17-4f90-4b63-9d08-b7c1e3f6a420
// last-edited: 2026-11-20

import { defineModule } from '../src/defineModule';

const Page = () => Promise.resolve({ default: () => null });

export const batcaveManifest = {
  id: 'batcave',
  version: '0.1.0',
  displayName: 'Batcave',
  pages: ['gadgets', 'vehicles'],
  nav: ['gadgets', 'vehicles', 'secret'],
  settingsPanels: ['cave'],
  widgets: [],
  banners: [],
  topBarItems: [],
  userPage: [],
  activityPanes: [],
  setupSteps: [],
  permissions: ['batcave:read', 'batcave:admin'],
} as const;

export function batcave(useBadge?: () => number | undefined) {
  return defineModule(batcaveManifest, {
    routes: [
      { id: 'gadgets', path: 'gadgets', lazy: Page },
      { id: 'vehicles', path: 'vehicles/:id', lazy: Page },
    ],
    nav: [
      { id: 'gadgets', label: 'Gadgets', to: 'gadgets', goKey: 'l', ...(useBadge ? { useBadge } : {}) },
      { id: 'vehicles', label: 'Vehicles', to: { page: 'vehicles', search: 'sort=speed' }, goKey: 's', order: 2 },
      { id: 'secret', label: 'Secret lab', to: 'gadgets', permission: 'batcave:admin' },
    ],
    settingsPanels: [{ id: 'cave', label: 'Cave', schemaSection: 'modules.batcave' }],
    permissionsHints: { view: ['batcave:read'] },
  });
}

export const arkhamManifest = {
  id: 'arkham',
  version: '0.1.0',
  displayName: 'Arkham',
  pages: ['inmates'],
  nav: ['inmates'],
  settingsPanels: [],
  widgets: [],
  banners: [],
  topBarItems: [],
  userPage: [],
  activityPanes: [],
  setupSteps: [],
  permissions: ['arkham:read'],
} as const;

export function arkham() {
  return defineModule(arkhamManifest, {
    routes: [{ id: 'inmates', path: 'inmates', lazy: Page }],
    // 'l' is batcave's (an earlier module ID wins): this one is dropped with a warning.
    nav: [{ id: 'inmates', label: 'Inmates', to: 'inmates', goKey: 'l' }],
    settingsPanels: [],
    permissionsHints: { view: [] },
  });
}
