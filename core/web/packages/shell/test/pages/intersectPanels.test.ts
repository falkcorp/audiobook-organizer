// file: core/web/packages/shell/test/pages/intersectPanels.test.ts
// version: 0.1.0
// guid: 7d0c4b92-3e15-4a68-9f27-b1a8e5c0d364
// last-edited: 2026-11-22

// A user-page panel is a user_page item like a row action or a tab: the
// intersection drops one the server does not declare (07 §7.5.2, G3).

import { describe, expect, it } from 'vitest';
import { defineModule } from '../../src/defineModule';
import { serverModulesFor } from '../../src/testing';
import { intersect } from '../../src/version';
import { CORE_VERSION } from '../../src/version/coreVersion';

const manifest = {
  id: 'gotham',
  version: '0.1.0',
  displayName: 'Gotham',
  pages: [],
  nav: [],
  settingsPanels: [],
  widgets: [],
  banners: [],
  topBarItems: [],
  userPage: ['merge', 'keys'],
  activityPanes: [],
  setupSteps: [],
  permissions: [],
} as const;

const mod = defineModule(manifest, {
  routes: [],
  nav: [],
  settingsPanels: [],
  permissionsHints: { view: [] },
  userPageExtensions: {
    rowActions: [{ id: 'merge', label: 'Merge', lazy: async () => ({ default: () => null }) }],
    panels: [{ id: 'keys', label: 'Keys', lazy: async () => ({ default: () => null }) }],
  },
});

describe('intersect and user-page panels', () => {
  it('keeps a declared panel', () => {
    const r = intersect(CORE_VERSION, serverModulesFor('test', [mod]), [mod], { isAdmin: true });
    expect(r.modules[0].userPageExtensions?.panels?.map((p) => p.id)).toEqual(['keys']);
  });

  it('drops a panel the server does not declare, with one diagnostic', () => {
    const server = serverModulesFor('test', [mod]);
    server.modules[0].ui.user_page = ['merge'];
    const r = intersect(CORE_VERSION, server, [mod], { isAdmin: true });
    expect(r.modules[0].userPageExtensions?.panels).toEqual([]);
    expect(r.modules[0].userPageExtensions?.rowActions?.map((a) => a.id)).toEqual(['merge']);
    expect(r.diagnostics.filter((d) => d.item === 'user_page:keys')).toHaveLength(1);
  });
});
