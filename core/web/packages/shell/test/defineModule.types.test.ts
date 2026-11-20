// file: core/web/packages/shell/test/defineModule.types.test.ts
// version: 0.1.0
// guid: 3b7f0d62-9e45-4a18-8c21-f4d6a0e9b375
// last-edited: 2026-11-20

// 07 §7.18: compile-time descriptor safety. Every slot is keyed by the
// manifest's literal IDs, so each `@ts-expect-error` below is a compile
// error that tsc (npm run typecheck) requires; were a slot to accept an
// undeclared ID, the directive itself would fail the typecheck. The runtime
// half checks what a type cannot.

import { describe, expect, it } from 'vitest';
import { defineModule } from '../src/defineModule';

const Page = () => Promise.resolve({ default: () => null });
const m = {
  id: 'batcave',
  version: '0.1.0',
  displayName: 'Batcave',
  pages: ['gadgets'],
  nav: ['gadgets'],
  settingsPanels: ['cave'],
  widgets: ['radar'],
  banners: ['alarm'],
  topBarItems: ['signal'],
  userPage: ['utility-belt'],
  activityPanes: ['patrols'],
  setupSteps: ['cave-door'],
  permissions: ['batcave:read'],
} as const;
const base = { routes: [], nav: [], settingsPanels: [], permissionsHints: { view: [] } } as const;

describe('defineModule types', () => {
  it('rejects an undeclared ID in every slot', () => {
    const typed = () => [
      // @ts-expect-error route ID not in manifest.pages
      defineModule(m, { ...base, routes: [{ id: 'joker', path: 'joker', lazy: Page }] }),
      // @ts-expect-error nav ID not in manifest.nav
      defineModule(m, { ...base, nav: [{ id: 'joker', label: 'J', to: 'gadgets' }] }),
      // @ts-expect-error nav target not a declared page
      defineModule(m, { ...base, nav: [{ id: 'gadgets', label: 'G', to: 'joker' }] }),
      // @ts-expect-error settings panel not in manifest.settingsPanels
      defineModule(m, { ...base, settingsPanels: [{ id: 'joker', label: 'J', schemaSection: 'x' }] }),
      // @ts-expect-error widget not in manifest.widgets
      defineModule(m, { ...base, widgets: [{ id: 'joker', placement: 'home', lazy: Page }] }),
      // @ts-expect-error banner not in manifest.banners
      defineModule(m, { ...base, banners: [{ id: 'joker', lazy: Page }] }),
      // @ts-expect-error top-bar item not in manifest.topBarItems
      defineModule(m, { ...base, topBarItems: [{ id: 'joker', lazy: Page }] }),
      // @ts-expect-error user-page tab not in manifest.userPage
      defineModule(m, { ...base, userPageExtensions: { tabs: [{ id: 'joker', label: 'J', lazy: Page }] } }),
      // @ts-expect-error activity pane not in manifest.activityPanes
      defineModule(m, { ...base, activityPanes: [{ id: 'joker', label: 'J', lazy: Page }] }),
      // @ts-expect-error setup step not in manifest.setupSteps
      defineModule(m, { ...base, setup: { isRequired: async () => false, steps: [{ id: 'joker', title: 'J', lazy: Page }] } }),
      // @ts-expect-error permission not in manifest.permissions (nor core:*)
      defineModule(m, { ...base, routes: [{ id: 'gadgets', path: 'g', lazy: Page, permission: 'arkham:read' }] }),
      // @ts-expect-error task kind outside the module's namespace
      defineModule(m, { ...base, taskKinds: { 'arkham.breakout': { label: 'Breakout' } } }),
    ];
    expect(typeof typed).toBe('function'); // compile-time only; never called
  });

  it('accepts declared IDs and core permissions', () => {
    const ui = defineModule(m, {
      ...base,
      routes: [{ id: 'gadgets', path: 'gadgets', lazy: Page, permission: 'core:admin' }],
      nav: [{ id: 'gadgets', label: 'Gadgets', to: 'gadgets', permission: 'batcave:read' }],
      taskKinds: { 'batcave.scan': { label: 'Scan' } },
    });
    expect(ui.id).toBe('batcave');
    expect(ui.manifest).toBe(m);
  });

  it('refuses at runtime what a type cannot: duplicates and absolute paths', () => {
    expect(() =>
      defineModule(m, { ...base, routes: [{ id: 'gadgets', path: 'a', lazy: Page }, { id: 'gadgets', path: 'b', lazy: Page }] }),
    ).toThrow(/twice/);
    expect(() => defineModule(m, { ...base, routes: [{ id: 'gadgets', path: '/gadgets', lazy: Page }] })).toThrow(/relative/);
  });
});
